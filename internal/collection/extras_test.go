package collection

import (
	"reflect"
	"slices"
	"strings"
	"testing"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/tsuga-dev/tsuga-operator/api/v1alpha1"
)

func rawJSON(s string) *apiextensionsv1.JSON { return &apiextensionsv1.JSON{Raw: []byte(s)} }

func renderErr(t *testing.T, cfg v1alpha1.TsugaCollectorConfigSpec) string {
	t.Helper()
	_, err := RenderCollectors(cfg)
	if err == nil {
		t.Fatal("render succeeded, want error")
	}
	return err.Error()
}

func pipelineList(t *testing.T, config map[string]interface{}, pipeline, field string) []string {
	t.Helper()
	return stringSlice(configField(t, config, "service", "pipelines", pipeline, field))
}

func TestExtraComponentsAdded(t *testing.T) {
	cfg := effective()
	cfg.Agent.ExtraReceivers = rawJSON(`{"filelog/custom":{"include":["/var/log/pods/*/*.log"]}}`)
	cfg.Agent.ExtraProcessors = rawJSON(`{"filter/health":{"error_mode":"ignore"}}`)
	cfg.Agent.ExtraConnectors = rawJSON(`{"count":{}}`)
	cfg.Agent.ExtraPipelines = rawJSON(`{
		"traces/in":{"receivers":["otlp"],"exporters":["count"]},
		"metrics/out":{"receivers":["count"],"exporters":["otlp_http/tsuga"]}}`)
	cfg.Agent.ExtraExtensions = rawJSON(`{"pprof":{"endpoint":"localhost:1777"}}`)
	config := collectorConfig(t, cfg, agentName)
	for _, p := range [][]string{
		{"receivers", "filelog/custom", "include"},
		{"processors", "filter/health", "error_mode"},
		{"connectors", "count"},
		{"extensions", "pprof", "endpoint"},
	} {
		if configField(t, config, p...) == nil {
			t.Errorf("%v missing", p)
		}
	}
	if got := pipelineList(t, config, "traces/in", "exporters"); !reflect.DeepEqual(got, []string{"count"}) {
		t.Errorf("traces/in exporters = %v", got)
	}
	if got := pipelineList(t, config, "metrics/out", "receivers"); !reflect.DeepEqual(got, []string{"count"}) {
		t.Errorf("metrics/out receivers = %v", got)
	}
}

func TestExtraComponentCollision(t *testing.T) {
	cases := []struct {
		name  string
		set   func(*v1alpha1.CollectorTuning)
		wants string
	}{
		{"receiver", func(t *v1alpha1.CollectorTuning) { t.ExtraReceivers = rawJSON(`{"otlp":{}}`) }, "receiver otlp is built in; choose another name"},
		{"processor", func(t *v1alpha1.CollectorTuning) { t.ExtraProcessors = rawJSON(`{"batch":{}}`) }, "processor batch is built in; choose another name"},
		{"connector", func(t *v1alpha1.CollectorTuning) { t.ExtraConnectors = rawJSON(`{"span_metrics":{}}`) }, "connector span_metrics is built in; choose another name"},
		{"extension", func(t *v1alpha1.CollectorTuning) { t.ExtraExtensions = rawJSON(`{"health_check":{}}`) }, "extension health_check is built in; choose another name"},
		{"connector vs receiver", func(t *v1alpha1.CollectorTuning) { t.ExtraConnectors = rawJSON(`{"otlp":{}}`) }, "connector otlp is built in; choose another name"},
		{"receiver vs connector", func(t *v1alpha1.CollectorTuning) { t.ExtraReceivers = rawJSON(`{"span_metrics":{}}`) }, "receiver span_metrics is built in; choose another name"},
		{"connector vs extra exporter", func(t *v1alpha1.CollectorTuning) {
			t.ExtraExporters = rawJSON(`{"forward/x":{}}`)
			t.ExtraConnectors = rawJSON(`{"forward/x":{}}`)
		}, "connector forward/x is built in; choose another name"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := effective()
			tc.set(&cfg.Agent.CollectorTuning)
			if got := renderErr(t, cfg); !strings.Contains(got, "tsuga-agent: "+tc.wants) {
				t.Fatalf("error = %q, want it to contain %q", got, tc.wants)
			}
		})
	}
}

