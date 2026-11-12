package collection

import (
	"cmp"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	utiljson "k8s.io/apimachinery/pkg/util/json"
	"k8s.io/utils/ptr"

	"github.com/tsuga-dev/tsuga-operator/api/v1alpha1"
)

const (
	customExporterName    = "otlp/tsuga-agent"
	memoryLimiterName     = "memory_limiter"
	batchName             = "batch"
	customCollectorPrefix = "tsuga-"
)

// CustomCollectorName is the OpenTelemetryCollector a TsugaCollector renders.
// The tsuga- prefix puts its pods under the collector-health monitors.
func CustomCollectorName(name string) string {
	return customCollectorPrefix + name
}

// RenderCustomCollector builds the OpenTelemetryCollector for a TsugaCollector:
// the user's spec as written, plus what the toggles enable. Nothing the spec
// defines is replaced. cluster must be resolved with ResolveCollector;
// agentHTTPEndpoint is the agent's OTLP/HTTP base URL.
func RenderCustomCollector(name, namespace string, spec v1alpha1.TsugaCollectorSpec, cluster v1alpha1.TsugaCollectorConfigSpec, agentGRPCEndpoint, agentHTTPEndpoint string) (*unstructured.Unstructured, error) {
	if spec.Collector == nil || len(spec.Collector.Raw) == 0 {
		return nil, errors.New("spec.collector is required")
	}
	var otel map[string]interface{}
	if err := utiljson.Unmarshal(spec.Collector.Raw, &otel); err != nil || otel == nil {
		return nil, fmt.Errorf("spec.collector must be an OpenTelemetryCollector spec object: %v", err)
	}
	config, ok := asMap(otel["config"])
	if !ok {
		return nil, errors.New("spec.collector.config must be an object (opentelemetry.io/v1beta1 takes the collector config as an object, not a string)")
	}
	pipelines := asMapOrEmpty(pipelinesOf(config))
	if len(pipelines) == 0 {
		return nil, errors.New("spec.collector.config.service.pipelines is empty; a collector without pipelines does not start")
	}

	if image, _ := otel["image"].(string); image == "" {
		otel["image"] = cmp.Or(cluster.Image, defaultAgentImage)
	}
	// The two security contexts are filled as a pair: a pod-level
	// runAsNonRoot would contradict a user's container runAsUser: 0.
	_, podSC := otel["podSecurityContext"]
	_, containerSC := otel["securityContext"]
	for key, value := range defaultCollectorPodFields() {
		_, set := otel[key]
		if key == "podSecurityContext" || key == "securityContext" {
			set = podSC || containerSC
		}
		if !set {
			otel[key] = value
		}
	}
	if otel["mode"] == PostgresCollectorSidecar && !podSC && !containerSC {
		moveSecurityContextToContainer(otel)
	}

	exporter := ptr.Deref(spec.TsugaExporter, true)
	processors := ptr.Deref(spec.DefaultProcessors, true)
	if exporter {
		if _, taken := asMapOrEmpty(config[fieldExporters])[customExporterName]; taken {
			return nil, fmt.Errorf("exporter %s is added by tsugaExporter; rename yours or turn tsugaExporter off", customExporterName)
		}
	}
	connectors := asMapOrEmpty(config[fieldConnectors])
	for _, pipelineName := range slices.Sorted(maps.Keys(pipelines)) {
		pipeline, ok := asMap(pipelines[pipelineName])
		if !ok || passesThroughConnector(pipelineName, pipeline, connectors, pipelines) {
			continue
		}
		if processors {
			addDefaultProcessors(pipeline)
		}
		if exporter {
			if err := checkAgentAccepts(pipelineName, cluster.Agent); err != nil {
				return nil, err
			}
			pipeline[fieldExporters] = appendMissing(sliceOf(pipeline[fieldExporters]), []string{customExporterName})
		}
	}
	if processors {
		defineReferencedProcessors(config, pipelines)
	}
	if exporter {
		nestedMap(config, fieldExporters)[customExporterName] = map[string]interface{}{
			"endpoint":    agentGRPCEndpoint,
			"compression": "gzip",
			"tls":         map[string]interface{}{"insecure": true},
		}
	}
	if ptr.Deref(spec.SelfTelemetry, true) {
		if err := addSelfTelemetry(otel, config, name, cluster.Agent, agentHTTPEndpoint); err != nil {
			return nil, err
		}
	}

	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": collectorAPIVersion,
		"kind":       collectorKind,
		"metadata": map[string]interface{}{
			"name":      CustomCollectorName(name),
			"namespace": namespace,
		},
		"spec": otel,
	}}, nil
}

// passesThroughConnector reports whether the pipeline hands its own data,
// through a connector, to another pipeline of the same signal, which exports
// it (forward, routing, failover...). A connector feeding another signal
// (spanmetrics, count, datadog) derives new data, so the source pipeline
// still has to export its own.
func passesThroughConnector(name string, pipeline, connectors, pipelines map[string]interface{}) bool {
	signal := componentType(name)
	return slices.ContainsFunc(stringSlice(pipeline[fieldExporters]), func(e string) bool {
		if _, ok := connectors[e]; !ok {
			return false
		}
		for other, raw := range pipelines {
			if other != name && componentType(other) == signal && slices.Contains(stringSlice(asMapOrEmpty(raw)[fieldReceivers]), e) {
				return true
			}
		}
		return false
	})
}

