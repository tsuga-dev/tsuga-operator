package e2efull

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
)

// openAPISpecPath is where the suite reads the Tsuga API contract from,
// relative to this package's directory.
const openAPISpecPath = "../../public-open-api.json"

// The paths into the create-request schemas that hold each discriminated
// union. Kept here so the variant listing and the body generator can never
// drift onto different nodes.
var (
	//nolint:goconst // OpenAPI vocabulary
	visualizationPath = []string{"properties", "graphs", "items", "properties", "visualization"}
	configurationPath = []string{"properties", "configuration"}
)

// specCache holds parsed OpenAPI documents. The spec is 2 MB and Tier C
// renders roughly 150 resources from it, so parsing it per render would cost
// more than the HTTP calls the tier is actually testing.
var (
	specMu    sync.Mutex
	specCache = map[string]map[string]any{}
)

func loadSpec(specPath string) (map[string]any, error) {
	specMu.Lock()
	defer specMu.Unlock()
	if cached, ok := specCache[specPath]; ok {
		return cached, nil
	}
	raw, err := os.ReadFile(specPath)
	if err != nil {
		return nil, fmt.Errorf("read OpenAPI spec: %w", err)
	}
	var spec map[string]any
	if err := json.Unmarshal(raw, &spec); err != nil {
		return nil, fmt.Errorf("parse OpenAPI spec: %w", err)
	}
	specCache[specPath] = spec
	return spec, nil
}

// MonitorConfigurationVariants returns the discriminator subtypes accepted by
// POST /v1/monitors, read from the OpenAPI spec so a new monitor type becomes
// a new scenario on the next spec sync.
func MonitorConfigurationVariants(specPath string) ([]string, error) {
	return discriminatorMapping(specPath, "/v1/monitors", configurationPath)
}

// VisualizationVariants returns the graph visualization subtypes accepted by
// POST /v1/dashboards.
func VisualizationVariants(specPath string) ([]string, error) {
	return discriminatorMapping(specPath, "/v1/dashboards", visualizationPath)
}

// discriminatorMapping walks to a node in a create endpoint's request schema
// and returns the keys of its discriminator mapping.
func discriminatorMapping(specPath, path string, keys []string) ([]string, error) {
	mapping, err := variantMapping(specPath, path, keys)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(mapping))
	for name := range mapping {
		names = append(names, name)
	}
	sort.Strings(names)
	if len(names) == 0 {
		return nil, fmt.Errorf("%s: discriminator mapping is empty", path)
	}
	return names, nil
}

// variantMapping returns the discriminator mapping of the union at keys
// inside path's create-request schema.
func variantMapping(specPath, path string, keys []string) (map[string]any, error) {
	spec, err := loadSpec(specPath)
	if err != nil {
		return nil, err
	}
	node, err := descend(spec, spec, append([]string{
		"paths", path, "post", "requestBody", "content", "application/json", "schema",
	}, keys...))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	mapping, err := descend(spec, node, []string{"discriminator", "mapping"})
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return mapping, nil
}

// descend walks a decoded JSON tree, resolving $ref at every step and
// returning a clear error naming the first key that is missing rather than a
// nil map panic.
func descend(spec, node map[string]any, keys []string) (map[string]any, error) {
	current, err := resolveRef(spec, node)
	if err != nil {
		return nil, err
	}
	for i, key := range keys {
		next, ok := current[key]
		if !ok {
			return nil, fmt.Errorf("key %q not found at %v", key, keys[:i])
		}
		asMap, ok := next.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("key %q at %v is not an object", key, keys[:i])
		}
		if current, err = resolveRef(spec, asMap); err != nil {
			return nil, fmt.Errorf("key %q at %v: %w", key, keys[:i], err)
		}
	}
	return current, nil
}

// maxRefHops bounds $ref chasing, so a spec carrying a reference cycle
// produces an error rather than hanging the suite.
const maxRefHops = 32

