package e2efull

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
)

func TestJSONFieldPlainNestedObjectPath(t *testing.T) {
	payload := []byte(`{"data": {"name": "checkout-e2e-abc123"}}`)
	got, err := jsonField(payload, "data", "name")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "checkout-e2e-abc123" {
		t.Fatalf("want %q, got %q", "checkout-e2e-abc123", got)
	}
}

func TestJSONFieldIndexesIntoArrayByNumericString(t *testing.T) {
	payload := []byte(`{
		"data": {
			"graphs": [
				{"visualization": {"type": "table"}}
			]
		}
	}`)
	got, err := jsonField(payload, "data", "graphs", "0", "visualization", "type")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "table" {
		t.Fatalf("want %q, got %q", "table", got)
	}
}

func TestJSONFieldOutOfRangeIndexReturnsErrorNotPanic(t *testing.T) {
	payload := []byte(`{"data": {"graphs": [{"visualization": {"type": "table"}}]}}`)
	_, err := jsonField(payload, "data", "graphs", "5", "visualization", "type")
	if err == nil {
		t.Fatal("want an error for an out-of-range index, got none")
	}
	if !strings.Contains(err.Error(), "out of range") {
		t.Fatalf("want error mentioning out of range, got: %v", err)
	}
}

func TestJSONFieldNonNumericKeyAgainstArrayReturnsClearError(t *testing.T) {
	payload := []byte(`{"data": {"graphs": [{"visualization": {"type": "table"}}]}}`)
	_, err := jsonField(payload, "data", "graphs", "visualization", "type")
	if err == nil {
		t.Fatal("want an error when indexing an array with a non-numeric key, got none")
	}
	if !strings.Contains(err.Error(), "array") {
		t.Fatalf("want error explaining the parent is an array, got: %v", err)
	}
}

func TestJSONFieldMissingKeyReturnsErrorNamingTheKey(t *testing.T) {
	payload := []byte(`{"data": {"name": "checkout-e2e-abc123"}}`)
	_, err := jsonField(payload, "data", "owner")
	if err == nil {
		t.Fatal("want an error for a missing key, got none")
	}
	if !strings.Contains(err.Error(), `"owner"`) {
		t.Fatalf("want error naming the missing key %q, got: %v", "owner", err)
	}
}

func TestJSONFieldMalformedJSONReturnsError(t *testing.T) {
	_, err := jsonField([]byte("not json at all"), "data")
	if err == nil {
		t.Fatal("want an error for malformed JSON, got none")
	}
}

