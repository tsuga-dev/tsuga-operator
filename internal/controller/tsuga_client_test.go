package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"

	"github.com/tsuga-dev/tsuga-operator/api/v1alpha1"
)

func TestTsugaClient_OversizedBodyReturnsError(t *testing.T) {
	// Server returns a body that exceeds maxResponseBytes
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		// Write maxResponseBytes + 1 byte of data
		oversized := make([]byte, maxResponseBytes+1)
		w.Write(oversized) //nolint:errcheck
	}))
	defer srv.Close()

	_, _, sloClient := NewTsugaClients(srv.URL, "token")
	_, err := sloClient.Create(context.Background(), []byte(`{}`))
	if err == nil {
		t.Fatal("expected error for oversized response body, got nil")
	}
	if !strings.Contains(err.Error(), "exceeded") {
		t.Errorf("expected error message to mention 'exceeded', got: %v", err)
	}
}

func TestIsClientError_ExcludesRetryable(t *testing.T) {
	for _, code := range []int{401, 403, 408, 409, 425, 429} {
		err := &tsugaHTTPError{statusCode: code}
		if isClientError(err) {
			t.Errorf("isClientError should not match %d, isRetryableClientError covers it", code)
		}
		if !isRetryableClientError(err) {
			t.Errorf("isRetryableClientError should match %d", code)
		}
	}
}

func TestIsClientError_Matches4xxExcluding404And429(t *testing.T) {
	for _, code := range []int{400, 422} {
		err := &tsugaHTTPError{statusCode: code}
		if !isClientError(err) {
			t.Errorf("isClientError should match %d", code)
		}
	}
}

func TestIsClientError_Excludes404(t *testing.T) {
	err := &tsugaHTTPError{statusCode: 404}
	if isClientError(err) {
		t.Error("isClientError should not match 404")
	}
}

func TestIsNotFound_Matches404(t *testing.T) {
	err := &tsugaHTTPError{statusCode: 404}
	if !isNotFound(err) {
		t.Error("isNotFound should match 404")
	}
}

func TestTsugaSLOAdapter_CreatePostsToSLOsPathAndReturnsID(t *testing.T) {
	var gotMethod, gotPath, gotAuth, gotBody string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		body, _ := io.ReadAll(r.Body)
		gotBody = string(body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"requestId":"req-1","data":{"id":"slo-abc"}}`))
	}))
	defer srv.Close()

	_, _, sloClient := NewTsugaClients(srv.URL, "test-token")

	id, err := sloClient.Create(context.Background(), []byte(`{"name":"x"}`))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if id != "slo-abc" {
		t.Errorf("expected id slo-abc, got %q", id)
	}
	if gotMethod != "POST" {
		t.Errorf("expected POST, got %s", gotMethod)
	}
	if gotPath != "/v1/slos" {
		t.Errorf("expected /v1/slos, got %s", gotPath)
	}
	if gotAuth != "Bearer test-token" {
		t.Errorf("expected a bearer token header, got %q", gotAuth)
	}
	if gotBody != `{"name":"x"}` {
		t.Errorf("expected the payload to be forwarded verbatim, got %q", gotBody)
	}
}

func TestTsugaSLOAdapter_UpdateAndDeleteAddressTheIDPath(t *testing.T) {
	var paths []string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.Method+" "+r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"requestId":"req-1","data":{"id":"slo-abc"}}`))
	}))
	defer srv.Close()

	_, _, sloClient := NewTsugaClients(srv.URL, "test-token")

	if err := sloClient.Update(context.Background(), "slo-abc", []byte(`{"name":"x"}`)); err != nil {
		t.Fatalf("update: %v", err)
	}
	if err := sloClient.Delete(context.Background(), "slo-abc"); err != nil {
		t.Fatalf("delete: %v", err)
	}

	want := []string{"PUT /v1/slos/slo-abc", "DELETE /v1/slos/slo-abc"}
	if len(paths) != len(want) || paths[0] != want[0] || paths[1] != want[1] {
		t.Fatalf("expected %v, got %v", want, paths)
	}
}

