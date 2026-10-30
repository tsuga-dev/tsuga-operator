package collection

import (
	"cmp"
	"encoding/json"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/yaml"

	"github.com/tsuga-dev/tsuga-operator/api/v1alpha1"
	"github.com/tsuga-dev/tsuga-operator/internal/collection/assets"
)

const (
	collectorAPIVersion = "opentelemetry.io/v1beta1"
	collectorKind       = "OpenTelemetryCollector"
	agentName           = "tsuga-agent"
	gatewayName         = "tsuga-gateway"
	scraperName         = "tsuga-scraper"
	profilerName        = "tsuga-profiler"

	// Collector deployment modes (OpenTelemetryCollector spec.mode).
	modeDaemonset   = "daemonset"
	modeDeployment  = "deployment"
	modeStatefulset = "statefulset"

	// tsugaEndpointEnvVar is the env var name used to inject the OTLP
	// endpoint when it is sourced from a secret rather than a literal.
	tsugaEndpointEnvVar = "TSUGA_OTLP_ENDPOINT"

	// Default OTel Collector images used when the CRD leaves spec.image empty.
	// The OTel Operator's own default is the CORE distribution, which lacks the
	// receivers/processors/connectors our embedded configs rely on, so we pin a
	// distribution per mode: the daemonset agent uses the broad contrib
	// distribution (host_metrics, file_log, kubelet_stats, and room for
	// additional receivers), while the gateway only needs the smaller k8s
	// distribution (k8s_cluster, k8s_objects).
	//
	// The floor is v0.161.0: the configs extract container.image.tags, which
	// older collectors accept but never emit.
	defaultAgentImage   = "ghcr.io/open-telemetry/opentelemetry-collector-releases/opentelemetry-collector-contrib:0.161.0"
	defaultGatewayImage = "ghcr.io/open-telemetry/opentelemetry-collector-releases/opentelemetry-collector-k8s:0.161.0"

	// The scraper needs the prometheus receiver, which the k8s distribution
	// does not carry, so it takes the same contrib distribution as the agent.
	defaultScraperImage = defaultAgentImage
	// The profiling receiver ships only in the eBPF profiler distribution,
	// so the profiler never takes spec.image.
	defaultProfilerImage = "ghcr.io/open-telemetry/opentelemetry-collector-releases/opentelemetry-collector-ebpf-profiler:0.161.0"
)

// profilerDefaultResources follows the chart: eBPF sampling cost scales with
// the node's core count, so it gets more headroom than the other collectors
// and does not follow spec.resources.
var profilerDefaultResources = &corev1.ResourceRequirements{
	Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("200m"), corev1.ResourceMemory: resource.MustParse("256Mi")},
	Limits:   corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("1"), corev1.ResourceMemory: resource.MustParse("1Gi")},
}

// agentDefaultAffinity keeps the agent off the nodes its default tolerations
// would otherwise admit but a Linux DaemonSet cannot run on: Windows, EKS
// Fargate and virtual-kubelet nodes.
func agentDefaultAffinity() map[string]interface{} {
	expr := func(key, op, value string) interface{} {
		return map[string]interface{}{"key": key, "operator": op, "values": []interface{}{value}}
	}
	return map[string]interface{}{"nodeAffinity": map[string]interface{}{
		"requiredDuringSchedulingIgnoredDuringExecution": map[string]interface{}{
			"nodeSelectorTerms": []interface{}{map[string]interface{}{"matchExpressions": []interface{}{
				expr("kubernetes.io/os", "In", "linux"),
				expr("eks.amazonaws.com/compute-type", "NotIn", "fargate"),
				expr("type", "NotIn", "virtual-kubelet"),
			}}},
		},
	}}
}

// substitute replaces the embedded config placeholders with the resolved
// endpoint and cluster name for this collector configuration. When the
// endpoint is sourced from a secret, the placeholder resolves to an env var
// reference instead of a literal value; buildCollector wires that env var.
func substitute(base string, cfg v1alpha1.TsugaCollectorConfigSpec) string {
	endpoint := ""
	if cfg.Export != nil {
		endpoint = cfg.Export.Endpoint
		if cfg.Export.EndpointSecretRef != nil {
			endpoint = fmt.Sprintf("${env:%s}", tsugaEndpointEnvVar)
		}
	}
	r := strings.NewReplacer(
		"__TSUGA_ENDPOINT__", endpoint,
		"__CLUSTER_NAME__", cfg.ClusterName,
		"__SCRAPE_INTERVAL__", cfg.Prometheus.ScrapeInterval,
	)
	return r.Replace(base)
}

