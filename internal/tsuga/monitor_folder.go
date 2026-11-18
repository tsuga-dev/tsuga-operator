package tsugamapper

import (
	"encoding/json"

	"github.com/tsuga-dev/tsuga-operator/api/v1alpha1"
)

// MonitorFolderSpecToPayload marshals a MonitorFolderSpec into the Tsuga API
// request body. Create and update send the same body.
func MonitorFolderSpecToPayload(spec v1alpha1.MonitorFolderSpec) ([]byte, error) {
	// parentFolderRef and ownerName are Kubernetes-only; callers resolve them
	// into parentFolderId and owner.
	spec.ParentFolderRef = nil
	spec.OwnerName = ""
	return json.Marshal(spec)
}
