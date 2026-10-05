package tsugamapper

import (
	"encoding/json"

	"github.com/tsuga-dev/tsuga-operator/api/v1alpha1"
)

// MonitorSpecToPayload marshals a MonitorSpec into the Tsuga API request body.
// Create and update send the same body.
func MonitorSpecToPayload(spec v1alpha1.MonitorSpec) ([]byte, error) {
	// dashboardRef is Kubernetes-only; callers resolve it into dashboardId.
	spec.DashboardRef = nil
	return json.Marshal(spec)
}