type renderedDashboard struct {
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`
	Metadata   struct {
		Name      string `json:"name"`
		Namespace string `json:"namespace"`
	} `json:"metadata"`
	Spec struct {
		Name  string `json:"name"`
		Owner string `json:"owner"`
		Tags  []struct {
			Key   string `json:"key"`
			Value string `json:"value"`
		} `json:"tags"`
		// These field names mirror the graph item in
		// config/crd/bases/observability.tsuga.com_dashboards.yaml, which
		// declares [description, id, layout, name, visualization] and
		// requires id and visualization. An earlier version of this struct
		// had a "Title" field that the CRD does not define, so it round-
		// tripped happily here while the API server pruned it and rejected
		// every Tier C dashboard for a missing spec.graphs[0].id.
		Graphs []struct {
			ID            string         `json:"id"`
			Name          string         `json:"name"`
			Visualization map[string]any `json:"visualization"`
		} `json:"graphs"`
	} `json:"spec"`
}

func TestRenderDashboardProducesValidManifest(t *testing.T) {
	previousRunID := runID
	runID = "e2e-testrunid"
	defer func() { runID = previousRunID }()

	manifest, err := renderDashboard("checkout-viz-00", "e2e-tier-c", "team-123", "timeseries")
	if err != nil {
		t.Fatalf("rendering the dashboard: %v", err)
	}

	var parsed renderedDashboard
	if err := yaml.Unmarshal([]byte(manifest), &parsed); err != nil {
		t.Fatalf("manifest is not valid YAML: %v\n%s", err, manifest)
	}

	if parsed.APIVersion != "observability.tsuga.com/v1alpha1" {
		t.Errorf("want apiVersion %q, got %q", "observability.tsuga.com/v1alpha1", parsed.APIVersion)
	}
	if parsed.Kind != "Dashboard" {
		t.Errorf("want kind %q, got %q", "Dashboard", parsed.Kind)
	}
	if parsed.Metadata.Name != "checkout-viz-00" {
		t.Errorf("want metadata.name %q, got %q", "checkout-viz-00", parsed.Metadata.Name)
	}
	if parsed.Metadata.Namespace != "e2e-tier-c" {
		t.Errorf("want metadata.namespace %q, got %q", "e2e-tier-c", parsed.Metadata.Namespace)
	}
	if parsed.Spec.Owner != "team-123" {
		t.Errorf("want spec.owner %q, got %q", "team-123", parsed.Spec.Owner)
	}

	if len(parsed.Spec.Graphs) != 1 {
		t.Fatalf("want exactly one graph, got %d", len(parsed.Spec.Graphs))
	}
	graph := parsed.Spec.Graphs[0]
	// The CRD requires a graph id with minLength 1. Without it the API
	// server rejects the object, so a rendered graph without one fails every
	// Tier C dashboard scenario at apply time.
	if graph.ID == "" {
		t.Error("want a non-empty spec.graphs[0].id, which the CRD requires")
	}
	if graph.Name != "timeseries" {
		t.Errorf("want graph name %q, got %q", "timeseries", graph.Name)
	}
	if got := graph.Visualization["type"]; got != "timeseries" {
		t.Errorf("want graph visualization type %q, got %v", "timeseries", got)
	}

	assertRunTag(t, parsed.Spec.Tags, runID)
}

// TestRenderDashboardSatisfiesEveryFieldTheCRDRequires reads the required
// lists straight out of the installed CRD rather than restating them, so a
// field added to the CRD becomes a failure here rather than a Tier C apply
// error 90 minutes into a run.
func TestRenderDashboardSatisfiesEveryFieldTheCRDRequires(t *testing.T) {
	previousRunID := runID
	runID = "e2e-testrunid"
	defer func() { runID = previousRunID }()

	specRequired, graphRequired, graphProperties := dashboardCRDContract(t)

	manifest, err := renderDashboard("checkout-viz-00", "e2e-tier-c", "team-123", "timeseries")
	if err != nil {
		t.Fatalf("rendering the dashboard: %v", err)
	}
	var parsed struct {
		Spec map[string]any `json:"spec"`
	}
	if err := yaml.Unmarshal([]byte(manifest), &parsed); err != nil {
		t.Fatalf("manifest is not valid YAML: %v\n%s", err, manifest)
	}

	for _, field := range specRequired {
		value, ok := parsed.Spec[field]
		if !ok {
			t.Errorf("spec.%s is required by the CRD but the manifest omits it", field)
			continue
		}
		if text, isString := value.(string); isString && text == "" {
			t.Errorf("spec.%s is required with minLength 1 but is empty", field)
		}
	}

	graphs, ok := parsed.Spec["graphs"].([]any)
	if !ok || len(graphs) == 0 {
		t.Fatalf("want at least one graph (the CRD sets minItems 1), got %v", parsed.Spec["graphs"])
	}
	graph, ok := graphs[0].(map[string]any)
	if !ok {
		t.Fatalf("want the graph to be an object, got %T", graphs[0])
	}
	for _, field := range graphRequired {
		value, ok := graph[field]
		if !ok {
			t.Errorf("spec.graphs[0].%s is required by the CRD but the manifest omits it", field)
			continue
		}
		if text, isString := value.(string); isString && text == "" {
			t.Errorf("spec.graphs[0].%s is required with minLength 1 but is empty", field)
		}
	}
	// A key the CRD does not declare is pruned silently by the API server,
	// which is how "title" survived review while breaking every apply.
	for field := range graph {
		if !graphProperties[field] {
			t.Errorf("spec.graphs[0].%s is not a property the CRD declares; "+
				"the API server prunes it and the object then fails validation", field)
		}
	}
}

// dashboardCRDContract returns the Dashboard CRD's required spec fields, its
// required graph fields, and the set of graph properties it declares.
func dashboardCRDContract(t *testing.T) (specRequired, graphRequired []string, graphProperties map[string]bool) {
	t.Helper()
	root, err := repoRoot()
	if err != nil {
		t.Fatalf("resolving the repo root: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(root,
		"config/crd/bases/observability.tsuga.com_dashboards.yaml"))
	if err != nil {
		t.Fatalf("reading the Dashboard CRD: %v", err)
	}
	var crd struct {
		Spec struct {
			Versions []struct {
				Schema struct {
					OpenAPIV3Schema struct {
						Properties struct {
							Spec struct {
								Required   []string `json:"required"`
								Properties struct {
									Graphs struct {
										Items struct {
											Required   []string                  `json:"required"`
											Properties map[string]map[string]any `json:"properties"`
										} `json:"items"`
									} `json:"graphs"`
								} `json:"properties"`
							} `json:"spec"`
						} `json:"properties"`
					} `json:"openAPIV3Schema"`
				} `json:"schema"`
			} `json:"versions"`
		} `json:"spec"`
	}
	if err := yaml.Unmarshal(raw, &crd); err != nil {
		t.Fatalf("parsing the Dashboard CRD: %v", err)
	}
	if len(crd.Spec.Versions) == 0 {
		t.Fatal("the Dashboard CRD declares no versions")
	}
	schema := crd.Spec.Versions[0].Schema.OpenAPIV3Schema.Properties.Spec
	graph := schema.Properties.Graphs.Items
	if len(schema.Required) == 0 || len(graph.Required) == 0 || len(graph.Properties) == 0 {
		t.Fatalf("the CRD contract read back empty (spec required %v, graph required %v); "+
			"the parsing above no longer matches the generated CRD",
			schema.Required, graph.Required)
	}
	properties := make(map[string]bool, len(graph.Properties))
	for name := range graph.Properties {
		properties[name] = true
	}
	return schema.Required, graph.Required, properties
}

type renderedMonitor struct {
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`
	Metadata   struct {
		Name      string `json:"name"`
		Namespace string `json:"namespace"`
	} `json:"metadata"`
	Spec struct {
		Name         string `json:"name"`
		Owner        string `json:"owner"`
		Priority     int    `json:"priority"`
		Permissions  string `json:"permissions"`
		DashboardID  string `json:"dashboardId"`
		DashboardRef *struct {
			Name string `json:"name"`
		} `json:"dashboardRef"`
		Tags []struct {
			Key   string `json:"key"`
			Value string `json:"value"`
		} `json:"tags"`
		Configuration map[string]any `json:"configuration"`
	} `json:"spec"`
}

