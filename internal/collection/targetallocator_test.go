package collection

import (
	"reflect"
	"slices"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/utils/ptr"

	"github.com/tsuga-dev/tsuga-operator/api/v1alpha1"
)

// effectiveWithTargetAllocator returns the scraping fixture with the Target
// Allocator switched on.
func effectiveWithTargetAllocator() v1alpha1.TsugaCollectorConfigSpec {
	cfg := effectiveWithPrometheus()
	cfg.Prometheus.TargetAllocator.Enabled = true
	cfg.Prometheus.TargetAllocator.AllocationStrategy = "least-weighted"
	return cfg
}

func TestRenderOmitsTargetAllocatorWhenDisabled(t *testing.T) {
	objs, err := RenderCollectors(effectiveWithPrometheus())
	if err != nil {
		t.Fatal(err)
	}
	for _, o := range objs {
		if o.GetKind() == "TargetAllocator" {
			t.Fatal("TargetAllocator rendered although the toggle is off")
		}
	}
	scraper := objs[2]
	if _, ok := scraper.GetLabels()[targetAllocatorLabel]; ok {
		t.Fatal("collector carries the target-allocator label with the toggle off")
	}
}

func TestRenderTargetAllocatorCR(t *testing.T) {
	objs, err := RenderCollectors(effectiveWithTargetAllocator())
	if err != nil {
		t.Fatal(err)
	}
	if len(objs) != 4 {
		t.Fatalf("want agent+gateway+scraper+ta, got %d objects", len(objs))
	}
	ta := objs[3]
	if ta.GetKind() != "TargetAllocator" {
		t.Fatalf("kind want TargetAllocator got %q", ta.GetKind())
	}
	// v1alpha1, not v1beta1: the standalone TargetAllocator CRD has not
	// graduated alongside OpenTelemetryCollector.
	if ta.GetAPIVersion() != "opentelemetry.io/v1alpha1" {
		t.Fatalf("apiVersion want opentelemetry.io/v1alpha1 got %q", ta.GetAPIVersion())
	}
	if ta.GetName() != "tsuga-scraper-ta" {
		t.Fatalf("name want tsuga-scraper-ta got %q", ta.GetName())
	}
	if ta.GetNamespace() != effectiveWithTargetAllocator().CollectorNamespace {
		t.Fatalf("unexpected namespace %q", ta.GetNamespace())
	}
	strategy, _, _ := unstructured.NestedString(ta.Object, "spec", "allocationStrategy")
	if strategy != "least-weighted" {
		t.Fatalf("allocationStrategy want least-weighted got %q", strategy)
	}
	sa, _, _ := unstructured.NestedString(ta.Object, "spec", "serviceAccount")
	if sa != "tsuga-scraper" {
		t.Fatalf("serviceAccount want %q got %q", "tsuga-scraper", sa)
	}
}

func TestRenderScraperLinkedToTargetAllocator(t *testing.T) {
	objs, err := RenderCollectors(effectiveWithTargetAllocator())
	if err != nil {
		t.Fatal(err)
	}
	scraper := objs[2]
	// The label is the entire link. Verified against OTel Operator 0.159.0:
	// on seeing it the operator moves scrape_configs into the allocator's
	// ConfigMap and writes the receiver's target_allocator block itself,
	// pointing at the Service it names "<allocator>-targetallocator".
	if got := scraper.GetLabels()[targetAllocatorLabel]; got != "tsuga-scraper-ta" {
		t.Fatalf("collector label want tsuga-scraper-ta got %q", got)
	}
	// Anything written here is overwritten by the operator, so writing a
	// target_allocator block only invites drift between what this package
	// renders and what actually runs.
	if hasNestedField(scraper, "spec", "config", "receivers", "prometheus", "target_allocator") {
		t.Fatal("rendered a target_allocator block the operator will overwrite")
	}
	// scrape_configs must stay: the operator reads them off this collector to
	// build the allocator's ConfigMap, and scrape_interval survives the move.
	if !hasNestedField(scraper, "spec", "config", "receivers", "prometheus", "config", "scrape_configs") {
		t.Fatal("scrape_configs missing from the scraper config")
	}
	raw, err := yamlRoundtrip(scraper)
	if err != nil {
		t.Fatal(err)
	}
	if !contains(raw, "scrape_interval: 45s") {
		t.Fatal("scrape_interval not carried into the scrape job")
	}
}

