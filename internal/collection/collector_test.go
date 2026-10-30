package collection

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/yaml"

	"github.com/tsuga-dev/tsuga-operator/api/v1alpha1"
)

func effective() v1alpha1.TsugaCollectorConfigSpec {
	return ResolveCollector(v1alpha1.TsugaCollectorConfigSpec{
		ClusterName: "prod-eu",
		Export: &v1alpha1.ExportSpec{
			TokenSecretRef: v1alpha1.SecretKeyRef{Name: "tsuga-credentials", Key: "api-key"},
			Endpoint:       "https://otlp.tsuga.com",
		},
		Agent:   v1alpha1.TelemetryToggles{Traces: true, Metrics: true, Logs: true},
		Gateway: v1alpha1.GatewayToggles{ClusterMetrics: true, KubernetesObjects: true, KubernetesEvents: true},
	})
}

// unstructuredString reads a nested string field from an unstructured object.
func unstructuredString(obj *unstructured.Unstructured, fields ...string) (string, bool, error) {
	return unstructured.NestedString(obj.Object, fields...)
}

// yamlRoundtrip marshals the unstructured object back to YAML for substring
// assertions against the rendered config.
func yamlRoundtrip(obj *unstructured.Unstructured) (string, error) {
	b, err := yaml.Marshal(obj.Object)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// contains is a thin wrapper around strings.Contains for readability in
// assertions below.
func contains(haystack, needle string) bool {
	return strings.Contains(haystack, needle)
}

// hasNestedField reports whether a path exists in an unstructured object,
// e.g. hasNestedField(obj, "spec", "config", "receivers", "otlp").
func hasNestedField(obj *unstructured.Unstructured, fields ...string) bool {
	_, found, err := unstructured.NestedFieldNoCopy(obj.Object, fields...)
	return err == nil && found
}

// nestedStringSliceContains reports whether the string list at the given
// path contains value, e.g. checking a pipeline's receivers: or exporters:
// array for a given component name.
func nestedStringSliceContains(obj *unstructured.Unstructured, value string, fields ...string) bool {
	list, found, err := unstructured.NestedStringSlice(obj.Object, fields...)
	if err != nil || !found {
		return false
	}
	for _, item := range list {
		if item == value {
			return true
		}
	}
	return false
}

func TestRenderCollectorsProducesAgentAndGateway(t *testing.T) {
	objs, err := RenderCollectors(effective())
	if err != nil {
		t.Fatal(err)
	}
	if len(objs) != 2 {
		t.Fatalf("want 2 collectors, got %d", len(objs))
	}
	agent := objs[0]
	if agent.GetKind() != "OpenTelemetryCollector" {
		t.Fatalf("wrong kind %q", agent.GetKind())
	}
	if agent.GetAPIVersion() != "opentelemetry.io/v1beta1" {
		t.Fatalf("wrong apiVersion %q", agent.GetAPIVersion())
	}
	mode, _, _ := unstructuredString(agent, "spec", "mode")
	if mode != "daemonset" {
		t.Fatalf("agent mode want daemonset got %q", mode)
	}
}

func TestRenderSubstitutesEndpointAndCluster(t *testing.T) {
	objs, err := RenderCollectors(effective())
	if err != nil {
		t.Fatal(err)
	}
	raw, err := yamlRoundtrip(objs[0])
	if err != nil {
		t.Fatal(err)
	}
	if !contains(raw, "https://otlp.tsuga.com") {
		t.Fatal("endpoint not substituted")
	}
	if contains(raw, "__TSUGA_ENDPOINT__") {
		t.Fatal("placeholder left unsubstituted")
	}
	if !contains(raw, "prod-eu") {
		t.Fatal("cluster name not substituted")
	}
}

func TestRenderGatewayShape(t *testing.T) {
	objs, err := RenderCollectors(effective())
	if err != nil {
		t.Fatal(err)
	}
	if len(objs) != 2 {
		t.Fatalf("want 2 collectors, got %d", len(objs))
	}
	gw := objs[1]
	if gw.GetName() != "tsuga-gateway" {
		t.Fatalf("gateway name want tsuga-gateway got %q", gw.GetName())
	}
	mode, _, _ := unstructuredString(gw, "spec", "mode")
	if mode != "deployment" {
		t.Fatalf("gateway mode want deployment got %q", mode)
	}
	ns, _, _ := unstructuredString(gw, "metadata", "namespace")
	if ns != defaultCollectorNamespace {
		t.Fatalf("gateway namespace want %q got %q", defaultCollectorNamespace, ns)
	}
	raw, _ := yamlRoundtrip(gw)
	if !contains(raw, "https://otlp.tsuga.com") {
		t.Fatal("gateway endpoint not substituted")
	}
	if hasNestedField(gw, "spec", "config", "receivers", "otlp") {
		t.Fatal("gateway config should not have an otlp receiver")
	}
}

func TestRenderAgentLogsToggleOff(t *testing.T) {
	cfg := effective()
	cfg.Agent = v1alpha1.TelemetryToggles{Traces: true, Metrics: true, Logs: false}
	objs, err := RenderCollectors(cfg)
	if err != nil {
		t.Fatal(err)
	}
	agent := objs[0]
	if hasNestedField(agent, "spec", "config", "receivers", "file_log") {
		t.Fatal("file_log receiver should be pruned when Logs is off")
	}
	if hasNestedField(agent, "spec", "config", "service", "pipelines", "logs") {
		t.Fatal("logs pipeline should be pruned when Logs is off")
	}
	if !hasNestedField(agent, "spec", "config", "receivers", "otlp") {
		t.Fatal("otlp receiver should still be present (used by traces/metrics)")
	}
}

func TestRenderAgentOnlyTraces(t *testing.T) {
	cfg := effective()
	cfg.Agent = v1alpha1.TelemetryToggles{Traces: true, Metrics: false, Logs: false}
	objs, err := RenderCollectors(cfg)
	if err != nil {
		t.Fatal(err)
	}
	agent := objs[0]
	if hasNestedField(agent, "spec", "config", "receivers", "kubelet_stats") {
		t.Fatal("kubelet_stats receiver should be pruned when Metrics is off")
	}
	if hasNestedField(agent, "spec", "config", "receivers", "host_metrics") {
		t.Fatal("host_metrics receiver should be pruned when Metrics is off")
	}
	if hasNestedField(agent, "spec", "config", "receivers", "file_log") {
		t.Fatal("file_log receiver should be pruned when Logs is off")
	}
	if hasNestedField(agent, "spec", "config", "connectors", "span_metrics") {
		t.Fatal("span_metrics connector should be pruned when Metrics is off (no pipeline receives from it)")
	}
	if nestedStringSliceContains(agent, "span_metrics", "spec", "config", "service", "pipelines", "traces", "exporters") {
		t.Fatal("traces pipeline should not still export to the pruned span_metrics connector")
	}
}

func TestRenderAgentTracesOffDropsSpanMetricsFromMetrics(t *testing.T) {
	cfg := effective()
	cfg.Agent = v1alpha1.TelemetryToggles{Traces: false, Metrics: true, Logs: true}
	objs, err := RenderCollectors(cfg)
	if err != nil {
		t.Fatal(err)
	}
	agent := objs[0]
	if hasNestedField(agent, "spec", "config", "connectors", "span_metrics") {
		t.Fatal("span_metrics connector should be pruned when Traces is off (no pipeline exports to it)")
	}
	if nestedStringSliceContains(agent, "span_metrics", "spec", "config", "service", "pipelines", "metrics", "receivers") {
		t.Fatal("metrics pipeline should not still receive from the pruned span_metrics connector")
	}
}

func TestRenderGatewayClusterMetricsOnlyToggle(t *testing.T) {
	cfg := effective()
	cfg.Gateway = v1alpha1.GatewayToggles{ClusterMetrics: true, KubernetesObjects: false}
	objs, err := RenderCollectors(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(objs) != 2 {
		t.Fatalf("want 2 collectors, got %d", len(objs))
	}
	gw := objs[1]
	if !hasNestedField(gw, "spec", "config", "receivers", "k8s_cluster") {
		t.Fatal("k8s_cluster receiver should still be present")
	}
	if !hasNestedField(gw, "spec", "config", "service", "pipelines", "metrics") {
		t.Fatal("metrics pipeline should still be present")
	}
	if hasNestedField(gw, "spec", "config", "receivers", "k8s_objects") {
		t.Fatal("k8s_objects receiver should be pruned when KubernetesObjects is off")
	}
	if hasNestedField(gw, "spec", "config", "service", "pipelines", "logs") {
		t.Fatal("logs pipeline should be pruned when KubernetesObjects is off")
	}
}

func TestRenderNoGatewayWhenBothTogglesOff(t *testing.T) {
	cfg := effective()
	cfg.Gateway = v1alpha1.GatewayToggles{ClusterMetrics: false, KubernetesObjects: false}
	objs, err := RenderCollectors(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(objs) != 1 {
		t.Fatalf("want 1 collector (agent only), got %d", len(objs))
	}
}

func TestRenderEndpointFromSecretRefInjectsEnvVar(t *testing.T) {
	cfg := effective()
	cfg.Export.EndpointSecretRef = &v1alpha1.SecretKeyRef{Name: "tsuga-credentials", Key: "otlp-endpoint"}
	objs, err := RenderCollectors(cfg)
	if err != nil {
		t.Fatal(err)
	}
	agent := objs[0]

	endpoint, found, err := unstructured.NestedString(agent.Object, "spec", "config", "exporters", "otlp_http/tsuga", "endpoint")
	if err != nil || !found {
		t.Fatalf("exporter endpoint not found: found=%v err=%v", found, err)
	}
	if endpoint != "${env:TSUGA_OTLP_ENDPOINT}" {
		t.Fatalf("want endpoint env reference, got %q", endpoint)
	}

	env, found, err := unstructured.NestedSlice(agent.Object, "spec", "env")
	if err != nil || !found {
		t.Fatalf("spec.env not found: found=%v err=%v", found, err)
	}
	var gotEnvEntry bool
	for _, raw := range env {
		entry, ok := raw.(map[string]interface{})
		if !ok || entry["name"] != "TSUGA_OTLP_ENDPOINT" {
			continue
		}
		gotEnvEntry = true
		name, _, _ := unstructured.NestedString(entry, "valueFrom", "secretKeyRef", "name")
		key, _, _ := unstructured.NestedString(entry, "valueFrom", "secretKeyRef", "key")
		if name != "tsuga-credentials" || key != "otlp-endpoint" {
			t.Fatalf("want secretKeyRef {tsuga-credentials otlp-endpoint}, got {%s %s}", name, key)
		}
	}
	if !gotEnvEntry {
		t.Fatal("want TSUGA_OTLP_ENDPOINT env entry, not found")
	}
}

func TestRenderDefaultsCollectorImage(t *testing.T) {
	objs, err := RenderCollectors(effective())
	if err != nil {
		t.Fatal(err)
	}
	if len(objs) != 2 {
		t.Fatalf("want 2 collectors, got %d", len(objs))
	}
	want := map[string]string{
		agentName:   defaultAgentImage,   // daemonset agent -> contrib
		gatewayName: defaultGatewayImage, // gateway -> k8s
	}
	for _, obj := range objs {
		image, found, err := unstructuredString(obj, "spec", "image")
		if err != nil || !found {
			t.Fatalf("collector %q: spec.image not set: found=%v err=%v", obj.GetName(), found, err)
		}
		if image != want[obj.GetName()] {
			t.Fatalf("collector %q: want default image %q, got %q", obj.GetName(), want[obj.GetName()], image)
		}
	}
}

func TestRenderImageOverride(t *testing.T) {
	cfg := effective()
	cfg.Image = "example.com/custom-collector:v9"
	objs, err := RenderCollectors(cfg)
	if err != nil {
		t.Fatal(err)
	}
	for _, obj := range objs {
		image, _, _ := unstructuredString(obj, "spec", "image")
		if image != "example.com/custom-collector:v9" {
			t.Fatalf("collector %q: want override image, got %q", obj.GetName(), image)
		}
	}
}

func TestRenderInjectsDownwardAPIEnv(t *testing.T) {
	objs, err := RenderCollectors(effective())
	if err != nil {
		t.Fatal(err)
	}
	agent := objs[0]

	env, found, err := unstructured.NestedSlice(agent.Object, "spec", "env")
	if err != nil || !found {
		t.Fatalf("spec.env not found: found=%v err=%v", found, err)
	}

	wantPaths := map[string]string{
		"MY_POD_IP":     "status.podIP",
		"NODE_IP":       "status.hostIP",
		"POD_NAME":      "metadata.name",
		"POD_UID":       "metadata.uid",
		"K8S_NODE_NAME": "spec.nodeName",
	}
	gotPaths := map[string]string{}
	var haveAPIKey bool
	for _, raw := range env {
		entry, ok := raw.(map[string]interface{})
		if !ok {
			continue
		}
		name, _ := entry["name"].(string)
		if name == "TSUGA_API_KEY" {
			haveAPIKey = true
			continue
		}
		if _, want := wantPaths[name]; !want {
			continue
		}
		path, _, _ := unstructured.NestedString(entry, "valueFrom", "fieldRef", "fieldPath")
		gotPaths[name] = path
	}

	if !haveAPIKey {
		t.Fatal("TSUGA_API_KEY env entry must be preserved alongside downward-API vars")
	}
	for name, wantPath := range wantPaths {
		if gotPaths[name] != wantPath {
			t.Fatalf("env %q: want fieldPath %q, got %q", name, wantPath, gotPaths[name])
		}
	}
}

func TestRenderAgentHasHostVolumeMounts(t *testing.T) {
	objs, err := RenderCollectors(effective())
	if err != nil {
		t.Fatal(err)
	}
	agent := objs[0]

	mounts, found, err := unstructured.NestedSlice(agent.Object, "spec", "volumeMounts")
	if err != nil || !found {
		t.Fatalf("agent spec.volumeMounts not found: found=%v err=%v", found, err)
	}
	wantMountPaths := map[string]bool{
		"/hostfs":                    false,
		"/var/log/pods":              false,
		"/var/lib/docker/containers": false,
	}
	for _, raw := range mounts {
		mount, ok := raw.(map[string]interface{})
		if !ok {
			continue
		}
		path, _ := mount["mountPath"].(string)
		if _, want := wantMountPaths[path]; want {
			wantMountPaths[path] = true
		}
		if path == "/hostfs" {
			if prop, _ := mount["mountPropagation"].(string); prop != "HostToContainer" {
				t.Fatalf("hostfs mount: want mountPropagation HostToContainer, got %q", prop)
			}
		}
	}
	for path, got := range wantMountPaths {
		if !got {
			t.Fatalf("agent volumeMounts missing mountPath %q", path)
		}
	}

	volumes, found, err := unstructured.NestedSlice(agent.Object, "spec", "volumes")
	if err != nil || !found {
		t.Fatalf("agent spec.volumes not found: found=%v err=%v", found, err)
	}
	if len(volumes) != 3 {
		t.Fatalf("want 3 host volumes, got %d", len(volumes))
	}
	for _, raw := range volumes {
		vol, _ := raw.(map[string]interface{})
		hostPath, _ := vol["hostPath"].(map[string]interface{})
		if hostPath["path"] == "/var/lib/docker/containers" && hostPath["type"] != "DirectoryOrCreate" {
			t.Fatalf("docker containers hostPath: want type DirectoryOrCreate, got %v", hostPath["type"])
		}
	}
}

func TestRenderGatewayHasNoHostVolumeMounts(t *testing.T) {
	objs, err := RenderCollectors(effective())
	if err != nil {
		t.Fatal(err)
	}
	gw := objs[1]
	if hasNestedField(gw, "spec", "volumeMounts") {
		t.Fatal("gateway collector should not have volumeMounts")
	}
	if hasNestedField(gw, "spec", "volumes") {
		t.Fatal("gateway collector should not have volumes")
	}
}

func TestRenderDefaultsCollectorResources(t *testing.T) {
	objs, err := RenderCollectors(effective())
	if err != nil {
		t.Fatal(err)
	}
	for _, obj := range objs {
		limit, found, err := unstructuredString(obj, "spec", "resources", "limits", "memory")
		if err != nil || !found {
			t.Fatalf("collector %q: spec.resources.limits.memory not found: found=%v err=%v", obj.GetName(), found, err)
		}
		if limit != "512Mi" {
			t.Fatalf("collector %q: want default memory limit 512Mi, got %q", obj.GetName(), limit)
		}
	}
}

func TestRenderResourcesOverride(t *testing.T) {
	cfg := effective()
	cfg.Resources = &corev1.ResourceRequirements{
		Limits: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("4Gi")},
	}
	objs, err := RenderCollectors(cfg)
	if err != nil {
		t.Fatal(err)
	}
	for _, obj := range objs {
		limit, _, _ := unstructuredString(obj, "spec", "resources", "limits", "memory")
		if limit != "4Gi" {
			t.Fatalf("collector %q: want overridden memory limit 4Gi, got %q", obj.GetName(), limit)
		}
	}
}

func TestRenderGatewayPinsSingleReplica(t *testing.T) {
	objs, err := RenderCollectors(effective())
	if err != nil {
		t.Fatal(err)
	}
	replicas, found, err := unstructured.NestedInt64(objs[1].Object, "spec", "replicas")
	if err != nil || !found {
		t.Fatalf("gateway spec.replicas not found: found=%v err=%v", found, err)
	}
	if replicas != 1 {
		t.Fatalf("want gateway replicas 1 (k8s_cluster/k8s_objects have no leader election), got %d", replicas)
	}
	if hasNestedField(objs[0], "spec", "replicas") {
		t.Fatal("daemonset agent should not set spec.replicas")
	}
}

func TestRenderExporterHasCompressionAndEncoding(t *testing.T) {
	objs, err := RenderCollectors(effective())
	if err != nil {
		t.Fatal(err)
	}
	for _, obj := range objs {
		enc, _, _ := unstructuredString(obj, "spec", "config", "exporters", "otlp_http/tsuga", "encoding")
		if enc != "json" {
			t.Fatalf("collector %q: want exporter encoding json, got %q", obj.GetName(), enc)
		}
		comp, _, _ := unstructuredString(obj, "spec", "config", "exporters", "otlp_http/tsuga", "compression")
		if comp != "gzip" {
			t.Fatalf("collector %q: want exporter compression gzip, got %q", obj.GetName(), comp)
		}
	}
}

func TestRenderShipsCollectorSelfTelemetry(t *testing.T) {
	objs, err := RenderCollectors(effective())
	if err != nil {
		t.Fatal(err)
	}
	for _, obj := range objs {
		if !hasNestedField(obj, "spec", "config", "service", "telemetry", "metrics", "readers") {
			t.Fatalf("collector %q: missing service.telemetry.metrics.readers", obj.GetName())
		}
		raw, _ := yamlRoundtrip(obj)
		if !contains(raw, "https://otlp.tsuga.com/v1/metrics") {
			t.Fatalf("collector %q: self-telemetry endpoint not substituted to <endpoint>/v1/metrics", obj.GetName())
		}
	}
}

func TestRenderGatewayHasMemoryLimiter(t *testing.T) {
	objs, err := RenderCollectors(effective())
	if err != nil {
		t.Fatal(err)
	}
	gw := objs[1]
	if !hasNestedField(gw, "spec", "config", "processors", "memory_limiter") {
		t.Fatal("gateway should define a memory_limiter processor")
	}
	for _, pipeline := range []string{"logs", "metrics"} {
		if !nestedStringSliceContains(gw, "memory_limiter", "spec", "config", "service", "pipelines", pipeline, "processors") {
			t.Fatalf("gateway %s pipeline should include memory_limiter", pipeline)
		}
	}
}

func TestRenderNoEndpointConfiguredReturnsError(t *testing.T) {
	cfg := effective()
	cfg.Export.Endpoint = ""
	if _, err := RenderCollectors(cfg); err == nil {
		t.Fatal("want error when neither endpoint literal nor endpointSecretRef is set")
	}
}

func TestRenderAllAgentTogglesOffReturnsError(t *testing.T) {
	cfg := effective()
	cfg.Agent = v1alpha1.TelemetryToggles{Traces: false, Metrics: false, Logs: false}
	if _, err := RenderCollectors(cfg); err == nil {
		t.Fatal("want error when all agent pipelines are pruned away")
	}
}

// effectiveWithPrometheus returns the shared fixture with annotation
// scraping enabled and the Target Allocator left off.
func effectiveWithPrometheus() v1alpha1.TsugaCollectorConfigSpec {
	cfg := effective()
	cfg.Prometheus = v1alpha1.PrometheusSpec{
		Enabled:         true,
		ScrapeInterval:  "45s",
		Replicas:        1,
		TargetAllocator: v1alpha1.TargetAllocatorSpec{AllocationStrategy: "consistent-hashing"},
	}
	return cfg
}

func TestRenderOmitsScraperWhenDisabled(t *testing.T) {
	objs, err := RenderCollectors(effective())
	if err != nil {
		t.Fatal(err)
	}
	for _, o := range objs {
		if o.GetName() == "tsuga-scraper" {
			t.Fatal("scraper rendered although prometheus.enabled is false")
		}
	}
}

func TestRenderScraperShape(t *testing.T) {
	objs, err := RenderCollectors(effectiveWithPrometheus())
	if err != nil {
		t.Fatal(err)
	}
	if len(objs) != 3 {
		t.Fatalf("want agent+gateway+scraper, got %d objects", len(objs))
	}
	scraper := objs[2]
	if scraper.GetName() != "tsuga-scraper" {
		t.Fatalf("scraper name want tsuga-scraper got %q", scraper.GetName())
	}
	mode, _, _ := unstructuredString(scraper, "spec", "mode")
	if mode != "statefulset" {
		t.Fatalf("scraper mode want statefulset got %q", mode)
	}
	image, _, _ := unstructuredString(scraper, "spec", "image")
	if !contains(image, "opentelemetry-collector-contrib") {
		t.Fatalf("scraper image want contrib distribution got %q", image)
	}
	if !hasNestedField(scraper, "spec", "config", "receivers", "prometheus") {
		t.Fatal("scraper has no prometheus receiver")
	}
	if !nestedStringSliceContains(scraper, "prometheus", "spec", "config", "service", "pipelines", "metrics", "receivers") {
		t.Fatal("prometheus receiver not wired into the metrics pipeline")
	}
}

func TestRenderScraperReplicasFromSpec(t *testing.T) {
	cfg := effectiveWithPrometheus()
	cfg.Prometheus.Replicas = 4
	cfg.Prometheus.TargetAllocator.Enabled = true
	objs, err := RenderCollectors(cfg)
	if err != nil {
		t.Fatal(err)
	}
	replicas, found, err := unstructured.NestedInt64(objs[2].Object, "spec", "replicas")
	if err != nil || !found {
		t.Fatal("scraper has no replicas field")
	}
	if replicas != 4 {
		t.Fatalf("scraper replicas want 4 got %d", replicas)
	}
}

func TestRenderScraperSubstitutesScrapeInterval(t *testing.T) {
	objs, err := RenderCollectors(effectiveWithPrometheus())
	if err != nil {
		t.Fatal(err)
	}
	raw, err := yamlRoundtrip(objs[2])
	if err != nil {
		t.Fatal(err)
	}
	if contains(raw, "__SCRAPE_INTERVAL__") {
		t.Fatal("scrape interval placeholder left unsubstituted")
	}
	if !contains(raw, "45s") {
		t.Fatal("scrape interval not substituted")
	}
}

func TestRenderScraperKeepsOnlyAnnotatedPods(t *testing.T) {
	objs, err := RenderCollectors(effectiveWithPrometheus())
	if err != nil {
		t.Fatal(err)
	}
	raw, err := yamlRoundtrip(objs[2])
	if err != nil {
		t.Fatal(err)
	}
	// The keep rule is what makes this opt-in rather than a scrape of every
	// pod in the cluster; assert the annotation it keys on explicitly.
	if !contains(raw, "__meta_kubernetes_pod_annotation_prometheus_io_scrape") {
		t.Fatal("scrape job does not filter on prometheus.io/scrape")
	}
	for _, label := range []string{
		"__meta_kubernetes_pod_annotation_prometheus_io_path",
		"__meta_kubernetes_pod_annotation_prometheus_io_port",
		"__meta_kubernetes_pod_annotation_prometheus_io_scheme",
	} {
		if !contains(raw, label) {
			t.Fatalf("scrape job does not honour %s", label)
		}
	}
}

func TestRenderKeepsNumericClusterNameAString(t *testing.T) {
	cfg := effective()
	cfg.ClusterName = "2024"
	objs, err := RenderCollectors(cfg)
	if err != nil {
		t.Fatal(err)
	}
	for _, obj := range objs {
		v, found, err := unstructured.NestedFieldNoCopy(obj.Object, "spec", "config", "service", "telemetry", "resource", "k8s.cluster.name")
		if err != nil || !found {
			t.Fatalf("%s: k8s.cluster.name missing: %v", obj.GetName(), err)
		}
		if v != "2024" {
			t.Fatalf("%s: expected string \"2024\", got %T %v", obj.GetName(), v, v)
		}
	}
}

func TestRenderKeepsEndpointInsideItsScalar(t *testing.T) {
	cfg := effective()
	cfg.Export.Endpoint = "https://otlp.tsuga.com\n    tls:\n      insecure_skip_verify: true"
	objs, err := RenderCollectors(cfg)
	if err != nil {
		t.Fatal(err)
	}
	for _, obj := range objs {
		_, found, _ := unstructured.NestedFieldNoCopy(obj.Object, "spec", "config", "exporters", "otlp_http/tsuga", "tls")
		if found {
			t.Fatalf("%s: endpoint injected a tls key into the exporter", obj.GetName())
		}
	}
}

func TestRenderUsesImageTagsAttribute(t *testing.T) {
	cfg := effective()
	cfg.Prometheus.Enabled = true
	objs, err := RenderCollectors(cfg)
	if err != nil {
		t.Fatal(err)
	}
	for _, obj := range objs {
		md, found, err := unstructured.NestedStringSlice(obj.Object, "spec", "config", "processors", "k8s_attributes", "extract", "metadata")
		if err != nil || !found {
			t.Fatalf("%s: k8s_attributes metadata not found: %v", obj.GetName(), err)
		}
		if !slices.Contains(md, "container.image.tags") || slices.Contains(md, "container.image.tag") {
			t.Fatalf("%s: want container.image.tags and not container.image.tag, got %v", obj.GetName(), md)
		}
	}
}

func TestRenderGatewayCollectsWarningEvents(t *testing.T) {
	objs, err := RenderCollectors(effective())
	if err != nil {
		t.Fatal(err)
	}
	gw := objs[1]
	if !nestedStringSliceContains(gw, "k8s_objects/events", "spec", "config", "service", "pipelines", "logs/events", "receivers") {
		t.Fatal("logs/events must read from k8s_objects/events")
	}
	objects, _, _ := unstructured.NestedSlice(gw.Object, "spec", "config", "receivers", "k8s_objects/events", "objects")
	if len(objects) != 1 {
		t.Fatalf("want one watched object, got %v", objects)
	}
	o := objects[0].(map[string]interface{})
	if o["name"] != "events" || o["group"] != "" || o["field_selector"] != "type=Warning" {
		t.Fatalf("want core/v1 events filtered to Warning, got %v", o)
	}
	if initial, _, _ := unstructured.NestedBool(gw.Object, "spec", "config", "receivers", "k8s_objects/events", "include_initial_state"); initial {
		t.Fatal("events must not replay the retained window on restart")
	}
	procs, _, _ := unstructured.NestedStringSlice(gw.Object, "spec", "config", "service", "pipelines", "logs/events", "processors")
	if !slices.Equal(procs, []string{"memory_limiter", "transform/k8s_event_severity", "resource", "batch"}) {
		t.Fatalf("unexpected logs/events processors %v", procs)
	}
}

func TestRenderGatewayEventsOnly(t *testing.T) {
	cfg := effective()
	cfg.Gateway = v1alpha1.GatewayToggles{KubernetesEvents: true}
	objs, err := RenderCollectors(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(objs) != 2 {
		t.Fatalf("want agent and gateway, got %d", len(objs))
	}
	gw := objs[1]
	pipelines, _, _ := unstructured.NestedMap(gw.Object, "spec", "config", "service", "pipelines")
	if len(pipelines) != 1 || pipelines["logs/events"] == nil {
		t.Fatalf("want only logs/events, got %v", pipelines)
	}
	for _, r := range []string{"k8s_cluster", "k8s_objects"} {
		if hasNestedField(gw, "spec", "config", "receivers", r) {
			t.Fatalf("%s should be pruned", r)
		}
	}
}

func TestRenderGatewayEventsToggleOff(t *testing.T) {
	cfg := effective()
	cfg.Gateway.KubernetesEvents = false
	objs, err := RenderCollectors(cfg)
	if err != nil {
		t.Fatal(err)
	}
	gw := objs[1]
	if hasNestedField(gw, "spec", "config", "service", "pipelines", "logs/events") ||
		hasNestedField(gw, "spec", "config", "receivers", "k8s_objects/events") {
		t.Fatal("events pipeline and receiver should be pruned when KubernetesEvents is off")
	}
}

func TestRenderNoGatewayWhenAllTogglesOff(t *testing.T) {
	cfg := effective()
	cfg.Gateway = v1alpha1.GatewayToggles{}
	objs, err := RenderCollectors(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(objs) != 1 {
		t.Fatalf("want agent only, got %d", len(objs))
	}
}

func TestRenderNoProfilerByDefault(t *testing.T) {
	objs, err := RenderCollectors(effective())
	if err != nil {
		t.Fatal(err)
	}
	for _, obj := range objs {
		if obj.GetName() == profilerName {
			t.Fatal("profiler must be opt-in")
		}
	}
}

func TestRenderProfilerShape(t *testing.T) {
	p := collectorByName(t, allCollectorsOn(), profilerName)
	if mode, _, _ := unstructuredString(p, "spec", "mode"); mode != modeDaemonset {
		t.Fatalf("want daemonset, got %q", mode)
	}
	if hostPID, _, _ := unstructured.NestedBool(p.Object, "spec", "hostPID"); !hostPID {
		t.Fatal("profiler needs hostPID to see other processes")
	}
	if priv, _, _ := unstructured.NestedBool(p.Object, "spec", "securityContext", "privileged"); !priv {
		t.Fatal("profiler must be privileged to load eBPF programs")
	}
	if gate, _, _ := unstructuredString(p, "spec", "args", "feature-gates"); gate != "+service.profilesSupport" {
		t.Fatalf("want profiles feature gate, got %q", gate)
	}
	mounts, _, _ := unstructured.NestedSlice(p.Object, "spec", "volumeMounts")
	paths := make([]string, 0, len(mounts))
	for _, m := range mounts {
		mm := m.(map[string]interface{})
		if mm["readOnly"] != true {
			t.Fatalf("mount %v must be read-only", mm)
		}
		paths = append(paths, mm["mountPath"].(string))
	}
	if !slices.Equal(paths, []string{"/sys/kernel", "/sys/kernel/tracing", "/lib/modules"}) {
		t.Fatalf("unexpected mounts %v", paths)
	}
	procs, _, _ := unstructured.NestedStringSlice(p.Object, "spec", "config", "service", "pipelines", "profiles", "processors")
	if slices.Contains(procs, "batch") {
		t.Fatal("batch does not register profiles; the collector refuses to start")
	}
	if enc, _, _ := unstructuredString(p, "spec", "config", "exporters", "otlp_http/tsuga", "encoding"); enc != "proto" {
		t.Fatalf("profiles intake takes protobuf, got %q", enc)
	}
	if rate, _, _ := unstructured.NestedFieldNoCopy(p.Object, "spec", "config", "receivers", "profiling", "samples_per_second"); rate != float64(20) && rate != int64(20) {
		t.Fatalf("Tsuga assumes 20 Hz, got %v", rate)
	}
}

func TestRenderProfilerIgnoresSpecImage(t *testing.T) {
	cfg := allCollectorsOn()
	cfg.Image = "example.com/custom-collector:v9"
	if image, _, _ := unstructuredString(collectorByName(t, cfg, profilerName), "spec", "image"); image != defaultProfilerImage {
		t.Fatalf("spec.image must not reach the profiler, got %q", image)
	}
	cfg.Profiling.Image = "mirror.example.com/ebpf-profiler:0.161.0"
	if image, _, _ := unstructuredString(collectorByName(t, cfg, profilerName), "spec", "image"); image != cfg.Profiling.Image {
		t.Fatalf("profiling.image must override, got %q", image)
	}
}

func TestRenderSpanMetricsAreDelta(t *testing.T) {
	objs, err := RenderCollectors(effective())
	if err != nil {
		t.Fatal(err)
	}
	got, _, _ := unstructuredString(objs[0], "spec", "config", "connectors", "span_metrics", "aggregation_temporality")
	if got != "AGGREGATION_TEMPORALITY_DELTA" {
		t.Fatalf("cumulative span metrics re-export idle series forever; want delta, got %q", got)
	}
}

func TestRenderGatewayReportsAllocatablePods(t *testing.T) {
	objs, err := RenderCollectors(effective())
	if err != nil {
		t.Fatal(err)
	}
	if !nestedStringSliceContains(objs[1], "pods", "spec", "config", "receivers", "k8s_cluster", "allocatable_types_to_report") {
		t.Fatal("k8s.node.allocatable_pods needs pods in allocatable_types_to_report")
	}
}

func TestRenderGatewayEnablesStatusReasons(t *testing.T) {
	objs, err := RenderCollectors(effective())
	if err != nil {
		t.Fatal(err)
	}
	for _, metric := range []string{"k8s.pod.status_reason", "k8s.container.status.reason"} {
		enabled, _, _ := unstructured.NestedBool(objs[1].Object, "spec", "config", "receivers", "k8s_cluster", "metrics", metric, "enabled")
		if !enabled {
			t.Errorf("gateway k8s_cluster should enable %s", metric)
		}
	}
}

func collectorByName(t *testing.T, cfg v1alpha1.TsugaCollectorConfigSpec, name string) *unstructured.Unstructured {
	t.Helper()
	objs, err := RenderCollectors(cfg)
	if err != nil {
		t.Fatal(err)
	}
	for _, o := range objs {
		if o.GetName() == name {
			return o
		}
	}
	t.Fatalf("collector %q not rendered", name)
	return nil
}

func allCollectorsOn() v1alpha1.TsugaCollectorConfigSpec {
	cfg := effective()
	cfg.Prometheus.Enabled = true
	cfg.Profiling.Enabled = true
	return cfg
}

func TestRenderPodDefaultsWhenNothingSet(t *testing.T) {
	cfg := allCollectorsOn()
	for _, name := range []string{agentName, gatewayName, scraperName, profilerName} {
		obj := collectorByName(t, cfg, name)
		for _, f := range []string{"nodeSelector", "tolerations", "affinity"} {
			// See TestRenderProfilerDefaultsToLinuxNodes.
			if name == profilerName && f == "nodeSelector" {
				continue
			}
			// See TestRenderAgentDefaultPlacement.
			if name == agentName && f != "nodeSelector" {
				continue
			}
			if hasNestedField(obj, "spec", f) {
				t.Fatalf("%s: %s must be absent when unset", name, f)
			}
		}
	}
	limit, _, _ := unstructuredString(collectorByName(t, cfg, gatewayName), "spec", "resources", "limits", "memory")
	if limit != "512Mi" {
		t.Fatalf("gateway keeps the built-in 512Mi limit, got %q", limit)
	}
}

func TestRenderSpecLevelSchedulingAppliesToEveryCollector(t *testing.T) {
	cfg := allCollectorsOn()
	cfg.NodeSelector = map[string]string{"pool": "observability"}
	cfg.Tolerations = []corev1.Toleration{{Key: "dedicated", Operator: corev1.TolerationOpExists, Effect: corev1.TaintEffectNoSchedule}}
	cfg.Affinity = &corev1.Affinity{NodeAffinity: &corev1.NodeAffinity{RequiredDuringSchedulingIgnoredDuringExecution: &corev1.NodeSelector{
		NodeSelectorTerms: []corev1.NodeSelectorTerm{{MatchExpressions: []corev1.NodeSelectorRequirement{{Key: "zone", Operator: corev1.NodeSelectorOpIn, Values: []string{"a"}}}}},
	}}}
	for _, name := range []string{agentName, gatewayName, scraperName, profilerName} {
		obj := collectorByName(t, cfg, name)
		if v, _, _ := unstructuredString(obj, "spec", "nodeSelector", "pool"); v != "observability" {
			t.Fatalf("%s: nodeSelector not applied", name)
		}
		tol, _, _ := unstructured.NestedSlice(obj.Object, "spec", "tolerations")
		if len(tol) != 1 || tol[0].(map[string]interface{})["key"] != "dedicated" {
			t.Fatalf("%s: tolerations not applied: %v", name, tol)
		}
		if !hasNestedField(obj, "spec", "affinity", "nodeAffinity") {
			t.Fatalf("%s: affinity not applied", name)
		}
	}
}

func TestRenderCollectorOwnValuesReplaceDefaults(t *testing.T) {
	cfg := allCollectorsOn()
	cfg.NodeSelector = map[string]string{"pool": "default"}
	cfg.Tolerations = []corev1.Toleration{{Key: "default", Operator: corev1.TolerationOpExists}}
	cfg.Agent.NodeSelector = map[string]string{"pool": "agents"}
	cfg.Agent.Tolerations = []corev1.Toleration{{Operator: corev1.TolerationOpExists}}
	cfg.Gateway.Resources = &corev1.ResourceRequirements{Limits: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("2Gi")}}
	cfg.Affinity = &corev1.Affinity{PodAntiAffinity: &corev1.PodAntiAffinity{}}
	cfg.Agent.Affinity = &apiextensionsv1.JSON{Raw: []byte(`{"nodeAffinity":{}}`)}

	agent := collectorByName(t, cfg, agentName)
	if affinity, _, _ := unstructured.NestedMap(agent.Object, "spec", "affinity"); len(affinity) != 1 || affinity["nodeAffinity"] == nil {
		t.Fatalf("agent affinity must be its own, got %v", affinity)
	}
	if v, _, _ := unstructuredString(agent, "spec", "nodeSelector", "pool"); v != "agents" {
		t.Fatalf("agent nodeSelector want agents, got %q", v)
	}
	if ns, _, _ := unstructured.NestedStringMap(agent.Object, "spec", "nodeSelector"); len(ns) != 1 {
		t.Fatalf("own nodeSelector replaces, never merges: %v", ns)
	}
	tol, _, _ := unstructured.NestedSlice(agent.Object, "spec", "tolerations")
	if len(tol) != 1 || tol[0].(map[string]interface{})["key"] != nil {
		t.Fatalf("agent tolerations must be its own, got %v", tol)
	}
	gw := collectorByName(t, cfg, gatewayName)
	if v, _, _ := unstructuredString(gw, "spec", "nodeSelector", "pool"); v != "default" {
		t.Fatalf("gateway falls back to the default nodeSelector, got %q", v)
	}
	if v, _, _ := unstructuredString(gw, "spec", "resources", "limits", "memory"); v != "2Gi" {
		t.Fatalf("gateway resources want 2Gi, got %q", v)
	}
	if hasNestedField(gw, "spec", "resources", "requests") {
		t.Fatal("own resources replace wholesale; built-in requests must not leak in")
	}
}

func TestRenderProfilerHasItsOwnDefaultResources(t *testing.T) {
	cfg := allCollectorsOn()
	cfg.Resources = &corev1.ResourceRequirements{Limits: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("3Gi")}}
	p := collectorByName(t, cfg, profilerName)
	if v, _, _ := unstructuredString(p, "spec", "resources", "limits", "memory"); v != "1Gi" {
		t.Fatalf("spec.resources must not reach the profiler; want 1Gi, got %q", v)
	}
	if v, _, _ := unstructuredString(p, "spec", "resources", "requests", "cpu"); v != "200m" {
		t.Fatalf("profiler request cpu want 200m, got %q", v)
	}
	cfg.Profiling.Resources = &corev1.ResourceRequirements{Limits: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("2")}}
	if v, _, _ := unstructuredString(collectorByName(t, cfg, profilerName), "spec", "resources", "limits", "cpu"); v != "2" {
		t.Fatalf("profiling.resources must win, got %q", v)
	}
	if v, _, _ := unstructuredString(collectorByName(t, cfg, agentName), "spec", "resources", "limits", "memory"); v != "3Gi" {
		t.Fatalf("spec.resources still reaches the agent, got %q", v)
	}
}

func TestRenderExtraEnvsAppendAfterOperatorEnv(t *testing.T) {
	cfg := effective()
	cfg.Agent.ExtraEnvs = []corev1.EnvVar{{Name: "GOMEMLIMIT", Value: "400MiB"}, {Name: "B", Value: "2"}}
	env, _, _ := unstructured.NestedSlice(collectorByName(t, cfg, agentName).Object, "spec", "env")
	n := len(env)
	if n < 3 || env[n-2].(map[string]interface{})["name"] != "GOMEMLIMIT" || env[n-1].(map[string]interface{})["name"] != "B" {
		t.Fatalf("extraEnvs must come last, in order: %v", env)
	}
	if env[0].(map[string]interface{})["name"] != "TSUGA_API_KEY" {
		t.Fatal("operator env must stay first")
	}
	gwEnv, _, _ := unstructured.NestedSlice(collectorByName(t, cfg, gatewayName).Object, "spec", "env")
	for _, e := range gwEnv {
		if e.(map[string]interface{})["name"] == "GOMEMLIMIT" {
			t.Fatal("agent extraEnvs leaked into the gateway")
		}
	}
}

func TestRenderAgentHostNetwork(t *testing.T) {
	agent := collectorByName(t, effective(), agentName)
	if hasNestedField(agent, "spec", "hostNetwork") || hasNestedField(agent, "spec", "dnsPolicy") {
		t.Fatal("the agent stays off the host network by default")
	}
	cfg := effective()
	cfg.Agent.HostNetwork = true
	agent = collectorByName(t, cfg, agentName)
	if v, _, _ := unstructured.NestedBool(agent.Object, "spec", "hostNetwork"); !v {
		t.Fatal("hostNetwork: true renders spec.hostNetwork")
	}
	if v, _, _ := unstructuredString(agent, "spec", "dnsPolicy"); v != "ClusterFirstWithHostNet" {
		t.Fatalf("host-network pods need ClusterFirstWithHostNet for cluster DNS, got %q", v)
	}
	if hasNestedField(collectorByName(t, cfg, gatewayName), "spec", "hostNetwork") {
		t.Fatal("only the agent uses the host network")
	}
}

func TestRenderNoAgentWhenDisabled(t *testing.T) {
	cfg := effective()
	cfg.Agent.Enabled = ptr.To(false)
	objs, err := RenderCollectors(cfg)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(objs))
	for _, o := range objs {
		names = append(names, o.GetName())
	}
	if !slices.Contains(names, "tsuga-gateway") || slices.Contains(names, "tsuga-agent") {
		t.Fatalf("want the gateway and no agent, got %v", names)
	}
	for _, o := range RenderCollectorServiceAccounts(cfg) {
		if o.GetName() == "tsuga-agent" {
			t.Fatal("agent ServiceAccount rendered")
		}
	}
	for _, o := range RenderCollectorRBAC(cfg) {
		if strings.Contains(o.GetName(), "tsuga-agent") {
			t.Fatalf("agent RBAC rendered: %s", o.GetName())
		}
	}
}

