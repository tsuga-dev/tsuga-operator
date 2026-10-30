package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// SecretKeyRef references a key in a Kubernetes Secret.
type SecretKeyRef struct {
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`
	// +kubebuilder:validation:MinLength=1
	Key string `json:"key"`
}

// ExportSpec describes how telemetry is exported to Tsuga.
// +kubebuilder:validation:XValidation:rule="has(self.endpointSecretRef) || (has(self.endpoint) && size(self.endpoint) > 0)",message="export requires endpointSecretRef or a non-empty endpoint"
type ExportSpec struct {
	// Secret holding the Tsuga API key.
	TokenSecretRef SecretKeyRef `json:"tokenSecretRef"`
	// Optional secret holding the Tsuga OTLP endpoint. If nil, Endpoint is used.
	EndpointSecretRef *SecretKeyRef `json:"endpointSecretRef,omitempty"`
	// Literal OTLP endpoint, used when EndpointSecretRef is nil. It is spliced
	// into the collector YAML, so the pattern requires an https URL (the
	// Bearer token must not travel in cleartext) and keeps out whitespace,
	// quotes, backslashes and $ (collector expansion).
	// +kubebuilder:validation:MaxLength=2048
	// +kubebuilder:validation:Pattern=`^https://[^\s"'\\$]+$`
	Endpoint string `json:"endpoint,omitempty"`
	// OTLP payload encoding for the Tsuga exporter. The profiler always uses
	// proto: the profiles intake accepts nothing else.
	// +kubebuilder:validation:Enum=json;proto
	// +optional
	Encoding string `json:"encoding,omitempty"`
	// Compression for the Tsuga exporter. Default gzip.
	// +kubebuilder:validation:Enum=gzip;zlib;deflate;snappy;zstd;lz4;none
	// +optional
	Compression string `json:"compression,omitempty"`
}

// CollectorPodSpec holds the pod settings each collector can set for itself.
// A non-empty value here replaces the spec-level default for that collector.
type CollectorPodSpec struct {
	// Replaces spec.resources for this collector.
	// +optional
	Resources *corev1.ResourceRequirements `json:"resources,omitempty"`
	// Replaces spec.nodeSelector for this collector.
	// +optional
	NodeSelector map[string]string `json:"nodeSelector,omitempty"`
	// Replaces spec.tolerations for this collector.
	// +optional
	Tolerations []corev1.Toleration `json:"tolerations,omitempty"`
	// Replaces spec.affinity for this collector. A corev1 Affinity object,
	// validated by the OTel Operator rather than this CRD: typing it here five
	// times put the CRD near the 256 KB client-side apply limit.
	// +kubebuilder:pruning:PreserveUnknownFields
	// +kubebuilder:validation:Schemaless
	// +kubebuilder:validation:Type=object
	// +optional
	Affinity *apiextensionsv1.JSON `json:"affinity,omitempty"`
	// Added after the variables the operator sets (TSUGA_API_KEY,
	// TSUGA_OTLP_ENDPOINT, MY_POD_IP, NODE_IP, POD_NAME, POD_UID,
	// K8S_NODE_NAME). A variable with the same name overrides the operator's.
	// +optional
	ExtraEnvs []corev1.EnvVar `json:"extraEnvs,omitempty"`
}

