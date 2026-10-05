package collection

import (
	"testing"

	"github.com/tsuga-dev/tsuga-operator/api/v1alpha1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestRenderInstrumentationSetsExporterAndLanguages(t *testing.T) {
	obj := RenderInstrumentation("payments", "default",
		v1alpha1.InstrumentationSpec{Enabled: true, Languages: []string{"java", "nodejs"}},
		"https://otlp.tsuga.com")
	if obj.GetAPIVersion() != "opentelemetry.io/v1alpha1" || obj.GetKind() != "Instrumentation" {
		t.Fatalf("wrong gvk: %s/%s", obj.GetAPIVersion(), obj.GetKind())
	}
	ep, _, _ := unstructured.NestedString(obj.Object, "spec", "exporter", "endpoint")
	if ep != "https://otlp.tsuga.com" {
		t.Fatalf("endpoint not set: %q", ep)
	}
	if _, found, _ := unstructured.NestedMap(obj.Object, "spec", "java"); !found {
		t.Fatal("java block missing")
	}
	if _, found, _ := unstructured.NestedMap(obj.Object, "spec", "python"); found {
		t.Fatal("python block should be absent")
	}
}
