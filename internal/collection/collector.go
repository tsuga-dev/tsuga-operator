package collection

import (
	"fmt"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
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
	// The floor is v0.157.0: the agent config uses the cumulative_to_delta
	// processor, which older collectors only accept under its previous name.
	defaultAgentImage   = "ghcr.io/open-telemetry/opentelemetry-collector-releases/opentelemetry-collector-contrib:0.157.0"
	defaultGatewayImage = "ghcr.io/open-telemetry/opentelemetry-collector-releases/opentelemetry-collector-k8s:0.157.0"

	// The scraper needs the prometheus receiver, which the k8s distribution
	// does not carry, so it takes the same contrib distribution as the agent.
	defaultScraperImage = defaultAgentImage
)

// substitute replaces the embedded config placeholders with the resolved
// endpoint and cluster name for this collector configuration. When the
// endpoint is sourced from a secret, the placeholder resolves to an env var
// reference instead of a literal value; buildCollector wires that env var.
func substitute(base string, cfg v1alpha1.TsugaCollectorConfigSpec) string {
	endpoint := cfg.Export.Endpoint
	if cfg.Export.EndpointSecretRef != nil {
		endpoint = fmt.Sprintf("${env:%s}", tsugaEndpointEnvVar)
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
	// mutate adjusts the parsed collector config in place, before the
	// empty-pipeline check.
	mutate func(map[string]interface{})
	// customize sets mode-specific fields on the rendered object, after the
	// shared image and resources are in place.
	customize func(*unstructured.Unstructured)
}

// collectorSpecs returns the collectors this configuration renders, in a
// stable order: the node agent always, then the cluster gateway when any of
// its receivers is enabled, then the Prometheus scraper when it is enabled.
func collectorSpecs(cfg v1alpha1.TsugaCollectorConfigSpec) []collectorSpec {
	specs := []collectorSpec{{
		name:   agentName,
		mode:   modeDaemonset,
		config: assets.DaemonsetConfig,
		image:  defaultAgentImage,
		mutate: func(c map[string]interface{}) { pruneAgentConfig(c, cfg.Agent) },
		customize: func(obj *unstructured.Unstructured) {
			_ = unstructured.SetNestedSlice(obj.Object, hostVolumes(), "spec", "volumes")
			_ = unstructured.SetNestedSlice(obj.Object, hostVolumeMounts(), "spec", "volumeMounts")
		},
	}}
	if cfg.Gateway.ClusterMetrics || cfg.Gateway.KubernetesObjects {
		specs = append(specs, collectorSpec{
			name:   gatewayName,
			mode:   modeDeployment,
			config: assets.GatewayConfig,
			image:  defaultGatewayImage,
			mutate: func(c map[string]interface{}) { pruneGatewayConfig(c, cfg.Gateway) },
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
	if cfg.Prometheus.Enabled {
		specs = append(specs, collectorSpec{
			name:   scraperName,
			mode:   modeStatefulset,
			config: assets.ScraperConfig,
			image:  defaultScraperImage,
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
	return specs
}

// buildCollector renders a single OpenTelemetryCollector unstructured object
// from an embedded base config, substituting placeholders, applying the
// spec's config mutation, and wiring the Tsuga API key (and, if configured,
// OTLP endpoint) env vars from their secrets.
func buildCollector(spec collectorSpec, cfg v1alpha1.TsugaCollectorConfigSpec) (*unstructured.Unstructured, error) {
	if cfg.Export.EndpointSecretRef == nil && cfg.Export.Endpoint == "" {
		return nil, fmt.Errorf("%s: no OTLP endpoint configured: set export.endpoint or export.endpointSecretRef", spec.name)
	}

	var config map[string]interface{}
	if err := yaml.Unmarshal([]byte(substitute(spec.config, cfg)), &config); err != nil {
		return nil, fmt.Errorf("parse %s config: %w", spec.name, err)
	}
	if spec.mutate != nil {
		spec.mutate(config)
	}
	if err := ensureNonEmptyPipelines(spec.name, config); err != nil {
		return nil, err
	}

	env := []interface{}{
		map[string]interface{}{
			"name": "TSUGA_API_KEY",
			"valueFrom": map[string]interface{}{
				"secretKeyRef": map[string]interface{}{
					"name": cfg.Export.TokenSecretRef.Name,
					"key":  cfg.Export.TokenSecretRef.Key,
				},
			},
		},
	}
	// Downward-API env vars referenced by the embedded configs
	// (${env:MY_POD_IP}, ${env:NODE_IP}, ${POD_UID}, node_from_env_var:
	// K8S_NODE_NAME, ...). Without these the collector aborts at startup with
	// "unset environment variable".
	env = append(env, downwardAPIEnv()...)
	if cfg.Export.EndpointSecretRef != nil {
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
	resources, err := collectorResources(cfg)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", spec.name, err)
	}
	_ = unstructured.SetNestedMap(obj.Object, resources, "spec", "resources")
	if spec.customize != nil {
		spec.customize(obj)
	}
	return obj, nil
}

// collectorResources returns the CPU/memory requests and limits for a
// collector pod: the configured override, or defaults sized for a cluster of
// up to ~50 nodes. Some limit must be set, because the configs' memory_limiter
// is a percentage of the container limit and falls back to the node's total
// memory without one.
func collectorResources(cfg v1alpha1.TsugaCollectorConfigSpec) (map[string]interface{}, error) {
	if cfg.Resources == nil {
		return map[string]interface{}{
			"limits":   map[string]interface{}{"cpu": "500m", "memory": "512Mi"},
			"requests": map[string]interface{}{"cpu": "100m", "memory": "128Mi"},
		}, nil
	}
	out, err := runtime.DefaultUnstructuredConverter.ToUnstructured(cfg.Resources)
	if err != nil {
		return nil, fmt.Errorf("convert spec.resources: %w", err)
	}
	return out, nil
}

// hostVolumes returns the host-path volumes the daemonset agent needs to
// back its host_metrics (root_path: /hostfs) and file_log (/var/log/pods)
// receivers. The gateway/deployment collector does not run on every node and
// must not get these. /var/lib/docker/containers is where /var/log/pods
// symlinks resolve on Docker-runtime nodes; containerd nodes lack it, so it
// is DirectoryOrCreate rather than a mount that fails pod start.
func hostVolumes() []interface{} {
	return []interface{}{
		map[string]interface{}{
			"name":     "hostfs",
			"hostPath": map[string]interface{}{"path": "/"},
		},
		map[string]interface{}{
			"name":     "varlogpods",
			"hostPath": map[string]interface{}{"path": "/var/log/pods"},
		},
		map[string]interface{}{
			"name":     "varlibdockercontainers",
			"hostPath": map[string]interface{}{"path": "/var/lib/docker/containers", "type": "DirectoryOrCreate"},
		},
	}
}

// hostVolumeMounts returns the mounts corresponding to hostVolumes.
func hostVolumeMounts() []interface{} {
	return []interface{}{
		map[string]interface{}{
			"name":      "hostfs",
			"mountPath": "/hostfs",
			"readOnly":  true,
			// Mounts the node creates after the pod starts still show up under
			// /hostfs, which the filesystem scraper needs to see them.
			"mountPropagation": "HostToContainer",
		},
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
	}
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
func ensureNonEmptyPipelines(name string, config map[string]interface{}) error {
	pipelines, ok := asMap(pipelinesOf(config))
	if !ok || len(pipelines) == 0 {
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
	if cfg.Prometheus.Enabled && cfg.Prometheus.TargetAllocator.Enabled {
		out = append(out, renderTargetAllocator(cfg))
	}
	return out, nil
}