// TelemetryToggles enables signal types on the node agent.
type TelemetryToggles struct {
	// +kubebuilder:default=true
	Traces bool `json:"traces"`
	// +kubebuilder:default=true
	Metrics bool `json:"metrics"`
	// +kubebuilder:default=true
	Logs bool `json:"logs"`
	// Runs the agent in the node's network namespace, so its OTLP ports are
	// reachable at the node IP. The ports then bind on the node: anything
	// already holding 4317, 4318 or 13133 makes the agent crash-loop, and
	// anything that can reach a node can send unauthenticated OTLP that the
	// agent forwards with the Tsuga ingestion key.
	// +optional
	HostNetwork bool `json:"hostNetwork,omitempty"`
	// Render the node agent. Auto-instrumentation and database monitoring
	// send through it, so they report AgentDisabled when it is off.
	// +kubebuilder:default=true
	// +optional
	Enabled *bool `json:"enabled,omitempty"`
	// +optional
	OTLP AgentOTLP `json:"otlp,omitempty"`
	// Collect pod logs from /var/log/pods. Off keeps the logs pipeline for
	// OTLP logs; agent.logs: false removes the pipeline entirely.
	// +optional
	CollectLogs *bool `json:"collectLogs,omitempty"`
	// Also collect the collectors' own container logs, excluded by default.
	// +optional
	CollectOtelLogs bool `json:"collectOtelLogs,omitempty"`
	// Mount /var/log/pods and /var/lib/docker/containers even when collectLogs is off.
	// +optional
	AddLogsVolumes *bool `json:"addLogsVolumes,omitempty"`
	// +optional
	FileLog FileLogSpec `json:"fileLog,omitempty"`
	// Enable the network scraper (including conntrack).
	// +optional
	CollectNetwork bool `json:"collectNetwork,omitempty"`
	// Enable the process and processes scrapers.
	// +optional
	CollectProcesses bool `json:"collectProcesses,omitempty"`
	// +optional
	HostMetrics HostMetricsSpec `json:"hostMetrics,omitempty"`
	// +optional
	KubeletStats KubeletStatsSpec `json:"kubeletStats,omitempty"`
	// +optional
	SpanMetrics      SpanMetricsSpec `json:"spanMetrics,omitempty"`
	CollectorPodSpec `json:",inline"`
	CollectorTuning  `json:",inline"`
}

type AgentOTLP struct {
	// gRPC listen address. Unset uses the built-in ${env:MY_POD_IP}:4317.
	// +kubebuilder:validation:Pattern=`^.*:[1-9][0-9]{0,4}$`
	// +kubebuilder:validation:XValidation:rule="!self.matches(\"^.*:[0-9]{1,5}$\") || int(self.split(\":\")[size(self.split(\":\")) - 1]) <= 65535",message="agent.otlp endpoints must end in a port between 1 and 65535"
	// +optional
	GRPCEndpoint string `json:"grpcEndpoint,omitempty"`
	// HTTP listen address. Unset uses the built-in ${env:MY_POD_IP}:4318.
	// +kubebuilder:validation:Pattern=`^.*:[1-9][0-9]{0,4}$`
	// +kubebuilder:validation:XValidation:rule="!self.matches(\"^.*:[0-9]{1,5}$\") || int(self.split(\":\")[size(self.split(\":\")) - 1]) <= 65535",message="agent.otlp endpoints must end in a port between 1 and 65535"
	// +optional
	HTTPEndpoint string `json:"httpEndpoint,omitempty"`
}

type FileLogSpec struct {
	// Replaces the built-in include globs (/var/log/pods/*/*/*.log).
	// +optional
	Include []string `json:"include,omitempty"`
	// Added to the built-in excludes.
	// +optional
	Exclude []string `json:"exclude,omitempty"`
}

type HostMetricsSpec struct {
	// +kubebuilder:validation:Pattern=`^[0-9]+(ms|s|m|h)$`
	// +optional
	CollectionInterval string `json:"collectionInterval,omitempty"`
	// Replaces the built-in host_metrics scrapers map wholesale (it is not
	// merged) and is passed to the receiver as-is, so list every scraper to
	// keep. A null entry only removes an entry from this map, and an empty
	// result fails the render. collectNetwork / collectProcesses do not apply
	// to a custom map.
	// +kubebuilder:pruning:PreserveUnknownFields
	// +kubebuilder:validation:Schemaless
	// +kubebuilder:validation:Type=object
	// +optional
	Scrapers *apiextensionsv1.JSON `json:"scrapers,omitempty"`
}

type KubeletStatsSpec struct {
	// +kubebuilder:validation:Pattern=`^[0-9]+(ms|s|m|h)$`
	// +optional
	CollectionInterval string `json:"collectionInterval,omitempty"`
	// +optional
	InsecureSkipVerify *bool `json:"insecureSkipVerify,omitempty"`
	// CA bundle path in the agent container for verifying the kubelet.
	// +optional
	CAFile string `json:"caFile,omitempty"`
	// +kubebuilder:validation:items:Enum=node;pod;container;volume
	// +optional
	MetricGroups []string `json:"metricGroups,omitempty"`
	// Call the kubelet /pods endpoint for volume types and request/limit
	// utilization. GKE Autopilot denies it, which loses every scrape; set false there.
	// +optional
	UsePodsEndpoint *bool `json:"usePodsEndpoint,omitempty"`
}