// collectorSpec describes one managed collector: which embedded config it
// renders from, how that config is adjusted for the effective configuration,
// and which mode-specific fields its object carries. buildCollector does
// everything the collectors share; mutate and customize carry what only one
// of them needs.
type collectorSpec struct {
	name   string
	mode   string
	config string
	// image is the default used when the CRD leaves spec.image empty.
	image string
	// pod is the collector's own placement, sizing and env.
	pod v1alpha1.CollectorPodSpec
	// tuning is the collector's CollectorTuning; buildCollector reads its
	// exportToTsuga to decide whether the Tsuga endpoint and key are needed.
	tuning v1alpha1.CollectorTuning
	// defaultResources, when set, replaces spec.resources as this
	// collector's fallback.
	defaultResources *corev1.ResourceRequirements
	// mutate adjusts the parsed collector config in place, before the
	// empty-pipeline check.
	mutate func(map[string]interface{}) error
	// customize sets mode-specific fields on the rendered object, after the
	// shared image and resources are in place.
	customize func(*unstructured.Unstructured)
}

// collectorSpecs returns the collectors this configuration renders, in a
// stable order: the node agent unless agent.enabled is false, then the
// cluster gateway when any of its receivers is enabled, then the Prometheus
// scraper when it is enabled, then the profiler when it is enabled.
func collectorSpecs(cfg v1alpha1.TsugaCollectorConfigSpec) []collectorSpec {
	var specs []collectorSpec
	if AgentEnabled(cfg) {
		specs = append(specs, collectorSpec{
			name:   agentName,
			mode:   modeDaemonset,
			config: assets.DaemonsetConfig,
			image:  defaultAgentImage,
			pod:    cfg.Agent.CollectorPodSpec,
			tuning: cfg.Agent.CollectorTuning,
			mutate: func(c map[string]interface{}) error {
				pruneAgentConfig(c, cfg.Agent)
				return tuneAgentConfig(c, cfg)
			},
			customize: func(obj *unstructured.Unstructured) {
				if cfg.Agent.HostNetwork {
					_ = unstructured.SetNestedField(obj.Object, true, "spec", "hostNetwork")
					// Without it the pod resolves names with the node's DNS and
					// loses *.svc.cluster.local.
					_ = unstructured.SetNestedField(obj.Object, "ClusterFirstWithHostNet", "spec", "dnsPolicy")
				}
				_ = unstructured.SetNestedSlice(obj.Object, hostVolumes(agentMountsLogs(cfg)), "spec", "volumes")
				_ = unstructured.SetNestedSlice(obj.Object, hostVolumeMounts(agentMountsLogs(cfg)), "spec", "volumeMounts")
				// The OTel Operator gives a DaemonSet's Service
				// internalTrafficPolicy: Local, so instrumented pods on a node
				// without an agent drop their OTLP. Tolerating every taint is
				// only safe with the default affinity's exclusions, and an
				// explicit `tolerations: []` opts out.
				if cfg.Agent.Affinity == nil && cfg.Affinity == nil {
					_ = unstructured.SetNestedMap(obj.Object, agentDefaultAffinity(), "spec", "affinity")
					if cfg.Agent.Tolerations == nil && cfg.Tolerations == nil {
						_ = unstructured.SetNestedSlice(obj.Object, []interface{}{map[string]interface{}{"operator": "Exists"}}, "spec", "tolerations")
					}
				}
			},
		})
	}
	if cfg.Gateway.ClusterMetrics || cfg.Gateway.KubernetesObjects || cfg.Gateway.KubernetesEvents {
		specs = append(specs, collectorSpec{
			name:   gatewayName,
			mode:   modeDeployment,
			config: assets.GatewayConfig,
			image:  defaultGatewayImage,
			pod:    cfg.Gateway.CollectorPodSpec,
			tuning: cfg.Gateway.CollectorTuning,
			mutate: func(c map[string]interface{}) error {
				pruneGatewayConfig(c, cfg.Gateway)
				return tuneGatewayConfig(c, cfg)
			},
			customize: func(obj *unstructured.Unstructured) {
				// k8s_cluster and k8s_objects have no leader election, so a
				// second replica reports the same cluster state again: every
				// cluster metric counted twice, every Kubernetes object
				// ingested twice. Pinned explicitly so a `kubectl scale`
				// against the collector's scale subresource does not survive
				// the next reconcile.
				_ = unstructured.SetNestedField(obj.Object, int64(1), "spec", "replicas")
			},
		})
	}
	if scraperEnabled(cfg.Prometheus) {
		specs = append(specs, collectorSpec{
			name:   scraperName,
			mode:   modeStatefulset,
			config: assets.ScraperConfig,
			image:  defaultScraperImage,
			pod:    cfg.Prometheus.CollectorPodSpec,
			tuning: cfg.Prometheus.CollectorTuning,
			mutate: func(c map[string]interface{}) error {
				return tuneScraperConfig(c, cfg)
			},
			customize: func(obj *unstructured.Unstructured) {
				// Above one replica the Target Allocator is what divides the
				// targets; the CRD's CEL rule rejects replicas > 1 without it.
				_ = unstructured.SetNestedField(obj.Object, int64(cfg.Prometheus.Replicas), "spec", "replicas")
				// The label alone drives the whole Target Allocator wiring;
				// see targetAllocatorLabel.
				if cfg.Prometheus.TargetAllocator.Enabled {
					labels := obj.GetLabels()
					if labels == nil {
						labels = map[string]string{}
					}
					labels[targetAllocatorLabel] = targetAllocatorName
					obj.SetLabels(labels)
				}
			},
		})
	}
	if cfg.Profiling.Enabled {
		image := cmp.Or(cfg.Profiling.Image, defaultProfilerImage)
		specs = append(specs, collectorSpec{
			name:             profilerName,
			mode:             modeDaemonset,
			config:           assets.ProfilingConfig,
			image:            image,
			pod:              cfg.Profiling.CollectorPodSpec,
			tuning:           cfg.Profiling.CollectorTuning,
			defaultResources: profilerDefaultResources,
			mutate: func(c map[string]interface{}) error {
				return tuneProfilerConfig(c, cfg)
			},
			customize: func(obj *unstructured.Unstructured) {
				_ = unstructured.SetNestedField(obj.Object, image, "spec", "image")
				// Profiles are an alpha signal; without the gate the
				// collector rejects the profiles pipeline.
				_ = unstructured.SetNestedStringMap(obj.Object, map[string]string{"feature-gates": "+service.profilesSupport"}, "spec", "args")
				// Reading other processes' memory and loading eBPF programs
				// needs the host PID namespace and a privileged root container.
				_ = unstructured.SetNestedField(obj.Object, true, "spec", "hostPID")
				_ = unstructured.SetNestedMap(obj.Object, map[string]interface{}{"privileged": true, "runAsUser": int64(0)}, "spec", "securityContext")
				_ = unstructured.SetNestedSlice(obj.Object, profilerVolumes(), "spec", "volumes")
				_ = unstructured.SetNestedSlice(obj.Object, profilerVolumeMounts(), "spec", "volumeMounts")
				// The eBPF profiler only runs on Linux nodes.
				if len(cfg.Profiling.NodeSelector) == 0 && len(cfg.NodeSelector) == 0 {
					_ = unstructured.SetNestedStringMap(obj.Object, map[string]string{"kubernetes.io/os": "linux"}, "spec", "nodeSelector")
				}
			},
		})
	}
	return specs
}

