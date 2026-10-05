package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// InstrumentationSpec controls auto-instrumentation for a namespace.
type InstrumentationSpec struct {
	// When turned off, the inject annotations the operator added to workloads
	// in this namespace are removed, so they come back uninstrumented on their
	// next roll. Inject annotations set by hand are left in place.
	// +kubebuilder:default=false
	Enabled bool `json:"enabled"`
	// Languages to inject. Valid: java, nodejs, python, dotnet.
	// Dropping a language removes the inject annotation the operator added for
	// it; an annotation set by hand for that language is left in place.
	// +kubebuilder:validation:items:Enum=java;nodejs;python;dotnet
	Languages []string `json:"languages,omitempty"`
}

// TsugaMonitoringSpec is the desired per-namespace monitoring state.
type TsugaMonitoringSpec struct {
	Instrumentation InstrumentationSpec `json:"instrumentation,omitempty"`
	// When true, every eligible workload in the namespace is annotated for
	// injection. When false, the operator annotates nothing; annotate selected
	// pod templates manually.
	// +kubebuilder:default=true
	// +optional
	InjectExistingWorkloads bool `json:"injectExistingWorkloads"`
}

// TsugaMonitoringStatus is CollectionStatus plus the observed instrumentation
// coverage. The count is namespace-scoped, so it lives here rather than on the
// shared CollectionStatus where the cluster-scoped TsugaCollectorConfig would
// inherit a field that can never be meaningful for it.
type TsugaMonitoringStatus struct {
	CollectionStatus `json:",inline"`
	// Number of workloads in this namespace whose pod template carries an
	// inject annotation pointing at this TsugaMonitoring, whether the operator
	// or an app team set it.
	// +optional
	InstrumentedWorkloads int32 `json:"instrumentedWorkloads"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:path=tsugamonitorings,scope=Namespaced,singular=tsugamonitoring,shortName=tsmon
// +kubebuilder:printcolumn:name="Instrumented",type=boolean,JSONPath=".spec.instrumentation.enabled"
// +kubebuilder:printcolumn:name="Workloads",type=integer,JSONPath=".status.instrumentedWorkloads"
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=".status.phase"
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=".metadata.creationTimestamp"
type TsugaMonitoring struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              TsugaMonitoringSpec   `json:"spec,omitempty"`
	Status            TsugaMonitoringStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type TsugaMonitoringList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []TsugaMonitoring `json:"items"`
}

func init() {
	SchemeBuilder.Register(&TsugaMonitoring{}, &TsugaMonitoringList{})
}
