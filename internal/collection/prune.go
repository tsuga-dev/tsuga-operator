package collection

import "github.com/tsuga-dev/tsuga-operator/api/v1alpha1"

// pruneAgentConfig removes pipelines disabled by the daemonset's telemetry
// toggles and garbage-collects any receiver or connector left unreferenced
// by the remaining pipelines.
func pruneAgentConfig(config map[string]interface{}, toggles v1alpha1.TelemetryToggles) {
	pipelines, ok := asMap(pipelinesOf(config))
	if !ok {
		return
	}
	if !toggles.Traces {
		delete(pipelines, "traces")
	}
	if !toggles.Metrics {
		delete(pipelines, "metrics")
	}
	if !toggles.Logs {
		delete(pipelines, "logs")
	}
	pruneUnreferencedReceivers(config)
}

// pruneGatewayConfig removes pipelines disabled by the gateway's toggles and
// garbage-collects any receiver left unreferenced by the remaining
// pipelines.
func pruneGatewayConfig(config map[string]interface{}, toggles v1alpha1.GatewayToggles) {
	pipelines, ok := asMap(pipelinesOf(config))
	if !ok {
		return
	}
	if !toggles.ClusterMetrics {
		delete(pipelines, "metrics")
	}
	if !toggles.KubernetesObjects {
		delete(pipelines, "logs")
	}
	pruneUnreferencedReceivers(config)
}

// pruneUnreferencedReceivers deletes any entry under "receivers" that no
// remaining pipeline lists in its receivers: array (e.g. file_log when the
// logs pipeline is gone), while keeping entries shared across surviving
// pipelines (e.g. otlp).
//
// A connector (e.g. span_metrics) is different: it acts as an exporter for
// one pipeline and a receiver for another, so it is only valid while both
// sides are wired up. Any connector referenced as a receiver in some
// pipeline but not as an exporter in any pipeline (or vice versa) is a
// dangling reference the collector would refuse to start with, so it is
// dropped from "connectors" and stripped from every pipeline's
// receivers/exporters arrays.
func pruneUnreferencedReceivers(config map[string]interface{}) {
	pipelines, ok := asMap(pipelinesOf(config))
	if !ok {
		return
	}

	asReceiver := map[string]bool{}
	asExporter := map[string]bool{}
	for _, raw := range pipelines {
		pipeline, ok := asMap(raw)
		if !ok {
			continue
		}
		for _, name := range stringSlice(pipeline["receivers"]) {
			asReceiver[name] = true
		}
		for _, name := range stringSlice(pipeline["exporters"]) {
			asExporter[name] = true
		}
	}

	deleteUnreferencedEntries(config, "receivers", asReceiver)

	if connectors, ok := asMap(config["connectors"]); ok {
		for name := range connectors {
			if asReceiver[name] && asExporter[name] {
				continue
			}
			delete(connectors, name)
			removePipelineReference(pipelines, name)
		}
	}
}

// pipelinesOf returns the raw service.pipelines value from a parsed
// collector config, or nil if the shape doesn't match.
func pipelinesOf(config map[string]interface{}) interface{} {
	service, ok := asMap(config["service"])
	if !ok {
		return nil
	}
	return service["pipelines"]
}

// deleteUnreferencedEntries drops keys from config[section] that are absent
// from referenced.
func deleteUnreferencedEntries(config map[string]interface{}, section string, referenced map[string]bool) {
	entries, ok := asMap(config[section])
	if !ok {
		return
	}
	for name := range entries {
		if !referenced[name] {
			delete(entries, name)
		}
	}
}

// removePipelineReference strips name from every remaining pipeline's
// receivers: and exporters: arrays.
func removePipelineReference(pipelines map[string]interface{}, name string) {
	for _, raw := range pipelines {
		pipeline, ok := asMap(raw)
		if !ok {
			continue
		}
		pipeline["receivers"] = stripName(pipeline["receivers"], name)
		pipeline["exporters"] = stripName(pipeline["exporters"], name)
	}
}

// stripName returns v (a decoded YAML/JSON string list) with name removed.
func stripName(v interface{}, name string) []interface{} {
	remaining := make([]interface{}, 0)
	for _, item := range stringSlice(v) {
		if item != name {
			remaining = append(remaining, item)
		}
	}
	return remaining
}

// asMap type-asserts a decoded YAML/JSON value as a string-keyed map.
func asMap(v interface{}) (map[string]interface{}, bool) {
	m, ok := v.(map[string]interface{})
	return m, ok
}

// stringSlice type-asserts a decoded YAML/JSON value as a list of strings.
func stringSlice(v interface{}) []string {
	list, ok := v.([]interface{})
	if !ok {
		return nil
	}
	out := make([]string, 0, len(list))
	for _, item := range list {
		if s, ok := item.(string); ok {
			out = append(out, s)
		}
	}
	return out
}
