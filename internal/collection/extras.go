package collection

import (
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"

	"github.com/tsuga-dev/tsuga-operator/api/v1alpha1"
)

// tuneExtras adds the user's own components and pipelines to a collector's
// config. It runs after the collector's own tuning, so the built-in names are
// all present and a clash is detectable. Undefined references are left to the
// collector, which rejects them at startup.
func tuneExtras(c map[string]interface{}, t v1alpha1.CollectorTuning) error {
	// Receivers, exporters and connectors share one ID namespace in the
	// collector, so a connector may not reuse a receiver or exporter name.
	kinds := []struct {
		kind, section, field string
		extra                *apiextensionsv1.JSON
		alsoTaken            []string
	}{
		{"receiver", fieldReceivers, "extraReceivers", t.ExtraReceivers, []string{fieldConnectors}},
		{"exporter", fieldExporters, "extraExporters", t.ExtraExporters, []string{fieldConnectors}},
		{"processor", fieldProcessors, "extraProcessors", t.ExtraProcessors, nil},
		{"connector", fieldConnectors, "extraConnectors", t.ExtraConnectors, []string{fieldReceivers, fieldExporters}},
		{"extension", "extensions", "extraExtensions", t.ExtraExtensions, nil},
	}
	for _, k := range kinds {
		extras, err := decodeExtras(k.field, k.extra)
		if err != nil {
			return err
		}
		if err := addExtras(c, k.section, k.kind, extras, k.alsoTaken...); err != nil {
			return err
		}
	}

	pipelines, err := decodeExtras("extraPipelines", t.ExtraPipelines)
	if err != nil {
		return err
	}
	if len(pipelines) > 0 {
		if err := addExtras(nestedMap(c, "service"), "pipelines", "pipeline", pipelines); err != nil {
			return err
		}
	}

	appendToPipelines(c, fieldReceivers, t.PipelineReceivers)
	appendToPipelines(c, fieldProcessors, t.PipelineProcessors)
	appendToPipelines(c, fieldExporters, t.PipelineExporters)

	if len(t.ServiceExtensions) > 0 {
		service := nestedMap(c, "service")
		service["extensions"] = appendMissing(sliceOf(service["extensions"]), t.ServiceExtensions)
	}
	return checkPipelinesComplete(c)
}

const (
	fieldReceivers  = "receivers"
	fieldProcessors = "processors"
	fieldExporters  = "exporters"
	fieldConnectors = "connectors"
)

func decodeExtras(field string, raw *apiextensionsv1.JSON) (map[string]interface{}, error) {
	out := map[string]interface{}{}
	if raw == nil {
		return out, nil
	}
	if err := json.Unmarshal(raw.Raw, &out); err != nil {
		return nil, fmt.Errorf("%s: %w", field, err)
	}
	return out, nil
}

// nestedMap returns m[key] as a map, creating and attaching an empty one when
// absent so writes through it land in the config.
func nestedMap(m map[string]interface{}, key string) map[string]interface{} {
	child, ok := asMap(m[key])
	if !ok {
		child = map[string]interface{}{}
		m[key] = child
	}
	return child
}

// addExtras adds extras to parent[section], rejecting a name already in that
// section or in any of the alsoTaken sections of parent.
func addExtras(parent map[string]interface{}, section, kind string, extras map[string]interface{}, alsoTaken ...string) error {
	if len(extras) == 0 {
		return nil
	}
	dst := nestedMap(parent, section)
	for _, name := range slices.Sorted(maps.Keys(extras)) {
		taken := slices.ContainsFunc(append([]string{section}, alsoTaken...), func(s string) bool {
			_, ok := asMapOrEmpty(parent[s])[name]
			return ok
		})
		if taken {
			return fmt.Errorf("%s %s is built in; choose another name", kind, name)
		}
		dst[name] = extras[name]
	}
	return nil
}

// appendToPipelines appends the named components to the end of field in every
// pipeline of each signal, so processors run after batch, as in the chart.
func appendToPipelines(c map[string]interface{}, field string, add v1alpha1.PipelineComponents) {
	pipelines := asMapOrEmpty(pipelinesOf(c))
	for signal, names := range map[string][]string{
		"traces": add.Traces, "metrics": add.Metrics, "logs": add.Logs, "profiles": add.Profiles,
	} {
		if len(names) == 0 {
			continue
		}
		for pipelineName, raw := range pipelines {
			pipeline, ok := asMap(raw)
			if !ok || (pipelineName != signal && !strings.HasPrefix(pipelineName, signal+"/")) {
				continue
			}
			pipeline[field] = appendMissing(sliceOf(pipeline[field]), names)
		}
	}
}

func appendMissing(list []interface{}, names []string) []interface{} {
	for _, name := range names {
		if !slices.Contains(stringSlice(list), name) {
			list = append(list, name)
		}
	}
	return list
}

func checkPipelinesComplete(c map[string]interface{}) error {
	pipelines := asMapOrEmpty(pipelinesOf(c))
	for _, pipelineName := range slices.Sorted(maps.Keys(pipelines)) {
		pipeline, ok := asMap(pipelines[pipelineName])
		if !ok || len(sliceOf(pipeline[fieldReceivers])) == 0 {
			return fmt.Errorf("pipeline %s needs receivers and exporters", pipelineName)
		}
		if len(sliceOf(pipeline[fieldExporters])) == 0 {
			signal, _, _ := strings.Cut(pipelineName, "/")
			return fmt.Errorf("pipeline %s has no exporter; keep exportToTsuga or add pipelineExporters.%s", pipelineName, signal)
		}
	}
	return nil
}