func TestExtraPipelineCollision(t *testing.T) {
	cfg := effective()
	cfg.Agent.ExtraPipelines = rawJSON(`{"logs":{"receivers":["otlp"],"exporters":["otlp_http/tsuga"]}}`)
	if got, want := renderErr(t, cfg), "tsuga-agent: pipeline logs is built in; choose another name"; !strings.Contains(got, want) {
		t.Fatalf("error = %q, want it to contain %q", got, want)
	}
}

func TestPipelineReceiversAndProcessors(t *testing.T) {
	cfg := effective()
	cfg.Agent.ExtraReceivers = rawJSON(`{"filelog/custom":{}}`)
	cfg.Agent.ExtraProcessors = rawJSON(`{"filter/health":{}}`)
	cfg.Agent.PipelineReceivers.Logs = []string{"filelog/custom"}
	cfg.Agent.PipelineProcessors.Traces = []string{"filter/health"}
	config := collectorConfig(t, cfg, agentName)
	if got := pipelineList(t, config, "logs", "receivers"); got[len(got)-1] != "filelog/custom" {
		t.Errorf("logs receivers = %v", got)
	}
	got := pipelineList(t, config, "traces", "processors")
	if !slices.Equal(got[len(got)-2:], []string{"filter/health", "batch"}) {
		t.Errorf("traces processors = %v, want filter/health before batch", got)
	}
	if slices.Contains(pipelineList(t, config, "metrics", "receivers"), "filelog/custom") {
		t.Error("metrics pipeline got a logs receiver")
	}
}

func TestPipelineProcessorsReachLogsEvents(t *testing.T) {
	cfg := effective()
	cfg.Gateway.ExtraProcessors = rawJSON(`{"filter/noise":{}}`)
	cfg.Gateway.PipelineProcessors.Logs = []string{"filter/noise"}
	config := collectorConfig(t, cfg, gatewayName)
	for _, p := range []string{"logs", "logs/events"} {
		got := pipelineList(t, config, p, "processors")
		if !slices.Equal(got[len(got)-2:], []string{"filter/noise", "batch"}) {
			t.Errorf("%s processors = %v", p, got)
		}
	}
}

func TestPipelineProcessorsKeepOrderBeforeBatch(t *testing.T) {
	cfg := effective()
	cfg.Agent.ExtraProcessors = rawJSON(`{"filter/a":{},"transform/b":{}}`)
	cfg.Agent.PipelineProcessors.Logs = []string{"filter/a", "transform/b"}
	got := pipelineList(t, collectorConfig(t, cfg, agentName), "logs", "processors")
	if !slices.Equal(got[len(got)-3:], []string{"filter/a", "transform/b", "batch"}) {
		t.Errorf("logs processors = %v", got)
	}
}

func TestPipelineProcessorsWithoutBatchAppendLast(t *testing.T) {
	cfg := allCollectorsOn()
	cfg.Profiling.ExtraProcessors = rawJSON(`{"filter/p":{}}`)
	cfg.Profiling.PipelineProcessors.Profiles = []string{"filter/p"}
	got := pipelineList(t, collectorConfig(t, cfg, profilerName), "profiles", "processors")
	if got[len(got)-1] != "filter/p" {
		t.Errorf("profiles processors = %v", got)
	}
}

func TestServiceExtensions(t *testing.T) {
	cfg := effective()
	cfg.Agent.ExtraExtensions = rawJSON(`{"pprof":{}}`)
	cfg.Agent.ServiceExtensions = []string{"pprof", "health_check"}
	got := stringSlice(configField(t, collectorConfig(t, cfg, agentName), "service", "extensions"))
	if !reflect.DeepEqual(got, []string{"health_check", "pprof"}) {
		t.Fatalf("service.extensions = %v", got)
	}
}