func withPrometheusCR(cfg v1alpha1.TsugaCollectorConfigSpec) v1alpha1.TsugaCollectorConfigSpec {
	cfg.Prometheus.PrometheusCR = v1alpha1.PrometheusCRSpec{
		Enabled:                true,
		ServiceMonitorSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"team": "payments"}},
		AllowNamespaces:        []string{"payments"},
		ScrapeInterval:         "60s",
	}
	return cfg
}

func scrapeJobNames(t *testing.T, cfg v1alpha1.TsugaCollectorConfigSpec) []string {
	t.Helper()
	config := collectorConfig(t, cfg, scraperName)
	jobs := sliceOf(configField(t, config, "receivers", "prometheus", "config", "scrape_configs"))
	names := make([]string, 0, len(jobs))
	for _, j := range jobs {
		names = append(names, j.(map[string]interface{})["job_name"].(string))
	}
	return names
}

func TestTargetAllocatorPrometheusCR(t *testing.T) {
	objs, err := RenderCollectors(withPrometheusCR(effectiveWithTargetAllocator()))
	if err != nil {
		t.Fatal(err)
	}
	var ta *unstructured.Unstructured
	for _, o := range objs {
		if o.GetKind() == "TargetAllocator" {
			ta = o
		}
	}
	if ta == nil {
		t.Fatal("no TargetAllocator rendered")
	}
	cr, found, _ := unstructured.NestedMap(ta.Object, "spec", "prometheusCR")
	if !found {
		t.Fatal("spec.prometheusCR missing")
	}
	if cr["enabled"] != true {
		t.Fatalf("enabled = %v", cr["enabled"])
	}
	team, _, _ := unstructured.NestedString(cr, "serviceMonitorSelector", "matchLabels", "team")
	if team != "payments" {
		t.Fatalf("serviceMonitorSelector team = %q", team)
	}
	if got, _, _ := unstructured.NestedStringSlice(cr, "allowNamespaces"); !slices.Equal(got, []string{"payments"}) {
		t.Fatalf("allowNamespaces = %v", got)
	}
	if cr["scrapeInterval"] != "60s" {
		t.Fatalf("scrapeInterval = %v", cr["scrapeInterval"])
	}
	if got := scrapeJobNames(t, withPrometheusCR(effectiveWithTargetAllocator())); !slices.Equal(got, []string{"tsuga-placeholder"}) {
		t.Fatalf("scrape jobs with annotationDiscovery unset = %v", got)
	}
	both := withPrometheusCR(effectiveWithTargetAllocator())
	both.Prometheus.AnnotationDiscovery = ptr.To(true)
	if got := scrapeJobNames(t, both); !slices.Equal(got, []string{"tsuga-pod-annotations"}) {
		t.Fatalf("scrape jobs with annotationDiscovery true = %v", got)
	}
}

func TestTargetAllocatorPrometheusCRDefaultsSelectorsToMatchAll(t *testing.T) {
	cfg := effectiveWithTargetAllocator()
	cfg.Prometheus.PrometheusCR = v1alpha1.PrometheusCRSpec{Enabled: true}
	objs, err := RenderCollectors(cfg)
	if err != nil {
		t.Fatal(err)
	}
	for _, o := range objs {
		if o.GetKind() != "TargetAllocator" {
			continue
		}
		cr, _, _ := unstructured.NestedMap(o.Object, "spec", "prometheusCR")
		for _, key := range []string{"serviceMonitorSelector", "podMonitorSelector"} {
			if sel, ok := cr[key].(map[string]interface{}); !ok || len(sel) != 0 {
				t.Errorf("%s = %v, want {}", key, cr[key])
			}
		}
		for _, key := range []string{"serviceMonitorNamespaceSelector", "podMonitorNamespaceSelector", "denyNamespaces"} {
			if _, ok := cr[key]; ok {
				t.Errorf("%s rendered", key)
			}
		}
		return
	}
	t.Fatal("no TargetAllocator rendered")
}

