package collection

import (
	"reflect"
	"slices"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/yaml"

	"github.com/tsuga-dev/tsuga-operator/api/v1alpha1"
	"github.com/tsuga-dev/tsuga-operator/internal/collection/assets"
)

func collectorConfig(t *testing.T, cfg v1alpha1.TsugaCollectorConfigSpec, name string) map[string]interface{} {
	t.Helper()
	config, found, err := unstructured.NestedMap(collectorByName(t, cfg, name).Object, "spec", "config")
	if err != nil || !found {
		t.Fatalf("%s config: found=%v err=%v", name, found, err)
	}
	return config
}

func configField(t *testing.T, config map[string]interface{}, path ...string) interface{} {
	t.Helper()
	v, _, err := unstructured.NestedFieldNoCopy(config, path...)
	if err != nil {
		t.Fatalf("%v: %v", path, err)
	}
	return v
}

func toFloat(t *testing.T, v interface{}) float64 {
	t.Helper()
	switch n := v.(type) {
	case int64:
		return float64(n)
	case float64:
		return n
	}
	t.Fatalf("not a number: %#v", v)
	return 0
}

func TestTuningUnsetLeavesConfigsUnchanged(t *testing.T) {
	cfg := effective()
	cfg.Prometheus.Enabled = true
	cfg.Profiling.Enabled = true
	cfg.Redaction.Config = rawJSON(`{"allow_all_keys":true}`)
	cases := []struct {
		name  string
		asset string
		tune  func(map[string]interface{}, v1alpha1.TsugaCollectorConfigSpec) error
	}{
		{"agent", assets.DaemonsetConfig, tuneAgentConfig},
		{"gateway", assets.GatewayConfig, tuneGatewayConfig},
		{"scraper", assets.ScraperConfig, tuneScraperConfig},
		{"profiler", assets.ProfilingConfig, tuneProfilerConfig},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var config map[string]interface{}
			if err := yaml.Unmarshal([]byte(substitute(tc.asset, cfg)), &config); err != nil {
				t.Fatal(err)
			}
			want := runtime.DeepCopyJSON(config)
			if err := tc.tune(config, cfg); err != nil {
				t.Fatal(err)
			}
			if err := tuneExtras(config, v1alpha1.CollectorTuning{}); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(config, want) {
				t.Fatalf("unset tuning changed the config:\n got: %v\nwant: %v", config, want)
			}
		})
	}
}

func TestHealthCheckEndpointOverride(t *testing.T) {
	cfg := effective()
	cfg.Gateway.HealthCheckEndpoint = ptr.To("0.0.0.0:13133")
	config := collectorConfig(t, cfg, gatewayName)
	if got := configField(t, config, "extensions", "health_check", "endpoint"); got != "0.0.0.0:13133" {
		t.Fatalf("endpoint = %v", got)
	}
}

func TestHealthCheckEndpointEmptyRemovesExtension(t *testing.T) {
	cfg := effective()
	cfg.Prometheus.Enabled = true
	cfg.Prometheus.HealthCheckEndpoint = ptr.To("")
	config := collectorConfig(t, cfg, scraperName)
	if configField(t, config, "extensions", "health_check") != nil {
		t.Fatal("health_check extension still present")
	}
	for _, e := range stringSlice(configField(t, config, "service", "extensions")) {
		if e == "health_check" {
			t.Fatal("service.extensions still lists health_check")
		}
	}
}

func TestExtraMappingsAppend(t *testing.T) {
	m := v1alpha1.AttributeMapping{TagName: "app.version", Key: "app.version", From: "pod"}
	want := map[string]interface{}{"tag_name": "app.version", "key": "app.version", "from": "pod"}

	cfg := effective()
	cfg.Agent.ExtraLabelMapping = []v1alpha1.AttributeMapping{m}
	cfg.Gateway.ExtraAnnotationsMapping = []v1alpha1.AttributeMapping{m}

	labels := configField(t, collectorConfig(t, cfg, agentName), "processors", "k8s_attributes", "extract", "labels").([]interface{})
	if got := labels[len(labels)-1]; !reflect.DeepEqual(got, want) {
		t.Fatalf("agent last label = %v", got)
	}
	if len(labels) < 3 || labels[0].(map[string]interface{})["tag_name"] != "env" || labels[1].(map[string]interface{})["tag_name"] != "team" {
		t.Fatalf("built-in labels lost: %v", labels)
	}
	annotations := configField(t, collectorConfig(t, cfg, gatewayName), "processors", "k8s_attributes", "extract", "annotations").([]interface{})
	if got := annotations[len(annotations)-1]; !reflect.DeepEqual(got, want) {
		t.Fatalf("gateway last annotation = %v", got)
	}
}