// assertRunTag checks the e2e-run tag, which records which run produced a
// resource when someone is looking at it in the Tsuga UI.
//
// The tag is NOT what either sweep matches on. Both sweepTsugaResources
// (cluster.go) and `make e2e-sweep` (sweep/main.go) select resources by
// NAME, via strings.Contains(item.Name, runID) over a `<kind> list -o json`
// response that carries id and name and no tags. Every name this file
// renders is prefixed with the run id for exactly that reason.
func assertRunTag(t *testing.T, tags []struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}, wantRunID string) {
	t.Helper()
	for _, tag := range tags {
		if tag.Key == "e2e-run" {
			if tag.Value != wantRunID {
				t.Errorf("want e2e-run tag value %q, got %q", wantRunID, tag.Value)
			}
			return
		}
	}
	t.Fatal("want an e2e-run tag recording the run that created this resource, found none")
}

func TestRenderMonitorProducesValidManifest(t *testing.T) {
	previousRunID := runID
	runID = "e2e-testrunid"
	defer func() { runID = previousRunID }()

	manifest, err := renderMonitor("checkout-mon-00", "e2e-tier-c", "team-123", "metric", 4, "owning-team-only")
	if err != nil {
		t.Fatalf("rendering the monitor: %v", err)
	}

	var parsed renderedMonitor
	if err := yaml.Unmarshal([]byte(manifest), &parsed); err != nil {
		t.Fatalf("manifest is not valid YAML: %v\n%s", err, manifest)
	}

	if parsed.APIVersion != "observability.tsuga.com/v1alpha1" {
		t.Errorf("want apiVersion %q, got %q", "observability.tsuga.com/v1alpha1", parsed.APIVersion)
	}
	if parsed.Kind != "Monitor" {
		t.Errorf("want kind %q, got %q", "Monitor", parsed.Kind)
	}
	if parsed.Metadata.Name != "checkout-mon-00" {
		t.Errorf("want metadata.name %q, got %q", "checkout-mon-00", parsed.Metadata.Name)
	}
	if parsed.Metadata.Namespace != "e2e-tier-c" {
		t.Errorf("want metadata.namespace %q, got %q", "e2e-tier-c", parsed.Metadata.Namespace)
	}
	if parsed.Spec.Owner != "team-123" {
		t.Errorf("want spec.owner %q, got %q", "team-123", parsed.Spec.Owner)
	}
	if parsed.Spec.Priority != 4 {
		t.Errorf("want spec.priority %d, got %d", 4, parsed.Spec.Priority)
	}
	if parsed.Spec.Permissions != "owning-team-only" {
		t.Errorf("want spec.permissions %q, got %q", "owning-team-only", parsed.Spec.Permissions)
	}
	if got := parsed.Spec.Configuration["type"]; got != "metric" {
		t.Errorf("want spec.configuration.type %q, got %v", "metric", got)
	}
	// A bare {type} is rejected by the API for every monitor variant; the
	// metric variant's schema requires these alongside it.
	for _, field := range []string{
		"conditions", "noDataBehavior", "timeframe", "groupByFields", "queries",
	} {
		if _, ok := parsed.Spec.Configuration[field]; !ok {
			t.Errorf("want spec.configuration.%s, which the metric variant requires", field)
		}
	}

	assertRunTag(t, parsed.Spec.Tags, runID)
}

