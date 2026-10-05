package v1alpha1

import (
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type LocalObjectReference struct {
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`
}

// MonitorSpec is the desired state of a Tsuga monitor.
type MonitorSpec struct {
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`

	Message *string `json:"message,omitempty"`

	Tags []ResourceTag `json:"tags,omitempty"`

	// Configuration is kept schemaless because the Tsuga API models monitor configuration as a discriminator-based union.
	// The operator passes it through as opaque JSON; drift against the OpenAPI contract is gated by oasdiff in CI
	// (see the openapi-drift-check make target) rather than encoding the full union in Go types.
	// +kubebuilder:validation:Schemaless
	// +kubebuilder:pruning:PreserveUnknownFields
	Configuration apiextensionsv1.JSON `json:"configuration"`

	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=5
	Priority int32 `json:"priority"`

	// Team ID that owns and manages the monitor.
	// +kubebuilder:validation:MinLength=1
	Owner string `json:"owner"`

	// Optional related dashboard ID in Tsuga.
	DashboardID *string `json:"dashboardId,omitempty"`

	// Optional reference to a Dashboard custom resource in the same namespace.
	DashboardRef *LocalObjectReference `json:"dashboardRef,omitempty"`

	// This controls which data the resource can see.
	// +kubebuilder:validation:Enum=all;owning-team-and-public;owning-team-only
	Permissions string `json:"permissions"`
}

// MonitorStatus is the observed state of a Tsuga monitor.
type MonitorStatus struct {
	SyncStatus `json:",inline"`

	// Tsuga dashboard ID last sent to Tsuga, resolved from dashboardId or dashboardRef
	DashboardID string `json:"dashboardId,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:path=monitors,scope=Namespaced,singular=monitor,shortName=tmon
// +kubebuilder:printcolumn:name="Owner",type=string,JSONPath=".spec.owner"
// +kubebuilder:printcolumn:name="Priority",type=number,JSONPath=".spec.priority"
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=".status.phase"
// +kubebuilder:printcolumn:name="TsugaID",type=string,JSONPath=".status.id"
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=".metadata.creationTimestamp"
type Monitor struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   MonitorSpec   `json:"spec,omitempty"`
	Status MonitorStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type MonitorList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Monitor `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Monitor{}, &MonitorList{})
}