func TestGeneratedTsugaClients_ResourceOperations(t *testing.T) {
	for _, resource := range []string{"monitors", "dashboards", "slos"} {
		t.Run(resource, func(t *testing.T) {
			var requests []string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
					t.Errorf("authorization = %q", got)
				}
				requests = append(requests, r.Method+" "+r.URL.Path)
				if r.Method != http.MethodDelete {
					if got := r.Header.Get("Content-Type"); got != "application/json" {
						t.Errorf("content type = %q", got)
					}
					body, _ := io.ReadAll(r.Body)
					if string(body) != `{"name":"example"}` {
						t.Errorf("body = %q", body)
					}
				}
				w.Header().Set("Content-Type", "application/json")
				if r.Method == http.MethodDelete {
					w.WriteHeader(http.StatusNoContent)
					return
				}
				_, _ = w.Write([]byte(`{"data":{"id":"remote-1"}}`))
			}))
			defer srv.Close()

			monitor, dashboard, slo := NewTsugaClients(srv.URL, "test-token")
			var api TsugaResourceClient
			switch resource {
			case "monitors":
				api = monitor
			case "dashboards":
				api = dashboard
			case "slos":
				api = slo
			}
			ctx := context.Background()
			payload := []byte(`{"name":"example"}`)
			id, err := api.Create(ctx, payload)
			if err != nil || id != "remote-1" {
				t.Fatalf("create: id=%q err=%v", id, err)
			}
			if err := api.Update(ctx, id, payload); err != nil {
				t.Fatalf("update: %v", err)
			}
			if err := api.Delete(ctx, id); err != nil {
				t.Fatalf("delete: %v", err)
			}
			want := []string{
				"POST /v1/" + resource,
				"PUT /v1/" + resource + "/remote-1",
				"DELETE /v1/" + resource + "/remote-1",
			}
			if len(requests) != len(want) {
				t.Fatalf("requests = %v, want %v", requests, want)
			}
			for i := range want {
				if requests[i] != want[i] {
					t.Errorf("request %d = %q, want %q", i, requests[i], want[i])
				}
			}
		})
	}
}

func TestTsugaClients_FindByTagQueriesByTag(t *testing.T) {
	for _, resource := range []string{"monitors", "dashboards", "slos"} {
		t.Run(resource, func(t *testing.T) {
			var gotPath, gotBody string
			respBody := `{"requestId":"req-1","data":[{"id":"remote-1","tags":[{"key":"team","value":"a"},{"key":"tsuga-operator/uid","value":"uid-1"}]}]}`
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotPath = r.Method + " " + r.URL.Path
				body, _ := io.ReadAll(r.Body)
				gotBody = string(body)
				_, _ = w.Write([]byte(respBody))
			}))
			defer srv.Close()

			monitor, dashboard, slo := NewTsugaClients(srv.URL, "test-token")
			api := map[string]TsugaResourceClient{"monitors": monitor, "dashboards": dashboard, "slos": slo}[resource]

			id, err := api.FindByTag(context.Background(), ownerTagKey, "uid-1")
			if err != nil || id != "remote-1" {
				t.Fatalf("find: id=%q err=%v", id, err)
			}
			if gotPath != "POST /v1/"+resource+"/query" {
				t.Errorf("request = %q", gotPath)
			}
			wantBody := `{"filters":{"tags":{"values":[{"key":"tsuga-operator/uid","value":"uid-1"}]}},"limit":1}`
			if gotBody != wantBody {
				t.Errorf("body = %s, want %s", gotBody, wantBody)
			}

			respBody = `{"requestId":"req-2","data":[]}`
			id, err = api.FindByTag(context.Background(), ownerTagKey, "uid-1")
			if err != nil || id != "" {
				t.Fatalf("find with no match: id=%q err=%v", id, err)
			}

			// A server that ignores or loosely applies the filter must not
			// hand back an unrelated resource to overwrite or delete.
			for _, data := range []string{
				`[{"id":"other"}]`,
				`[{"id":"other","tags":[{"key":"tsuga-operator/uid","value":"uid-10"}]}]`,
				`[{"id":"other","tags":[{"key":"tsuga-operator/uid-x","value":"uid-1"}]}]`,
			} {
				respBody = `{"requestId":"req-3","data":` + data + `}`
				id, err = api.FindByTag(context.Background(), ownerTagKey, "uid-1")
				if err != nil || id != "" {
					t.Fatalf("find with untagged item %s: id=%q err=%v", data, id, err)
				}
			}
		})
	}
}

func TestTsugaHTTPError_MessageIgnoresRequestID(t *testing.T) {
	first := &tsugaHTTPError{statusCode: 404, body: []byte(`{"requestId":"b1b4-1","error":{"message":"Team (ID: does-not-exist) not found","statusCode":404,"code":"RESOURCE_NOT_FOUND"}}`)}
	second := &tsugaHTTPError{statusCode: 404, body: []byte(`{"requestId":"c9d2-2","error":{"message":"Team (ID: does-not-exist) not found","statusCode":404,"code":"RESOURCE_NOT_FOUND"}}`)}

	if first.Error() != second.Error() {
		t.Fatalf("errors differing only by requestId must render the same message:\n%s\n%s", first.Error(), second.Error())
	}
	want := "tsuga API error 404: Team (ID: does-not-exist) not found (RESOURCE_NOT_FOUND)"
	if first.Error() != want {
		t.Errorf("Error() = %q, want %q", first.Error(), want)
	}
	if first.requestID() != "b1b4-1" {
		t.Errorf("requestID() = %q, want b1b4-1", first.requestID())
	}
}