func TestK8sAttributesMetadataReplaces(t *testing.T) {
	cfg := allCollectorsOn()
	cfg.K8sAttributes.Metadata = []string{"k8s.namespace.name"}
	for _, name := range []string{agentName, gatewayName, scraperName} {
		got := configField(t, collectorConfig(t, cfg, name), "processors", "k8s_attributes", "extract", "metadata")
		if !reflect.DeepEqual(got, []interface{}{"k8s.namespace.name"}) {
			t.Errorf("%s metadata = %v", name, got)
		}
	}
	got := configField(t, collectorConfig(t, cfg, profilerName), "processors", "k8s_attributes", "extract", "metadata").([]interface{})
	if len(got) < 2 {
		t.Fatalf("profiler metadata replaced: %v", got)
	}
}

func TestBatchSettings(t *testing.T) {
	cfg := allCollectorsOn()
	cfg.Batch = v1alpha1.BatchSpec{SendBatchSize: ptr.To(int32(1000)), SendBatchMaxSize: ptr.To(int32(2000)), Timeout: "5s"}
	for _, name := range []string{agentName, gatewayName, scraperName} {
		batch := configField(t, collectorConfig(t, cfg, name), "processors", "batch").(map[string]interface{})
		if toFloat(t, batch["send_batch_size"]) != 1000 || toFloat(t, batch["send_batch_max_size"]) != 2000 || batch["timeout"] != "5s" {
			t.Errorf("%s batch = %v", name, batch)
		}
	}
	if configField(t, collectorConfig(t, cfg, profilerName), "processors", "batch") != nil {
		t.Fatal("profiler gained a batch processor")
	}
}

func TestGatewayClusterTuning(t *testing.T) {
	cfg := effective()
	cfg.Gateway.CollectionInterval = "30s"
	cfg.Gateway.NodeConditionsToReport = []string{"Ready"}
	cfg.Gateway.AllocatableTypesToReport = []string{"cpu"}
	cluster := configField(t, collectorConfig(t, cfg, gatewayName), "receivers", "k8s_cluster").(map[string]interface{})
	if cluster["collection_interval"] != "30s" ||
		!reflect.DeepEqual(cluster["node_conditions_to_report"], []interface{}{"Ready"}) ||
		!reflect.DeepEqual(cluster["allocatable_types_to_report"], []interface{}{"cpu"}) {
		t.Fatalf("k8s_cluster = %v", cluster)
	}
}

func TestProfilerTuning(t *testing.T) {
	cfg := allCollectorsOn()
	cfg.Profiling.K8sAttributesMetadata = []string{"k8s.node.name"}
	config := collectorConfig(t, cfg, profilerName)
	if got := configField(t, config, "processors", "k8s_attributes", "extract", "metadata"); !reflect.DeepEqual(got, []interface{}{"k8s.node.name"}) {
		t.Fatalf("metadata = %v", got)
	}
}

func TestProfilerServiceNameEnvVar(t *testing.T) {
	cfg := allCollectorsOn()
	cfg.Profiling.ServiceNameEnvVar = "APP_NAME"
	config := collectorConfig(t, cfg, profilerName)
	if got := configField(t, config, "receivers", "profiling", "include_env_vars"); got != "APP_NAME" {
		t.Fatalf("include_env_vars = %v", got)
	}
	transform := configField(t, config, "processors", "transform/service_name")
	if transform == nil {
		t.Fatal("transform/service_name missing")
	}
	b, _ := yaml.Marshal(transform)
	if !strings.Contains(string(b), "process.environment_variable.APP_NAME") {
		t.Fatalf("statements do not reference the variable:\n%s", b)
	}
	got := stringSlice(configField(t, config, "service", "pipelines", "profiles", "processors"))
	want := []string{"memory_limiter", "transform/service_name", "k8s_attributes"}
	if len(got) < 3 || !reflect.DeepEqual(got[:3], want) {
		t.Fatalf("profiles processors = %v", got)
	}
}

