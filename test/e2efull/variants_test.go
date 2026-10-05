package e2efull

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const specPath = "../../public-open-api.json"

func TestMonitorConfigurationVariants(t *testing.T) {
	got, err := MonitorConfigurationVariants(specPath)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"metric", "log", "trace", "anomaly-log", "anomaly-metric",
		"anomaly-trace", "log-error-pattern", "certificate-expiry",
	}
	if len(got) != len(want) {
		t.Fatalf("want %d monitor configurations, got %d: %v", len(want), len(got), got)
	}
	index := map[string]bool{}
	for _, name := range got {
		index[name] = true
	}
	for _, name := range want {
		if !index[name] {
			t.Errorf("missing monitor configuration variant %q, got: %v", name, got)
		}
	}
}

func TestVisualizationVariantsNonEmptyAndIncludesNewTypes(t *testing.T) {
	got, err := VisualizationVariants(specPath)
	if err != nil {
		t.Fatal(err)
	}

	// Minimum set: all 26 known visualization variants must be present.
	// This allows legitimate additions to the API while catching wrong or missing strings.
	requiredVariants := []string{
		"bar", "bar-connection", "bar-promql", "distribution", "gauge", "heatmap",
		"list", "list-connection", "list-log-patterns", "list-spans", "note", "pie",
		"pie-connection", "pie-promql", "query-value", "query-value-connection",
		"query-value-promql", "slo-over-time", "slo-uptime", "table", "timeseries",
		"timeseries-connection", "timeseries-promql", "top-list", "top-list-connection",
		"top-list-promql",
	}

	if len(got) < 26 {
		t.Fatalf("want at least 26 visualization variants, got %d: %v", len(got), got)
	}

	index := make(map[string]bool)
	for _, name := range got {
		index[name] = true
	}

	var missing []string
	for _, name := range requiredVariants {
		if !index[name] {
			missing = append(missing, name)
		}
	}

	if len(missing) > 0 {
		t.Errorf("missing visualization variants: %v", missing)
	}
}

func TestVariantsFailLoudlyOnMissingSpec(t *testing.T) {
	if _, err := MonitorConfigurationVariants("does-not-exist.json"); err == nil {
		t.Fatal("a missing spec must be an error, not an empty variant list")
	}
}

func TestMonitorVariantsErrorOnMissingPath(t *testing.T) {
	// Spec that parses but has no /v1/monitors path at all.
	spec := map[string]any{
		"paths": map[string]any{},
	}
	raw, _ := json.Marshal(spec)
	dir := t.TempDir()
	specPath := filepath.Join(dir, "spec.json")
	if err := os.WriteFile(specPath, raw, 0644); err != nil {
		t.Fatal(err)
	}

	_, err := MonitorConfigurationVariants(specPath)
	if err == nil {
		t.Fatal("want error for missing /v1/monitors path, got nil")
	}
}

func TestMonitorVariantsErrorOnMissingDiscriminator(t *testing.T) {
	// Spec where configuration exists but has no discriminator field.
	spec := map[string]any{
		"paths": map[string]any{
			"/v1/monitors": map[string]any{
				"post": map[string]any{
					"requestBody": map[string]any{
						"content": map[string]any{
							"application/json": map[string]any{
								"schema": map[string]any{
									"properties": map[string]any{
										"configuration": map[string]any{},
									},
								},
							},
						},
					},
				},
			},
		},
	}
	raw, _ := json.Marshal(spec)
	dir := t.TempDir()
	specPath := filepath.Join(dir, "spec.json")
	if err := os.WriteFile(specPath, raw, 0644); err != nil {
		t.Fatal(err)
	}

	_, err := MonitorConfigurationVariants(specPath)
	if err == nil {
		t.Fatal("want error for missing discriminator, got nil")
	}
}