type SpanMetricsSpec struct {
	// +optional
	Enabled *bool `json:"enabled,omitempty"`
	// +kubebuilder:validation:Enum=AGGREGATION_TEMPORALITY_DELTA;AGGREGATION_TEMPORALITY_CUMULATIVE
	// +optional
	AggregationTemporality string `json:"aggregationTemporality,omitempty"`
	// Replaces the built-in dimensions.
	// +optional
	Dimensions []SpanMetricsDimension `json:"dimensions,omitempty"`
}

type SpanMetricsDimension struct {
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`
	// +optional
	Default string `json:"default,omitempty"`
}

// AttributeMapping maps a Kubernetes label or annotation onto a resource attribute.
type AttributeMapping struct {
	// +kubebuilder:validation:MinLength=1
	TagName string `json:"tagName"`
	// +kubebuilder:validation:MinLength=1
	Key string `json:"key"`
	// +kubebuilder:validation:Enum=pod;namespace;node
	// Defaults to pod.
	// +optional
	From string `json:"from,omitempty"`
}

// CollectorTuning holds the config settings every collector shares.
type CollectorTuning struct {
	// health_check listen address. Empty removes the extension and, with it,
	// the liveness probe the OTel Operator derives from it.
	// +optional
	HealthCheckEndpoint *string `json:"healthCheckEndpoint,omitempty"`
	// Appended to the built-in label mappings. A `resource.opentelemetry.io/<attr>`
	// annotation wins over a mapping to the same attribute.
	// +optional
	ExtraLabelMapping []AttributeMapping `json:"extraLabelMapping,omitempty"`
	// Appended to the built-in annotation mappings. A `resource.opentelemetry.io/<attr>`
	// annotation wins over a mapping to the same attribute.
	// +optional
	ExtraAnnotationsMapping []AttributeMapping `json:"extraAnnotationsMapping,omitempty"`
	// Send this collector's telemetry to Tsuga. Unset means true.
	// +optional
	ExportToTsuga *bool `json:"exportToTsuga,omitempty"`
	// Exporters added to this collector's config, keyed by component name
	// (for example otlp_http/backup). Passed to the collector as written.
	// +kubebuilder:pruning:PreserveUnknownFields
	// +kubebuilder:validation:Schemaless
	// +kubebuilder:validation:Type=object
	// +optional
	ExtraExporters *apiextensionsv1.JSON `json:"extraExporters,omitempty"`
	// Extra exporter names to add to this collector's pipelines, per signal.
	// +optional
	PipelineExporters PipelineComponents `json:"pipelineExporters,omitempty"`
	// Receivers added to this collector's config, keyed by component name.
	// A name the collector already defines is a render error.
	// +kubebuilder:pruning:PreserveUnknownFields
	// +kubebuilder:validation:Schemaless
	// +kubebuilder:validation:Type=object
	// +optional
	ExtraReceivers *apiextensionsv1.JSON `json:"extraReceivers,omitempty"`
	// Processors added to this collector's config, keyed by component name.
	// A name the collector already defines is a render error.
	// +kubebuilder:pruning:PreserveUnknownFields
	// +kubebuilder:validation:Schemaless
	// +kubebuilder:validation:Type=object
	// +optional
	ExtraProcessors *apiextensionsv1.JSON `json:"extraProcessors,omitempty"`
	// Connectors added to this collector's config, keyed by component name.
	// A name the collector already defines is a render error.
	// +kubebuilder:pruning:PreserveUnknownFields
	// +kubebuilder:validation:Schemaless
	// +kubebuilder:validation:Type=object
	// +optional
	ExtraConnectors *apiextensionsv1.JSON `json:"extraConnectors,omitempty"`
	// Extensions added to this collector's config, keyed by component name.
	// A name the collector already defines is a render error.
	// +kubebuilder:pruning:PreserveUnknownFields
	// +kubebuilder:validation:Schemaless
	// +kubebuilder:validation:Type=object
	// +optional
	ExtraExtensions *apiextensionsv1.JSON `json:"extraExtensions,omitempty"`
	// Pipelines added under service.pipelines, keyed by pipeline name
	// (for example logs/custom). A name the collector already defines is a render error.
	// +kubebuilder:pruning:PreserveUnknownFields
	// +kubebuilder:validation:Schemaless
	// +kubebuilder:validation:Type=object
	// +optional
	ExtraPipelines *apiextensionsv1.JSON `json:"extraPipelines,omitempty"`
	// The collector's entire config. Replaces the generated one; every other
	// tuning, export, enrichment and extra setting on this collector is ignored,
	// except exportToTsuga, which still controls the Tsuga env vars.
	// `__TSUGA_ENDPOINT__`, `__CLUSTER_NAME__` and `__SCRAPE_INTERVAL__` are
	// substituted. The operator does not validate it.
	// +kubebuilder:pruning:PreserveUnknownFields
	// +kubebuilder:validation:Schemaless
	// +kubebuilder:validation:Type=object
	// +optional
	CustomConfig *apiextensionsv1.JSON `json:"customConfig,omitempty"`
	// Extra receiver names to add to this collector's pipelines, per signal.
	// +optional
	PipelineReceivers PipelineComponents `json:"pipelineReceivers,omitempty"`
	// Extra processor names inserted before batch in this collector's
	// pipelines (last in a pipeline without batch), per signal.
	// +optional
	PipelineProcessors PipelineComponents `json:"pipelineProcessors,omitempty"`
	// Extension names appended to service.extensions.
	// +optional
	ServiceExtensions []string `json:"serviceExtensions,omitempty"`
}

// PipelineComponents lists component names to add to each signal's pipelines.
type PipelineComponents struct {
	// +optional
	Traces []string `json:"traces,omitempty"`
	// +optional
	Metrics []string `json:"metrics,omitempty"`
	// +optional
	Logs []string `json:"logs,omitempty"`
	// +optional
	Profiles []string `json:"profiles,omitempty"`
}

type K8sAttributesSpec struct {
	// Replaces the metadata the agent, gateway and scraper extract.
	// +optional
	Metadata []string `json:"metadata,omitempty"`
}

// +kubebuilder:validation:XValidation:rule="(has(self.sendBatchMaxSize) && self.sendBatchMaxSize == 0) || (has(self.sendBatchMaxSize) ? self.sendBatchMaxSize : 5000) >= (has(self.sendBatchSize) ? self.sendBatchSize : 5000)",message="batch.sendBatchMaxSize must be 0 or at least sendBatchSize; an unset field counts as the built-in 5000"
type BatchSpec struct {
	// +kubebuilder:validation:Minimum=0
	// +optional
	SendBatchSize *int32 `json:"sendBatchSize,omitempty"`
	// +kubebuilder:validation:Minimum=0
	// +optional
	SendBatchMaxSize *int32 `json:"sendBatchMaxSize,omitempty"`
	// +kubebuilder:validation:Pattern=`^[0-9]+(ms|s|m|h)$`
	// +optional
	Timeout string `json:"timeout,omitempty"`
}

// GatewayToggles enables cluster-scoped receivers on the gateway collector.
type GatewayToggles struct {
	// +kubebuilder:default=true
	ClusterMetrics bool `json:"clusterMetrics"`
	// +kubebuilder:default=true
	KubernetesObjects bool `json:"kubernetesObjects"`
	// KubernetesEvents watches core/v1 Warning events, filtered at the API
	// server, and exports them as logs. Events already retained when the
	// gateway starts are not sent.
	// +kubebuilder:default=true
	KubernetesEvents bool `json:"kubernetesEvents"`
	// +kubebuilder:validation:Pattern=`^[0-9]+(ms|s|m|h)$`
	// +optional
	CollectionInterval string `json:"collectionInterval,omitempty"`
	// Replaces the built-in node conditions.
	// +optional
	NodeConditionsToReport []string `json:"nodeConditionsToReport,omitempty"`
	// Replaces the built-in allocatable types.
	// +kubebuilder:validation:items:Enum=cpu;memory;ephemeral-storage;pods
	// +optional
	AllocatableTypesToReport []string `json:"allocatableTypesToReport,omitempty"`
	CollectorPodSpec         `json:",inline"`
	CollectorTuning          `json:",inline"`
}

// TargetAllocatorSpec enables the Target Allocator, which decouples Prometheus
// service discovery from scraping so the scraper collector can run more than
// one replica without every replica scraping every target.
type TargetAllocatorSpec struct {
	// +kubebuilder:default=false
	// +optional
	Enabled bool `json:"enabled,omitempty"`
	// How the Target Allocator spreads targets across scraper replicas.
	// +kubebuilder:default=consistent-hashing
	// +kubebuilder:validation:Enum=consistent-hashing;least-weighted
	// +optional
	AllocationStrategy string `json:"allocationStrategy,omitempty"`
}

// PrometheusSpec configures the cluster's Prometheus scraping collector, a
// statefulset-mode OpenTelemetryCollector that scrapes pods carrying the
// prometheus.io/scrape, prometheus.io/path, prometheus.io/port and
// prometheus.io/scheme annotations.
//
// The validation rule guards both sides with has(): a TsugaCollectorConfig
// applied without a prometheus: block gets no defaulting inside it, because
// structural defaulting only descends into objects that are present.
// +kubebuilder:validation:XValidation:rule="!has(self.replicas) || self.replicas <= 1 || (has(self.targetAllocator) && self.targetAllocator.enabled)",message="prometheus.replicas > 1 requires prometheus.targetAllocator.enabled: without the Target Allocator every replica scrapes every target"
// +kubebuilder:validation:XValidation:rule="!has(self.prometheusCR) || !has(self.prometheusCR.enabled) || !self.prometheusCR.enabled || (has(self.targetAllocator) && self.targetAllocator.enabled)",message="prometheus.prometheusCR.enabled requires prometheus.targetAllocator.enabled: the Target Allocator is what watches ServiceMonitors and PodMonitors"
// +kubebuilder:validation:XValidation:rule="!has(self.prometheusCR) || !has(self.prometheusCR.allowNamespaces) || !has(self.prometheusCR.denyNamespaces)",message="prometheus.prometheusCR.allowNamespaces and denyNamespaces are mutually exclusive"
// +kubebuilder:validation:XValidation:rule="!has(self.enabled) || !self.enabled || !has(self.nodeLocal) || !self.nodeLocal || !has(self.annotationDiscovery) || self.annotationDiscovery",message="prometheus.nodeLocal scrapes pod annotations, so it cannot be combined with annotationDiscovery: false"
// +kubebuilder:validation:XValidation:rule="!has(self.enabled) || !self.enabled || !has(self.annotationDiscovery) || self.annotationDiscovery || (has(self.prometheusCR) && has(self.prometheusCR.enabled) && self.prometheusCR.enabled)",message="prometheus needs annotationDiscovery or prometheusCR.enabled, or the scraper has no targets"
type PrometheusSpec struct {
	// Enabled renders the scraper collector. Off by default: annotation
	// scraping can add substantial cardinality on a cluster dense in
	// annotated pods, so it is opted into deliberately.
	// +kubebuilder:default=false
	// +optional
	Enabled bool `json:"enabled,omitempty"`
	// How often to scrape each target, e.g. "60s". This does not set how often
	// the collector re-polls the Target Allocator: the OTel Operator writes
	// that interval itself.
	// +kubebuilder:default="30s"
	// +kubebuilder:validation:Pattern=`^[0-9]+(ms|s|m|h)$`
	// +optional
	ScrapeInterval string `json:"scrapeInterval,omitempty"`
	// Scraper replicas. Values above 1 require the Target Allocator, which is
	// what divides the targets between them.
	// +kubebuilder:default=1
	// +kubebuilder:validation:Minimum=1
	// +optional
	Replicas int32 `json:"replicas,omitempty"`
	// Scrape pods carrying prometheus.io/scrape and related annotations.
	// Unset means true, or false when prometheusCR.enabled is on, so a pod
	// that also has a ServiceMonitor or PodMonitor is not scraped twice.
	// +optional
	AnnotationDiscovery *bool `json:"annotationDiscovery,omitempty"`
	// Scrape annotated pods from the tsuga-agent on each pod's own node
	// instead of from the tsuga-scraper StatefulSet, so each target is
	// scraped exactly once with no Target Allocator. ServiceMonitors and
	// PodMonitors still go through tsuga-scraper.
	// +optional
	NodeLocal *bool `json:"nodeLocal,omitempty"`
	// Discover targets from Prometheus Operator ServiceMonitor and PodMonitor
	// resources. Requires targetAllocator.enabled and the monitoring.coreos.com CRDs.
	// +optional
	PrometheusCR PrometheusCRSpec `json:"prometheusCR,omitempty"`
	// +kubebuilder:default={enabled: false, allocationStrategy: "consistent-hashing"}
	// +optional
	TargetAllocator  TargetAllocatorSpec `json:"targetAllocator,omitempty"`
	CollectorPodSpec `json:",inline"`
	CollectorTuning  `json:",inline"`
}

// PrometheusCRSpec maps onto the TargetAllocator's spec.prometheusCR.
type PrometheusCRSpec struct {
	// +optional
	Enabled bool `json:"enabled,omitempty"`
	// Unset or empty matches every ServiceMonitor.
	// +optional
	ServiceMonitorSelector *metav1.LabelSelector `json:"serviceMonitorSelector,omitempty"`
	// Unset or empty matches every PodMonitor.
	// +optional
	PodMonitorSelector *metav1.LabelSelector `json:"podMonitorSelector,omitempty"`
	// Namespaces searched for ServiceMonitors; empty means all.
	// +optional
	ServiceMonitorNamespaceSelector *metav1.LabelSelector `json:"serviceMonitorNamespaceSelector,omitempty"`
	// Namespaces searched for PodMonitors; empty means all.
	// +optional
	PodMonitorNamespaceSelector *metav1.LabelSelector `json:"podMonitorNamespaceSelector,omitempty"`
	// +optional
	AllowNamespaces []string `json:"allowNamespaces,omitempty"`
	// +optional
	DenyNamespaces []string `json:"denyNamespaces,omitempty"`
	// Default scrape interval for monitors that set none.
	// +kubebuilder:validation:Pattern=`^[0-9]+(ms|s|m|h)$`
	// +optional
	ScrapeInterval string `json:"scrapeInterval,omitempty"`
}

// ProfilingSpec configures the eBPF profiling collector, a DaemonSet that
// samples CPU profiles from every process on each node with no application
// changes. It runs privileged with hostPID, so the collector namespace must
// admit privileged pods, and needs node kernels 5.10 or newer.
type ProfilingSpec struct {
	// Enabled renders the profiling collector. Off by default because it
	// runs privileged on every node.
	// +kubebuilder:default=false
	// +optional
	Enabled bool `json:"enabled,omitempty"`
	// Image overrides the profiling collector image. It must be a
	// distribution carrying the profiling receiver, such as the OTel eBPF
	// profiler distro; spec.image does not apply to this collector.
	// +optional
	Image string `json:"image,omitempty"`
	// Environment variable read as the service name. Spliced into OTTL, so
	// the pattern keeps out anything but a valid variable name.
	// +kubebuilder:validation:Pattern=`^[A-Za-z_][A-Za-z0-9_]*$`
	// +optional
	ServiceNameEnvVar string `json:"serviceNameEnvVar,omitempty"`
	// Replaces the profiler's k8s_attributes metadata list.
	// +optional
	K8sAttributesMetadata []string `json:"k8sAttributesMetadata,omitempty"`
	CollectorPodSpec      `json:",inline"`
	CollectorTuning       `json:",inline"`
}

// ServiceAccountSpec controls the ServiceAccounts the collectors run as.
// +kubebuilder:validation:XValidation:rule="!has(self.create) || self.create || (has(self.name) && size(self.name) > 0)",message="serviceAccount.name is required when serviceAccount.create is false"
type ServiceAccountSpec struct {
	// Create one ServiceAccount per collector, named after it. When false,
	// every collector and the Target Allocator run as name.
	// +kubebuilder:default=true
	// +optional
	Create *bool `json:"create,omitempty"`
	// Existing ServiceAccount in collectorNamespace, used when create is false.
	// +kubebuilder:validation:MaxLength=253
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9.]*[a-z0-9])?$`
	// +optional
	Name string `json:"name,omitempty"`
	// Annotations on the ServiceAccounts the operator creates, e.g.
	// eks.amazonaws.com/role-arn for IRSA.
	// +optional
	Annotations map[string]string `json:"annotations,omitempty"`
}

