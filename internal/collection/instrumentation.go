package collection

import (
	"encoding/json"
	"errors"
	"fmt"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/tsuga-dev/tsuga-operator/api/v1alpha1"
)

// RenderInstrumentation builds an Instrumentation unstructured object for a
// namespace, wiring the exporter endpoint and one empty map block per
// enabled language so the OpenTelemetry Operator applies its per-language
// auto-instrumentation defaults. The user's spec is deep-merged over that
// base; the managed exporter endpoint cannot be overridden. grpcEndpoint is
// the agent's OTLP/gRPC address for the webserver modules.
func RenderInstrumentation(namespace, name string, spec v1alpha1.InstrumentationSpec, endpoint, grpcEndpoint string) (*unstructured.Unstructured, error) {
	base := map[string]interface{}{
		"exporter":    map[string]interface{}{"endpoint": endpoint},
		"propagators": []interface{}{"tracecontext", "baggage"},
	}
	for _, lang := range spec.Languages {
		base[instrumentationKey(lang)] = map[string]interface{}{}
	}
	if spec.Spec != nil {
		var user map[string]interface{}
		if err := json.Unmarshal(spec.Spec.Raw, &user); err != nil {
			return nil, fmt.Errorf("instrumentation.spec: %w", err)
		}
		if exp, ok := user["exporter"]; ok {
			expMap, isMap := exp.(map[string]interface{})
			if !isMap {
				return nil, errors.New("instrumentation.spec.exporter must be an object; its endpoint is managed by the operator")
			}
			if _, found := expMap["endpoint"]; found {
				return nil, errors.New("instrumentation.spec.exporter.endpoint is managed by the operator (it points at the node agent); remove it")
			}
		}
		deepMerge(base, user)
	}
	if err := setWebserverEndpoints(base, spec.Languages, grpcEndpoint); err != nil {
		return nil, err
	}
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "opentelemetry.io/v1alpha1",
		"kind":       "Instrumentation",
		"metadata": map[string]interface{}{
			"name":      name,
			"namespace": namespace,
		},
		"spec": base,
	}}, nil
}

// webserverEndpointAttrs names the module setting that carries the exporter
// endpoint for the webserver languages. The OTel Operator loads only the gRPC
// OTLP exporter into these modules, so they need the agent's gRPC port, not
// spec.exporter.endpoint, which is OTLP/HTTP.
var webserverEndpointAttrs = map[string]string{
	"apacheHttpd": "ApacheModuleOtelExporterEndpoint",
	"nginx":       "NginxModuleOtelExporterEndpoint",
}

// setWebserverEndpoints puts the managed endpoint first in each enabled
// webserver language's attrs, keeping the user's other attrs.
func setWebserverEndpoints(base map[string]interface{}, languages []string, endpoint string) error {
	for _, lang := range languages {
		key := instrumentationKey(lang)
		attr, ok := webserverEndpointAttrs[key]
		if !ok {
			continue
		}
		block, ok := base[key].(map[string]interface{})
		if !ok {
			return fmt.Errorf("instrumentation.spec.%s must be an object", key)
		}
		attrs := sliceOf(block["attrs"])
		for _, a := range attrs {
			if m, _ := a.(map[string]interface{}); m["name"] == attr {
				return fmt.Errorf("instrumentation.spec.%s.attrs %s is managed by the operator (it points at the node agent); remove it", key, attr)
			}
		}
		block["attrs"] = append([]interface{}{map[string]interface{}{"name": attr, "value": endpoint}}, attrs...)
	}
	return nil
}

// instrumentationKey maps a language, which equals the inject annotation
// suffix, to its Instrumentation spec key.
func instrumentationKey(lang string) string {
	if lang == "apache-httpd" {
		return "apacheHttpd"
	}
	return lang
}

// deepMerge folds src into dst: maps merge recursively, anything else in src
// (lists, scalars) replaces the dst value.
func deepMerge(dst, src map[string]interface{}) {
	for k, sv := range src {
		sm, srcIsMap := sv.(map[string]interface{})
		dm, dstIsMap := dst[k].(map[string]interface{})
		if srcIsMap && dstIsMap {
			deepMerge(dm, sm)
			continue
		}
		dst[k] = sv
	}
}
