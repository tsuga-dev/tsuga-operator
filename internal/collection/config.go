package collection

import "github.com/tsuga-dev/tsuga-operator/api/v1alpha1"

const defaultCollectorNamespace = "tsuga-operator-system"

const (
	defaultScrapeInterval     = "30s"
	defaultAllocationStrategy = "consistent-hashing"
)

// ResolveCollector returns spec with its defaults filled in.
func ResolveCollector(spec v1alpha1.TsugaCollectorConfigSpec) v1alpha1.TsugaCollectorConfigSpec {
	if spec.CollectorNamespace == "" {
		spec.CollectorNamespace = defaultCollectorNamespace
	}
	// The CRD carries these defaults too, but ResolveCollector is also driven
	// straight from tests and from any caller that never went through API
	// server defaulting, so the zero value is filled in here as well.
	if spec.Prometheus.ScrapeInterval == "" {
		spec.Prometheus.ScrapeInterval = defaultScrapeInterval
	}
	if spec.Prometheus.Replicas < 1 {
		spec.Prometheus.Replicas = 1
	}
	if spec.Prometheus.TargetAllocator.AllocationStrategy == "" {
		spec.Prometheus.TargetAllocator.AllocationStrategy = defaultAllocationStrategy
	}
	return spec
}