// buildCollector renders a single OpenTelemetryCollector unstructured object
// from an embedded base config, substituting placeholders, applying the
// spec's config mutation, and wiring the Tsuga API key (and, if configured,
// OTLP endpoint) env vars from their secrets.
func buildCollector(spec collectorSpec, cfg v1alpha1.TsugaCollectorConfigSpec) (*unstructured.Unstructured, error) {
	exports := ptr.Deref(spec.tuning.ExportToTsuga, true)
	if exports {
		if cfg.Export == nil {
			return nil, fmt.Errorf("%s: exportToTsuga needs spec.export (tokenSecretRef and an endpoint)", spec.name)
		}
		if cfg.Export.EndpointSecretRef == nil && cfg.Export.Endpoint == "" {
			return nil, fmt.Errorf("%s: no OTLP endpoint configured: set export.endpoint or export.endpointSecretRef", spec.name)
		}
	}

	base, custom := spec.config, spec.tuning.CustomConfig
	if custom != nil {
		base = string(custom.Raw)
	}
	var config map[string]interface{}
	if err := yaml.Unmarshal([]byte(substitute(base, cfg)), &config); err != nil {
		return nil, fmt.Errorf("parse %s config: %w", spec.name, err)
	}
	if custom == nil {
		if spec.mutate != nil {
			if err := spec.mutate(config); err != nil {
				return nil, fmt.Errorf("%s: %w", spec.name, err)
			}
		}
		if err := tuneExtras(config, spec.tuning); err != nil {
			return nil, fmt.Errorf("%s: %w", spec.name, err)
		}
		if err := tuneEnrichment(config, cfg, spec.name == agentName); err != nil {
			return nil, fmt.Errorf("%s: %w", spec.name, err)
		}
	}
	if err := ensureNonEmptyPipelines(spec.name, config, custom != nil); err != nil {
		return nil, err
	}

	var env []interface{}
	if exports {
		env = append(env, map[string]interface{}{
			"name": "TSUGA_API_KEY",
			"valueFrom": map[string]interface{}{
				"secretKeyRef": map[string]interface{}{
					"name": cfg.Export.TokenSecretRef.Name,
					"key":  cfg.Export.TokenSecretRef.Key,
				},
			},
		})
	}
	// Downward-API env vars referenced by the embedded configs
	// (${env:MY_POD_IP}, ${env:NODE_IP}, ${POD_UID}, node_from_env_var:
	// K8S_NODE_NAME, ...). Without these the collector aborts at startup with
	// "unset environment variable".
	env = append(env, downwardAPIEnv()...)
	if exports && cfg.Export.EndpointSecretRef != nil {
		env = append(env, map[string]interface{}{
			"name": tsugaEndpointEnvVar,
			"valueFrom": map[string]interface{}{
				"secretKeyRef": map[string]interface{}{
					"name": cfg.Export.EndpointSecretRef.Name,
					"key":  cfg.Export.EndpointSecretRef.Key,
				},
			},
		})
	}

	obj := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": collectorAPIVersion,
		"kind":       collectorKind,
		"metadata": map[string]interface{}{
			"name":      spec.name,
			"namespace": cfg.CollectorNamespace,
		},
		"spec": map[string]interface{}{
			"mode":   spec.mode,
			"config": config,
			"env":    env,
		},
	}}
	image := cfg.Image
	if image == "" {
		image = spec.image
	}
	_ = unstructured.SetNestedField(obj.Object, image, "spec", "image")
	_ = unstructured.SetNestedField(obj.Object, ServiceAccountName(cfg, spec.name), "spec", "serviceAccount")
	if err := applyPodSettings(obj, spec, cfg); err != nil {
		return nil, fmt.Errorf("%s: %w", spec.name, err)
	}
	if spec.customize != nil {
		spec.customize(obj)
	}
	return obj, nil
}

