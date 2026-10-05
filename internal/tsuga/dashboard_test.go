package tsugamapper

import (
	"encoding/json"
	"testing"

	"github.com/tsuga-dev/tsuga-operator/api/v1alpha1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
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
