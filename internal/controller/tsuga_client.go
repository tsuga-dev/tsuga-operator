package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/tsuga-dev/tsuga-operator/internal/tsugaapi"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
)

// TsugaResourceClient abstracts the Tsuga API operations on one resource
// type (monitors, dashboards or SLOs) for testability.
type TsugaResourceClient interface {
	// FindByTag returns the ID of a resource carrying the tag, or "" when none does.
	FindByTag(ctx context.Context, key, value string) (string, error)
	Create(ctx context.Context, payload []byte) (string, error)
	Update(ctx context.Context, id string, payload []byte) error
	Delete(ctx context.Context, id string) error
}

type (
	bodyCall   func(context.Context, string, io.Reader, ...tsugaapi.RequestEditorFn) (*http.Response, error)
	updateCall func(context.Context, string, string, io.Reader, ...tsugaapi.RequestEditorFn) (*http.Response, error)
	deleteCall func(context.Context, string, ...tsugaapi.RequestEditorFn) (*http.Response, error)
)

// tsugaResourceAPI binds TsugaResourceClient to one resource's generated endpoints.
type tsugaResourceAPI struct {
	query, create bodyCall
	update        updateCall
	delete        deleteCall
}

// NewTsugaClients constructs the real monitor, dashboard and SLO clients from
// the Tsuga API base URL and a bearer token.
func NewTsugaClients(baseURL, token string) (monitors, dashboards, slos TsugaResourceClient) {
	api, err := tsugaapi.NewClient(baseURL,
		tsugaapi.WithHTTPClient(&http.Client{Timeout: 30 * time.Second}),
		tsugaapi.WithRequestEditorFn(func(_ context.Context, req *http.Request) error {
			req.Header.Set("Authorization", "Bearer "+token)
			return nil
		}),
	)
	if err != nil {
		// The fixed options above cannot fail; retain this invariant if a future
		// generated version changes that behavior.
		panic(fmt.Sprintf("construct Tsuga API client: %v", err))
	}
	return &tsugaResourceAPI{api.QueryMonitorsWithBody, api.CreateMonitorWithBody, api.UpdateMonitorWithBody, api.DeleteMonitor},
		&tsugaResourceAPI{api.QueryDashboardsWithBody, api.CreateDashboardWithBody, api.UpdateDashboardWithBody, api.DeleteDashboard},
		&tsugaResourceAPI{api.QuerySlosWithBody, api.CreateSloWithBody, api.UpdateSloWithBody, api.DeleteSlo}
}

func (a *tsugaResourceAPI) FindByTag(ctx context.Context, key, value string) (string, error) {
	resp, err := a.query(ctx, "application/json", tagQueryBody(key, value))
	return readQueryResponse(ctx, resp, err, key, value)
}

func (a *tsugaResourceAPI) Create(ctx context.Context, payload []byte) (string, error) {
	resp, err := a.create(ctx, "application/json", bytes.NewReader(payload))
	return readAPIResponse(ctx, resp, err, false)
}

func (a *tsugaResourceAPI) Update(ctx context.Context, id string, payload []byte) error {
	resp, err := a.update(ctx, id, "application/json", bytes.NewReader(payload))
	_, err = readAPIResponse(ctx, resp, err, false)
	return err
}

func (a *tsugaResourceAPI) Delete(ctx context.Context, id string) error {
	resp, err := a.delete(ctx, id)
	_, err = readAPIResponse(ctx, resp, err, true)
	return err
}

func tagQueryBody(key, value string) io.Reader {
	body, _ := json.Marshal(map[string]any{
		"limit":   1,
		"filters": map[string]any{"tags": map[string]any{"values": []map[string]string{{"key": key, "value": value}}}},
	})
	return bytes.NewReader(body)
}

// maxResponseBytes limits reads from the Tsuga API to avoid unbounded memory use.
const maxResponseBytes = 10 * 1024 * 1024

// readAPIResponse preserves the controller's error classification and ID
// extraction while endpoint construction comes from the generated client.
func readAPIResponse(ctx context.Context, resp *http.Response, requestErr error, deleteRequest bool) (string, error) {
	respBody, err := readAPIBody(ctx, resp, requestErr)
	if err != nil || deleteRequest {
		return "", err
	}

	var envelope struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(respBody, &envelope); err != nil {
		return "", fmt.Errorf("failed to parse response: %w", err)
	}
	if envelope.Data.ID == "" {
		return "", errors.New("tsuga API returned success but data.id was absent")
	}
	return envelope.Data.ID, nil
}

