package v1alpha1

import (
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// TsugaCollectorSpec wraps a native OpenTelemetryCollector spec. The operator
// renders it as OpenTelemetryCollector tsuga-<name>, adding only what the
// toggles enable. Nothing the spec already defines is replaced.
type TsugaCollectorSpec struct {
	// Adds an otlp/tsuga-agent exporter, which sends to the node agent, to
	// every pipeline that does not pass its data to a forward, routing,
	// failover, round_robin or datadog connector. The agent adds the
	// Tsuga credentials and cluster enrichment.
	// +kubebuilder:default=true
	// +optional
	TsugaExporter *bool `json:"tsugaExporter,omitempty"`
	// Sends the collector's internal metrics to the node agent, unless
	// service.telemetry.metrics is already set.
	// +kubebuilder:default=true
	// +optional
	SelfTelemetry *bool `json:"selfTelemetry,omitempty"`
	// Puts memory_limiter first and batch last in every pipeline that does
	// not pass its data to a forward, routing, failover, round_robin or datadog
	// connector and has none of that type yet.
	// +kubebuilder:default=true
	// +optional
	DefaultProcessors *bool `json:"defaultProcessors,omitempty"`
	// OpenTelemetryCollector spec (opentelemetry.io/v1beta1), passed through.
	// The operator adds only what the toggles enable, and fills image,
	// resources and security contexts when unset.
	// Creating a TsugaCollector is equivalent to creating pods in its
	// namespace, with access to every Secret and ServiceAccount there; grant
	// it like pod create.
	// +kubebuilder:pruning:PreserveUnknownFields
	// +kubebuilder:validation:Schemaless
	// +kubebuilder:validation:Type=object
	Collector *apiextensionsv1.JSON `json:"collector"`
}

// The 36 character cap keeps tsuga-<name>-collector-monitoring, the longest
// name the OTel Operator derives, within 63 characters.
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:path=tsugacollectors,scope=Namespaced,singular=tsugacollector,shortName=tscol
// +kubebuilder:validation:XValidation:rule="size(self.metadata.name) <= 36",message="metadata.name must be at most 36 characters"
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=".status.phase"
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=".metadata.creationTimestamp"
type TsugaCollector struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              TsugaCollectorSpec `json:"spec,omitempty"`
	Status            CollectionStatus   `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type TsugaCollectorList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []TsugaCollector `json:"items"`
}

func init() {
	SchemeBuilder.Register(&TsugaCollector{}, &TsugaCollectorList{})
}