// RBACSpec controls the per-collector ClusterRoles and ClusterRoleBindings.
type RBACSpec struct {
	// Without these roles kubelet_stats, k8s_cluster, k8s_objects and
	// k8s_attributes are denied by the API server; turn off only when you
	// grant the same access yourself.
	// +kubebuilder:default=true
	// +optional
	Create *bool `json:"create,omitempty"`
}

// ResourceDetectionSpec adds cloud and host attributes (cloud.provider,
// cloud.region, cloud.account.id, host.id...) to the agent, gateway, scraper and
// profiler. A detector
// that fails stops the collector from starting, so list only those matching
// where you run: [env, eks, ec2] on EKS, [env, gcp] on GKE, [env, aks, azure]
// on AKS, [env] elsewhere.
// +kubebuilder:validation:XValidation:rule="!has(self.detectors) || size(self.detectors) > 0",message="resourceDetection.detectors cannot be empty: the collector refuses to start with no detector"
// +kubebuilder:validation:XValidation:rule="!has(self.detectors) || !self.detectors.exists(d, d == \"eks\") || !self.detectors.exists(d, d == \"ec2\") || self.detectors.indexOf(\"ec2\") > self.detectors.indexOf(\"eks\")",message="resourceDetection.detectors must list ec2 after eks, or cloud.platform loses aws_eks"
type ResourceDetectionSpec struct {
	// +optional
	Enabled bool `json:"enabled,omitempty"`
	// Detectors run in order; the first to supply an attribute wins, and
	// attributes already on the telemetry are never replaced. Default [env].
	// +kubebuilder:validation:MaxItems=6
	// +kubebuilder:validation:items:Enum=env;eks;ec2;gcp;aks;azure
	// +listType=set
	// +optional
	Detectors []string `json:"detectors,omitempty"`
	// Deadline for the whole detection pass. Default 15s.
	// +kubebuilder:validation:Pattern=`^[1-9][0-9]*(ms|s|m|h)$`
	// +optional
	Timeout string `json:"timeout,omitempty"`
}

