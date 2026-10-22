package collection

import (
	"reflect"
	"strings"
	"testing"

	"github.com/tsuga-dev/tsuga-operator/api/v1alpha1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func renderInst(t *testing.T, langs []string, userSpec string) (*unstructured.Unstructured, error) {
	t.Helper()
	spec := v1alpha1.InstrumentationSpec{Enabled: true, Languages: langs}
	if userSpec != "" {
		spec.Spec = &apiextensionsv1.JSON{Raw: []byte(userSpec)}
	}
	return RenderInstrumentation("payments", "default", spec, "https://otlp.tsuga.com", "http://agent:4317")
}

func TestInstrumentationUnsetUnchanged(t *testing.T) {
	obj, err := renderInst(t, []string{"java", "python"}, "")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]interface{}{
		"apiVersion": "opentelemetry.io/v1alpha1",
		"kind":       "Instrumentation",
		"metadata":   map[string]interface{}{"name": "default", "namespace": "payments"},
		"spec": map[string]interface{}{
			"exporter":    map[string]interface{}{"endpoint": "https://otlp.tsuga.com"},
			"propagators": []interface{}{"tracecontext", "baggage"},
			"java":        map[string]interface{}{},
			"python":      map[string]interface{}{},
		},
	}
	if !reflect.DeepEqual(obj.Object, want) {
		t.Fatalf("got %v, want %v", obj.Object, want)
	}
}

func TestInstrumentationLanguageKeys(t *testing.T) {
	obj, err := renderInst(t, []string{"apache-httpd", "nginx", "go"}, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"apacheHttpd", "nginx", "go"} {
		if _, found, _ := unstructured.NestedMap(obj.Object, "spec", key); !found {
			t.Errorf("spec.%s missing", key)
		}
	}
	if _, found, _ := unstructured.NestedMap(obj.Object, "spec", "apache-httpd"); found {
		t.Error("spec.apache-httpd must not be rendered")
	}
}

func TestInstrumentationSpecMerge(t *testing.T) {
	obj, err := renderInst(t, []string{"java"}, `{"sampler":{"type":"parentbased_traceidratio","argument":"0.25"},"propagators":["b3"],"java":{"image":"x/java:1"},"exporter":{"tls":{"ca_file":"/ca.crt"}},"python":{"env":[{"name":"A","value":"1"}]}}`)
	if err != nil {
		t.Fatal(err)
	}
	spec := obj.Object["spec"].(map[string]interface{})
	if got, _, _ := unstructured.NestedString(spec, "sampler", "type"); got != "parentbased_traceidratio" {
		t.Errorf("sampler.type = %q", got)
	}
	if !reflect.DeepEqual(spec["propagators"], []interface{}{"b3"}) {
		t.Errorf("propagators = %v", spec["propagators"])
	}
	if got, _, _ := unstructured.NestedString(spec, "java", "image"); got != "x/java:1" {
		t.Errorf("java.image = %q", got)
	}
	if got, _, _ := unstructured.NestedString(spec, "exporter", "endpoint"); got != "https://otlp.tsuga.com" {
		t.Errorf("exporter.endpoint = %q", got)
	}
	if got, _, _ := unstructured.NestedString(spec, "exporter", "tls", "ca_file"); got != "/ca.crt" {
		t.Errorf("exporter.tls.ca_file = %q", got)
	}
	if _, found, _ := unstructured.NestedMap(spec, "python"); !found {
		t.Error("python block missing")
	}
}

func TestInstrumentationEndpointRejected(t *testing.T) {
	_, err := renderInst(t, []string{"java"}, `{"exporter":{"endpoint":"http://x"}}`)
	const want = "instrumentation.spec.exporter.endpoint is managed by the operator (it points at the node agent); remove it"
	if err == nil || err.Error() != want {
		t.Fatalf("got %v, want %q", err, want)
	}
}

func TestInstrumentationExporterMustBeObject(t *testing.T) {
	const want = "instrumentation.spec.exporter must be an object; its endpoint is managed by the operator"
	for _, userSpec := range []string{`{"exporter":null}`, `{"exporter":"x"}`} {
		_, err := renderInst(t, []string{"java"}, userSpec)
		if err == nil || err.Error() != want {
			t.Errorf("%s: got %v, want %q", userSpec, err, want)
		}
	}
}

func TestInstrumentationWebserversExportToTheGRPCPort(t *testing.T) {
	obj, err := renderInst(t, []string{"nginx", "apache-httpd", "java"},
		`{"nginx":{"attrs":[{"name":"NginxModuleTraceAsError","value":"OFF"}]}}`)
	if err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string][]interface{}{
		"nginx": {
			map[string]interface{}{"name": "NginxModuleOtelExporterEndpoint", "value": "http://agent:4317"},
			map[string]interface{}{"name": "NginxModuleTraceAsError", "value": "OFF"},
		},
		"apacheHttpd": {
			map[string]interface{}{"name": "ApacheModuleOtelExporterEndpoint", "value": "http://agent:4317"},
		},
	} {
		if got, _, _ := unstructured.NestedSlice(obj.Object, "spec", key, "attrs"); !reflect.DeepEqual(got, want) {
			t.Errorf("%s attrs = %v, want %v", key, got, want)
		}
	}
	if _, found, _ := unstructured.NestedFieldNoCopy(obj.Object, "spec", "java", "attrs"); found {
		t.Error("java gets no webserver attrs")
	}
}

func TestInstrumentationWebserverEndpointIsManaged(t *testing.T) {
	_, err := renderInst(t, []string{"nginx"}, `{"nginx":{"attrs":[{"name":"NginxModuleOtelExporterEndpoint","value":"x:4317"}]}}`)
	if err == nil || !strings.Contains(err.Error(), "NginxModuleOtelExporterEndpoint is managed") {
		t.Fatalf("want managed-attr error, got %v", err)
	}
}
