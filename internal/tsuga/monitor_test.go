package tsugamapper

import (
	"encoding/json"
	"testing"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/utils/ptr"

	"github.com/tsuga-dev/tsuga-operator/api/v1alpha1"
)

func minimalMonitorSpec() v1alpha1.MonitorSpec {
	return v1alpha1.MonitorSpec{
		Name:          "Checkout errors",
		Configuration: apiextensionsv1.JSON{Raw: []byte(`{"type":"logs"}`)},
		Priority:      2,
		Owner:         "team-checkout",
		Permissions:   "all",
	}
}

func monitorPayloadBody(t *testing.T, spec v1alpha1.MonitorSpec) map[string]any {
	t.Helper()
	raw, err := MonitorSpecToPayload(spec)
	if err != nil {
		t.Fatalf("MonitorSpecToPayload: %v", err)
	}
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("payload is not valid json: %v", err)
	}
	return body
}

func TestMonitorSpecToPayload_SendsFolderID(t *testing.T) {
	spec := minimalMonitorSpec()
	spec.FolderID = ptr.To("folder-1")

	body := monitorPayloadBody(t, spec)
	if body["folderId"] != "folder-1" {
		t.Errorf("expected folderId folder-1, got %v", body["folderId"])
	}
}

// The Tsuga API keeps a monitor's current folder when folderId is absent on
// update, so an unset spec.folderId must be omitted rather than sent as null,
// which would unfile a monitor filed in the UI.
func TestMonitorSpecToPayload_OmitsUnsetFolderID(t *testing.T) {
	body := monitorPayloadBody(t, minimalMonitorSpec())
	if value, present := body["folderId"]; present {
		t.Errorf("folderId should be omitted when unset, got %v", value)
	}
}