const wantEmptyScrapersMessage = "agent.hostMetrics.scrapers is empty: the host_metrics receiver needs at least one scraper"

func agentVolumeNames(t *testing.T, cfg v1alpha1.TsugaCollectorConfigSpec) []string {
	t.Helper()
	vols, _, _ := unstructured.NestedSlice(collectorByName(t, cfg, agentName).Object, "spec", "volumes")
	names := make([]string, 0, len(vols))
	for _, v := range vols {
		names = append(names, v.(map[string]interface{})["name"].(string))
	}
	return names
}

func TestAgentOTLPEndpoints(t *testing.T) {
	cfg := effective()
	cfg.Agent.OTLP.GRPCEndpoint = "0.0.0.0:4317"
	config := collectorConfig(t, cfg, agentName)
	if got := configField(t, config, "receivers", "otlp", "protocols", "grpc", "endpoint"); got != "0.0.0.0:4317" {
		t.Fatalf("grpc endpoint = %v", got)
	}
	if got := configField(t, config, "receivers", "otlp", "protocols", "http", "endpoint"); got != "${env:MY_POD_IP}:4318" {
		t.Fatalf("http endpoint = %v", got)
	}
}

func TestAgentCollectLogsOff(t *testing.T) {
	cfg := effective()
	cfg.Agent.CollectLogs = ptr.To(false)
	config := collectorConfig(t, cfg, agentName)
	if configField(t, config, "receivers", "file_log") != nil {
		t.Fatal("file_log still present")
	}
	if got := stringSlice(configField(t, config, "service", "pipelines", "logs", "receivers")); !reflect.DeepEqual(got, []string{"otlp"}) {
		t.Fatalf("logs receivers = %v", got)
	}
	if slices.Contains(agentVolumeNames(t, cfg), "varlogpods") {
		t.Fatal("varlogpods volume present")
	}

	cfg.Agent.AddLogsVolumes = ptr.To(true)
	if !slices.Contains(agentVolumeNames(t, cfg), "varlogpods") {
		t.Fatal("addLogsVolumes did not keep the volume")
	}
	if configField(t, collectorConfig(t, cfg, agentName), "receivers", "file_log") != nil {
		t.Fatal("file_log present with addLogsVolumes")
	}
}

func TestAgentCollectOtelLogs(t *testing.T) {
	cfg := effective()
	cfg.Agent.CollectOtelLogs = true
	exclude := stringSlice(configField(t, collectorConfig(t, cfg, agentName), "receivers", "file_log", "exclude"))
	for _, p := range []string{"/var/log/pods/*/otc-container/*.log", "/var/log/pods/*/otel-collector/*.log"} {
		if slices.Contains(exclude, p) {
			t.Errorf("exclude still has %s", p)
		}
	}
}

func TestAgentFileLogIncludeExclude(t *testing.T) {
	cfg := effective()
	cfg.Agent.FileLog.Include = []string{"/var/log/pods/app_*/*/*.log"}
	cfg.Agent.FileLog.Exclude = []string{"/var/log/pods/kube-system_*/*/*.log"}
	fl := configField(t, collectorConfig(t, cfg, agentName), "receivers", "file_log").(map[string]interface{})
	if got := stringSlice(fl["include"]); !reflect.DeepEqual(got, []string{"/var/log/pods/app_*/*/*.log"}) {
		t.Fatalf("include = %v", got)
	}
	want := []string{"/var/log/pods/*/otc-container/*.log", "/var/log/pods/*/otel-collector/*.log", "/var/log/pods/kube-system_*/*/*.log"}
	if got := stringSlice(fl["exclude"]); !reflect.DeepEqual(got, want) {
		t.Fatalf("exclude = %v", got)
	}
}