func TestAgentDisabledWithNoOtherCollectorRendersNothing(t *testing.T) {
	cfg := effective()
	cfg.Agent.Enabled = ptr.To(false)
	cfg.Gateway = v1alpha1.GatewayToggles{}
	objs, err := RenderCollectors(cfg)
	if err != nil || len(objs) != 0 {
		t.Fatalf("want no objects and no error, got %d, %v", len(objs), err)
	}
}

func TestAgentOTLPPort(t *testing.T) {
	cases := []struct {
		name     string
		protocol string
		endpoint string
		port     int32
	}{
		{"unset http", "http", "", 4318},
		{"unset grpc", "grpc", "", 4317},
		{"custom host and port", "http", "0.0.0.0:14318", 14318},
		{"env host", "grpc", "${env:MY_POD_IP}:4320", 4320},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var otlp v1alpha1.AgentOTLP
			if c.protocol == "http" {
				otlp.HTTPEndpoint = c.endpoint
			} else {
				otlp.GRPCEndpoint = c.endpoint
			}
			cfg := v1alpha1.TsugaCollectorConfigSpec{Agent: v1alpha1.TelemetryToggles{OTLP: otlp}}
			if port := AgentOTLPPort(cfg, c.protocol); port != c.port {
				t.Fatalf("got %d, want %d", port, c.port)
			}
		})
	}
}