// collectorResources returns r as an unstructured map, or the built-in
// defaults sized for a cluster of up to ~50 nodes when r is nil. Some limit
// must be set, because the configs' memory_limiter is a percentage of the
// container limit and falls back to the node's total memory without one.
func collectorResources(r *corev1.ResourceRequirements) (map[string]interface{}, error) {
	if r == nil {
		return map[string]interface{}{
			"limits":   map[string]interface{}{"cpu": "500m", "memory": "512Mi"},
			"requests": map[string]interface{}{"cpu": "100m", "memory": "128Mi"},
		}, nil
	}
	return runtime.DefaultUnstructuredConverter.ToUnstructured(r)
}

// applyPodSettings writes resources, placement and extraEnvs onto the
// rendered collector: the collector's own value when set, else the
// spec-level default. Empty results are left out so the OTel Operator's
// defaults apply.
func applyPodSettings(obj *unstructured.Unstructured, spec collectorSpec, cfg v1alpha1.TsugaCollectorConfigSpec) error {
	resources, err := collectorResources(cmp.Or(spec.pod.Resources, spec.defaultResources, cfg.Resources))
	if err != nil {
		return fmt.Errorf("convert resources: %w", err)
	}
	_ = unstructured.SetNestedMap(obj.Object, resources, "spec", "resources")

	placement, err := podPlacement(spec.pod, cfg)
	if err != nil {
		return err
	}
	for key, value := range placement {
		_ = unstructured.SetNestedField(obj.Object, value, "spec", key)
	}

	if len(spec.pod.ExtraEnvs) > 0 {
		extra, err := toUnstructuredSlice(spec.pod.ExtraEnvs)
		if err != nil {
			return fmt.Errorf("convert extraEnvs: %w", err)
		}
		env, _, _ := unstructured.NestedSlice(obj.Object, "spec", "env")
		_ = unstructured.SetNestedSlice(obj.Object, append(env, extra...), "spec", "env")
	}
	return nil
}

