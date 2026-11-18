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

	// How the operator reacts to changes made in Tsuga outside Kubernetes:
	// Overwrite reverts them to match the spec, Warn keeps them and reports
	// the drift until the annotation observability.tsuga.com/accept-remote is set.
	// +optional
	// +kubebuilder:default=Overwrite
	// +kubebuilder:validation:Enum=Overwrite;Warn
	DriftPolicy DriftPolicy `json:"driftPolicy,omitempty"`

	// Ordered widgets that compose the dashboard.
	// +kubebuilder:validation:MinItems=1
	Graphs []DashboardGraph `json:"graphs"`

	// Filters applied to every widget on the dashboard.
	Filters []DashboardFilter `json:"filters,omitempty"`

	// Tags applied to the dashboard.
	Tags []ResourceTag `json:"tags,omitempty"`

	// Example: "last_1h"
	TimePreset *string `json:"timePreset,omitempty"`

	// Optional dashboard folder ID in Tsuga. When unset (and no folderRef), the dashboard keeps its current folder.
	// +optional
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=250
	FolderID *string `json:"folderId,omitempty"`

	// Optional reference to a DashboardFolder custom resource in the same namespace.
	// folderId takes precedence when both are set.
	// +optional
	FolderRef *LocalObjectReference `json:"folderRef,omitempty"`
}

// DashboardStatus is the observed state of a Tsuga dashboard.
type DashboardStatus struct {
	SyncStatus `json:",inline"`

	// Dashboard folder ID last sent to Tsuga from folderRef
	FolderID string `json:"folderId,omitempty"`
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

	Spec   DashboardSpec   `json:"spec,omitempty"`
	Status DashboardStatus `json:"status,omitempty"`
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
