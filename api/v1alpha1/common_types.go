package v1alpha1

import (
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// SyncStatus is the observed state of a resource synced to the Tsuga API.
type SyncStatus struct {
	// Tsuga resource ID
	ID string `json:"id,omitempty"`

	// Last generation successfully reconciled
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Human-readable phase: Ready, Error, Deleting
	Phase string `json:"phase,omitempty"`

	// Last error or status message
	Message string `json:"message,omitempty"`

	// Last successful sync time
	LastSyncedAt *metav1.Time `json:"lastSyncedAt,omitempty"`
}

type ResourceTag struct {
	// +kubebuilder:validation:MaxLength=128
	Key string `json:"key"`

	// +kubebuilder:validation:MaxLength=256
	Value string `json:"value"`
}

type DashboardFilter struct {
	// +kubebuilder:validation:MinLength=1
	Key string `json:"key"`

	// +kubebuilder:validation:MinItems=1
	Values []string `json:"values"`
}

type DashboardGraphLayout struct {
	X int32 `json:"x"`
	Y int32 `json:"y"`

	// +kubebuilder:validation:Minimum=1
	W int32 `json:"w"`

	// +kubebuilder:validation:Minimum=1
	H int32 `json:"h"`
}

type DashboardGraph struct {
	// Unique widget id sent to Tsuga (required).
	// +kubebuilder:validation:MinLength=1
	ID string `json:"id"`

	Name        string                `json:"name,omitempty"`
	Description string                `json:"description,omitempty"`
	Layout      *DashboardGraphLayout `json:"layout,omitempty"`

	// Visualization is kept schemaless because the Tsuga API models this field as a discriminator-based union.
	// The operator passes it through as opaque JSON; drift against the OpenAPI contract is gated by oasdiff in CI
	// (see the openapi-drift-check make target) rather than encoding the full union in Go types.
	// +kubebuilder:validation:Schemaless
	// +kubebuilder:pruning:PreserveUnknownFields
	Visualization apiextensionsv1.JSON `json:"visualization"`
}
