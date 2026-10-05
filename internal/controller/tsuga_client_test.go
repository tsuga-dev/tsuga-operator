package controller

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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
