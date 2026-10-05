package collection

import (
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/yaml"

	"github.com/tsuga-dev/tsuga-operator/api/v1alpha1"
)

func effective() v1alpha1.TsugaCollectorConfigSpec {
	return ResolveCollector(v1alpha1.TsugaCollectorConfigSpec{
		ClusterName: "prod-eu",
		Export: v1alpha1.ExportSpec{
			TokenSecretRef: v1alpha1.SecretKeyRef{Name: "tsuga-credentials", Key: "api-key"},
			Endpoint:       "https://otlp.tsuga.com",
		},
		Agent:   v1alpha1.TelemetryToggles{Traces: true, Metrics: true, Logs: true},
		Gateway: v1alpha1.GatewayToggles{ClusterMetrics: true, KubernetesObjects: true},
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