func TestRenderMonitorWithDashboardRefResolvesByNameNotID(t *testing.T) {
	previousRunID := runID
	runID = "e2e-testrunid"
	defer func() { runID = previousRunID }()

	manifest, err := renderMonitorWithDashboardRef("ref-mon-00", "e2e-tier-c", "team-123", "ref-dash-target")
	if err != nil {
		t.Fatalf("rendering the monitor: %v", err)
	}

	var parsed renderedMonitor
	if err := yaml.Unmarshal([]byte(manifest), &parsed); err != nil {
		t.Fatalf("manifest is not valid YAML: %v\n%s", err, manifest)
	}

	if parsed.APIVersion != "observability.tsuga.com/v1alpha1" {
		t.Errorf("want apiVersion %q, got %q", "observability.tsuga.com/v1alpha1", parsed.APIVersion)
	}
	if parsed.Kind != "Monitor" {
		t.Errorf("want kind %q, got %q", "Monitor", parsed.Kind)
	}
	if parsed.Metadata.Name != "ref-mon-00" {
		t.Errorf("want metadata.name %q, got %q", "ref-mon-00", parsed.Metadata.Name)
	}
	if parsed.Metadata.Namespace != "e2e-tier-c" {
		t.Errorf("want metadata.namespace %q, got %q", "e2e-tier-c", parsed.Metadata.Namespace)
	}
	if parsed.Spec.Owner != "team-123" {
		t.Errorf("want spec.owner %q, got %q", "team-123", parsed.Spec.Owner)
	}

	if parsed.Spec.DashboardRef == nil {
		t.Fatal("want spec.dashboardRef to be set")
	}
	if parsed.Spec.DashboardRef.Name != "ref-dash-target" {
		t.Errorf("want spec.dashboardRef.name %q, got %q", "ref-dash-target", parsed.Spec.DashboardRef.Name)
	}
	if parsed.Spec.DashboardID != "" {
		t.Errorf("want no literal spec.dashboardId, since the operator must resolve the ref; got %q",
			parsed.Spec.DashboardID)
	}

	assertRunTag(t, parsed.Spec.Tags, runID)
}

