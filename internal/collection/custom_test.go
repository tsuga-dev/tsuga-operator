package collection

import (
	"reflect"
	"strings"
	"testing"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	utiljson "k8s.io/apimachinery/pkg/util/json"
	"k8s.io/utils/ptr"

	"github.com/tsuga-dev/tsuga-operator/api/v1alpha1"
)

const (
	testCustomGRPC = "tsuga-agent-collector.tsuga-operator-system.svc.cluster.local:4317"
	testCustomHTTP = "http://tsuga-agent-collector.tsuga-operator-system.svc.cluster.local:4318"
)

func customCluster() v1alpha1.TsugaCollectorConfigSpec {
	return ResolveCollector(v1alpha1.TsugaCollectorConfigSpec{
		ClusterName: "demo",
		Agent:       v1alpha1.TelemetryToggles{Traces: true, Metrics: true, Logs: true},
	})
}

func customSpec(collector string, mutate ...func(*v1alpha1.TsugaCollectorSpec)) v1alpha1.TsugaCollectorSpec {
	spec := v1alpha1.TsugaCollectorSpec{Collector: &apiextensionsv1.JSON{Raw: []byte(collector)}}
	for _, m := range mutate {
		m(&spec)
	}
	return spec
}

func togglesOff(s *v1alpha1.TsugaCollectorSpec) {
	s.TsugaExporter, s.SelfTelemetry, s.DefaultProcessors = ptr.To(false), ptr.To(false), ptr.To(false)
}

func onlyToggle(name string) func(*v1alpha1.TsugaCollectorSpec) {
	return func(s *v1alpha1.TsugaCollectorSpec) {
		togglesOff(s)
		switch name {
		case "exporter":
			s.TsugaExporter = ptr.To(true)
		case "telemetry":
			s.SelfTelemetry = ptr.To(true)
		case "processors":
			s.DefaultProcessors = ptr.To(true)
		}
	}
}

func renderCustomWith(t *testing.T, cluster v1alpha1.TsugaCollectorConfigSpec, collector string, mutate ...func(*v1alpha1.TsugaCollectorSpec)) *unstructured.Unstructured {
	t.Helper()
	obj, err := RenderCustomCollector("ndm", "observability", customSpec(collector, mutate...), cluster, testCustomGRPC, testCustomHTTP)
	if err != nil {
		t.Fatal(err)
	}
	return obj
}

func renderCustom(t *testing.T, collector string, mutate ...func(*v1alpha1.TsugaCollectorSpec)) *unstructured.Unstructured {
	t.Helper()
	return renderCustomWith(t, customCluster(), collector, mutate...)
}

func pipelineField(t *testing.T, obj *unstructured.Unstructured, pipeline, field string) []string {
	t.Helper()
	list, _, err := unstructured.NestedStringSlice(obj.Object, "spec", "config", "service", "pipelines", pipeline, field)
	if err != nil {
		t.Fatal(err)
	}
	return list
}

