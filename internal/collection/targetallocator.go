package collection

import (
	"fmt"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"

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
// with the scraper collector. It is placed like the scraper, but keeps the OTel
// Operator's default resources.
//
// spec.serviceAccount is the scraper collector's ServiceAccount, which keeps
// the collector ClusterRoleBinding to a single subject.
func renderTargetAllocator(cfg v1alpha1.TsugaCollectorConfigSpec) (*unstructured.Unstructured, error) {
	spec, err := podPlacement(cfg.Prometheus.CollectorPodSpec, cfg)
	if err != nil {
		return nil, err
	}
	spec["allocationStrategy"] = cfg.Prometheus.TargetAllocator.AllocationStrategy
	spec["serviceAccount"] = ServiceAccountName(cfg, scraperName)
	if cfg.Prometheus.PrometheusCR.Enabled {
		cr, err := renderPrometheusCR(cfg.Prometheus.PrometheusCR)
		if err != nil {
			return nil, err
		}
		spec["prometheusCR"] = cr
	}
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": targetAllocatorAPIVersion,
		"kind":       targetAllocatorKind,
		"metadata": map[string]interface{}{
			"name":      targetAllocatorName,
			"namespace": cfg.CollectorNamespace,
		},
		"spec": spec,
	}}, nil
}

// renderPrometheusCR always renders the monitor selectors as {} when unset:
// upstream passes a missing selector through to prometheus-operator, where nil
// selects nothing. Namespace selectors stay omitted because the Target
// Allocator defaults them to {}.
func renderPrometheusCR(cr v1alpha1.PrometheusCRSpec) (map[string]interface{}, error) {
	out, err := runtime.DefaultUnstructuredConverter.ToUnstructured(&cr)
	if err != nil {
		return nil, fmt.Errorf("convert prometheusCR: %w", err)
	}
	for _, key := range []string{"serviceMonitorSelector", "podMonitorSelector"} {
		if _, ok := out[key]; !ok {
			out[key] = map[string]interface{}{}
		}
	}
	return out, nil
}