func TestMonitorVariantsErrorOnEmptyMapping(t *testing.T) {
	// Spec where discriminator.mapping exists but is empty {}.
	spec := map[string]any{
		"paths": map[string]any{
			"/v1/monitors": map[string]any{
				"post": map[string]any{
					"requestBody": map[string]any{
						"content": map[string]any{
							"application/json": map[string]any{
								"schema": map[string]any{
									"properties": map[string]any{
										"configuration": map[string]any{
											"discriminator": map[string]any{
												"mapping": map[string]any{},
											},
										},
									},
								},
							},
						},
					},
				},
			},
		},
	}
	raw, _ := json.Marshal(spec)
	dir := t.TempDir()
	specPath := filepath.Join(dir, "spec.json")
	if err := os.WriteFile(specPath, raw, 0644); err != nil {
		t.Fatal(err)
	}

	_, err := MonitorConfigurationVariants(specPath)
	if err == nil {
		t.Fatal("want error for empty mapping, got nil")
	}
}

func TestMonitorVariantsErrorOnNonObjectMapping(t *testing.T) {
	// Spec where mapping is not an object (string instead).
	spec := map[string]any{
		"paths": map[string]any{
			"/v1/monitors": map[string]any{
				"post": map[string]any{
					"requestBody": map[string]any{
						"content": map[string]any{
							"application/json": map[string]any{
								"schema": map[string]any{
									"properties": map[string]any{
										"configuration": map[string]any{
											"discriminator": map[string]any{
												"mapping": "not an object",
											},
										},
									},
								},
							},
						},
					},
				},
			},
		},
	}
	raw, _ := json.Marshal(spec)
	dir := t.TempDir()
	specPath := filepath.Join(dir, "spec.json")
	if err := os.WriteFile(specPath, raw, 0644); err != nil {
		t.Fatal(err)
	}

	_, err := MonitorConfigurationVariants(specPath)
	if err == nil {
		t.Fatal("want error for non-object mapping, got nil")
	}
}

// requiredFields reads a variant's required list straight from the spec, so
// the tests below compare generated bodies against the contract rather than
// against a list restated here that could drift from it.
func requiredFields(t *testing.T, path string, keys []string, variant string) []string {
	t.Helper()
	spec, err := loadSpec(specPath)
	if err != nil {
		t.Fatalf("loading the spec: %v", err)
	}
	mapping, err := variantMapping(specPath, path, keys)
	if err != nil {
		t.Fatalf("reading the discriminator mapping: %v", err)
	}
	ref, ok := mapping[variant].(string)
	if !ok {
		t.Fatalf("no variant %q in %s", variant, path)
	}
	schema, err := resolveRef(spec, map[string]any{"$ref": ref})
	if err != nil {
		t.Fatalf("resolving %s: %v", ref, err)
	}
	raw, _ := schema["required"].([]any)
	fields := make([]string, 0, len(raw))
	for _, name := range raw {
		fields = append(fields, name.(string))
	}
	if len(fields) == 0 {
		t.Fatalf("variant %q declares no required fields; the test would prove nothing", variant)
	}
	return fields
}

func assertBodyCarriesRequiredFields(t *testing.T, body map[string]any,
	required []string, variant string) {

	t.Helper()
	for _, field := range required {
		value, ok := body[field]
		if !ok {
			t.Errorf("%s: generated body omits required field %q", variant, field)
			continue
		}
		if list, isList := value.([]any); isList && len(list) == 0 {
			t.Errorf("%s: required list %q is empty; a body the API rejects "+
				"reads as a broken operator", variant, field)
		}
	}
	if got := body["type"]; got != variant {
		t.Errorf("%s: want the discriminator type %q, got %v", variant, variant, got)
	}
}

func TestMinimalVisualizationCarriesEachVariantsRequiredFields(t *testing.T) {
	// timeseries needs source and queries, table needs columns (minItems 1),
	// list-connection needs connectionId and a SQL query: three shapes that
	// a bare {type} body fails in three different ways.
	for _, variant := range []string{"timeseries", "table", "list-connection"} {
		body, err := MinimalVisualization(specPath, variant)
		if err != nil {
			t.Fatalf("%s: %v", variant, err)
		}
		assertBodyCarriesRequiredFields(t, body,
			requiredFields(t, "/v1/dashboards", visualizationPath, variant), variant)
	}
}

func TestMinimalMonitorConfigurationCarriesEachVariantsRequiredFields(t *testing.T) {
	// metric is the five-field shape, certificate-expiry has its own
	// single-value enums, log-error-pattern requires a nested filter object.
	for _, variant := range []string{"metric", "certificate-expiry", "log-error-pattern"} {
		body, err := MinimalMonitorConfiguration(specPath, variant)
		if err != nil {
			t.Fatalf("%s: %v", variant, err)
		}
		assertBodyCarriesRequiredFields(t, body,
			requiredFields(t, "/v1/monitors", configurationPath, variant), variant)
	}
}

