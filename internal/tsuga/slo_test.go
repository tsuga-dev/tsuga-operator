package tsugamapper

import (
	"encoding/json"
	"testing"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"

	"github.com/tsuga-dev/tsuga-operator/api/v1alpha1"
)

func minimalSLOSpec() v1alpha1.SLOSpec {
	return v1alpha1.SLOSpec{
		Name:          "Checkout availability",
		Configuration: apiextensionsv1.JSON{Raw: []byte(`{"type":"event","dataSource":"traces"}`)},
		Target:        99.9,
		TimeframeDays: 30,
		Owner:         "team-checkout",
		Permissions:   "owning-team-and-public",
	}
}

func TestSLOSpecToPayload_MapsEveryField(t *testing.T) {
	description := "checkout must stay up"
	spec := minimalSLOSpec()
	spec.Description = &description
	spec.Tags = []v1alpha1.ResourceTag{{Key: "env", Value: "prod"}}
	spec.ClusterIDs = []string{"cluster-a"}
	spec.Alerts = []v1alpha1.SLOAlert{
		{Priority: 2, Configuration: apiextensionsv1.JSON{Raw: []byte(`{"type":"burn-rate","burnRate":14.4}`)}},
	}

	raw, err := SLOSpecToPayload(spec)
	if err != nil {
		t.Fatalf("SLOSpecToPayload: %v", err)
	}

	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("payload is not valid json: %v", err)
	}

	if body["name"] != "Checkout availability" {
		t.Errorf("name: got %v", body["name"])
	}
	if body["description"] != description {
		t.Errorf("description: got %v", body["description"])
	}
	if body["target"] != 99.9 {
		t.Errorf("target: got %v", body["target"])
	}
	if body["timeframeDays"] != float64(30) {
		t.Errorf("timeframeDays: got %v", body["timeframeDays"])
	}
	if body["owner"] != "team-checkout" {
		t.Errorf("owner: got %v", body["owner"])
	}
	if body["permissions"] != "owning-team-and-public" {
		t.Errorf("permissions: got %v", body["permissions"])
	}

	configuration, ok := body["configuration"].(map[string]any)
	if !ok {
		t.Fatalf("configuration: got %T", body["configuration"])
	}
	if configuration["dataSource"] != "traces" {
		t.Errorf("configuration.dataSource: got %v", configuration["dataSource"])
	}

	tags, ok := body["tags"].([]any)
	if !ok || len(tags) != 1 {
		t.Fatalf("tags: got %v", body["tags"])
	}
	if tag := tags[0].(map[string]any); tag["key"] != "env" || tag["value"] != "prod" {
		t.Errorf("tags[0]: got %v", tag)
	}

	clusterIDs, ok := body["clusterIds"].([]any)
	if !ok || len(clusterIDs) != 1 || clusterIDs[0] != "cluster-a" {
		t.Errorf("clusterIds: got %v", body["clusterIds"])
	}

	alerts, ok := body["alerts"].([]any)
	if !ok || len(alerts) != 1 {
		t.Fatalf("alerts: got %v", body["alerts"])
	}
	alert := alerts[0].(map[string]any)
	if alert["priority"] != float64(2) {
		t.Errorf("alerts[0].priority: got %v", alert["priority"])
	}
	if alert["configuration"].(map[string]any)["type"] != "burn-rate" {
		t.Errorf("alerts[0].configuration.type: got %v", alert["configuration"])
	}
}

// The Tsuga API requires the alerts key even when there are no alerts, so an
// absent spec.alerts must marshal as [] rather than being omitted or null.
func TestSLOSpecToPayload_EmitsEmptyAlertsArray(t *testing.T) {
	raw, err := SLOSpecToPayload(minimalSLOSpec())
	if err != nil {
		t.Fatalf("SLOSpecToPayload: %v", err)
	}

	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("payload is not valid json: %v", err)
	}

	alerts, ok := body["alerts"]
	if !ok {
		t.Fatal("alerts key must be present even when empty")
	}
	list, ok := alerts.([]any)
	if !ok {
		t.Fatalf("alerts must be an array, got %T", alerts)
	}
	if len(list) != 0 {
		t.Fatalf("expected an empty alerts array, got %v", list)
	}
}

func TestSLOSpecToPayload_OmitsAbsentOptionalFields(t *testing.T) {
	raw, err := SLOSpecToPayload(minimalSLOSpec())
	if err != nil {
		t.Fatalf("SLOSpecToPayload: %v", err)
	}

	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("payload is not valid json: %v", err)
	}

	for _, key := range []string{"description", "tags"} {
		if _, present := body[key]; present {
			t.Errorf("%s should be omitted when unset, got %v", key, body[key])
		}
	}
}

// The Tsuga API treats an absent clusterIds key as "preserve the current
// cluster scope" on update, so an unset spec.clusterIds (meaning "all
// clusters") must marshal as [] rather than being omitted or null.
func TestSLOSpecToPayload_EmitsEmptyClusterIDsArray(t *testing.T) {
	raw, err := SLOSpecToPayload(minimalSLOSpec())
	if err != nil {
		t.Fatalf("SLOSpecToPayload: %v", err)
	}

	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("payload is not valid json: %v", err)
	}

	clusterIDs, ok := body["clusterIds"]
	if !ok {
		t.Fatal("clusterIds key must be present even when empty")
	}
	list, ok := clusterIDs.([]any)
	if !ok {
		t.Fatalf("clusterIds must be an array, got %T", clusterIDs)
	}
	if len(list) != 0 {
		t.Fatalf("expected an empty clusterIds array, got %v", list)
	}
}
