package v1alpha1

import (
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// SLOAlert is an alert created on an SLO.
type SLOAlert struct {
	// Alert priority; 1 is highest, 5 is lowest.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=5
	Priority int32 `json:"priority"`

	// Configuration is kept schemaless because the Tsuga API models alert configuration as a
	// discriminator-based union (burn-rate or threshold). The operator passes it through as opaque JSON;
	// drift against the OpenAPI contract is gated by oasdiff in CI (see the openapi-drift-check make
	// target) rather than encoding the union in Go types.
	// +kubebuilder:validation:Schemaless
	// +kubebuilder:pruning:PreserveUnknownFields
	Configuration apiextensionsv1.JSON `json:"configuration"`
}

// SLOSpec is the desired state of a Tsuga SLO.
type SLOSpec struct {
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=250
	Name string `json:"name"`

	// +kubebuilder:validation:MaxLength=50000
	Description *string `json:"description,omitempty"`

	// Tags applied to the SLO.
	// +kubebuilder:validation:MaxItems=50
	Tags []ResourceTag `json:"tags,omitempty"`

	// Configuration is the SLI configuration. It is kept schemaless because the Tsuga API models it as a
	// discriminator-based union (event or time). The operator passes it through as opaque JSON; drift
	// against the OpenAPI contract is gated by oasdiff in CI (see the openapi-drift-check make target)
	// rather than encoding the full union in Go types.
	// +kubebuilder:validation:Schemaless
	// +kubebuilder:pruning:PreserveUnknownFields
	Configuration apiextensionsv1.JSON `json:"configuration"`

	// Target percentage the SLO must meet, exclusive of both bounds. Example: 99.9
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:ExclusiveMinimum=true
	// +kubebuilder:validation:Maximum=100
	// +kubebuilder:validation:ExclusiveMaximum=true
	Target float64 `json:"target"`

	// Rolling evaluation window in days.
	// +kubebuilder:validation:Enum=7;30;90
	TimeframeDays int32 `json:"timeframeDays"`

	// Team ID that owns and manages the SLO.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=250
	Owner string `json:"owner"`

	// This controls which data the resource can see.
	// +kubebuilder:validation:Enum=all;owning-team-and-public;owning-team-only
	Permissions string `json:"permissions"`

	// Cluster IDs this SLO is evaluated on. Omit to evaluate on all clusters.
	ClusterIDs []string `json:"clusterIds,omitempty"`

	// Alerts to create on this SLO. Omit for none; the operator sends an empty array, which the Tsuga
	// API requires the key to carry.
	Alerts []SLOAlert `json:"alerts,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:path=slos,scope=Namespaced,singular=slo,shortName=tslo
// +kubebuilder:printcolumn:name="Owner",type=string,JSONPath=".spec.owner"
// +kubebuilder:printcolumn:name="Target",type=number,JSONPath=".spec.target"
// +kubebuilder:printcolumn:name="Timeframe",type=number,JSONPath=".spec.timeframeDays"
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=".status.phase"
// +kubebuilder:printcolumn:name="TsugaID",type=string,JSONPath=".status.id"
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=".metadata.creationTimestamp"
type SLO struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   SLOSpec    `json:"spec,omitempty"`
	Status SyncStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type SLOList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []SLO `json:"items"`
}

func init() {
	SchemeBuilder.Register(&SLO{}, &SLOList{})
}
