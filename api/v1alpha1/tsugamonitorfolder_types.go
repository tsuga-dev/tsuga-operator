package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// MonitorFolderSpec is the desired state of a Tsuga monitor folder.
// +kubebuilder:validation:XValidation:rule="has(self.owner) != has(self.ownerName)",message="exactly one of owner or ownerName is required"
type MonitorFolderSpec struct {
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=250
	Name string `json:"name"`

	// Team ID that owns the folder and controls access to it. Exactly one of owner and ownerName is required.
	// +optional
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=250
	Owner string `json:"owner,omitempty"`

	// Name of the team that owns the folder. The operator resolves it to
	// the team ID on every push to Tsuga.
	// +optional
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=250
	OwnerName string `json:"ownerName,omitempty"`

	// Optional parent monitor folder ID in Tsuga. The parent must be a top-level folder.
	// When unset, the folder keeps its current parent.
	// +optional
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=250
	ParentFolderID *string `json:"parentFolderId,omitempty"`

	// Optional reference to a parent MonitorFolder custom resource in the same namespace.
	// parentFolderId takes precedence when both are set.
	// +optional
	ParentFolderRef *LocalObjectReference `json:"parentFolderRef,omitempty"`

	// Tags applied to the folder.
	Tags []ResourceTag `json:"tags,omitempty"`
}

// MonitorFolderStatus is the observed state of a Tsuga monitor folder.
type MonitorFolderStatus struct {
	SyncStatus `json:",inline"`

	// Parent folder ID last pushed to Tsuga from parentFolderRef.
	ParentFolderID string `json:"parentFolderId,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:path=monitorfolders,scope=Namespaced,singular=monitorfolder,shortName=tmf
// +kubebuilder:printcolumn:name="Owner",type=string,JSONPath=".spec.owner"
// +kubebuilder:printcolumn:name="OwnerName",type=string,JSONPath=".spec.ownerName"
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=".status.phase"
// +kubebuilder:printcolumn:name="TsugaID",type=string,JSONPath=".status.id"
// +kubebuilder:printcolumn:name="Parent",type=string,JSONPath=".status.parentFolderId"
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=".metadata.creationTimestamp"
type MonitorFolder struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   MonitorFolderSpec   `json:"spec,omitempty"`
	Status MonitorFolderStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type MonitorFolderList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []MonitorFolder `json:"items"`
}

func init() {
	SchemeBuilder.Register(&MonitorFolder{}, &MonitorFolderList{})
}