func TestExtraPipelineWithExtraReceiver(t *testing.T) {
	cfg := effective()
	cfg.Agent.ExtraReceivers = rawJSON(`{"filelog/custom":{}}`)
	cfg.Agent.ExtraPipelines = rawJSON(`{"logs/custom":{"receivers":["filelog/custom"],"exporters":["otlp_http/tsuga"]}}`)
	config := collectorConfig(t, cfg, agentName)
	if configField(t, config, "receivers", "filelog/custom") == nil {
		t.Fatal("receiver referenced only from an extra pipeline was pruned")
	}
	if got := pipelineList(t, config, "logs/custom", "receivers"); !reflect.DeepEqual(got, []string{"filelog/custom"}) {
		t.Fatalf("receivers = %v", got)
	}
}

func TestExtraConnectorBothSides(t *testing.T) {
	cfg := effective()
	cfg.Agent.ExtraConnectors = rawJSON(`{"forward/x":{}}`)
	cfg.Agent.ExtraPipelines = rawJSON(`{
		"traces/in":{"receivers":["otlp"],"exporters":["forward/x"]},
		"traces/out":{"receivers":["forward/x"],"exporters":["otlp_http/tsuga"]}}`)
	config := collectorConfig(t, cfg, agentName)
	if configField(t, config, "connectors", "forward/x") == nil {
		t.Error("connector forward/x missing")
	}
	if got := pipelineList(t, config, "traces/in", "exporters"); !reflect.DeepEqual(got, []string{"forward/x"}) {
		t.Errorf("traces/in exporters = %v", got)
	}
	if got := pipelineList(t, config, "traces/out", "receivers"); !reflect.DeepEqual(got, []string{"forward/x"}) {
		t.Errorf("traces/out receivers = %v", got)
	}
}

func TestCustomConfigReplaces(t *testing.T) {
	cfg := effectiveWithPrometheus()
	cfg.Prometheus.CustomConfig = rawJSON(`{
		"receivers":{"otlp":{"protocols":{"grpc":{}}}},
		"exporters":{"otlp_http":{"endpoint":"__TSUGA_ENDPOINT__","headers":{"cluster":"__CLUSTER_NAME__"}}},
		"service":{"pipelines":{"metrics":{"receivers":["otlp"],"exporters":["otlp_http"]}}}}`)
	cfg.Prometheus.ExtraReceivers = rawJSON(`{"otlp":{}}`)
	cfg.Agent.KubeletStats.CollectionInterval = "99s"
	cfg.ResourceDetection = v1alpha1.ResourceDetectionSpec{Enabled: true, Detectors: []string{"env"}}
	want := map[string]interface{}{
		"receivers": map[string]interface{}{"otlp": map[string]interface{}{"protocols": map[string]interface{}{"grpc": map[string]interface{}{}}}},
		"exporters": map[string]interface{}{"otlp_http": map[string]interface{}{"endpoint": "https://otlp.tsuga.com", "headers": map[string]interface{}{"cluster": "prod-eu"}}},
		"service": map[string]interface{}{"pipelines": map[string]interface{}{"metrics": map[string]interface{}{
			"receivers": []interface{}{"otlp"}, "exporters": []interface{}{"otlp_http"}}}},
	}
	if got := collectorConfig(t, cfg, scraperName); !reflect.DeepEqual(got, want) {
		t.Fatalf("config = %v\nwant %v", got, want)
	}
}