// resolveRef follows a local $ref chain to the schema it names. Remote and
// URL references are rejected: the suite reads one self-contained document.
func resolveRef(spec, node map[string]any) (map[string]any, error) {
	for hop := 0; ; hop++ {
		raw, ok := node["$ref"]
		if !ok {
			return node, nil
		}
		if hop >= maxRefHops {
			return nil, fmt.Errorf("$ref chain longer than %d hops", maxRefHops)
		}
		ref, ok := raw.(string)
		if !ok || !strings.HasPrefix(ref, "#/") {
			return nil, fmt.Errorf("unsupported $ref %v: only local references are resolved", raw)
		}
		target := any(spec)
		for _, segment := range strings.Split(strings.TrimPrefix(ref, "#/"), "/") {
			parent, ok := target.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("$ref %q: %q has no object parent", ref, segment)
			}
			if target, ok = parent[segment]; !ok {
				return nil, fmt.Errorf("$ref %q: segment %q not found", ref, segment)
			}
		}
		if node, ok = target.(map[string]any); !ok {
			return nil, fmt.Errorf("$ref %q does not name an object", ref)
		}
	}
}

// MinimalVisualization returns the smallest graph visualization body the
// OpenAPI contract accepts for one discriminator variant.
func MinimalVisualization(specPath, variant string) (map[string]any, error) {
	return minimalVariantBody(specPath, "/v1/dashboards", visualizationPath, variant)
}

// MinimalMonitorConfiguration returns the smallest monitor configuration body
// the OpenAPI contract accepts for one discriminator variant.
func MinimalMonitorConfiguration(specPath, variant string) (map[string]any, error) {
	body, err := minimalVariantBody(specPath, "/v1/monitors", configurationPath, variant)
	if err != nil {
		return nil, err
	}
	// The spec marks only filter.env required, but its description (and the
	// API, with a 400) demands a non-empty teamIds or services as well - a
	// rule `required` cannot express, so the generator never emits either.
	if filter, ok := body["filter"].(map[string]any); ok && variant == "log-error-pattern" {
		filter["services"] = []any{"e2e"}
	}
	return body, nil
}

// minimalVariantBody generates a body for one variant of a discriminated
// union, carrying every field that variant's schema marks required.
//
// Generated rather than hand-written: there are 34 variants across the two
// unions, their required fields nest several levels deep, and a hand-written
// table would go stale the next time public-open-api.json is synced with
// nothing to notice. Reading `required` from the spec means a new required
// field becomes a new generated field on the next sync.
func minimalVariantBody(specPath, path string, keys []string, variant string) (map[string]any, error) {
	spec, err := loadSpec(specPath)
	if err != nil {
		return nil, err
	}
	mapping, err := variantMapping(specPath, path, keys)
	if err != nil {
		return nil, err
	}
	ref, ok := mapping[variant]
	if !ok {
		return nil, fmt.Errorf("%s: no discriminator variant %q", path, variant)
	}
	refString, ok := ref.(string)
	if !ok {
		return nil, fmt.Errorf("%s: discriminator mapping for %q is not a string", path, variant)
	}
	schema, err := resolveRef(spec, map[string]any{"$ref": refString})
	if err != nil {
		return nil, fmt.Errorf("%s variant %q: %w", path, variant, err)
	}
	value, err := minimalValue(spec, schema, 0)
	if err != nil {
		return nil, fmt.Errorf("%s variant %q: %w", path, variant, err)
	}
	body, ok := value.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%s variant %q: schema is not an object", path, variant)
	}
	return body, nil
}

// maxSchemaDepth bounds recursion, so a self-referential schema is reported
// as an error and cannot exhaust the stack.
const maxSchemaDepth = 24