type renderedCollectorConfig struct {
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`
	Metadata   struct {
		Name string `json:"name"`
	} `json:"metadata"`
	Spec struct {
		ClusterName        string `json:"clusterName"`
		CollectorNamespace string `json:"collectorNamespace"`
		Image              string `json:"image"`
		Export             struct {
			TokenSecretRef struct {
				Name string `json:"name"`
				Key  string `json:"key"`
			} `json:"tokenSecretRef"`
			Endpoint          string `json:"endpoint"`
			EndpointSecretRef *struct {
				Name string `json:"name"`
				Key  string `json:"key"`
			} `json:"endpointSecretRef"`
		} `json:"export"`
		Agent struct {
			Traces  bool `json:"traces"`
			Metrics bool `json:"metrics"`
			Logs    bool `json:"logs"`
		} `json:"agent"`
		Gateway struct {
			ClusterMetrics    bool `json:"clusterMetrics"`
			KubernetesObjects bool `json:"kubernetesObjects"`
		} `json:"gateway"`
		Resources *struct {
			Limits struct {
				CPU    string `json:"cpu"`
				Memory string `json:"memory"`
			} `json:"limits"`
			Requests struct {
				CPU    string `json:"cpu"`
				Memory string `json:"memory"`
			} `json:"requests"`
		} `json:"resources"`
	} `json:"spec"`
}

func parseCollectorConfig(t *testing.T, manifest string) renderedCollectorConfig {
	t.Helper()
	var parsed renderedCollectorConfig
	if err := yaml.Unmarshal([]byte(manifest), &parsed); err != nil {
		t.Fatalf("manifest is not valid YAML: %v\n%s", err, manifest)
	}
	if parsed.Kind != "TsugaCollectorConfig" {
		t.Errorf("want kind %q, got %q", "TsugaCollectorConfig", parsed.Kind)
	}
	return parsed
}

// TestRenderCollectorConfigIsNamedCluster is the only thing standing between
// a per-run collector-config name and a silently broken Tier B.
//
// TsugaMonitoringReconciler resolves the cluster config by the fixed name
// "cluster" (r.Get(ctx, types.NamespacedName{Name: "cluster"}, cluster)), so
// any other name leaves every TsugaMonitoring in Error with a NotFound and
// every Tier B spec failing on its wait for Ready. Tier A would keep passing
// throughout, since it reads its config back by whatever name it wrote -
// which is exactly what makes the breakage quiet.
//
// The literal is spelled out here rather than compared against
// collectorConfigName on purpose: a test that reads the same constant the
// renderer does would stay green if someone changed the constant, which is
// the failure this guards.
func TestRenderCollectorConfigIsNamedCluster(t *testing.T) {
	for _, variant := range []string{
		"", "endpointSecretRef", "customNamespace", "imageOverride", "resourcesOverride",
	} {
		scenario := CollectorScenario{
			Name:        "s00",
			ClusterName: "e2e-testrunid-s00",
			Agent:       AgentToggles{Traces: true, Metrics: true, Logs: true},
			Gateway:     GatewayToggles{ClusterMetrics: true, KubernetesObjects: true},
			Variant:     variant,
		}
		manifest := renderCollectorConfig(scenario, "otlp.example.com:4317", "tsuga-operator-system")
		parsed := parseCollectorConfig(t, manifest)

		if parsed.Metadata.Name != "cluster" {
			t.Errorf("variant %q: want metadata.name %q, which TsugaMonitoringReconciler "+
				"looks the cluster config up by; got %q", variant, "cluster", parsed.Metadata.Name)
		}
	}
}

func TestRenderCollectorConfigPlainScenario(t *testing.T) {
	scenario := CollectorScenario{
		Name:        "s00-agent[tml]-gateway[co]",
		ClusterName: "e2e-testrunid-s00",
		Agent:       AgentToggles{Traces: true, Metrics: true, Logs: true},
		Gateway:     GatewayToggles{ClusterMetrics: true, KubernetesObjects: true},
	}

	manifest := renderCollectorConfig(scenario, "otlp.example.com:4317", "tsuga-operator-system")
	parsed := parseCollectorConfig(t, manifest)

	if parsed.Spec.ClusterName != scenario.ClusterName {
		t.Errorf("want spec.clusterName %q, got %q", scenario.ClusterName, parsed.Spec.ClusterName)
	}
	if parsed.Spec.CollectorNamespace != "tsuga-operator-system" {
		t.Errorf("want spec.collectorNamespace %q, got %q",
			"tsuga-operator-system", parsed.Spec.CollectorNamespace)
	}
	if parsed.Spec.Export.Endpoint != "otlp.example.com:4317" {
		t.Errorf("want a literal spec.export.endpoint %q, got %q",
			"otlp.example.com:4317", parsed.Spec.Export.Endpoint)
	}
	if parsed.Spec.Export.EndpointSecretRef != nil {
		t.Error("want no spec.export.endpointSecretRef for the plain case")
	}

	if !parsed.Spec.Agent.Traces || !parsed.Spec.Agent.Metrics || !parsed.Spec.Agent.Logs {
		t.Errorf("want all agent toggles true, got %+v", parsed.Spec.Agent)
	}
	if !parsed.Spec.Gateway.ClusterMetrics || !parsed.Spec.Gateway.KubernetesObjects {
		t.Errorf("want all gateway toggles true, got %+v", parsed.Spec.Gateway)
	}
}

func TestRenderCollectorConfigEndpointSecretRefVariant(t *testing.T) {
	scenario := CollectorScenario{
		Name:        "s32-endpointSecretRef",
		ClusterName: "e2e-testrunid-s32",
		Agent:       AgentToggles{Traces: true, Metrics: true, Logs: true},
		Gateway:     GatewayToggles{ClusterMetrics: true, KubernetesObjects: true},
		Variant:     "endpointSecretRef",
	}

	manifest := renderCollectorConfig(scenario, "otlp.example.com:4317", "tsuga-operator-system")
	parsed := parseCollectorConfig(t, manifest)

	if parsed.Spec.Export.EndpointSecretRef == nil {
		t.Fatal("want spec.export.endpointSecretRef to be set")
	}
	if parsed.Spec.Export.EndpointSecretRef.Name != "tsuga-endpoint" {
		t.Errorf("want endpointSecretRef.name %q, got %q",
			"tsuga-endpoint", parsed.Spec.Export.EndpointSecretRef.Name)
	}
	if parsed.Spec.Export.Endpoint != "" {
		t.Errorf("want no literal spec.export.endpoint when using a secret ref, got %q",
			parsed.Spec.Export.Endpoint)
	}
}

func TestRenderCollectorConfigImageOverrideVariant(t *testing.T) {
	scenario := CollectorScenario{
		Name:        "s34-imageOverride",
		ClusterName: "e2e-testrunid-s34",
		Agent:       AgentToggles{Traces: true, Metrics: true, Logs: true},
		Gateway:     GatewayToggles{ClusterMetrics: true, KubernetesObjects: true},
		Variant:     "imageOverride",
	}

	manifest := renderCollectorConfig(scenario, "otlp.example.com:4317", "tsuga-operator-system")
	parsed := parseCollectorConfig(t, manifest)

	if !strings.Contains(parsed.Spec.Image, "0.157.0") {
		t.Errorf("want spec.image to carry tag %q, got %q", "0.157.0", parsed.Spec.Image)
	}
}

func TestRenderCollectorConfigResourcesOverrideVariant(t *testing.T) {
	scenario := CollectorScenario{
		Name:        "s35-resourcesOverride",
		ClusterName: "e2e-testrunid-s35",
		Agent:       AgentToggles{Traces: true, Metrics: true, Logs: true},
		Gateway:     GatewayToggles{ClusterMetrics: true, KubernetesObjects: true},
		Variant:     "resourcesOverride",
	}

	manifest := renderCollectorConfig(scenario, "otlp.example.com:4317", "tsuga-operator-system")
	parsed := parseCollectorConfig(t, manifest)

	if parsed.Spec.Resources == nil {
		t.Fatal("want spec.resources to be set")
	}
	if parsed.Spec.Resources.Limits.CPU != "1" {
		t.Errorf("want spec.resources.limits.cpu %q, got %q", "1", parsed.Spec.Resources.Limits.CPU)
	}
	if parsed.Spec.Resources.Limits.Memory != "1Gi" {
		t.Errorf("want spec.resources.limits.memory %q, got %q",
			"1Gi", parsed.Spec.Resources.Limits.Memory)
	}
}

func TestRenderCollectorConfigTogglesRoundTripMixedValues(t *testing.T) {
	scenario := CollectorScenario{
		Name:        "s13-agent[t-l]-gateway[-o]",
		ClusterName: "e2e-testrunid-s13",
		Agent:       AgentToggles{Traces: true, Metrics: false, Logs: true},
		Gateway:     GatewayToggles{ClusterMetrics: false, KubernetesObjects: true},
	}

	manifest := renderCollectorConfig(scenario, "otlp.example.com:4317", "tsuga-operator-system")
	parsed := parseCollectorConfig(t, manifest)

	if !parsed.Spec.Agent.Traces {
		t.Error("want spec.agent.traces true")
	}
	if parsed.Spec.Agent.Metrics {
		t.Error("want spec.agent.metrics false")
	}
	if !parsed.Spec.Agent.Logs {
		t.Error("want spec.agent.logs true")
	}
	if parsed.Spec.Gateway.ClusterMetrics {
		t.Error("want spec.gateway.clusterMetrics false")
	}
	if !parsed.Spec.Gateway.KubernetesObjects {
		t.Error("want spec.gateway.kubernetesObjects true")
	}
}

type renderedEmitterDeployment struct {
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`
	Metadata   struct {
		Name      string `json:"name"`
		Namespace string `json:"namespace"`
	} `json:"metadata"`
	Spec struct {
		Template struct {
			Spec struct {
				Containers []struct {
					Image           string `json:"image"`
					ImagePullPolicy string `json:"imagePullPolicy"`
					Env             []struct {
						Name  string `json:"name"`
						Value string `json:"value"`
					} `json:"env"`
				} `json:"containers"`
			} `json:"spec"`
		} `json:"template"`
	} `json:"spec"`
}

