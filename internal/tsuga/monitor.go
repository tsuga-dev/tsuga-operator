package tsugamapper

import (
	"encoding/json"

	"github.com/tsuga-dev/tsuga-operator/api/v1alpha1"
)

// MonitorSpecToPayload marshals a MonitorSpec into the Tsuga API request body.
// Create and update send the same body.
func MonitorSpecToPayload(spec v1alpha1.MonitorSpec) ([]byte, error) {
	// dashboardRef, folderRef, ownerName and driftPolicy are Kubernetes-only;
	// callers resolve them into dashboardId, folderId and owner.
	spec.DashboardRef = nil
	spec.FolderRef = nil
	spec.OwnerName = ""
	spec.DriftPolicy = ""
	return json.Marshal(spec)
}
