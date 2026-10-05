package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
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
}

// TelemetryToggles enables signal types on the node agent.
type TelemetryToggles struct {
	// +kubebuilder:default=true
	Traces bool `json:"traces"`
	// +kubebuilder:default=true
	Metrics bool `json:"metrics"`
	// +kubebuilder:default=true
	Logs bool `json:"logs"`
}

// GatewayToggles enables cluster-scoped receivers on the gateway collector.
type GatewayToggles struct {
	// +kubebuilder:default=true
	ClusterMetrics bool `json:"clusterMetrics"`
	// +kubebuilder:default=true
	KubernetesObjects bool `json:"kubernetesObjects"`
}

// TargetAllocatorSpec enables the Target Allocator, which decouples Prometheus
// service discovery from scraping so the scraper collector can run more than
// one replica without every replica scraping every target.
type TargetAllocatorSpec struct {
	// +kubebuilder:default=false
	// +optional
	Enabled bool `json:"enabled,omitempty"`
	// How the Target Allocator spreads targets across scraper replicas.
	// per-node assigns only targets that resolve to a node, so anything else
	// is left unscraped.
	// +kubebuilder:default=consistent-hashing
	// +kubebuilder:validation:Enum=consistent-hashing;least-weighted;per-node
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
	// +kubebuilder:default={enabled: false, allocationStrategy: "consistent-hashing"}
	// +optional
	TargetAllocator TargetAllocatorSpec `json:"targetAllocator,omitempty"`
}

// TsugaCollectorConfigSpec is the desired cluster collection topology.
type TsugaCollectorConfigSpec struct {
	// Human-readable cluster name added as k8s.cluster.name. It is spliced
	// into the collector YAML, so the pattern keeps out quotes, backslashes
	// and newlines.
	// +kubebuilder:validation:MaxLength=253
	// +kubebuilder:validation:Pattern=`^[A-Za-z0-9]([A-Za-z0-9 ._-]*[A-Za-z0-9])?$`
	ClusterName string `json:"clusterName,omitempty"`
	// Namespace where the managed collectors are created.
	// +kubebuilder:default=tsuga-operator-system
	CollectorNamespace string           `json:"collectorNamespace,omitempty"`
	Export             ExportSpec       `json:"export"`
	Agent              TelemetryToggles `json:"agent"`
	Gateway            GatewayToggles   `json:"gateway"`
	// Prometheus configures the annotation-scraping collector. Absent means
	// no scraper is rendered.
	// +optional
	Prometheus PrometheusSpec `json:"prometheus,omitempty"`
	// OTel Collector image override (defaults handled by the OTel Operator when empty).
	Image string `json:"image,omitempty"`
	// Resources overrides the CPU/memory requests and limits applied to the
	// agent, the gateway and, when enabled, the Prometheus scraper. The default sizes a cluster of up to ~50 nodes; the gateway
	// holds the whole cluster's object state in memory and cannot be scaled
	// out, so raise the limit with the cluster (roughly 2Gi at 100 nodes, 4Gi
	// at 250). The agent's memory_limiter is a percentage of the container
	// limit, so removing limits entirely makes it measure against the node.
	// +optional
	Resources *corev1.ResourceRequirements `json:"resources,omitempty"`
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
