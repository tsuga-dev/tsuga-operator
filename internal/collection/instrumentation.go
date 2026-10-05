package collection

import (
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/tsuga-dev/tsuga-operator/api/v1alpha1"
)

// RenderInstrumentation builds an Instrumentation unstructured object for a
// namespace, wiring the exporter endpoint and one empty map block per
// enabled language so the OpenTelemetry Operator applies its per-language
// auto-instrumentation defaults.
func RenderInstrumentation(namespace, name string, spec v1alpha1.InstrumentationSpec, endpoint string) *unstructured.Unstructured {
	obj := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "opentelemetry.io/v1alpha1",
		"kind":       "Instrumentation",
		"metadata": map[string]interface{}{
			"name":      name,
			"namespace": namespace,
		},
		"spec": map[string]interface{}{
			"exporter":    map[string]interface{}{"endpoint": endpoint},
			"propagators": []interface{}{"tracecontext", "baggage"},
		},
	}}
	for _, lang := range spec.Languages {
		_ = unstructured.SetNestedMap(obj.Object, map[string]interface{}{}, "spec", lang)
	}
	return obj
}