func TestAgentHostMetricsInterval(t *testing.T) {
	cfg := effective()
	cfg.Agent.HostMetrics.CollectionInterval = "30s"
	if got := configField(t, collectorConfig(t, cfg, agentName), "receivers", "host_metrics", "collection_interval"); got != "30s" {
		t.Fatalf("collection_interval = %v", got)
	}
}

func TestAgentCollectNetworkAndProcesses(t *testing.T) {
	cfg := effective()
	cfg.Agent.CollectNetwork = true
	cfg.Agent.CollectProcesses = true
	scrapers := configField(t, collectorConfig(t, cfg, agentName), "receivers", "host_metrics", "scrapers").(map[string]interface{})
	wantNetwork := map[string]interface{}{"metrics": map[string]interface{}{
		"system.network.conntrack.count": map[string]interface{}{"enabled": true},
		"system.network.conntrack.max":   map[string]interface{}{"enabled": true},
	}}
	wantProcess := map[string]interface{}{
		"mute_process_all_errors": true,
		"resource_attributes":     map[string]interface{}{"process.executable.path": map[string]interface{}{"enabled": false}},
		"metrics":                 map[string]interface{}{"process.uptime": map[string]interface{}{"enabled": true}},
	}
	if !reflect.DeepEqual(scrapers["network"], wantNetwork) {
		t.Errorf("network = %v", scrapers["network"])
	}
	if !reflect.DeepEqual(scrapers["process"], wantProcess) {
		t.Errorf("process = %v", scrapers["process"])
	}
	if !reflect.DeepEqual(scrapers["processes"], map[string]interface{}{}) {
		t.Errorf("processes = %v", scrapers["processes"])
	}

	off := configField(t, collectorConfig(t, effective(), agentName), "receivers", "host_metrics", "scrapers").(map[string]interface{})
	for _, k := range []string{"network", "process", "processes"} {
		if _, ok := off[k]; ok {
			t.Errorf("%s present by default", k)
		}
	}
}

func TestAgentCustomScrapers(t *testing.T) {
	cfg := effective()
	cfg.Agent.HostMetrics.Scrapers = rawJSON(`{"cpu":{},"network":{}}`)
	got := configField(t, collectorConfig(t, cfg, agentName), "receivers", "host_metrics", "scrapers")
	if !reflect.DeepEqual(got, map[string]interface{}{"cpu": map[string]interface{}{}, "network": map[string]interface{}{}}) {
		t.Fatalf("scrapers = %v", got)
	}
}

func TestAgentScrapersNullEntryDropsScraper(t *testing.T) {
	cfg := effective()
	cfg.Agent.HostMetrics.Scrapers = rawJSON(`{"cpu":null,"memory":{}}`)
	got := configField(t, collectorConfig(t, cfg, agentName), "receivers", "host_metrics", "scrapers")
	if !reflect.DeepEqual(got, map[string]interface{}{"memory": map[string]interface{}{}}) {
		t.Fatalf("scrapers = %v", got)
	}
}

func TestAgentEmptyScrapersFailsRender(t *testing.T) {
	cfg := effective()
	cfg.Agent.HostMetrics.Scrapers = rawJSON(`{"cpu":null}`)
	_, err := RenderCollectors(cfg)
	if err == nil || !strings.Contains(err.Error(), wantEmptyScrapersMessage) {
		t.Fatalf("err = %v", err)
	}
}

func TestAgentKubeletStats(t *testing.T) {
	cfg := effective()
	cfg.Agent.KubeletStats = v1alpha1.KubeletStatsSpec{
		CollectionInterval: "60s",
		InsecureSkipVerify: ptr.To(false),
		CAFile:             "/etc/kubelet-ca/ca.crt",
		MetricGroups:       []string{"node", "pod"},
	}
	k := configField(t, collectorConfig(t, cfg, agentName), "receivers", "kubelet_stats").(map[string]interface{})
	if k["collection_interval"] != "60s" || k["insecure_skip_verify"] != false ||
		k["ca_file"] != "/etc/kubelet-ca/ca.crt" || !reflect.DeepEqual(k["metric_groups"], []interface{}{"node", "pod"}) {
		t.Fatalf("kubelet_stats = %v", k)
	}
}

