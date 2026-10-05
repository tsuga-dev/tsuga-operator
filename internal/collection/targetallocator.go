package collection

import (
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/tsuga-dev/tsuga-operator/api/v1alpha1"
)

const (
	// targetAllocatorAPIVersion is still v1alpha1: the standalone
	// TargetAllocator CRD has not graduated alongside OpenTelemetryCollector.
	targetAllocatorAPIVersion = "opentelemetry.io/v1alpha1"
	targetAllocatorKind       = "TargetAllocator"

	// targetAllocatorName is the allocator paired with the scraper collector.
	targetAllocatorName = scraperName + "-ta"

	// targetAllocatorLabel is the label the OTel Operator reads off a
	// collector to find the allocator that owns its scrape configs. It is the
	// whole mechanism: on seeing it the operator moves the receiver's
	// scrape_configs into the allocator's ConfigMap and writes the receiver's
	// target_allocator block itself, pointing at the Service it names
	// "<allocator>-targetallocator". Anything this package writes under
	// target_allocator is discarded, so it writes none.
	targetAllocatorLabel = "opentelemetry.io/target-allocator"
)

// renderTargetAllocator builds the TargetAllocator custom resource paired
// with the scraper collector.
//
// spec.serviceAccount points at the scraper collector's own ServiceAccount,
// which keeps the collector ClusterRoleBinding to a single subject. That
// costs a startup race: the allocator pod fails admission while the OTel
// Operator has yet to create that ServiceAccount. Kubernetes retries with
// backoff, so it self-heals.
func renderTargetAllocator(cfg v1alpha1.TsugaCollectorConfigSpec) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": targetAllocatorAPIVersion,
		"kind":       targetAllocatorKind,
		"metadata": map[string]interface{}{
			"name":      targetAllocatorName,
			"namespace": cfg.CollectorNamespace,
		},
		"spec": map[string]interface{}{
			"allocationStrategy": cfg.Prometheus.TargetAllocator.AllocationStrategy,
			"serviceAccount":     ScraperServiceAccountName,
		},
	}}
}
