package collection

import (
	"testing"

	"github.com/tsuga-dev/tsuga-operator/api/v1alpha1"
)

func TestResolveCollectorDefaultsNamespace(t *testing.T) {
	got := ResolveCollector(v1alpha1.TsugaCollectorConfigSpec{
		Export: v1alpha1.ExportSpec{TokenSecretRef: v1alpha1.SecretKeyRef{Name: "s", Key: "k"}},
	})
	if got.CollectorNamespace != "tsuga-operator-system" {
		t.Fatalf("want default namespace, got %q", got.CollectorNamespace)
	}
}

func TestResolveCollectorDefaultsPrometheus(t *testing.T) {
	got := ResolveCollector(v1alpha1.TsugaCollectorConfigSpec{
		Export: v1alpha1.ExportSpec{TokenSecretRef: v1alpha1.SecretKeyRef{Name: "s", Key: "k"}},
	})
	if got.Prometheus.Enabled {
		t.Fatal("prometheus scraping must default to off")
	}
	if got.Prometheus.ScrapeInterval != "30s" {
		t.Fatalf("scrape interval want 30s, got %q", got.Prometheus.ScrapeInterval)
	}
	if got.Prometheus.Replicas != 1 {
		t.Fatalf("replicas want 1, got %d", got.Prometheus.Replicas)
	}
	if got.Prometheus.TargetAllocator.Enabled {
		t.Fatal("target allocator must default to off")
	}
	if got.Prometheus.TargetAllocator.AllocationStrategy != "consistent-hashing" {
		t.Fatalf("allocation strategy want consistent-hashing, got %q", got.Prometheus.TargetAllocator.AllocationStrategy)
	}
}

func TestResolveCollectorKeepsExplicitPrometheus(t *testing.T) {
	got := ResolveCollector(v1alpha1.TsugaCollectorConfigSpec{
		Export: v1alpha1.ExportSpec{TokenSecretRef: v1alpha1.SecretKeyRef{Name: "s", Key: "k"}},
		Prometheus: v1alpha1.PrometheusSpec{
			Enabled:        true,
			ScrapeInterval: "60s",
			Replicas:       3,
			TargetAllocator: v1alpha1.TargetAllocatorSpec{
				Enabled:            true,
				AllocationStrategy: "per-node",
			},
		},
	})
	if !got.Prometheus.Enabled || got.Prometheus.ScrapeInterval != "60s" || got.Prometheus.Replicas != 3 {
		t.Fatalf("explicit prometheus values overwritten: %+v", got.Prometheus)
	}
	if !got.Prometheus.TargetAllocator.Enabled || got.Prometheus.TargetAllocator.AllocationStrategy != "per-node" {
		t.Fatalf("explicit target allocator values overwritten: %+v", got.Prometheus.TargetAllocator)
	}
}
