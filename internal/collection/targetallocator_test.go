package collection

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/tsuga-dev/tsuga-operator/api/v1alpha1"
)

// effectiveWithTargetAllocator returns the scraping fixture with the Target
// Allocator switched on.
func effectiveWithTargetAllocator() v1alpha1.TsugaCollectorConfigSpec {
	cfg := effectiveWithPrometheus()
	cfg.Prometheus.TargetAllocator.Enabled = true
	cfg.Prometheus.TargetAllocator.AllocationStrategy = "per-node"
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
	if strategy != "per-node" {
		t.Fatalf("allocationStrategy want per-node got %q", strategy)
	}
	sa, _, _ := unstructured.NestedString(ta.Object, "spec", "serviceAccount")
	if sa != ScraperServiceAccountName {
		t.Fatalf("serviceAccount want %q got %q", ScraperServiceAccountName, sa)
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