func TestAgentKubeletUsePodsEndpointOff(t *testing.T) {
	cfg := effective()
	cfg.Agent.KubeletStats.UsePodsEndpoint = ptr.To(false)
	k := configField(t, collectorConfig(t, cfg, agentName), "receivers", "kubelet_stats").(map[string]interface{})
	if _, ok := k["extra_metadata_labels"]; ok {
		t.Fatal("extra_metadata_labels still present")
	}
	metrics, _ := k["metrics"].(map[string]interface{})
	for _, kind := range []string{"container", "pod"} {
		for _, res := range []string{"cpu", "memory"} {
			for _, suffix := range []string{"limit", "request"} {
				name := "k8s." + kind + "." + res + "_" + suffix + "_utilization"
				if _, ok := metrics[name]; ok {
					t.Errorf("%s still present", name)
				}
			}
		}
	}
}

func TestAgentSpanMetrics(t *testing.T) {
	cfg := effective()
	cfg.Agent.SpanMetrics.AggregationTemporality = "AGGREGATION_TEMPORALITY_CUMULATIVE"
	cfg.Agent.SpanMetrics.Dimensions = []v1alpha1.SpanMetricsDimension{{Name: "http.route"}, {Name: "env", Default: "prod"}}
	sm := configField(t, collectorConfig(t, cfg, agentName), "connectors", "span_metrics").(map[string]interface{})
	if sm["aggregation_temporality"] != "AGGREGATION_TEMPORALITY_CUMULATIVE" {
		t.Fatalf("temporality = %v", sm["aggregation_temporality"])
	}
	want := []interface{}{
		map[string]interface{}{"name": "http.route"},
		map[string]interface{}{"name": "env", "default": "prod"},
	}
	if !reflect.DeepEqual(sm["dimensions"], want) {
		t.Fatalf("dimensions = %v", sm["dimensions"])
	}
}

func TestAgentSpanMetricsOff(t *testing.T) {
	cfg := effective()
	cfg.Agent.SpanMetrics.Enabled = ptr.To(false)
	config := collectorConfig(t, cfg, agentName)
	if configField(t, config, "connectors", "span_metrics") != nil {
		t.Fatal("connector still present")
	}
	pipelines := configField(t, config, "service", "pipelines").(map[string]interface{})
	for name, p := range pipelines {
		pm := p.(map[string]interface{})
		if slices.Contains(stringSlice(pm["receivers"]), "span_metrics") || slices.Contains(stringSlice(pm["exporters"]), "span_metrics") {
			t.Errorf("pipeline %s still references span_metrics", name)
		}
	}
}

const backupExporter = `{"otlp_http/backup": {"endpoint":"https://b.example","headers":{"Authorization":"Bearer ${env:BACKUP_TOKEN}"}}}`

func TestExportEncodingCompression(t *testing.T) {
	cfg := effective()
	cfg.Prometheus.Enabled = true
	cfg.Profiling.Enabled = true
	cfg.Export.Encoding = "proto"
	cfg.Export.Compression = "zstd"
	for _, name := range []string{agentName, gatewayName, scraperName, profilerName} {
		exp := configField(t, collectorConfig(t, cfg, name), "exporters", "otlp_http/tsuga").(map[string]interface{})
		if exp["encoding"] != "proto" || exp["compression"] != "zstd" {
			t.Fatalf("%s exporter = %v", name, exp)
		}
	}
	cfg.Export.Encoding = "json"
	exp := configField(t, collectorConfig(t, cfg, profilerName), "exporters", "otlp_http/tsuga").(map[string]interface{})
	if exp["encoding"] != "proto" {
		t.Fatalf("profiler encoding = %v, want proto", exp["encoding"])
	}
}

func TestExtraExporterRoutedPerSignal(t *testing.T) {
	cfg := effective()
	cfg.Agent.ExtraExporters = rawJSON(backupExporter)
	cfg.Agent.PipelineExporters.Traces = []string{"otlp_http/backup"}
	config := collectorConfig(t, cfg, agentName)
	want := map[string]interface{}{
		"endpoint": "https://b.example",
		"headers":  map[string]interface{}{"Authorization": "Bearer ${env:BACKUP_TOKEN}"},
	}
	if got := configField(t, config, "exporters", "otlp_http/backup"); !reflect.DeepEqual(got, want) {
		t.Fatalf("backup exporter = %v", got)
	}
	traces := pipelineList(t, config, "traces", "exporters")
	if !slices.Contains(traces, "otlp_http/tsuga") || !slices.Contains(traces, "otlp_http/backup") {
		t.Fatalf("traces exporters = %v", traces)
	}
	if slices.Contains(pipelineList(t, config, "metrics", "exporters"), "otlp_http/backup") {
		t.Fatal("metrics pipeline got the traces-only exporter")
	}
}