func TestRenderEmitterWorkloadProducesValidManifest(t *testing.T) {
	manifest := renderEmitterWorkload("e2e-tier-a", "e2e-testrunid-s00", "tsuga-operator-system")

	var parsed renderedEmitterDeployment
	if err := yaml.Unmarshal([]byte(manifest), &parsed); err != nil {
		t.Fatalf("manifest is not valid YAML: %v\n%s", err, manifest)
	}

	if parsed.Kind != "Deployment" {
		t.Errorf("want kind %q, got %q", "Deployment", parsed.Kind)
	}
	if parsed.Metadata.Namespace != "e2e-tier-a" {
		t.Errorf("want metadata.namespace %q, got %q", "e2e-tier-a", parsed.Metadata.Namespace)
	}

	if len(parsed.Spec.Template.Spec.Containers) != 1 {
		t.Fatalf("want exactly one container, got %d", len(parsed.Spec.Template.Spec.Containers))
	}
	container := parsed.Spec.Template.Spec.Containers[0]
	if container.Image != "tsuga-e2e/otlp-emitter:latest" {
		t.Errorf("want image %q, got %q", "tsuga-e2e/otlp-emitter:latest", container.Image)
	}
	if container.ImagePullPolicy != "Never" {
		t.Errorf("want imagePullPolicy %q, got %q", "Never", container.ImagePullPolicy)
	}

	var sawEndpoint bool
	for _, env := range container.Env {
		if env.Name == "OTEL_EXPORTER_OTLP_ENDPOINT" {
			sawEndpoint = true
			if !strings.Contains(env.Value, "tsuga-agent-collector.tsuga-operator-system") {
				t.Errorf("want OTEL_EXPORTER_OTLP_ENDPOINT pointed at the collector namespace, got %q",
					env.Value)
			}
		}
	}
	if !sawEndpoint {
		t.Fatal("want an OTEL_EXPORTER_OTLP_ENDPOINT env var, found none")
	}
}

