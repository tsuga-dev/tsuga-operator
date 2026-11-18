package tsugamapper

import (
	"encoding/json"
	"testing"

	"github.com/tsuga-dev/tsuga-operator/api/v1alpha1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/utils/ptr"
)

func TestMarshalDashboardPayload_RequiresGraphID(t *testing.T) {
	vis := apiextensionsv1.JSON{Raw: []byte(`{"type":"timeseries"}`)}
	spec := v1alpha1.DashboardSpec{
		Name:  "x",
		Owner: "o",
		Graphs: []v1alpha1.DashboardGraph{
			{Visualization: vis},
		},
	}
	_, err := DashboardSpecToPayload(spec)
	if err == nil {
		t.Fatal("expected error for missing graph id")
	}
}

func TestMarshalDashboardPayload_KeepsGraphIDAndDefaultsTimePreset(t *testing.T) {
	vis := apiextensionsv1.JSON{Raw: []byte(`{"type":"timeseries"}`)}
	spec := v1alpha1.DashboardSpec{
		Name:   "x",
		Owner:  "o",
		Graphs: []v1alpha1.DashboardGraph{{ID: " g1 ", Visualization: vis}},
	}
	payload, err := DashboardSpecToPayload(spec)
	if err != nil {
		t.Fatal(err)
	}
	var body struct {
		Graphs     []struct{ ID string } `json:"graphs"`
		TimePreset string                `json:"timePreset"`
	}
	if err := json.Unmarshal(payload, &body); err != nil {
		t.Fatal(err)
	}
	if body.Graphs[0].ID != " g1 " {
		t.Fatalf("want graph id sent unchanged, got %q", body.Graphs[0].ID)
	}
	if body.TimePreset != defaultTimePreset {
		t.Fatalf("want timePreset %q when unset, got %q", defaultTimePreset, body.TimePreset)
	}
}

func dashboardPayloadBody(t *testing.T, spec v1alpha1.DashboardSpec) map[string]any {
	t.Helper()
	payload, err := DashboardSpecToPayload(spec)
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err := json.Unmarshal(payload, &body); err != nil {
		t.Fatal(err)
	}
	return body
}

func minimalDashboardSpec() v1alpha1.DashboardSpec {
	return v1alpha1.DashboardSpec{
		Name:   "x",
		Owner:  "o",
		Graphs: []v1alpha1.DashboardGraph{{ID: "g1", Visualization: apiextensionsv1.JSON{Raw: []byte(`{"type":"timeseries"}`)}}},
	}
}

func TestDashboardSpecToPayload_OmitsDriftPolicy(t *testing.T) {
	spec := minimalDashboardSpec()
	spec.DriftPolicy = v1alpha1.DriftPolicyWarn

	if _, ok := dashboardPayloadBody(t, spec)["driftPolicy"]; ok {
		t.Fatal("driftPolicy must not be sent to the API")
	}
}

func TestDashboardSpecToPayload_SendsFolderID(t *testing.T) {
	spec := minimalDashboardSpec()
	spec.FolderID = ptr.To("folder-1")

	if body := dashboardPayloadBody(t, spec); body["folderId"] != "folder-1" {
		t.Errorf("expected folderId folder-1, got %v", body["folderId"])
	}
}

// The Tsuga API keeps a dashboard's current folder when folderId is absent on
// update and clears it on null, which would unfile a dashboard filed in the UI.
func TestDashboardSpecToPayload_OmitsUnsetFolderID(t *testing.T) {
	if value, present := dashboardPayloadBody(t, minimalDashboardSpec())["folderId"]; present {
		t.Errorf("folderId should be omitted when unset, got %v", value)
	}
}

func TestDashboardSpecToPayload_DropsFolderRef(t *testing.T) {
	spec := minimalDashboardSpec()
	spec.FolderRef = &v1alpha1.LocalObjectReference{Name: "checkout"}

	if value, present := dashboardPayloadBody(t, spec)["folderRef"]; present {
		t.Errorf("folderRef is Kubernetes-only, got %v", value)
	}
}