func mustJSON(t *testing.T, s string) map[string]interface{} {
	t.Helper()
	out := map[string]interface{}{}
	if err := utiljson.Unmarshal([]byte(s), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

const ndmCollector = `{
  "mode": "deployment",
  "ports": [{"name": "netflow", "port": 2055, "protocol": "UDP"}],
  "env": [{"name": "SNMP_COMMUNITY", "value": "public"}],
  "config": {
    "receivers": {"netflow": {"scheme": "netflow", "port": 2055}},
    "processors": {"transform/hw": {"metric_statements": []}},
    "extensions": {"opamp": {"server": {"ws": {"endpoint": "ws://ofm:4320/v1/opamp"}}}},
    "service": {
      "extensions": ["opamp"],
      "pipelines": {"logs/netflow": {"receivers": ["netflow"], "processors": ["transform/hw"]}}
    }
  }
}`

func TestCustomCollectorName(t *testing.T) {
	if got := CustomCollectorName("ndm"); got != "tsuga-ndm" {
		t.Fatalf("got %q", got)
	}
}

func TestRenderCustomCollectorPassesThrough(t *testing.T) {
	obj := renderCustom(t, ndmCollector)
	if obj.GetName() != "tsuga-ndm" || obj.GetNamespace() != "observability" {
		t.Fatalf("metadata = %s/%s", obj.GetNamespace(), obj.GetName())
	}
	if obj.GetAPIVersion() != "opentelemetry.io/v1beta1" || obj.GetKind() != "OpenTelemetryCollector" {
		t.Fatalf("gvk = %s %s", obj.GetAPIVersion(), obj.GetKind())
	}
	in := mustJSON(t, ndmCollector)
	spec := obj.Object["spec"].(map[string]interface{})
	for _, key := range []string{"mode", "ports"} {
		if !reflect.DeepEqual(spec[key], in[key]) {
			t.Fatalf("%s = %v, want %v", key, spec[key], in[key])
		}
	}
	env := sliceOf(spec["env"])
	if !reflect.DeepEqual(env[0], sliceOf(in["env"])[0]) {
		t.Fatalf("user env not first and unchanged: %v", env)
	}
	config := spec["config"].(map[string]interface{})
	inConfig := in["config"].(map[string]interface{})
	if !reflect.DeepEqual(config["extensions"], inConfig["extensions"]) {
		t.Fatalf("extensions changed: %v", config["extensions"])
	}
	if !reflect.DeepEqual(asMapOrEmpty(config["processors"])["transform/hw"], asMapOrEmpty(inConfig["processors"])["transform/hw"]) {
		t.Fatal("user processor changed")
	}
	if got := stringSlice(asMapOrEmpty(config["service"])["extensions"]); !reflect.DeepEqual(got, []string{"opamp"}) {
		t.Fatalf("service.extensions = %v", got)
	}
}

func TestRenderCustomCollectorAllTogglesOff(t *testing.T) {
	obj := renderCustom(t, ndmCollector, togglesOff)
	config, _, _ := unstructured.NestedMap(obj.Object, "spec", "config")
	if !reflect.DeepEqual(config, mustJSON(t, ndmCollector)["config"]) {
		t.Fatalf("config changed with every toggle off: %v", config)
	}
}

func TestRenderCustomCollectorDefaultProcessors(t *testing.T) {
	obj := renderCustom(t, `{"config": {
	  "receivers": {"otlp": {}},
	  "processors": {"x": {}, "transform/hw": {}},
	  "service": {"pipelines": {
	    "logs/a": {"receivers": ["otlp"], "processors": ["transform/hw"]},
	    "logs/b": {"receivers": ["otlp"], "processors": ["batch", "x"]}
	  }}}}`, onlyToggle("processors"))
	if got := pipelineField(t, obj, "logs/a", "processors"); !reflect.DeepEqual(got, []string{"memory_limiter", "transform/hw", "batch"}) {
		t.Fatalf("logs/a processors = %v", got)
	}
	if got := pipelineField(t, obj, "logs/b", "processors"); !reflect.DeepEqual(got, []string{"memory_limiter", "batch", "x"}) {
		t.Fatalf("logs/b processors = %v", got)
	}
	processors, _, _ := unstructured.NestedMap(obj.Object, "spec", "config", "processors")
	want := map[string]interface{}{"check_interval": "5s", "limit_percentage": int64(80), "spike_limit_percentage": int64(25)}
	if !reflect.DeepEqual(processors["memory_limiter"], want) {
		t.Fatalf("memory_limiter = %#v", processors["memory_limiter"])
	}
	if !reflect.DeepEqual(processors["batch"], map[string]interface{}{}) {
		t.Fatalf("batch = %#v", processors["batch"])
	}
}

func TestRenderCustomCollectorDefaultProcessorsKeepUserTypes(t *testing.T) {
	collector := `{"config": {
	  "receivers": {"otlp": {}},
	  "processors": {"memory_limiter/custom": {"limit_mib": 384}, "batch/big": {"send_batch_size": 1000}, "x": {}},
	  "service": {"pipelines": {
	    "metrics": {"receivers": ["otlp"], "processors": ["memory_limiter/custom", "x", "batch/big"]}
	  }}}}`
	obj := renderCustom(t, collector, onlyToggle("processors"))
	if got := pipelineField(t, obj, "metrics", "processors"); !reflect.DeepEqual(got, []string{"memory_limiter/custom", "x", "batch/big"}) {
		t.Fatalf("processors = %v", got)
	}
	processors, _, _ := unstructured.NestedMap(obj.Object, "spec", "config", "processors")
	if !reflect.DeepEqual(processors, asMapOrEmpty(mustJSON(t, collector)["config"].(map[string]interface{})["processors"])) {
		t.Fatalf("processor definitions changed: %v", processors)
	}
}

func TestRenderCustomCollectorDefaultProcessorsKeepUserBodies(t *testing.T) {
	obj := renderCustom(t, `{"config": {
	  "receivers": {"otlp": {}},
	  "processors": {"memory_limiter": {"limit_mib": 384}, "batch": {"send_batch_size": 1000}},
	  "service": {"pipelines": {"metrics": {"receivers": ["otlp"]}}}}}`, onlyToggle("processors"))
	processors, _, _ := unstructured.NestedMap(obj.Object, "spec", "config", "processors")
	if !reflect.DeepEqual(processors["memory_limiter"], map[string]interface{}{"limit_mib": int64(384)}) {
		t.Fatalf("memory_limiter = %#v", processors["memory_limiter"])
	}
	if !reflect.DeepEqual(processors["batch"], map[string]interface{}{"send_batch_size": int64(1000)}) {
		t.Fatalf("batch = %#v", processors["batch"])
	}
	if got := pipelineField(t, obj, "metrics", "processors"); !reflect.DeepEqual(got, []string{"memory_limiter", "batch"}) {
		t.Fatalf("processors = %v", got)
	}
}

func TestRenderCustomCollectorTsugaExporter(t *testing.T) {
	obj := renderCustom(t, `{"config": {
	  "receivers": {"otlp": {}},
	  "exporters": {"debug": {}},
	  "service": {"pipelines": {
	    "logs": {"receivers": ["otlp"]},
	    "metrics": {"receivers": ["otlp"], "exporters": ["debug"]},
	    "traces": {"receivers": ["otlp"], "exporters": ["otlp/tsuga-agent"]}
	  }}}}`, onlyToggle("exporter"))
	exporter, _, _ := unstructured.NestedMap(obj.Object, "spec", "config", "exporters", "otlp/tsuga-agent")
	want := map[string]interface{}{"endpoint": testCustomGRPC, "compression": "gzip", "tls": map[string]interface{}{"insecure": true}}
	if !reflect.DeepEqual(exporter, want) {
		t.Fatalf("exporter = %#v", exporter)
	}
	for pipeline, want := range map[string][]string{
		"logs":    {"otlp/tsuga-agent"},
		"metrics": {"debug", "otlp/tsuga-agent"},
		"traces":  {"otlp/tsuga-agent"},
	} {
		if got := pipelineField(t, obj, pipeline, "exporters"); !reflect.DeepEqual(got, want) {
			t.Fatalf("%s exporters = %v, want %v", pipeline, got, want)
		}
	}
}

func TestRenderCustomCollectorSkipsConnectorPipelines(t *testing.T) {
	obj := renderCustom(t, `{"config": {
	  "receivers": {"otlp": {}},
	  "connectors": {"forward": {}},
	  "service": {"pipelines": {
	    "logs/raw": {"receivers": ["otlp"], "exporters": ["forward"]},
	    "logs/out": {"receivers": ["forward"]}
	  }}}}`)
	if got := pipelineField(t, obj, "logs/raw", "exporters"); !reflect.DeepEqual(got, []string{"forward"}) {
		t.Fatalf("logs/raw exporters = %v", got)
	}
	if got := pipelineField(t, obj, "logs/raw", "processors"); len(got) != 0 {
		t.Fatalf("logs/raw processors = %v", got)
	}
	if got := pipelineField(t, obj, "logs/out", "exporters"); !reflect.DeepEqual(got, []string{"otlp/tsuga-agent"}) {
		t.Fatalf("logs/out exporters = %v", got)
	}
	if got := pipelineField(t, obj, "logs/out", "processors"); !reflect.DeepEqual(got, []string{"memory_limiter", "batch"}) {
		t.Fatalf("logs/out processors = %v", got)
	}
}

func TestRenderCustomCollectorSkipsRoundRobinConnector(t *testing.T) {
	obj := renderCustom(t, `{"config": {
	  "receivers": {"otlp": {}},
	  "connectors": {"round_robin": {}},
	  "service": {"pipelines": {
	    "traces/in": {"receivers": ["otlp"], "exporters": ["round_robin"]},
	    "traces/out": {"receivers": ["round_robin"]}
	  }}}}`)
	if got := pipelineField(t, obj, "traces/in", "exporters"); !reflect.DeepEqual(got, []string{"round_robin"}) {
		t.Fatalf("traces/in exporters = %v", got)
	}
}

func TestRenderCustomCollectorSignalChecks(t *testing.T) {
	noLogs := customCluster()
	noLogs.Agent.Logs = false
	for _, tc := range []struct {
		name, pipeline string
		cluster        v1alpha1.TsugaCollectorConfigSpec
	}{
		{"agent logs off", "logs/x", noLogs},
		{"profiles", "profiles", customCluster()},
		{"unknown signal", "foo/x", customCluster()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			collector := `{"config": {"receivers": {"otlp": {}}, "service": {"pipelines": {"` + tc.pipeline + `": {"receivers": ["otlp"]}}}}}`
			_, err := RenderCustomCollector("ndm", "observability", customSpec(collector, onlyToggle("exporter")), tc.cluster, testCustomGRPC, testCustomHTTP)
			if err == nil || !strings.Contains(err.Error(), tc.pipeline) {
				t.Fatalf("want an error naming %s, got %v", tc.pipeline, err)
			}
			if _, err := RenderCustomCollector("ndm", "observability", customSpec(collector, togglesOff), tc.cluster, testCustomGRPC, testCustomHTTP); err != nil {
				t.Fatalf("tsugaExporter off: %v", err)
			}
		})
	}
}

func TestRenderCustomCollectorSelfTelemetry(t *testing.T) {
	obj := renderCustom(t, `{
	  "env": [{"name": "POD_UID", "value": "mine"}],
	  "config": {
	    "receivers": {"otlp": {}},
	    "service": {
	      "telemetry": {"resource": {"service.name": "custom"}},
	      "pipelines": {"logs": {"receivers": ["otlp"]}}
	    }}}`, onlyToggle("telemetry"))
	readers, _, _ := unstructured.NestedSlice(obj.Object, "spec", "config", "service", "telemetry", "metrics", "readers")
	want := []interface{}{map[string]interface{}{"periodic": map[string]interface{}{"exporter": map[string]interface{}{"otlp": map[string]interface{}{
		"protocol":               "http/protobuf",
		"temporality_preference": "delta",
		"endpoint":               testCustomHTTP + "/v1/metrics",
	}}}}}
	if !reflect.DeepEqual(readers, want) {
		t.Fatalf("readers = %#v", readers)
	}
	resource, _, _ := unstructured.NestedStringMap(obj.Object, "spec", "config", "service", "telemetry", "resource")
	wantResource := map[string]string{"service.name": "custom", "service.instance.id": "${env:POD_UID}", "k8s.pod.name": "${env:POD_NAME}"}
	if !reflect.DeepEqual(resource, wantResource) {
		t.Fatalf("resource = %v", resource)
	}
	env, _, _ := unstructured.NestedSlice(obj.Object, "spec", "env")
	counts := map[string]int{}
	for _, e := range env {
		counts[e.(map[string]interface{})["name"].(string)]++
	}
	if counts["POD_UID"] != 1 || counts["POD_NAME"] != 1 {
		t.Fatalf("env = %v", env)
	}
	if env[0].(map[string]interface{})["value"] != "mine" {
		t.Fatalf("user POD_UID replaced: %v", env[0])
	}
}

func TestRenderCustomCollectorKeepsUserTelemetry(t *testing.T) {
	collector := `{"config": {
	  "receivers": {"otlp": {}},
	  "service": {
	    "telemetry": {"metrics": {"readers": [{"periodic": {"exporter": {"otlp": {"endpoint": "https://otlp.tsuga.com/v1/metrics"}}}}]}},
	    "pipelines": {"logs": {"receivers": ["otlp"]}}
	  }}}`
	noMetrics := customCluster()
	noMetrics.Agent.Metrics = false
	obj := renderCustomWith(t, noMetrics, collector, onlyToggle("telemetry"))
	metrics, _, _ := unstructured.NestedMap(obj.Object, "spec", "config", "service", "telemetry", "metrics")
	in := mustJSON(t, collector)["config"].(map[string]interface{})["service"].(map[string]interface{})["telemetry"].(map[string]interface{})["metrics"]
	if !reflect.DeepEqual(metrics, in) {
		t.Fatalf("user telemetry metrics changed: %v", metrics)
	}
	if id, _, _ := unstructured.NestedString(obj.Object, "spec", "config", "service", "telemetry", "resource", "service.instance.id"); id != "${env:POD_UID}" {
		t.Fatalf("service.instance.id = %q", id)
	}
}

func TestRenderCustomCollectorClashes(t *testing.T) {
	noMetrics := customCluster()
	noMetrics.Agent.Metrics = false
	for _, tc := range []struct {
		name, collector, want string
		toggle                string
		cluster               v1alpha1.TsugaCollectorConfigSpec
	}{
		{"user defines otlp/tsuga-agent", `{"config": {"receivers": {"otlp": {}}, "exporters": {"otlp/tsuga-agent": {}}, "service": {"pipelines": {"logs": {"receivers": ["otlp"]}}}}}`, "otlp/tsuga-agent", "exporter", customCluster()},
		{"self telemetry with agent metrics off", `{"config": {"receivers": {"otlp": {}}, "service": {"pipelines": {"logs": {"receivers": ["otlp"]}}}}}`, "metrics", "telemetry", noMetrics},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := RenderCustomCollector("ndm", "observability", customSpec(tc.collector, onlyToggle(tc.toggle)), tc.cluster, testCustomGRPC, testCustomHTTP)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want an error mentioning %q, got %v", tc.want, err)
			}
		})
	}
}

