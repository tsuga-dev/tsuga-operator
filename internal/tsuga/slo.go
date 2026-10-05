package tsugamapper

import (
	"encoding/json"

	"github.com/tsuga-dev/tsuga-operator/api/v1alpha1"
)

// sloPayload shadows the spec fields whose keys the API needs even when
// empty, which the CRD's omitempty tags would otherwise drop.
type sloPayload struct {
	v1alpha1.SLOSpec
	// The API treats an absent key as "keep the current cluster scope" on
	// update, so an unset spec (meaning "all clusters") must still send the
	// key as an empty array to clear it.
	ClusterIDs []string `json:"clusterIds"`
	// The API requires the key, and accepts an empty array to mean "no alerts".
	Alerts []v1alpha1.SLOAlert `json:"alerts"`
}

// SLOSpecToPayload marshals an SLOSpec into the Tsuga API request body.
// Create and update send the same body.
func SLOSpecToPayload(spec v1alpha1.SLOSpec) ([]byte, error) {
	return json.Marshal(sloPayload{
		SLOSpec:    spec,
		ClusterIDs: append([]string{}, spec.ClusterIDs...),
		Alerts:     append([]v1alpha1.SLOAlert{}, spec.Alerts...),
	})
}