func TestPipelineExportersLogsReachEvents(t *testing.T) {
	cfg := effective()
	cfg.Gateway.ExtraExporters = rawJSON(backupExporter)
	cfg.Gateway.PipelineExporters.Logs = []string{"otlp_http/backup"}
	config := collectorConfig(t, cfg, gatewayName)
	for _, p := range []string{"logs", "logs/events"} {
		if !slices.Contains(pipelineList(t, config, p, "exporters"), "otlp_http/backup") {
			t.Fatalf("%s exporters = %v", p, pipelineList(t, config, p, "exporters"))
		}
	}
}

func TestExportToTsugaOffOnAgentOnly(t *testing.T) {
	cfg := effective()
	cfg.Agent.ExportToTsuga = ptr.To(false)
	cfg.Agent.ExtraExporters = rawJSON(backupExporter)
	cfg.Agent.PipelineExporters = v1alpha1.PipelineComponents{
		Traces: []string{"otlp_http/backup"}, Metrics: []string{"otlp_http/backup"}, Logs: []string{"otlp_http/backup"},
	}
	gateway := collectorConfig(t, cfg, gatewayName)
	if !slices.Contains(pipelineList(t, gateway, "metrics", "exporters"), "otlp_http/tsuga") ||
		configField(t, gateway, "service", "telemetry", "metrics", "readers") == nil {
		t.Fatal("gateway lost its Tsuga export")
	}
}

func TestTsugaLessRender(t *testing.T) {
	cfg := effective()
	cfg.Export = nil
	for _, tuning := range []*v1alpha1.CollectorTuning{&cfg.Agent.CollectorTuning, &cfg.Gateway.CollectorTuning} {
		tuning.ExportToTsuga = ptr.To(false)
		tuning.ExtraExporters = rawJSON(backupExporter)
		tuning.PipelineExporters = v1alpha1.PipelineComponents{
			Traces: []string{"otlp_http/backup"}, Metrics: []string{"otlp_http/backup"}, Logs: []string{"otlp_http/backup"},
		}
	}
	objs, err := RenderCollectors(cfg)
	if err != nil {
		t.Fatal(err)
	}
	for _, o := range objs {
		out, err := yamlRoundtrip(o)
		if err != nil {
			t.Fatal(err)
		}
		if cfg := configField(t, o.Object, "spec", "config"); cfg != nil {
			assertTelemetryOff(t, cfg.(map[string]interface{}))
		}
		for _, banned := range []string{"otlp_http/tsuga", "TSUGA_API_KEY", "TSUGA_OTLP_ENDPOINT", "__TSUGA"} {
			if strings.Contains(out, banned) {
				t.Fatalf("%s still contains %q", o.GetName(), banned)
			}
		}
	}

	cfg.Prometheus.Enabled = true
	_, err = RenderCollectors(cfg)
	want := "tsuga-scraper: exportToTsuga needs spec.export (tokenSecretRef and an endpoint)"
	if err == nil || err.Error() != want {
		t.Fatalf("err = %v, want %q", err, want)
	}
}