// readQueryResponse returns the ID of the first item carrying the exact
// key=value tag, or "" when none does. The server-side filter is not trusted:
// the ID feeds an overwriting PUT on adopt and a DELETE on finalize, so an
// ignored or loosely matched filter must not hand back someone else's resource.
func readQueryResponse(ctx context.Context, resp *http.Response, requestErr error, key, value string) (string, error) {
	respBody, err := readAPIBody(ctx, resp, requestErr)
	if err != nil {
		return "", err
	}

	type tag struct {
		Key   string `json:"key"`
		Value string `json:"value"`
	}
	var envelope struct {
		Data []struct {
			ID   string `json:"id"`
			Tags []tag  `json:"tags"`
		} `json:"data"`
	}
	if err := json.Unmarshal(respBody, &envelope); err != nil {
		return "", fmt.Errorf("failed to parse response: %w", err)
	}
	for _, item := range envelope.Data {
		if !slices.Contains(item.Tags, tag{key, value}) {
			logf.FromContext(ctx).Info("tag query returned a resource without the requested tag; ignoring it",
				"id", item.ID, "tag", key+"="+value)
			continue
		}
		if item.ID == "" {
			return "", errors.New("tsuga API returned a tag match without an id")
		}
		return item.ID, nil
	}
	return "", nil
}

func readAPIBody(ctx context.Context, resp *http.Response, requestErr error) ([]byte, error) {
	if requestErr != nil {
		if resp != nil && resp.Body != nil {
			_ = resp.Body.Close()
		}
		return nil, requestErr
	}
	if resp == nil {
		return nil, errors.New("tsuga API returned no response")
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.Request != nil {
		logf.FromContext(ctx).V(1).Info("tsuga API request", "method", resp.Request.Method, "url", resp.Request.URL.String())
	}

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(respBody)) > maxResponseBytes {
		return nil, fmt.Errorf("tsuga API response body exceeded %d bytes", maxResponseBytes)
	}
	if resp.StatusCode >= 400 {
		return nil, &tsugaHTTPError{statusCode: resp.StatusCode, body: respBody}
	}
	return respBody, nil
}

type tsugaHTTPError struct {
	statusCode int
	body       []byte
}

// maxErrorBodyBytes caps the response body quoted in errors, which end up in
// CR status where anyone who can read the CR sees them.
const maxErrorBodyBytes = 512

// tsugaErrorBody is the JSON error envelope the Tsuga API returns.
type tsugaErrorBody struct {
	RequestID string `json:"requestId"`
	Error     struct {
		Message string `json:"message"`
		Code    string `json:"code"`
	} `json:"error"`
}

func (e *tsugaHTTPError) parsedBody() (tsugaErrorBody, bool) {
	var parsed tsugaErrorBody
	return parsed, json.Unmarshal(e.body, &parsed) == nil
}

// requestID returns the API's per-request trace ID, or "" when the body has none.
func (e *tsugaHTTPError) requestID() string {
	parsed, _ := e.parsedBody()
	return parsed.RequestID
}

// Error leaves out the requestId: it differs on every call, and a message that
// changes each attempt turns every failure into a status write, which
// re-triggers the watch and bypasses workqueue backoff.
func (e *tsugaHTTPError) Error() string {
	detail := string(e.body)
	if parsed, ok := e.parsedBody(); ok && parsed.Error.Message != "" {
		detail = parsed.Error.Message
		if parsed.Error.Code != "" {
			detail += " (" + parsed.Error.Code + ")"
		}
	}
	if len(detail) > maxErrorBodyBytes {
		detail = strings.ToValidUTF8(detail[:maxErrorBodyBytes], "") + "..."
	}
	return fmt.Sprintf("tsuga API error %d: %s", e.statusCode, detail)
}

func isNotFound(err error) bool {
	var e *tsugaHTTPError
	return errors.As(err, &e) && e.statusCode == 404
}

// retryableClientStatus lists 4xx codes that can succeed unchanged on a later
// attempt: auth (a permission grant clears it server-side; a rotated token
// needs a manager restart, as it is read once from the environment at
// startup), timeout, conflict, too early, rate limited.
var retryableClientStatus = []int{
	http.StatusUnauthorized, http.StatusForbidden,
	http.StatusRequestTimeout, http.StatusConflict, http.StatusTooEarly, http.StatusTooManyRequests,
}

func isRetryableClientError(err error) bool {
	var e *tsugaHTTPError
	return errors.As(err, &e) && slices.Contains(retryableClientStatus, e.statusCode)
}

func isClientError(err error) bool {
	var e *tsugaHTTPError
	return errors.As(err, &e) && e.statusCode >= 400 && e.statusCode < 500 && e.statusCode != 404 && !isRetryableClientError(err)
}