func TestPromCRUnsetLeavesScraperAndAllocatorUnchanged(t *testing.T) {
	cfg := effectiveWithTargetAllocator()
	cfg.Prometheus.PrometheusCR = v1alpha1.PrometheusCRSpec{
		ServiceMonitorSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"team": "payments"}},
		AllowNamespaces:        []string{"payments"},
	}
	objs, err := RenderCollectors(cfg)
	if err != nil {
		t.Fatal(err)
	}
	baseline, err := RenderCollectors(effectiveWithTargetAllocator())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(objs, baseline) {
		t.Fatal("prometheusCR fields without enabled changed the render")
	}
	for _, o := range objs {
		if hasNestedField(o, "spec", "prometheusCR") {
			t.Fatalf("%s carries prometheusCR while disabled", o.GetKind())
		}
	}
	if got := scrapeJobNames(t, cfg); !slices.Equal(got, []string{"tsuga-pod-annotations"}) {
		t.Fatalf("scrape jobs = %v", got)
	}
}

func TestScraperPlaceholderWhenAnnotationDiscoveryOff(t *testing.T) {
	cfg := withPrometheusCR(effectiveWithTargetAllocator())
	cfg.Prometheus.AnnotationDiscovery = ptr.To(false)
	config := collectorConfig(t, cfg, scraperName)
	jobs := sliceOf(configField(t, config, "receivers", "prometheus", "config", "scrape_configs"))
	if len(jobs) != 1 {
		t.Fatalf("want exactly the placeholder job, got %v", jobs)
	}
	job := jobs[0].(map[string]interface{})
	if job["job_name"] != "tsuga-placeholder" || job["scrape_interval"] != cfg.Prometheus.ScrapeInterval {
		t.Fatalf("placeholder = %v", job)
	}
	static := sliceOf(job["static_configs"])
	if len(static) != 1 || !slices.Equal(stringSlice(static[0].(map[string]interface{})["targets"]), []string{"localhost:8888"}) {
		t.Fatalf("static_configs = %v", static)
	}
}

func TestTargetAllocatorFollowsScraperPlacement(t *testing.T) {
	cfg := effectiveWithTargetAllocator()
	cfg.NodeSelector = map[string]string{"pool": "default"}
	cfg.Tolerations = []corev1.Toleration{{Key: "dedicated", Operator: corev1.TolerationOpExists}}
	cfg.Affinity = &corev1.Affinity{PodAntiAffinity: &corev1.PodAntiAffinity{}}
	cfg.Prometheus.NodeSelector = map[string]string{"pool": "scrapers"}
	cfg.Prometheus.Affinity = &apiextensionsv1.JSON{Raw: []byte(`{"nodeAffinity":{}}`)}
	objs, err := RenderCollectors(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ta := objs[3]
	if ns, _, _ := unstructured.NestedStringMap(ta.Object, "spec", "nodeSelector"); len(ns) != 1 || ns["pool"] != "scrapers" {
		t.Fatalf("scraper nodeSelector must replace the spec default, got %v", ns)
	}
	if tol, _, _ := unstructured.NestedSlice(ta.Object, "spec", "tolerations"); len(tol) != 1 || tol[0].(map[string]interface{})["key"] != "dedicated" {
		t.Fatalf("spec tolerations not applied, got %v", tol)
	}
	if affinity, _, _ := unstructured.NestedMap(ta.Object, "spec", "affinity"); len(affinity) != 1 || affinity["nodeAffinity"] == nil {
		t.Fatalf("scraper affinity must replace the spec default, got %v", affinity)
	}
	if _, found, _ := unstructured.NestedFieldNoCopy(ta.Object, "spec", "resources"); found {
		t.Fatal("TargetAllocator keeps the OTel Operator's default resources")
	}
}
