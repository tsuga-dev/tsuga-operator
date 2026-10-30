package tsugamapper

import (
	"encoding/json"

	"github.com/tsuga-dev/tsuga-operator/api/v1alpha1"
)

// MonitorSpecToPayload marshals a MonitorSpec into the Tsuga API request body.
// Create and update send the same body.
func MonitorSpecToPayload(spec v1alpha1.MonitorSpec) ([]byte, error) {
	// dashboardRef and ownerName are Kubernetes-only; callers resolve them
	// into dashboardId and owner.
	spec.DashboardRef = nil
	spec.OwnerName = ""
	return json.Marshal(spec)
}