// podPlacement returns the nodeSelector, tolerations and affinity for pod,
// each the pod's own value when set, else the spec-level default. Empty
// fields are left out.
func podPlacement(pod v1alpha1.CollectorPodSpec, cfg v1alpha1.TsugaCollectorConfigSpec) (map[string]interface{}, error) {
	out := map[string]interface{}{}

	nodeSelector := pod.NodeSelector
	if len(nodeSelector) == 0 {
		nodeSelector = cfg.NodeSelector
	}
	if len(nodeSelector) > 0 {
		m := make(map[string]interface{}, len(nodeSelector))
		for k, v := range nodeSelector {
			m[k] = v
		}
		out["nodeSelector"] = m
	}

	tolerations := pod.Tolerations
	if len(tolerations) == 0 {
		tolerations = cfg.Tolerations
	}
	if len(tolerations) > 0 {
		t, err := toUnstructuredSlice(tolerations)
		if err != nil {
			return nil, fmt.Errorf("convert tolerations: %w", err)
		}
		out["tolerations"] = t
	}

	var affinity map[string]interface{}
	var err error
	switch {
	case pod.Affinity != nil:
		err = json.Unmarshal(pod.Affinity.Raw, &affinity)
	case cfg.Affinity != nil:
		affinity, err = runtime.DefaultUnstructuredConverter.ToUnstructured(cfg.Affinity)
	}
	if err != nil {
		return nil, fmt.Errorf("convert affinity: %w", err)
	}
	if affinity != nil {
		out["affinity"] = affinity
	}
	return out, nil
}