type renderedMonitoring struct {
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`
	Metadata   struct {
		Name      string `json:"name"`
		Namespace string `json:"namespace"`
	} `json:"metadata"`
	Spec struct {
		InjectExistingWorkloads bool `json:"injectExistingWorkloads"`
		Instrumentation         struct {
			Enabled   bool     `json:"enabled"`
			Languages []string `json:"languages"`
		} `json:"instrumentation"`
	} `json:"spec"`
}

func parseMonitoring(t *testing.T, manifest string) renderedMonitoring {
	t.Helper()
	var parsed renderedMonitoring
	if err := yaml.Unmarshal([]byte(manifest), &parsed); err != nil {
		t.Fatalf("manifest is not valid YAML: %v\n%s", err, manifest)
	}
	if parsed.Kind != "TsugaMonitoring" {
		t.Errorf("want kind %q, got %q", "TsugaMonitoring", parsed.Kind)
	}
	return parsed
}

// hasKey unmarshals manifest into a generic map and reports whether the key
// exists anywhere on the path, distinguishing "absent" from "present but
// empty" - something a struct field alone cannot do, since an empty YAML
// list and a missing key both unmarshal to a nil Go slice.
func hasKey(t *testing.T, manifest string, path ...string) bool {
	t.Helper()
	var node map[string]interface{}
	if err := yaml.Unmarshal([]byte(manifest), &node); err != nil {
		t.Fatalf("manifest is not valid YAML: %v\n%s", err, manifest)
	}
	var current interface{} = node
	for _, key := range path {
		m, ok := current.(map[string]interface{})
		if !ok {
			return false
		}
		current, ok = m[key]
		if !ok {
			return false
		}
	}
	return true
}

func TestRenderMonitoringProducesValidManifest(t *testing.T) {
	manifest := renderMonitoring("e2e-inject-nodejs", true, []string{"nodejs", "java"}, true)
	parsed := parseMonitoring(t, manifest)

	if parsed.APIVersion != "observability.tsuga.com/v1alpha1" {
		t.Errorf("want apiVersion %q, got %q", "observability.tsuga.com/v1alpha1", parsed.APIVersion)
	}
	if parsed.Metadata.Namespace != "e2e-inject-nodejs" {
		t.Errorf("want metadata.namespace %q, got %q", "e2e-inject-nodejs", parsed.Metadata.Namespace)
	}
	if !parsed.Spec.InjectExistingWorkloads {
		t.Error("want spec.injectExistingWorkloads true")
	}
	if !parsed.Spec.Instrumentation.Enabled {
		t.Error("want spec.instrumentation.enabled true")
	}
	if !reflect.DeepEqual(parsed.Spec.Instrumentation.Languages, []string{"nodejs", "java"}) {
		t.Errorf("want spec.instrumentation.languages %v, got %v",
			[]string{"nodejs", "java"}, parsed.Spec.Instrumentation.Languages)
	}
}

func TestRenderMonitoringDisabledInstrumentation(t *testing.T) {
	manifest := renderMonitoring("e2e-inject-off", false, nil, true)
	parsed := parseMonitoring(t, manifest)

	if parsed.Spec.Instrumentation.Enabled {
		t.Error("want spec.instrumentation.enabled false")
	}
}

func TestRenderMonitoringInjectExistingWorkloadsFalse(t *testing.T) {
	manifest := renderMonitoring("e2e-inject-optin", true, []string{"nodejs"}, false)
	parsed := parseMonitoring(t, manifest)

	if parsed.Spec.InjectExistingWorkloads {
		t.Error("want spec.injectExistingWorkloads false")
	}
}

func TestRenderMonitoringOmitsLanguagesKeyWhenEmpty(t *testing.T) {
	manifest := renderMonitoring("e2e-inject-off", false, nil, true)

	if hasKey(t, manifest, "spec", "instrumentation", "languages") {
		t.Error("want no spec.instrumentation.languages key when languages is empty, " +
			"an empty list and an absent key are different things to the CRD")
	}
	if strings.Contains(manifest, "languages:") {
		t.Errorf("want no literal \"languages:\" in the manifest when languages is empty:\n%s", manifest)
	}
}

type renderedLanguageWorkload struct {
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`
	Metadata   struct {
		Name      string `json:"name"`
		Namespace string `json:"namespace"`
	} `json:"metadata"`
	Spec struct {
		Selector struct {
			MatchLabels map[string]string `json:"matchLabels"`
		} `json:"selector"`
		Template struct {
			Metadata struct {
				Labels      map[string]string `json:"labels"`
				Annotations map[string]string `json:"annotations"`
			} `json:"metadata"`
			Spec struct {
				Containers []struct {
					Image           string `json:"image"`
					ImagePullPolicy string `json:"imagePullPolicy"`
				} `json:"containers"`
			} `json:"spec"`
		} `json:"template"`
	} `json:"spec"`
}