func TestRenderCustomCollectorInvalidInput(t *testing.T) {
	for _, tc := range []struct {
		name string
		spec v1alpha1.TsugaCollectorSpec
		want string
	}{
		{"nil collector", v1alpha1.TsugaCollectorSpec{}, "collector"},
		{"collector not an object", customSpec(`[]`), "collector"},
		{"config as a string", customSpec(`{"config": "receivers: {}"}`), "object"},
		{"no config", customSpec(`{"mode": "deployment"}`), "config"},
		{"no pipelines", customSpec(`{"config": {"receivers": {"otlp": {}}}}`), "pipelines"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := RenderCustomCollector("ndm", "observability", tc.spec, customCluster(), testCustomGRPC, testCustomHTTP)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want an error mentioning %q, got %v", tc.want, err)
			}
		})
	}
}

func TestRenderCustomCollectorImageAndPodDefaults(t *testing.T) {
	const minimal = `{"config": {"receivers": {"otlp": {}}, "service": {"pipelines": {"logs": {"receivers": ["otlp"]}}}}}`
	image := func(obj *unstructured.Unstructured) string {
		s, _, _ := unstructured.NestedString(obj.Object, "spec", "image")
		return s
	}
	if got := image(renderCustom(t, minimal)); got != defaultAgentImage {
		t.Fatalf("default image = %q", got)
	}
	cluster := customCluster()
	cluster.Image = "registry/cluster:1"
	if got := image(renderCustomWith(t, cluster, minimal)); got != "registry/cluster:1" {
		t.Fatalf("cluster image = %q", got)
	}
	userImage := `{"image": "registry/user:2", "resources": {"limits": {"memory": "1Gi"}}, "config": {"receivers": {"otlp": {}}, "service": {"pipelines": {"logs": {"receivers": ["otlp"]}}}}}`
	obj := renderCustomWith(t, cluster, userImage)
	if got := image(obj); got != "registry/user:2" {
		t.Fatalf("user image = %q", got)
	}
	resources, _, _ := unstructured.NestedMap(obj.Object, "spec", "resources")
	if !reflect.DeepEqual(resources, map[string]interface{}{"limits": map[string]interface{}{"memory": "1Gi"}}) {
		t.Fatalf("user resources replaced: %v", resources)
	}
	if nonRoot, _, _ := unstructured.NestedBool(obj.Object, "spec", "podSecurityContext", "runAsNonRoot"); !nonRoot {
		t.Fatal("podSecurityContext not defaulted")
	}
	defaults := renderCustom(t, minimal)
	if _, found, _ := unstructured.NestedMap(defaults.Object, "spec", "resources"); !found {
		t.Fatal("resources not defaulted")
	}
	if _, found, _ := unstructured.NestedMap(defaults.Object, "spec", "securityContext"); !found {
		t.Fatal("securityContext not defaulted")
	}
	root := renderCustom(t, `{"securityContext": {"runAsUser": 0}, "config": {"receivers": {"otlp": {}}, "service": {"pipelines": {"logs": {"receivers": ["otlp"]}}}}}`)
	if _, found, _ := unstructured.NestedFieldNoCopy(root.Object, "spec", "podSecurityContext"); found {
		t.Fatal("a user container securityContext must not get the pod-level runAsNonRoot default")
	}
}