// componentType is a component ID's type: batch for both batch and batch/big.
func componentType(id string) string {
	t, _, _ := strings.Cut(id, "/")
	return t
}

func hasType(list []interface{}, typ string) bool {
	return slices.ContainsFunc(stringSlice(list), func(id string) bool { return componentType(id) == typ })
}

// addDefaultProcessors puts memory_limiter first and batch last unless the
// pipeline already has a processor of that type.
func addDefaultProcessors(pipeline map[string]interface{}) {
	list := sliceOf(pipeline[fieldProcessors])
	if !hasType(list, memoryLimiterName) {
		list = append([]interface{}{memoryLimiterName}, list...)
	}
	if !hasType(list, batchName) {
		list = append(list, batchName)
	}
	pipeline[fieldProcessors] = list
}

// defineReferencedProcessors defines memory_limiter and batch when a pipeline
// references them and the config has no definition of that name.
func defineReferencedProcessors(config, pipelines map[string]interface{}) {
	defaults := map[string]map[string]interface{}{
		memoryLimiterName: {"check_interval": "5s", "limit_percentage": int64(80), "spike_limit_percentage": int64(25)},
		batchName:         {},
	}
	for _, name := range []string{memoryLimiterName, batchName} {
		referenced := slices.ContainsFunc(slices.Collect(maps.Values(pipelines)), func(raw interface{}) bool {
			return slices.Contains(stringSlice(asMapOrEmpty(raw)[fieldProcessors]), name)
		})
		if !referenced {
			continue
		}
		defined := nestedMap(config, fieldProcessors)
		if _, ok := defined[name]; !ok {
			defined[name] = defaults[name]
		}
	}
}

// checkAgentAccepts rejects a pipeline whose signal the node agent drops: the
// agent has no profiles pipeline, and its signal toggles remove whole
// pipelines.
func checkAgentAccepts(pipelineName string, agent v1alpha1.TelemetryToggles) error {
	signal := componentType(pipelineName)
	accepted, known := map[string]bool{
		"traces":   agent.Traces,
		"metrics":  agent.Metrics,
		"logs":     agent.Logs,
		"profiles": false,
	}[signal]
	switch {
	case !known:
		return fmt.Errorf("pipeline %s: unknown signal %s", pipelineName, signal)
	case !accepted:
		return fmt.Errorf("pipeline %s: the node agent does not accept %s; turn tsugaExporter off or enable it on the agent", pipelineName, signal)
	}
	return nil
}

// addSelfTelemetry sends the collector's internal metrics to the agent unless
// the user configured service.telemetry.metrics, and fills absent resource
// keys and the env vars they read.
func addSelfTelemetry(otel, config map[string]interface{}, name string, agent v1alpha1.TelemetryToggles, agentHTTPEndpoint string) error {
	telemetry := nestedMap(nestedMap(config, "service"), "telemetry")
	if _, set := telemetry["metrics"]; !set {
		if !agent.Metrics {
			return errors.New("selfTelemetry needs metrics enabled on the node agent; enable agent.metrics or turn selfTelemetry off")
		}
		telemetry["metrics"] = map[string]interface{}{"readers": []interface{}{
			map[string]interface{}{"periodic": map[string]interface{}{"exporter": map[string]interface{}{"otlp": map[string]interface{}{
				"protocol": "http/protobuf",
				// Tsuga monitors sum delta counters; the SDK default is cumulative.
				"temporality_preference": "delta",
				"endpoint":               agentHTTPEndpoint + "/v1/metrics",
			}}}},
		}}
	}
	resource := nestedMap(telemetry, "resource")
	for key, value := range map[string]string{
		"service.name":        CustomCollectorName(name),
		"service.instance.id": "${env:POD_UID}",
		"k8s.pod.name":        "${env:POD_NAME}",
	} {
		if _, set := resource[key]; !set {
			resource[key] = value
		}
	}
	env := sliceOf(otel["env"])
	for _, v := range []struct{ name, field string }{{"POD_UID", "metadata.uid"}, {"POD_NAME", "metadata.name"}} {
		if slices.ContainsFunc(env, func(e interface{}) bool { return asMapOrEmpty(e)["name"] == v.name }) {
			continue
		}
		env = append(env, map[string]interface{}{
			"name":      v.name,
			"valueFrom": map[string]interface{}{"fieldRef": map[string]interface{}{"fieldPath": v.field}},
		})
	}
	otel["env"] = env
	return nil
}

// defaultCollectorPodFields are the resources and security contexts the
// operator gives a collector whose spec leaves them unset.
func defaultCollectorPodFields() map[string]interface{} {
	return map[string]interface{}{
		"resources": map[string]interface{}{
			"requests": map[string]interface{}{"cpu": "50m", "memory": "64Mi"},
			"limits":   map[string]interface{}{"cpu": "200m", "memory": "256Mi"},
		},
		"podSecurityContext": map[string]interface{}{
			"runAsNonRoot":   true,
			"seccompProfile": map[string]interface{}{"type": "RuntimeDefault"},
		},
		"securityContext": map[string]interface{}{
			"allowPrivilegeEscalation": false,
			"capabilities":             map[string]interface{}{"drop": []interface{}{"ALL"}},
		},
	}
}