// RedactionSpec masks credentials in the agent's traces, metrics and logs.
type RedactionSpec struct {
	// +optional
	Enabled bool `json:"enabled,omitempty"`
	// redaction processor configuration, passed as written. Unset applies the
	// built-in credential rules. A config that would delete every attribute
	// (allow_all_keys not true and no allowed_keys) fails the render.
	// +kubebuilder:pruning:PreserveUnknownFields
	// +kubebuilder:validation:Schemaless
	// +kubebuilder:validation:Type=object
	// +optional
	Config *apiextensionsv1.JSON `json:"config,omitempty"`
}

// TsugaCollectorConfigSpec is the desired cluster collection topology.
// +kubebuilder:validation:XValidation:rule="!has(self.prometheus) || !has(self.prometheus.enabled) || !self.prometheus.enabled || !has(self.prometheus.nodeLocal) || !self.prometheus.nodeLocal || ((!has(self.agent.enabled) || self.agent.enabled) && self.agent.metrics)",message="prometheus.nodeLocal requires agent.enabled and agent.metrics: the agent does the scraping"
// +kubebuilder:validation:XValidation:rule="!has(self.prometheus) || !has(self.prometheus.enabled) || !self.prometheus.enabled || !has(self.prometheus.nodeLocal) || !self.prometheus.nodeLocal || !has(self.agent.customConfig)",message="prometheus.nodeLocal cannot be applied to an agent customConfig; add the prometheus receiver there yourself"
type TsugaCollectorConfigSpec struct {
	// Human-readable cluster name added as k8s.cluster.name. It is spliced
	// into the collector YAML, so the pattern keeps out quotes, backslashes
	// and newlines.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	// +kubebuilder:validation:Pattern=`^[A-Za-z0-9]([A-Za-z0-9 ._-]*[A-Za-z0-9])?$`
	// +required
	ClusterName string `json:"clusterName"`
	// Namespace where the managed collectors are created.
	// +kubebuilder:default=tsuga-operator-system
	CollectorNamespace string `json:"collectorNamespace,omitempty"`
	// +optional
	Export  *ExportSpec      `json:"export,omitempty"`
	Agent   TelemetryToggles `json:"agent"`
	Gateway GatewayToggles   `json:"gateway"`
	// Prometheus configures the annotation-scraping collector. Absent means
	// no scraper is rendered.
	// +optional
	Prometheus PrometheusSpec `json:"prometheus,omitempty"`
	// Profiling configures the eBPF profiling collector. Absent means no
	// profiler is rendered.
	// +optional
	Profiling ProfilingSpec `json:"profiling,omitempty"`
	// +optional
	K8sAttributes K8sAttributesSpec `json:"k8sAttributes,omitempty"`
	// +optional
	Batch BatchSpec `json:"batch,omitempty"`
	// OTel Collector image override (defaults handled by the OTel Operator when empty).
	// It does not apply to the profiling collector; see profiling.image.
	Image string `json:"image,omitempty"`
	// Resources is the default CPU/memory for the agent, the gateway and,
	// when enabled, the Prometheus scraper; each can replace it with its own
	// resources. The profiler has its own default. The default sizes a
	// cluster of up to ~50 nodes; the gateway holds the whole cluster's
	// object state in memory and cannot be scaled out, so raise the limit
	// with the cluster (roughly 2Gi at 100 nodes, 4Gi at 250). The agent's
	// memory_limiter is a percentage of the container limit, so removing
	// limits entirely makes it measure against the node.
	// +optional
	Resources *corev1.ResourceRequirements `json:"resources,omitempty"`
	// Default node selector for every collector.
	// +optional
	NodeSelector map[string]string `json:"nodeSelector,omitempty"`
	// Default tolerations for every collector. When neither this,
	// agent.tolerations nor an affinity is set, the agent tolerates every
	// taint; an empty list opts out.
	// +optional
	Tolerations []corev1.Toleration `json:"tolerations,omitempty"`
	// Default affinity for every collector. When neither this nor
	// agent.affinity is set, the agent keeps off Windows, EKS Fargate and
	// virtual-kubelet nodes.
	// +optional
	Affinity *corev1.Affinity `json:"affinity,omitempty"`
	// +kubebuilder:default={create: true}
	// +optional
	ServiceAccount ServiceAccountSpec `json:"serviceAccount,omitempty"`
	// +kubebuilder:default={create: true}
	// +optional
	RBAC RBACSpec `json:"rbac,omitempty"`
	// +optional
	ResourceDetection ResourceDetectionSpec `json:"resourceDetection,omitempty"`
	// +optional
	Redaction RedactionSpec `json:"redaction,omitempty"`
}

