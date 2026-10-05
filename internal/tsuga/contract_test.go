package tsugamapper

import (
	"bytes"
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/tsuga-dev/tsuga-operator/api/v1alpha1"
	"github.com/tsuga-dev/tsuga-operator/internal/tsugaapi"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
)

// The hand-owned Kubernetes API deliberately differs from Tsuga's API. These
// checks catch mapper fields and shapes that no longer fit the generated
// request models when the public OpenAPI document changes.
func TestPayloadsMatchGeneratedRequestModels(t *testing.T) {
	visualization := apiextensionsv1.JSON{Raw: []byte(`{"type":"timeseries"}`)}
	cases := []struct {
		name    string
		payload func() ([]byte, error)
		create  any
		update  any
	}{
		{
			name: "dashboard",
			payload: func() ([]byte, error) {
				return DashboardSpecToPayload(v1alpha1.DashboardSpec{
					Name: "Example", Owner: "team-1",
					Graphs: []v1alpha1.DashboardGraph{{ID: "graph-1", Visualization: visualization}},
				})
			},
			create: &tsugaapi.CreateDashboardJSONBody{},
			update: &tsugaapi.UpdateDashboardJSONBody{},
		},
		{
			name: "monitor",
			payload: func() ([]byte, error) {
				return MonitorSpecToPayload(v1alpha1.MonitorSpec{
					Name: "Example", Owner: "team-1", Priority: 2,
					Permissions: "all", Configuration: apiextensionsv1.JSON{Raw: []byte(`{"type":"logs"}`)},
				})
			},
			create: &tsugaapi.CreateMonitorJSONBody{},
			update: &tsugaapi.UpdateMonitorJSONBody{},
		},
		{
			name: "slo",
			payload: func() ([]byte, error) {
				return SLOSpecToPayload(minimalSLOSpec())
			},
			create: &tsugaapi.CreateSloJSONBody{},
			update: &tsugaapi.UpdateSloJSONBody{},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			payload, err := tc.payload()
			if err != nil {
				t.Fatal(err)
			}
			for name, model := range map[string]any{"create": tc.create, "update": tc.update} {
				decoder := json.NewDecoder(bytes.NewReader(payload))
				decoder.DisallowUnknownFields()
				if err := decoder.Decode(model); err != nil {
					t.Errorf("%s payload does not fit generated model: %v", name, err)
				}
			}
		})
	}
}

// These are the API fields the Kubernetes types do not expose yet. Keeping
// this inventory exact makes newly added API fields a reviewable CI failure.
func TestGeneratedRequestFieldGapsAreReviewed(t *testing.T) {
	cases := []struct {
		name     string
		api      any
		crd      any
		expected []string
	}{
		{"dashboard create", tsugaapi.CreateDashboardJSONBody{}, v1alpha1.DashboardSpec{}, []string{"filters[].exclude", "folderId", "graphs[].descriptionAlign", "graphs[].descriptionJustifyContent"}},
		{"dashboard update", tsugaapi.UpdateDashboardJSONBody{}, v1alpha1.DashboardSpec{}, []string{"filters[].exclude", "folderId", "graphs[].descriptionAlign", "graphs[].descriptionJustifyContent"}},
		{"monitor create", tsugaapi.CreateMonitorJSONBody{}, v1alpha1.MonitorSpec{}, []string{"clusterIds"}},
		{"monitor update", tsugaapi.UpdateMonitorJSONBody{}, v1alpha1.MonitorSpec{}, []string{"clusterIds"}},
		{"slo create", tsugaapi.CreateSloJSONBody{}, v1alpha1.SLOSpec{}, nil},
		{"slo update", tsugaapi.UpdateSloJSONBody{}, v1alpha1.SLOSpec{}, []string{"alerts[].id"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			apiFields := jsonLeafPaths(reflect.TypeOf(tc.api))
			crdFields := jsonLeafPaths(reflect.TypeOf(tc.crd))
			var missing []string
			for field := range apiFields {
				if _, exists := crdFields[field]; !exists {
					missing = append(missing, field)
				}
			}
			slices.Sort(missing)
			if !slices.Equal(missing, tc.expected) {
				t.Errorf("unreviewed API/CRD field gap: got %v, expected %v", missing, tc.expected)
			}
		})
	}
}

func jsonLeafPaths(root reflect.Type) map[string]struct{} {
	paths := make(map[string]struct{})
	var walk func(reflect.Type, string)
	walk = func(typ reflect.Type, prefix string) {
		for typ.Kind() == reflect.Pointer {
			typ = typ.Elem()
		}
		for i := 0; i < typ.NumField(); i++ {
			field := typ.Field(i)
			name := strings.Split(field.Tag.Get("json"), ",")[0]
			if name == "" || name == "-" {
				continue
			}
			path := prefix + name
			child := field.Type
			for child.Kind() == reflect.Pointer {
				child = child.Elem()
			}
			if child.Kind() == reflect.Slice {
				child = child.Elem()
				for child.Kind() == reflect.Pointer {
					child = child.Elem()
				}
				if child.Kind() == reflect.Struct {
					path += "[]"
				}
			}
			if child.Kind() == reflect.Struct && child != reflect.TypeOf(apiextensionsv1.JSON{}) {
				walk(child, path+".")
			} else {
				paths[path] = struct{}{}
			}
		}
	}
	walk(root, "")
	return paths
}