func TestMinimalLogErrorPatternConfigurationSelectsAService(t *testing.T) {
	body, err := MinimalMonitorConfiguration(specPath, "log-error-pattern")
	if err != nil {
		t.Fatal(err)
	}
	filter, ok := body["filter"].(map[string]any)
	if !ok {
		t.Fatalf("filter is %T, want an object", body["filter"])
	}
	services, ok := filter["services"].([]any)
	if !ok || len(services) == 0 {
		t.Fatalf("filter.services = %v, want a non-empty list", filter["services"])
	}
}

// TestEveryVariantGeneratesABody is the guard that matters when the spec is
// re-synced: a new variant, or a new schema construct the generator does not
// handle, fails here rather than 10 minutes into Tier C.
func TestEveryVariantGeneratesABody(t *testing.T) {
	visualizations, err := VisualizationVariants(specPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, variant := range visualizations {
		body, err := MinimalVisualization(specPath, variant)
		if err != nil {
			t.Errorf("visualization %s: %v", variant, err)
			continue
		}
		assertBodyCarriesRequiredFields(t, body,
			requiredFields(t, "/v1/dashboards", visualizationPath, variant), variant)
	}

	configurations, err := MonitorConfigurationVariants(specPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, variant := range configurations {
		body, err := MinimalMonitorConfiguration(specPath, variant)
		if err != nil {
			t.Errorf("monitor configuration %s: %v", variant, err)
			continue
		}
		assertBodyCarriesRequiredFields(t, body,
			requiredFields(t, "/v1/monitors", configurationPath, variant), variant)
	}
}

func TestMinimalVariantBodyRejectsAnUnknownVariant(t *testing.T) {
	_, err := MinimalVisualization(specPath, "no-such-visualization")
	if err == nil {
		t.Fatal("want an error for a variant the spec does not define, got nil")
	}
	if !strings.Contains(err.Error(), "no-such-visualization") {
		t.Fatalf("want the error to name the variant, got: %v", err)
	}
}

func TestSimplestOptionPicksTheBranchWithFewestRequiredFields(t *testing.T) {
	// The aggregate union: "count" needs only a type, every other branch
	// also needs a field. Picking the smallest branch is what keeps a
	// generated query body minimal.
	body, err := MinimalMonitorConfiguration(specPath, "metric")
	if err != nil {
		t.Fatal(err)
	}
	queries, ok := body["queries"].([]any)
	if !ok || len(queries) == 0 {
		t.Fatalf("want at least one generated query, got %v", body["queries"])
	}
	query, ok := queries[0].(map[string]any)
	if !ok {
		t.Fatalf("want the query to be an object, got %T", queries[0])
	}
	aggregate, ok := query["aggregate"].(map[string]any)
	if !ok {
		t.Fatalf("want a generated aggregate object, got %v", query["aggregate"])
	}
	if aggregate["type"] != "count" {
		t.Errorf("want the count aggregate, the only branch needing no field, got %v",
			aggregate["type"])
	}
}

// TestMinimalBodyHonoursNumericBounds pins a constraint the API enforces but
// the "required" list does not express. anomaly-log's timeframe declares
// minimum 5; a body carrying timeframe 1 is schema-complete and still earns a
// 400 ("body/configuration/timeframe must be >= 5") from the live API.
func TestMinimalBodyHonoursNumericBounds(t *testing.T) {
	body, err := MinimalMonitorConfiguration(specPath, "anomaly-log")
	if err != nil {
		t.Fatal(err)
	}
	raw, ok := body["timeframe"]
	if !ok {
		t.Fatalf("anomaly-log body has no timeframe: %v", body)
	}
	timeframe, ok := raw.(int)
	if !ok {
		t.Fatalf("timeframe should be an integer, got %T (%v)", raw, raw)
	}
	if timeframe < 5 || timeframe > 1440 {
		t.Fatalf("timeframe %d is outside the schema's documented 5..1440", timeframe)
	}
}