// CollectionStatus is the observed state shared by collection CRDs.
type CollectionStatus struct {
	// +kubebuilder:validation:Enum=Ready;Pending;Error;Deleting
	Phase              string       `json:"phase,omitempty"`
	Message            string       `json:"message,omitempty"`
	ObservedGeneration int64        `json:"observedGeneration,omitempty"`
	LastSyncedAt       *metav1.Time `json:"lastSyncedAt,omitempty"`
	// Names of the OTel resources this CR currently owns.
	ManagedResources []string `json:"managedResources,omitempty"`
	// Conditions carries the standard Kubernetes condition set. Both
	// collection controllers publish a single "Ready" condition whose reason
	// names the reconcile stage that failed.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:path=tsugacollectorconfigs,scope=Cluster,singular=tsugacollectorconfig,shortName=tcc
// +kubebuilder:printcolumn:name="Cluster",type=string,JSONPath=".spec.clusterName"
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=".status.phase"
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=".metadata.creationTimestamp"
// +kubebuilder:validation:XValidation:rule="self.metadata.name == 'cluster'",message="metadata.name must be 'cluster': only one TsugaCollectorConfig is supported per cluster"
type TsugaCollectorConfig struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              TsugaCollectorConfigSpec `json:"spec,omitempty"`
	Status            CollectionStatus         `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type TsugaCollectorConfigList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []TsugaCollectorConfig `json:"items"`
}

func init() {
	SchemeBuilder.Register(&TsugaCollectorConfig{}, &TsugaCollectorConfigList{})
}
