package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// DashboardSpec is the desired state of a Tsuga dashboard.
// +kubebuilder:validation:XValidation:rule="has(self.owner) != has(self.ownerName)",message="exactly one of owner or ownerName is required"
type DashboardSpec struct {
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`

	// Team ID that owns and manages the dashboard. Exactly one of owner and ownerName is required.
	// +optional
	// +kubebuilder:validation:MinLength=1
	Owner string `json:"owner,omitempty"`

	// Name of the team that owns and manages the dashboard. The operator resolves it to
	// the team ID on every push to Tsuga.
	// +optional
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=250
	OwnerName string `json:"ownerName,omitempty"`

	// Ordered widgets that compose the dashboard.
	// +kubebuilder:validation:MinItems=1
	Graphs []DashboardGraph `json:"graphs"`

	// Filters applied to every widget on the dashboard.
	Filters []DashboardFilter `json:"filters,omitempty"`

	// Tags applied to the dashboard.
	Tags []ResourceTag `json:"tags,omitempty"`

	// Example: "last_1h"
	TimePreset *string `json:"timePreset,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:path=dashboards,scope=Namespaced,singular=dashboard,shortName=tdb
// +kubebuilder:printcolumn:name="Owner",type=string,JSONPath=".spec.owner"
// +kubebuilder:printcolumn:name="OwnerName",type=string,JSONPath=".spec.ownerName"
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=".status.phase"
// +kubebuilder:printcolumn:name="TsugaID",type=string,JSONPath=".status.id"
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=".metadata.creationTimestamp"
type Dashboard struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   DashboardSpec `json:"spec,omitempty"`
	Status SyncStatus    `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type DashboardList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Dashboard `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Dashboard{}, &DashboardList{})
}