func TestExportRenderErrors(t *testing.T) {
	cases := []struct {
		name string
		edit func(*v1alpha1.TsugaCollectorConfigSpec)
		want string
	}{
		{"redefine", func(c *v1alpha1.TsugaCollectorConfigSpec) {
			c.Agent.ExtraExporters = rawJSON(`{"otlp_http/tsuga":{}}`)
		}, "tsuga-agent: exporter otlp_http/tsuga is built in; choose another name"},
		{"connector name", func(c *v1alpha1.TsugaCollectorConfigSpec) {
			c.Agent.ExtraExporters = rawJSON(`{"span_metrics":{}}`)
		}, "tsuga-agent: exporter span_metrics is built in; choose another name"},
		{"no exporter", func(c *v1alpha1.TsugaCollectorConfigSpec) {
			c.Agent.ExportToTsuga = ptr.To(false)
		}, "tsuga-agent: pipeline logs has no exporter; keep exportToTsuga or add pipelineExporters.logs"},
		{"no export", func(c *v1alpha1.TsugaCollectorConfigSpec) {
			c.Export = nil
		}, "tsuga-agent: exportToTsuga needs spec.export (tokenSecretRef and an endpoint)"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := effective()
			tc.edit(&cfg)
			_, err := RenderCollectors(cfg)
			if err == nil || err.Error() != tc.want {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
		})
	}
}

func assertTelemetryOff(t *testing.T, config map[string]interface{}) {
	t.Helper()
	metrics, _ := configField(t, config, "service", "telemetry", "metrics").(map[string]interface{})
	if _, has := metrics["readers"]; has || metrics["level"] != "none" {
		t.Fatalf("service.telemetry.metrics = %v, want {level: none}", metrics)
	}
}

func detectionOn(cfg v1alpha1.TsugaCollectorConfigSpec) v1alpha1.TsugaCollectorConfigSpec {
	cfg.ResourceDetection.Enabled = true
	return cfg
}

func TestResourceDetection(t *testing.T) {
	cases := []struct {
		name      string
		detectors []string
		timeout   string
		want      map[string]interface{}
	}{
		{"defaults", nil, "", map[string]interface{}{"detectors": []interface{}{"env"}, "timeout": "15s", "override": false}},
		{"custom", []string{"env", "eks", "ec2"}, "30s", map[string]interface{}{"detectors": []interface{}{"env", "eks", "ec2"}, "timeout": "30s", "override": false}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := detectionOn(allCollectorsOn())
			if tc.detectors != nil {
				cfg.ResourceDetection.Detectors = tc.detectors
				cfg.ResourceDetection.Timeout = tc.timeout
			}
			for _, name := range []string{agentName, gatewayName, scraperName, profilerName} {
				if got := configField(t, collectorConfig(t, cfg, name), "processors", "resource_detection"); !reflect.DeepEqual(got, tc.want) {
					t.Fatalf("%s resource_detection = %v", name, got)
				}
			}
		})
	}
}

func TestResourceDetectionPosition(t *testing.T) {
	cfg := detectionOn(allCollectorsOn())
	cfg.Profiling.ServiceNameEnvVar = "APP_NAME"
	cases := []struct {
		collector string
		pipelines []string
	}{
		{agentName, []string{"traces", "metrics", "logs"}},
		{gatewayName, []string{"metrics", "logs", "logs/events"}},
		{scraperName, []string{"metrics"}},
		{profilerName, []string{"profiles"}},
	}
	for _, tc := range cases {
		config := collectorConfig(t, cfg, tc.collector)
		for _, p := range tc.pipelines {
			got := pipelineList(t, config, p, "processors")
			if i := slices.Index(got, "resource_detection"); i < 1 || got[i-1] != "memory_limiter" && got[i-1] != "cumulative_to_delta" {
				t.Fatalf("%s %s processors = %v", tc.collector, p, got)
			}
		}
	}
	agentMetrics := pipelineList(t, collectorConfig(t, cfg, agentName), "metrics", "processors")
	wantMetrics := []string{"memory_limiter", "cumulative_to_delta", "resource_detection", "k8s_attributes", "resource", "resource/node", "batch"}
	if !reflect.DeepEqual(agentMetrics, wantMetrics) {
		t.Fatalf("agent metrics processors = %v", agentMetrics)
	}
	profiles := pipelineList(t, collectorConfig(t, cfg, profilerName), "profiles", "processors")
	wantProfiles := []string{"memory_limiter", "resource_detection", "transform/service_name", "k8s_attributes", "transform/host_processes", "resource"}
	if !reflect.DeepEqual(profiles, wantProfiles) {
		t.Fatalf("profiler processors = %v", profiles)
	}
}