func TestRenderAgentDefaultPlacement(t *testing.T) {
	agentTolerations := func(cfg v1alpha1.TsugaCollectorConfigSpec) []interface{} {
		tol, _, _ := unstructured.NestedSlice(collectorByName(t, cfg, agentName).Object, "spec", "tolerations")
		return tol
	}
	agentAffinity := func(cfg v1alpha1.TsugaCollectorConfigSpec) map[string]interface{} {
		a, _, _ := unstructured.NestedMap(collectorByName(t, cfg, agentName).Object, "spec", "affinity")
		return a
	}
	exprKeys := func(affinity map[string]interface{}) []string {
		terms, _, _ := unstructured.NestedSlice(affinity, "nodeAffinity", "requiredDuringSchedulingIgnoredDuringExecution", "nodeSelectorTerms")
		if len(terms) != 1 {
			return nil
		}
		var keys []string
		for _, e := range terms[0].(map[string]interface{})["matchExpressions"].([]interface{}) {
			m := e.(map[string]interface{})
			keys = append(keys, fmt.Sprintf("%s %s %v", m["key"], m["operator"], m["values"]))
		}
		return keys
	}
	wantExprs := []string{
		"kubernetes.io/os In [linux]",
		"eks.amazonaws.com/compute-type NotIn [fargate]",
		"type NotIn [virtual-kubelet]",
	}

	cfg := allCollectorsOn()
	if tol := agentTolerations(cfg); len(tol) != 1 || len(tol[0].(map[string]interface{})) != 1 || tol[0].(map[string]interface{})["operator"] != "Exists" {
		t.Fatalf("agent must tolerate every taint by default, got %v", tol)
	}
	if got := exprKeys(agentAffinity(cfg)); !slices.Equal(got, wantExprs) {
		t.Fatalf("agent default affinity want %v, got %v", wantExprs, got)
	}

	cfg = allCollectorsOn()
	cfg.Tolerations = []corev1.Toleration{{Key: "dedicated", Operator: corev1.TolerationOpExists, Effect: corev1.TaintEffectNoSchedule}}
	if tol := agentTolerations(cfg); len(tol) != 1 || tol[0].(map[string]interface{})["key"] != "dedicated" {
		t.Fatalf("spec.tolerations must replace the agent default, got %v", tol)
	}
	if got := exprKeys(agentAffinity(cfg)); !slices.Equal(got, wantExprs) {
		t.Fatalf("tolerations must not drop the affinity default, got %v", got)
	}

	cfg = allCollectorsOn()
	cfg.Agent.Tolerations = []corev1.Toleration{{Key: "gpu", Operator: corev1.TolerationOpExists}}
	if tol := agentTolerations(cfg); len(tol) != 1 || tol[0].(map[string]interface{})["key"] != "gpu" {
		t.Fatalf("agent.tolerations must replace the agent default, got %v", tol)
	}

	cfg = allCollectorsOn()
	cfg.Affinity = &corev1.Affinity{NodeAffinity: &corev1.NodeAffinity{RequiredDuringSchedulingIgnoredDuringExecution: &corev1.NodeSelector{
		NodeSelectorTerms: []corev1.NodeSelectorTerm{{MatchExpressions: []corev1.NodeSelectorRequirement{{Key: "zone", Operator: corev1.NodeSelectorOpIn, Values: []string{"a"}}}}},
	}}}
	if got := exprKeys(agentAffinity(cfg)); !slices.Equal(got, []string{"zone In [a]"}) {
		t.Fatalf("spec.affinity must replace the agent default, got %v", got)
	}
	if tol := agentTolerations(cfg); tol != nil {
		t.Fatalf("a custom affinity drops the exclusions, so it must drop the toleration default too, got %v", tol)
	}

	cfg = allCollectorsOn()
	cfg.Agent.Affinity = &apiextensionsv1.JSON{Raw: []byte(`{}`)}
	if a := agentAffinity(cfg); len(a) != 0 {
		t.Fatalf("agent.affinity must replace the agent default, got %v", a)
	}
	if tol := agentTolerations(cfg); tol != nil {
		t.Fatalf("an empty agent.affinity must drop the toleration default, got %v", tol)
	}

	// Decoded the way the API server's JSON reaches the controller.
	for _, doc := range []string{`{"agent":{"tolerations":[]}}`, `{"tolerations":[]}`} {
		cfg = allCollectorsOn()
		if err := yaml.Unmarshal([]byte(doc), &cfg); err != nil {
			t.Fatal(err)
		}
		if tol := agentTolerations(cfg); tol != nil {
			t.Fatalf("%s must opt the agent out of the toleration default, got %v", doc, tol)
		}
		if got := exprKeys(agentAffinity(cfg)); !slices.Equal(got, wantExprs) {
			t.Fatalf("%s must keep the affinity default, got %v", doc, got)
		}
	}
}

