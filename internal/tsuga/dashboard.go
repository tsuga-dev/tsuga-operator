package tsugamapper

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/tsuga-dev/tsuga-operator/api/v1alpha1"
)

// defaultTimePreset is the preset Tsuga applies when create omits one. Update
// omission keeps the current value instead, so it is sent explicitly to reset
// a preset removed from the spec.
const defaultTimePreset = "past-30-minutes"

// DashboardSpecToPayload marshals a DashboardSpec into the Tsuga API request
// body. Create and update send the same body.
func DashboardSpecToPayload(spec v1alpha1.DashboardSpec) ([]byte, error) {
	for idx, graph := range spec.Graphs {
		if strings.TrimSpace(graph.ID) == "" {
			return nil, fmt.Errorf("graphs[%d]: id is required", idx)
		}
	}
	if spec.TimePreset == nil {
		preset := defaultTimePreset
		spec.TimePreset = &preset
	}
	return json.Marshal(spec)
}