func TestTsugaHTTPError_NonJSONBodyFallsBackAndTruncates(t *testing.T) {
	err := &tsugaHTTPError{statusCode: 502, body: []byte(strings.Repeat("x", maxErrorBodyBytes+100))}
	want := "tsuga API error 502: " + strings.Repeat("x", maxErrorBodyBytes) + "..."
	if err.Error() != want {
		t.Errorf("Error() = %q, want the body truncated to %d bytes", err.Error(), maxErrorBodyBytes)
	}

	short := &tsugaHTTPError{statusCode: 500, body: []byte("upstream failed")}
	if got := short.Error(); got != "tsuga API error 500: upstream failed" {
		t.Errorf("Error() = %q", got)
	}
}

func TestTsugaHTTPError_LongJSONMessageIsTruncated(t *testing.T) {
	err := &tsugaHTTPError{statusCode: 400, body: []byte(`{"requestId":"r","error":{"message":"` + strings.Repeat("y", maxErrorBodyBytes+100) + `"}}`)}
	want := "tsuga API error 400: " + strings.Repeat("y", maxErrorBodyBytes) + "..."
	if err.Error() != want {
		t.Errorf("Error() = %q, want the message truncated to %d bytes", err.Error(), maxErrorBodyBytes)
	}
}

func TestTeamResolver_TeamID(t *testing.T) {
	cases := []struct {
		name, body, wantID string
	}{
		{"exact match", `{"data":[{"id":"team-1","name":"checkout"}]}`, "team-1"},
		{"ignores a loosely matched name", `{"data":[{"id":"team-2","name":"Checkout"}]}`, ""},
		{"no team", `{"data":[]}`, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var gotPath, gotName string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotPath, gotName = r.URL.Path, r.URL.Query().Get("name")
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()

			id, err := NewTeamResolver(srv.URL, "token").TeamID(context.Background(), "checkout")
			if gotPath != "/v1/teams" || gotName != "checkout" {
				t.Errorf("request = %s?name=%s, want /v1/teams?name=checkout", gotPath, gotName)
			}
			if id != tc.wantID {
				t.Errorf("id = %q, want %q", id, tc.wantID)
			}
			if tc.wantID == "" && (err == nil || !strings.Contains(err.Error(), `team "checkout" not found`)) {
				t.Errorf("expected a not-found error, got %v", err)
			}
			if tc.wantID != "" && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

type stubTeams map[string]string

func (s stubTeams) TeamID(_ context.Context, name string) (string, error) {
	if id, ok := s[name]; ok {
		return id, nil
	}
	return "", fmt.Errorf("team %q not found in Tsuga", name)
}

func TestPayload_ResolvesOwnerName(t *testing.T) {
	teams := stubTeams{"checkout": "team-1"}
	dashboard := &v1alpha1.Dashboard{Spec: v1alpha1.DashboardSpec{
		Name: "d", OwnerName: "checkout", Graphs: []v1alpha1.DashboardGraph{{ID: "g1"}},
	}}
	monitor := &v1alpha1.Monitor{Spec: v1alpha1.MonitorSpec{
		Name: "m", OwnerName: "checkout", Priority: 1, Permissions: "all",
		Configuration: apiextensionsv1.JSON{Raw: []byte(`{}`)},
	}}
	slo := &v1alpha1.SLO{Spec: v1alpha1.SLOSpec{
		Name: "s", OwnerName: "checkout", Permissions: "all", Target: 99, TimeframeDays: 30,
		Configuration: apiextensionsv1.JSON{Raw: []byte(`{}`)},
	}}
	payloads := map[string]func() ([]byte, error){
		"dashboard": func() ([]byte, error) { return dashboardAdapter{teams: teams}.Payload(context.Background(), dashboard) },
		"monitor":   func() ([]byte, error) { return (&monitorAdapter{Teams: teams}).Payload(context.Background(), monitor) },
		"slo":       func() ([]byte, error) { return sloAdapter{teams: teams}.Payload(context.Background(), slo) },
	}
	for kind, payload := range payloads {
		t.Run(kind, func(t *testing.T) {
			raw, err := payload()
			if err != nil {
				t.Fatal(err)
			}
			var body map[string]any
			if err := json.Unmarshal(raw, &body); err != nil {
				t.Fatal(err)
			}
			if body["owner"] != "team-1" {
				t.Errorf("owner = %v, want team-1", body["owner"])
			}
			if _, present := body["ownerName"]; present {
				t.Error("ownerName must not reach the Tsuga API")
			}
		})
	}

	if slo.Status.OwnerID != "team-1" {
		t.Errorf("status.ownerId = %q, want team-1", slo.Status.OwnerID)
	}

	// The recorded ID is reused while the spec is unchanged: no lookup.
	if _, err := (sloAdapter{teams: stubTeams{}}).Payload(context.Background(), slo); err != nil {
		t.Errorf("expected the recorded owner ID to be reused, got %v", err)
	}

	slo.Generation++
	slo.Spec.OwnerName = "unknown"
	if _, err := (sloAdapter{teams: teams}).Payload(context.Background(), slo); err == nil {
		t.Error("expected an error for an unknown team name")
	}
}