func TestRenderCustomCollectorExportsSourceOfDerivingConnector(t *testing.T) {
	obj := renderCustom(t, `{"config": {
	  "receivers": {"otlp": {}},
	  "connectors": {"spanmetrics": {}},
	  "service": {"pipelines": {
	    "traces": {"receivers": ["otlp"], "exporters": ["spanmetrics"]},
	    "metrics/spanmetrics": {"receivers": ["spanmetrics"]}
	  }}}}`)
	if got := pipelineField(t, obj, "traces", "exporters"); !reflect.DeepEqual(got, []string{"spanmetrics", "otlp/tsuga-agent"}) {
		t.Fatalf("traces exporters = %v", got)
	}
	if got := pipelineField(t, obj, "traces", "processors"); !reflect.DeepEqual(got, []string{"memory_limiter", "batch"}) {
		t.Fatalf("traces processors = %v", got)
	}
	if got := pipelineField(t, obj, "metrics/spanmetrics", "exporters"); !reflect.DeepEqual(got, []string{"otlp/tsuga-agent"}) {
		t.Fatalf("metrics/spanmetrics exporters = %v", got)
	}
}

// Datadog's default layout: the connector only derives trace stats into
// metrics, so the traces pipeline still exports its own spans.
func TestRenderCustomCollectorExportsSourceOfDatadogConnector(t *testing.T) {
	obj := renderCustom(t, `{"config": {
	  "receivers": {"otlp": {}},
	  "connectors": {"datadog/connector": {}},
	  "exporters": {"datadog": {}},
	  "service": {"pipelines": {
	    "traces": {"receivers": ["otlp"], "exporters": ["datadog/connector", "datadog"]},
	    "metrics": {"receivers": ["datadog/connector"], "exporters": ["datadog"]}
	  }}}}`)
	if got := pipelineField(t, obj, "traces", "exporters"); !reflect.DeepEqual(got, []string{"datadog/connector", "datadog", "otlp/tsuga-agent"}) {
		t.Fatalf("traces exporters = %v", got)
	}
}

// Sidecar injection copies only container-level fields into the app pod, so
// the pod-level defaults must move onto the container.
func TestRenderCustomCollectorSidecarSecurityContext(t *testing.T) {
	obj := renderCustom(t, `{"mode": "sidecar", "config": {"receivers": {"otlp": {}}, "service": {"pipelines": {"logs": {"receivers": ["otlp"]}}}}}`)
	if _, found, _ := unstructured.NestedFieldNoCopy(obj.Object, "spec", "podSecurityContext"); found {
		t.Fatal("sidecar injection drops podSecurityContext; it must not be rendered")
	}
	sc, _, _ := unstructured.NestedMap(obj.Object, "spec", "securityContext")
	if sc["runAsNonRoot"] != true || !reflect.DeepEqual(sc["seccompProfile"], map[string]interface{}{"type": "RuntimeDefault"}) || sc["allowPrivilegeEscalation"] != false {
		t.Fatalf("container securityContext = %#v", sc)
	}
}