func TestCustomConfigKeepsTsugaEnv(t *testing.T) {
	cfg := effective()
	cfg.Agent.CustomConfig = rawJSON(`{
		"receivers":{"otlp":{"protocols":{"grpc":{}}}},
		"exporters":{"otlp_http":{"endpoint":"https://x","headers":{"Authorization":"Bearer ${env:TSUGA_API_KEY}"}}},
		"service":{"pipelines":{"logs":{"receivers":["otlp"],"exporters":["otlp_http"]}}}}`)
	env, _, _ := unstructured.NestedSlice(collectorByName(t, cfg, agentName).Object, "spec", "env")
	names := make([]string, 0, len(env))
	for _, e := range env {
		names = append(names, e.(map[string]interface{})["name"].(string))
	}
	if !slices.Contains(names, "TSUGA_API_KEY") {
		t.Fatalf("env = %v, want TSUGA_API_KEY", names)
	}
}

func TestExtraPipelineMalformed(t *testing.T) {
	for name, raw := range map[string]string{
		"not an object": `{"logs/x":"nope"}`,
		"no receivers":  `{"logs/x":{"exporters":["otlp_http/tsuga"]}}`,
	} {
		t.Run(name, func(t *testing.T) {
			cfg := effective()
			cfg.Agent.ExtraPipelines = rawJSON(raw)
			want := "tsuga-agent: pipeline logs/x needs receivers and exporters"
			if got := renderErr(t, cfg); got != want {
				t.Fatalf("error = %q, want %q", got, want)
			}
		})
	}
}

func TestCustomConfigWithoutPipelines(t *testing.T) {
	cfg := effective()
	cfg.Agent.CustomConfig = rawJSON(`{"receivers":{"otlp":{}}}`)
	want := "tsuga-agent: customConfig has no service.pipelines"
	if got := renderErr(t, cfg); got != want {
		t.Fatalf("error = %q, want %q", got, want)
	}
}

func TestDecodeErrorNamesField(t *testing.T) {
	cfg := effective()
	cfg.Agent.ExtraProcessors = rawJSON(`[1]`)
	if got := renderErr(t, cfg); !strings.Contains(got, "extraProcessors:") {
		t.Fatalf("error = %q, want it to name extraProcessors", got)
	}
}

func TestExtraConnectorFedFromBuiltInPipeline(t *testing.T) {
	cfg := effective()
	cfg.Agent.ExtraConnectors = rawJSON(`{"count/x":{}}`)
	cfg.Agent.PipelineExporters.Traces = []string{"count/x"}
	cfg.Agent.ExtraPipelines = rawJSON(`{"metrics/count":{"receivers":["count/x"],"exporters":["otlp_http/tsuga"]}}`)
	config := collectorConfig(t, cfg, agentName)
	if got := pipelineList(t, config, "traces", "exporters"); !slices.Contains(got, "count/x") {
		t.Fatalf("traces exporters = %v, want count/x", got)
	}
}

func TestPipelineExportersReachExtraPipelines(t *testing.T) {
	cfg := effective()
	cfg.Agent.ExtraExporters = rawJSON(`{"debug":{}}`)
	cfg.Agent.PipelineExporters.Logs = []string{"debug"}
	cfg.Agent.ExtraPipelines = rawJSON(`{"logs/x":{"receivers":["otlp"],"exporters":["otlp_http/tsuga"]}}`)
	config := collectorConfig(t, cfg, agentName)
	if got := pipelineList(t, config, "logs/x", "exporters"); !slices.Contains(got, "debug") {
		t.Fatalf("logs/x exporters = %v, want debug", got)
	}
}

func TestPipelineProcessorsAfterBatchNotDuplicated(t *testing.T) {
	cfg := effective()
	cfg.Agent.ExtraProcessors = rawJSON(`{"filter/x":{}}`)
	cfg.Agent.ExtraPipelines = rawJSON(`{"logs/late":{"receivers":["otlp"],"processors":["batch","filter/x"],"exporters":["otlp_http/tsuga"]}}`)
	cfg.Agent.PipelineProcessors.Logs = []string{"filter/x"}
	got := pipelineList(t, collectorConfig(t, cfg, agentName), "logs/late", "processors")
	if !slices.Equal(got, []string{"batch", "filter/x"}) {
		t.Errorf("logs/late processors = %v", got)
	}
}