func TestRedactionAfterPipelineProcessors(t *testing.T) {
	cfg := redactionOn(effective())
	cfg.Agent.ExtraProcessors = rawJSON(`{"filter/health":{}}`)
	cfg.Agent.PipelineProcessors.Traces = []string{"filter/health"}
	got := pipelineList(t, collectorConfig(t, cfg, agentName), "traces", "processors")
	if !slices.Equal(got[len(got)-3:], []string{"filter/health", "redaction", "batch"}) {
		t.Errorf("traces processors = %v", got)
	}
}

func redactionOn(cfg v1alpha1.TsugaCollectorConfigSpec) v1alpha1.TsugaCollectorConfigSpec {
	cfg.Redaction.Enabled = true
	return cfg
}

func TestRedactionDefault(t *testing.T) {
	var want map[string]interface{}
	if err := yaml.Unmarshal([]byte(assets.RedactionDefault), &want); err != nil {
		t.Fatal(err)
	}
	got := configField(t, collectorConfig(t, redactionOn(allCollectorsOn()), agentName), "processors", "redaction")
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("redaction = %v", got)
	}
}

func TestRedactionUserConfig(t *testing.T) {
	cfg := redactionOn(allCollectorsOn())
	cfg.Redaction.Config = rawJSON(`{"allow_all_keys":false,"allowed_keys":["a"]}`)
	want := map[string]interface{}{"allow_all_keys": false, "allowed_keys": []interface{}{"a"}}
	if got := configField(t, collectorConfig(t, cfg, agentName), "processors", "redaction"); !reflect.DeepEqual(got, want) {
		t.Fatalf("redaction = %v", got)
	}
}

func TestRedactionPosition(t *testing.T) {
	cfg := redactionOn(allCollectorsOn())
	config := collectorConfig(t, cfg, agentName)
	for _, p := range []string{"traces", "metrics", "logs"} {
		got := pipelineList(t, config, p, "processors")
		i := slices.Index(got, "redaction")
		if i < 1 || got[i+1] != "batch" {
			t.Fatalf("agent %s processors = %v", p, got)
		}
	}
	for _, name := range []string{gatewayName, scraperName, profilerName} {
		c := collectorConfig(t, cfg, name)
		if _, found := nestedMap(c, "processors")["redaction"]; found {
			t.Fatalf("%s has a redaction processor", name)
		}
		for p := range nestedMap(nestedMap(c, "service"), "pipelines") {
			if slices.Contains(pipelineList(t, c, p, "processors"), "redaction") {
				t.Fatalf("%s pipeline %s references redaction", name, p)
			}
		}
	}
}

func TestEnrichmentCoversExtraPipelines(t *testing.T) {
	cfg := redactionOn(detectionOn(effective()))
	cfg.Agent.ExtraProcessors = rawJSON(`{"filter/x":{}}`)
	cfg.Agent.ExtraPipelines = rawJSON(`{
		"logs/plain":{"receivers":["otlp"],"exporters":["otlp_http/tsuga"]},
		"logs/batched":{"receivers":["otlp"],"processors":["filter/x","batch"],"exporters":["otlp_http/tsuga"]}}`)
	cfg.Agent.PipelineProcessors = v1alpha1.PipelineComponents{Logs: []string{"filter/x"}}
	config := collectorConfig(t, cfg, agentName)
	for p, want := range map[string][]string{
		"logs/plain":   {"resource_detection", "filter/x", "redaction"},
		"logs/batched": {"resource_detection", "filter/x", "redaction", "batch"},
		"logs":         {"memory_limiter", "resource_detection", "k8s_attributes", "resource", "resource/node", "filter/x", "redaction", "batch"},
	} {
		if got := pipelineList(t, config, p, "processors"); !reflect.DeepEqual(got, want) {
			t.Errorf("%s processors = %v, want %v", p, got, want)
		}
	}
}

func TestRedactionDeleteEverythingFails(t *testing.T) {
	cfg := redactionOn(allCollectorsOn())
	cfg.Redaction.Config = rawJSON(`{"allow_all_keys": false}`)
	_, err := RenderCollectors(cfg)
	want := "tsuga-agent: redaction config would delete every attribute; set allow_all_keys: true or list allowed_keys"
	if err == nil || err.Error() != want {
		t.Fatalf("err = %v", err)
	}
}