// minimalValue builds the smallest value satisfying schema: every required
// property, and nothing optional.
func minimalValue(spec, schema map[string]any, depth int) (any, error) {
	if depth > maxSchemaDepth {
		return nil, fmt.Errorf("schema nests deeper than %d levels", maxSchemaDepth)
	}
	schema, err := resolveRef(spec, schema)
	if err != nil {
		return nil, err
	}

	// An enum of one is how the spec pins a discriminator, so taking the
	// first value is what emits the correct "type" for each variant.
	if enum, ok := schema["enum"].([]any); ok && len(enum) > 0 {
		return enum[0], nil
	}
	if options, ok := schema["oneOf"].([]any); ok {
		pick, err := simplestOption(spec, options)
		if err != nil {
			return nil, err
		}
		return minimalValue(spec, pick, depth+1)
	}

	properties, hasProperties := schema["properties"].(map[string]any)
	switch schema["type"] {
	case "object":
	case "array":
		return minimalArray(spec, schema, depth)
	case "string":
		return placeholderString(schema), nil
	case "integer", "number":
		return numberWithinBounds(schema), nil
	case "boolean":
		return false, nil
	default:
		if !hasProperties {
			return nil, fmt.Errorf("unsupported schema type %v", schema["type"])
		}
	}

	body := map[string]any{}
	required, _ := schema["required"].([]any)
	for _, raw := range required {
		name, ok := raw.(string)
		if !ok {
			return nil, fmt.Errorf("required entry %v is not a string", raw)
		}
		property, ok := properties[name].(map[string]any)
		if !ok {
			return nil, fmt.Errorf("required property %q has no schema", name)
		}
		value, err := minimalValue(spec, property, depth+1)
		if err != nil {
			return nil, fmt.Errorf("property %q: %w", name, err)
		}
		body[name] = value
	}
	return body, nil
}

// minimalArray builds one element per minItems, and one element where the
// spec sets no minItems. An empty list is schema-valid but semantically
// empty - a monitor with no queries has nothing to evaluate - and
// config/samples carries a query per monitor, so one element is the smaller
// risk against an API whose business rules are not in the spec.
func minimalArray(spec, schema map[string]any, depth int) (any, error) {
	items, ok := schema["items"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("array schema has no items")
	}
	count := 1
	if minItems, ok := schema["minItems"].(float64); ok && int(minItems) > count {
		count = int(minItems)
	}
	list := make([]any, 0, count)
	for i := 0; i < count; i++ {
		item, err := minimalValue(spec, items, depth+1)
		if err != nil {
			return nil, err
		}
		list = append(list, item)
	}
	return list, nil
}

// simplestOption picks the branch of a oneOf with the fewest required
// fields, breaking ties on the $ref so the choice is stable across runs. In
// this spec every oneOf is the aggregate union, whose "count" branch needs
// only a type, which keeps the generated body genuinely minimal.
func simplestOption(spec map[string]any, options []any) (map[string]any, error) {
	var best map[string]any
	bestCount, bestKey := 0, ""
	for _, raw := range options {
		option, ok := raw.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("oneOf branch %v is not an object", raw)
		}
		key, _ := option["$ref"].(string)
		resolved, err := resolveRef(spec, option)
		if err != nil {
			return nil, err
		}
		required, _ := resolved["required"].([]any)
		if best == nil || len(required) < bestCount ||
			(len(required) == bestCount && key < bestKey) {
			best, bestCount, bestKey = resolved, len(required), key
		}
	}
	if best == nil {
		return nil, fmt.Errorf("oneOf has no branches")
	}
	return best, nil
}

// placeholderString returns a value for a required string. The formats come
// from the spec; the values match what config/samples uses, which is the
// only evidence available for what the API accepts.
// numberWithinBounds returns a value the API will accept for a numeric
// property: the schema's minimum where it sets one, clamped to any maximum,
// and 1 otherwise.
//
// Presence is not sufficient for these. The API enforces the bounds and
// rejects an out-of-range value with a 400 — a monitor's timeframe declares
// minimum 5, so a bare 1 satisfies "required" and still fails validation.
func numberWithinBounds(schema map[string]any) any {
	value := 1.0
	if minimum, ok := schema["minimum"].(float64); ok {
		value = minimum
	}
	if maximum, ok := schema["maximum"].(float64); ok && value > maximum {
		value = maximum
	}
	if schema["type"] == "integer" {
		return int(value)
	}
	return value
}

func placeholderString(schema map[string]any) string {
	switch schema["format"] {
	case "tsuga-query":
		return ""
	case "tsuga-formula":
		return "q1"
	case "readonly-sql-query":
		return "SELECT 1"
	default:
		return "e2e"
	}
}