func TestRenderProfilerDefaultsToLinuxNodes(t *testing.T) {
	cfg := allCollectorsOn()
	ns, _, _ := unstructured.NestedStringMap(collectorByName(t, cfg, profilerName).Object, "spec", "nodeSelector")
	if len(ns) != 1 || ns["kubernetes.io/os"] != "linux" {
		t.Fatalf("profiler nodeSelector want kubernetes.io/os=linux, got %v", ns)
	}
	if hasNestedField(collectorByName(t, cfg, agentName), "spec", "nodeSelector") {
		t.Fatal("agent must keep no default nodeSelector")
	}
	cfg.NodeSelector = map[string]string{"pool": "observability"}
	ns, _, _ = unstructured.NestedStringMap(collectorByName(t, cfg, profilerName).Object, "spec", "nodeSelector")
	if len(ns) != 1 || ns["pool"] != "observability" {
		t.Fatalf("spec.nodeSelector must replace the linux default, got %v", ns)
	}
	cfg.NodeSelector = nil
	cfg.Profiling.NodeSelector = map[string]string{"pool": "profiling"}
	ns, _, _ = unstructured.NestedStringMap(collectorByName(t, cfg, profilerName).Object, "spec", "nodeSelector")
	if len(ns) != 1 || ns["pool"] != "profiling" {
		t.Fatalf("profiling.nodeSelector must replace the linux default, got %v", ns)
	}
}