func parseLanguageWorkload(t *testing.T, manifest string) renderedLanguageWorkload {
	t.Helper()
	var parsed renderedLanguageWorkload
	if err := yaml.Unmarshal([]byte(manifest), &parsed); err != nil {
		t.Fatalf("manifest is not valid YAML: %v\n%s", err, manifest)
	}
	if parsed.Kind != "Deployment" {
		t.Errorf("want kind %q, got %q", "Deployment", parsed.Kind)
	}
	return parsed
}

func TestRenderLanguageWorkloadProducesValidManifest(t *testing.T) {
	manifest := renderLanguageWorkload("e2e-inject-nodejs", "nodejs", false)
	parsed := parseLanguageWorkload(t, manifest)

	if parsed.Metadata.Namespace != "e2e-inject-nodejs" {
		t.Errorf("want metadata.namespace %q, got %q", "e2e-inject-nodejs", parsed.Metadata.Namespace)
	}
	if parsed.Metadata.Name != "app-nodejs" {
		t.Errorf("want metadata.name %q, got %q", "app-nodejs", parsed.Metadata.Name)
	}

	if len(parsed.Spec.Template.Spec.Containers) != 1 {
		t.Fatalf("want exactly one container, got %d", len(parsed.Spec.Template.Spec.Containers))
	}
	container := parsed.Spec.Template.Spec.Containers[0]
	if container.Image != "tsuga-e2e/app-nodejs:latest" {
		t.Errorf("want image %q, got %q", "tsuga-e2e/app-nodejs:latest", container.Image)
	}
	if container.ImagePullPolicy != "Never" {
		t.Errorf("want imagePullPolicy %q, got %q (images are side-loaded into kind, never pulled)",
			"Never", container.ImagePullPolicy)
	}
}

func TestRenderLanguageWorkloadOptInAnnotationOnlyWhenRequested(t *testing.T) {
	withOptIn := renderLanguageWorkload("e2e-inject-optin", "nodejs", true)
	parsedWithOptIn := parseLanguageWorkload(t, withOptIn)
	optInAnnotations := parsedWithOptIn.Spec.Template.Metadata.Annotations
	if optInAnnotations["instrumentation.opentelemetry.io/inject-nodejs"] != "e2e-monitoring" {
		t.Errorf("want the opt-in annotation instrumentation.opentelemetry.io/inject-nodejs=e2e-monitoring "+
			"when optInLabel is true, got annotations %v",
			parsedWithOptIn.Spec.Template.Metadata.Annotations)
	}

	withoutOptIn := renderLanguageWorkload("e2e-inject-optin", "nodejs", false)
	parsedWithoutOptIn := parseLanguageWorkload(t, withoutOptIn)
	if len(parsedWithoutOptIn.Spec.Template.Metadata.Annotations) != 0 {
		t.Errorf("want no pod template annotations when optInLabel is false, got %v",
			parsedWithoutOptIn.Spec.Template.Metadata.Annotations)
	}
	if strings.Contains(withoutOptIn, "instrumentation.opentelemetry.io/inject-") {
		t.Errorf("want no opt-in annotation in the manifest when optInLabel is false:\n%s", withoutOptIn)
	}
}