func toUnstructuredSlice[T any](items []T) ([]interface{}, error) {
	out := make([]interface{}, 0, len(items))
	for i := range items {
		m, err := runtime.DefaultUnstructuredConverter.ToUnstructured(&items[i])
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, nil
}

// hostVolumes returns the host-path volumes the daemonset agent needs to
// back its host_metrics (root_path: /hostfs) and file_log (/var/log/pods)
// receivers. The gateway/deployment collector does not run on every node and
// must not get these. /var/lib/docker/containers is where /var/log/pods
// symlinks resolve on Docker-runtime nodes; containerd nodes lack it, so it
// is DirectoryOrCreate rather than a mount that fails pod start.
func hostVolumes(logs bool) []interface{} {
	out := make([]interface{}, 0, 3)
	out = append(out, map[string]interface{}{
		"name":     "hostfs",
		"hostPath": map[string]interface{}{"path": "/"},
	})
	if !logs {
		return out
	}
	return append(out,
		map[string]interface{}{
			"name":     "varlogpods",
			"hostPath": map[string]interface{}{"path": "/var/log/pods"},
		},
		map[string]interface{}{
			"name":     "varlibdockercontainers",
			"hostPath": map[string]interface{}{"path": "/var/lib/docker/containers", "type": "DirectoryOrCreate"},
		},
	)
}

// hostVolumeMounts returns the mounts corresponding to hostVolumes.
func hostVolumeMounts(logs bool) []interface{} {
	out := make([]interface{}, 0, 3)
	out = append(out, map[string]interface{}{
		"name":      "hostfs",
		"mountPath": "/hostfs",
		"readOnly":  true,
		// Mounts the node creates after the pod starts still show up under
		// /hostfs, which the filesystem scraper needs to see them.
		"mountPropagation": "HostToContainer",
	})
	if !logs {
		return out
	}
	return append(out,
		map[string]interface{}{
			"name":      "varlogpods",
			"mountPath": "/var/log/pods",
			"readOnly":  true,
		},
		map[string]interface{}{
			"name":      "varlibdockercontainers",
			"mountPath": "/var/lib/docker/containers",
			"readOnly":  true,
		},
	)
}

// downwardAPIEnv returns the pod/node metadata env entries every rendered
// collector needs, sourced via the downward API. These satisfy the
// ${env:...} / node_from_env_var references baked into the embedded configs.
func downwardAPIEnv() []interface{} {
	fields := []struct{ name, path string }{
		{"MY_POD_IP", "status.podIP"},
		{"NODE_IP", "status.hostIP"},
		{"POD_NAME", "metadata.name"},
		{"POD_UID", "metadata.uid"},
		{"K8S_NODE_NAME", "spec.nodeName"},
	}
	out := make([]interface{}, 0, len(fields))
	for _, f := range fields {
		out = append(out, map[string]interface{}{
			"name": f.name,
			"valueFrom": map[string]interface{}{
				"fieldRef": map[string]interface{}{
					"fieldPath": f.path,
				},
			},
		})
	}
	return out
}

// ensureNonEmptyPipelines returns an error if pruning left the config with
// no pipelines at all, which the collector would refuse to start with
// (service.pipelines: {}).
func ensureNonEmptyPipelines(name string, config map[string]interface{}, custom bool) error {
	pipelines, ok := asMap(pipelinesOf(config))
	if !ok || len(pipelines) == 0 {
		if custom {
			return fmt.Errorf("%s: customConfig has no service.pipelines", name)
		}
		return fmt.Errorf("%s: all pipelines are disabled; enable at least one telemetry toggle", name)
	}
	return nil
}

// RenderCollectors builds the OpenTelemetryCollector custom resources for a
// cluster's effective collection config.
func RenderCollectors(cfg v1alpha1.TsugaCollectorConfigSpec) ([]*unstructured.Unstructured, error) {
	specs := collectorSpecs(cfg)
	out := make([]*unstructured.Unstructured, 0, len(specs)+1)
	for _, spec := range specs {
		obj, err := buildCollector(spec, cfg)
		if err != nil {
			return nil, err
		}
		out = append(out, obj)
	}
	if scraperEnabled(cfg.Prometheus) && cfg.Prometheus.TargetAllocator.Enabled {
		ta, err := renderTargetAllocator(cfg)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", targetAllocatorName, err)
		}
		out = append(out, ta)
	}
	return out, nil
}

// profilerVolumes are what the eBPF profiler reads from the node: sysfs for
// the eBPF and perf plumbing, tracefs for the tracepoints the unwinder
// attaches to, and kernel module info for the symbolizer.
func profilerVolumes() []interface{} {
	return []interface{}{
		map[string]interface{}{"name": "sys-kernel", "hostPath": map[string]interface{}{"path": "/sys/kernel"}},
		map[string]interface{}{"name": "tracefs", "hostPath": map[string]interface{}{"path": "/sys/kernel/tracing"}},
		map[string]interface{}{"name": "lib-modules", "hostPath": map[string]interface{}{"path": "/lib/modules"}},
	}
}

func profilerVolumeMounts() []interface{} {
	return []interface{}{
		map[string]interface{}{"name": "sys-kernel", "mountPath": "/sys/kernel", "readOnly": true},
		map[string]interface{}{"name": "tracefs", "mountPath": "/sys/kernel/tracing", "readOnly": true},
		map[string]interface{}{"name": "lib-modules", "mountPath": "/lib/modules", "readOnly": true},
	}
}
