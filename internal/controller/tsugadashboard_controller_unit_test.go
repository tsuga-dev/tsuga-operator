package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	tsugav1alpha1 "github.com/tsuga-dev/tsuga-operator/api/v1alpha1"
)

const testDashboardID = "dash-123"

const (
	testNamespace = "default"
	testOwner     = "team-platform"
	testPermsAll  = "all"
	testGraphID   = "graph-1"
)

type mockDashboardClient struct {
	findFn   func(context.Context, string, string) (string, error)
	createFn func(context.Context, []byte) (string, error)
	updateFn func(context.Context, string, []byte) error
	deleteFn func(context.Context, string) error
}

func (m *mockDashboardClient) FindByTag(ctx context.Context, key, value string) (string, error) {
	if m.findFn == nil {
		return "", nil
	}
	return m.findFn(ctx, key, value)
}

func (m *mockDashboardClient) Create(ctx context.Context, payload []byte) (string, error) {
	if m.createFn == nil {
		return "", fmt.Errorf("unexpected create")
	}
	return m.createFn(ctx, payload)
}

func (m *mockDashboardClient) Update(ctx context.Context, id string, payload []byte) error {
	if m.updateFn == nil {
		return fmt.Errorf("unexpected update")
	}
	return m.updateFn(ctx, id, payload)
}

func (m *mockDashboardClient) Delete(ctx context.Context, id string) error {
	if m.deleteFn == nil {
		return fmt.Errorf("unexpected delete")
	}
	return m.deleteFn(ctx, id)
}

func newTestDashboard(name, namespace string) *tsugav1alpha1.Dashboard {
	obj := &tsugav1alpha1.Dashboard{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec: tsugav1alpha1.DashboardSpec{
			Name:  "Test Dashboard",
			Owner: testOwner,
			Graphs: []tsugav1alpha1.DashboardGraph{
				{
					ID: testGraphID,
					Visualization: apiextensionsv1.JSON{
						Raw: []byte(`{"type":"timeseries","source":"metrics","queries":[{"aggregate":{"type":"count"},"filter":"service:api"}]}`),
					},
				},
			},
		},
	}
	obj.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "observability.tsuga.com",
		Version: "v1alpha1",
		Kind:    "Dashboard",
	})
	return obj
}

func TestTsugaDashboardReconciler_Create(t *testing.T) {
	dashboard := newTestDashboard("test-dashboard", testNamespace)
	scheme := testScheme(t)

	createCalled := false
	mockClient := &mockDashboardClient{
		createFn: func(_ context.Context, payload []byte) (string, error) {
			createCalled = true
			var body map[string]any
			if err := json.Unmarshal(payload, &body); err != nil {
				t.Fatalf("payload is not valid json: %v", err)
			}
			if body["name"] != "Test Dashboard" {
				t.Fatalf("expected payload name to be Test Dashboard, got %v", body["name"])
			}
			return testDashboardID, nil
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(dashboard).
		WithStatusSubresource(dashboard).
		Build()

	reconciler := &TsugaDashboardReconciler{
		Client:      fakeClient,
		TsugaClient: mockClient,
	}

	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: dashboard.Name, Namespace: dashboard.Namespace}}
	if _, err := reconciler.Reconcile(context.Background(), req); err != nil {
		t.Fatalf("first reconcile: %v", err)
	}
	if createCalled {
		t.Fatal("create should not run before finalizer is added")
	}

	if _, err := reconciler.Reconcile(context.Background(), req); err != nil {
		t.Fatalf("second reconcile: %v", err)
	}
	if !createCalled {
		t.Fatal("expected create to be called")
	}

	updated := &tsugav1alpha1.Dashboard{}
	if err := fakeClient.Get(context.Background(), req.NamespacedName, updated); err != nil {
		t.Fatalf("get updated dashboard: %v", err)
	}
	if updated.Status.ID != testDashboardID {
		t.Fatalf("expected status id dash-123, got %q", updated.Status.ID)
	}
	if updated.Status.Phase != phaseReady {
		t.Fatalf("expected phase Ready, got %q", updated.Status.Phase)
	}
	if updated.Status.ObservedGeneration != updated.Generation {
		t.Fatalf("expected observedGeneration=%d, got %d", updated.Generation, updated.Status.ObservedGeneration)
	}
}

// A POST that succeeds followed by a failed status write must not create a
// second remote dashboard: the next reconcile finds it by owner tag and adopts it.
func TestTsugaDashboardReconciler_AdoptsRemoteAfterFailedStatusWrite(t *testing.T) {
	dashboard := newTestDashboard("test-dashboard", testNamespace)
	dashboard.UID = "uid-1"
	dashboard.Finalizers = []string{dashboardFinalizer}
	scheme := testScheme(t)

	remoteByOwner := map[string]string{}
	creates, updates := 0, 0
	mockClient := &mockDashboardClient{
		findFn: func(_ context.Context, key, value string) (string, error) {
			if key != ownerTagKey {
				t.Fatalf("find key = %q", key)
			}
			return remoteByOwner[value], nil
		},
		createFn: func(_ context.Context, payload []byte) (string, error) {
			creates++
			var body struct {
				Tags []tsugav1alpha1.ResourceTag `json:"tags"`
			}
			if err := json.Unmarshal(payload, &body); err != nil {
				t.Fatalf("payload is not valid json: %v", err)
			}
			for _, tag := range body.Tags {
				if tag.Key == ownerTagKey {
					remoteByOwner[tag.Value] = testDashboardID
				}
			}
			return testDashboardID, nil
		},
		updateFn: func(_ context.Context, id string, payload []byte) error {
			updates++
			if id != testDashboardID {
				t.Fatalf("update id = %q", id)
			}
			if !strings.Contains(string(payload), `{"key":"tsuga-operator/uid","value":"uid-1"}`) {
				t.Fatalf("update payload dropped owner tag: %s", payload)
			}
			return nil
		},
	}

	failStatusWrite := true
	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(dashboard).
		WithStatusSubresource(dashboard).
		WithInterceptorFuncs(interceptor.Funcs{
			SubResourceUpdate: func(ctx context.Context, c client.Client, sub string, obj client.Object, opts ...client.SubResourceUpdateOption) error {
				if failStatusWrite {
					failStatusWrite = false
					return fmt.Errorf("injected status write failure")
				}
				return c.SubResource(sub).Update(ctx, obj, opts...)
			},
		}).
		Build()

	reconciler := &TsugaDashboardReconciler{Client: fakeClient, TsugaClient: mockClient}
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: dashboard.Name, Namespace: dashboard.Namespace}}

	if _, err := reconciler.Reconcile(context.Background(), req); err == nil {
		t.Fatal("expected first reconcile to fail on status write")
	}
	if _, err := reconciler.Reconcile(context.Background(), req); err != nil {
		t.Fatalf("second reconcile: %v", err)
	}

	if creates != 1 || updates != 1 {
		t.Fatalf("creates=%d updates=%d, want 1 and 1", creates, updates)
	}
	updated := &tsugav1alpha1.Dashboard{}
	if err := fakeClient.Get(context.Background(), req.NamespacedName, updated); err != nil {
		t.Fatalf("get updated dashboard: %v", err)
	}
	if updated.Status.ID != testDashboardID || updated.Status.Phase != phaseReady {
		t.Fatalf("status id=%q phase=%q", updated.Status.ID, updated.Status.Phase)
	}
	if updated.Status.ObservedGeneration != updated.Generation {
		t.Fatalf("observedGeneration=%d, want %d", updated.Status.ObservedGeneration, updated.Generation)
	}
}

func TestTsugaDashboardReconciler_InvalidSpecIsTerminal(t *testing.T) {
	dashboard := newTestDashboard("test-dashboard", testNamespace)
	dashboard.Spec.Graphs[0].ID = ""
	scheme := testScheme(t)

	createCalled := false
	mockClient := &mockDashboardClient{
		createFn: func(_ context.Context, payload []byte) (string, error) {
			createCalled = true
			return testDashboardID, nil
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(dashboard).
		WithStatusSubresource(dashboard).
		Build()

	reconciler := &TsugaDashboardReconciler{
		Client:      fakeClient,
		TsugaClient: mockClient,
	}

	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: dashboard.Name, Namespace: dashboard.Namespace}}
	if _, err := reconciler.Reconcile(context.Background(), req); err != nil {
		t.Fatalf("first reconcile: %v", err)
	}
	// Retrying cannot fix a spec the mapper rejects, so it is reported in
	// status without an error that would requeue it forever.
	result, err := reconciler.Reconcile(context.Background(), req)
	if err != nil || result.RequeueAfter != 0 {
		t.Fatalf("invalid spec should not requeue: result=%+v err=%v", result, err)
	}
	if createCalled {
		t.Fatal("create should not be called when graph id is missing")
	}
	var updated tsugav1alpha1.Dashboard
	if err := fakeClient.Get(context.Background(), req.NamespacedName, &updated); err != nil {
		t.Fatalf("get dashboard: %v", err)
	}
	if updated.Status.Phase != phaseError {
		t.Fatalf("phase=%q, want %q", updated.Status.Phase, phaseError)
	}
}

func TestTsugaDashboardReconciler_UpdateOnGenerationChange(t *testing.T) {
	dashboard := newTestDashboard("test-dashboard", testNamespace)
	dashboard.Finalizers = []string{dashboardFinalizer}
	dashboard.Generation = 2
	dashboard.Status.ID = "dash-existing"
	dashboard.Status.ObservedGeneration = 1
	scheme := testScheme(t)

	updateCalled := false
	mockClient := &mockDashboardClient{
		updateFn: func(_ context.Context, id string, payload []byte) error {
			updateCalled = true
			if id != "dash-existing" {
				t.Fatalf("expected update id dash-existing, got %q", id)
			}
			var body map[string]any
			if err := json.Unmarshal(payload, &body); err != nil {
				t.Fatalf("payload is not valid json: %v", err)
			}
			if body["owner"] != testOwner {
				t.Fatalf("expected owner team-platform, got %v", body["owner"])
			}
			return nil
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(dashboard).
		WithStatusSubresource(dashboard).
		Build()

	reconciler := &TsugaDashboardReconciler{
		Client:      fakeClient,
		TsugaClient: mockClient,
	}

	if _, err := reconciler.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: dashboard.Name, Namespace: dashboard.Namespace}}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if !updateCalled {
		t.Fatal("expected update to be called")
	}
}

func TestTsugaDashboardReconciler_RecreatesAfter404(t *testing.T) {
	dashboard := newTestDashboard("test-dashboard", testNamespace)
	dashboard.Finalizers = []string{dashboardFinalizer}
	dashboard.Generation = 2
	dashboard.Status.ID = "dash-missing"
	dashboard.Status.ObservedGeneration = 1
	scheme := testScheme(t)

	createCalled := false
	mockClient := &mockDashboardClient{
		updateFn: func(_ context.Context, _ string, _ []byte) error {
			return &tsugaHTTPError{statusCode: 404, body: []byte("missing")}
		},
		createFn: func(_ context.Context, _ []byte) (string, error) {
			createCalled = true
			return "dash-recreated", nil
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(dashboard).
		WithStatusSubresource(dashboard).
		Build()

	reconciler := &TsugaDashboardReconciler{
		Client:      fakeClient,
		TsugaClient: mockClient,
	}

	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: dashboard.Name, Namespace: dashboard.Namespace}}
	if _, err := reconciler.Reconcile(context.Background(), req); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if !createCalled {
		t.Fatal("expected create after 404 on update")
	}

	updated := &tsugav1alpha1.Dashboard{}
	if err := fakeClient.Get(context.Background(), req.NamespacedName, updated); err != nil {
		t.Fatalf("get updated dashboard: %v", err)
	}
	if updated.Status.ID != "dash-recreated" {
		t.Fatalf("expected recreated id dash-recreated, got %q", updated.Status.ID)
	}
}

func TestTsugaDashboardReconciler_ClientErrorDoesNotBubble(t *testing.T) {
	dashboard := newTestDashboard("test-dashboard", testNamespace)
	dashboard.Finalizers = []string{dashboardFinalizer}
	scheme := testScheme(t)

	mockClient := &mockDashboardClient{
		createFn: func(_ context.Context, _ []byte) (string, error) {
			return "", &tsugaHTTPError{statusCode: 422, body: []byte("invalid")}
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(dashboard).
		WithStatusSubresource(dashboard).
		Build()

	reconciler := &TsugaDashboardReconciler{
		Client:      fakeClient,
		TsugaClient: mockClient,
	}

	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: dashboard.Name, Namespace: dashboard.Namespace}}
	if _, err := reconciler.Reconcile(context.Background(), req); err != nil {
		t.Fatalf("expected client error to be swallowed, got %v", err)
	}

	updated := &tsugav1alpha1.Dashboard{}
	if err := fakeClient.Get(context.Background(), req.NamespacedName, updated); err != nil {
		t.Fatalf("get updated dashboard: %v", err)
	}
	if updated.Status.Phase != phaseError {
		t.Fatalf("expected phase Error, got %q", updated.Status.Phase)
	}
}

func TestTsugaDashboardReconciler_PermanentClientErrorStopsWriting(t *testing.T) {
	dashboard := newTestDashboard("test-dashboard", testNamespace)
	dashboard.Finalizers = []string{dashboardFinalizer}
	scheme := testScheme(t)

	creates := 0
	mockClient := &mockDashboardClient{
		createFn: func(_ context.Context, _ []byte) (string, error) {
			creates++
			return "", &tsugaHTTPError{statusCode: 422, body: []byte("invalid")}
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(dashboard).
		WithStatusSubresource(dashboard).
		Build()

	reconciler := &TsugaDashboardReconciler{
		Client:      fakeClient,
		TsugaClient: mockClient,
	}

	// Re-reconcile while each pass still changes the object, as the watch would.
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: dashboard.Name, Namespace: dashboard.Namespace}}
	lastRV := ""
	for range 5 {
		if _, err := reconciler.Reconcile(context.Background(), req); err != nil {
			t.Fatalf("reconcile: %v", err)
		}
		current := &tsugav1alpha1.Dashboard{}
		if err := fakeClient.Get(context.Background(), req.NamespacedName, current); err != nil {
			t.Fatalf("get dashboard: %v", err)
		}
		if current.ResourceVersion == lastRV {
			break
		}
		lastRV = current.ResourceVersion
	}

	if creates != 2 {
		t.Fatalf("expected the error write to settle after 2 creates, got %d", creates)
	}
}

func TestTsugaDashboardReconciler_RateLimitRequeues(t *testing.T) {
	dashboard := newTestDashboard("test-dashboard", testNamespace)
	dashboard.Finalizers = []string{dashboardFinalizer}
	scheme := testScheme(t)

	mockClient := &mockDashboardClient{
		createFn: func(_ context.Context, _ []byte) (string, error) {
			return "", &tsugaHTTPError{statusCode: 429, body: []byte("rate limited")}
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(dashboard).
		WithStatusSubresource(dashboard).
		Build()

	reconciler := &TsugaDashboardReconciler{
		Client:      fakeClient,
		TsugaClient: mockClient,
	}

	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: dashboard.Name, Namespace: dashboard.Namespace}}
	result, err := reconciler.Reconcile(context.Background(), req)
	if err != nil {
		t.Fatalf("expected rate limit to requeue without error, got %v", err)
	}
	if result.RequeueAfter != rateLimitRequeueAfter {
		t.Fatalf("expected requeueAfter %s, got %s", rateLimitRequeueAfter, result.RequeueAfter)
	}
}

func reconcileDashboardWithCreateError(t *testing.T, createErr error) (ctrl.Result, *tsugav1alpha1.Dashboard) {
	t.Helper()
	dashboard := newTestDashboard("test-dashboard", testNamespace)
	dashboard.Finalizers = []string{dashboardFinalizer}

	fakeClient := fake.NewClientBuilder().
		WithScheme(testScheme(t)).
		WithObjects(dashboard).
		WithStatusSubresource(dashboard).
		Build()
	reconciler := &TsugaDashboardReconciler{
		Client: fakeClient,
		TsugaClient: &mockDashboardClient{
			createFn: func(_ context.Context, _ []byte) (string, error) { return "", createErr },
		},
	}

	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: dashboard.Name, Namespace: dashboard.Namespace}}
	result, err := reconciler.Reconcile(context.Background(), req)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	current := &tsugav1alpha1.Dashboard{}
	if err := fakeClient.Get(context.Background(), req.NamespacedName, current); err != nil {
		t.Fatalf("get dashboard: %v", err)
	}
	return result, current
}

func TestTsugaDashboardReconciler_AuthErrorRequeues(t *testing.T) {
	for _, code := range []int{401, 403} {
		result, _ := reconcileDashboardWithCreateError(t, &tsugaHTTPError{statusCode: code, body: []byte("unauthorized")})
		if result.RequeueAfter != rateLimitRequeueAfter {
			t.Errorf("%d: expected requeueAfter %s, got %s", code, rateLimitRequeueAfter, result.RequeueAfter)
		}
	}
}

func TestTsugaDashboardReconciler_BadRequestDoesNotRequeue(t *testing.T) {
	result, current := reconcileDashboardWithCreateError(t, &tsugaHTTPError{statusCode: 400, body: []byte("bad query")})
	if result.RequeueAfter != 0 {
		t.Errorf("expected no requeue for 400, got requeueAfter %s", result.RequeueAfter)
	}
	if current.Status.Message != "tsuga API error 400: bad query" {
		t.Errorf("expected validation message in status, got %q", current.Status.Message)
	}
}

func TestTsugaDashboardReconciler_LongErrorBodyTruncatedInStatus(t *testing.T) {
	body := strings.Repeat("secret-ish ", 1000)
	_, current := reconcileDashboardWithCreateError(t, &tsugaHTTPError{statusCode: 422, body: []byte(body)})
	if current.Status.Phase != phaseError {
		t.Fatalf("expected Error phase, got %q", current.Status.Phase)
	}
	if len(current.Status.Message) > 600 {
		t.Errorf("expected status message to be capped, got %d bytes", len(current.Status.Message))
	}
	if !strings.HasSuffix(current.Status.Message, "...") {
		t.Errorf("expected truncated status message to end with an ellipsis")
	}
}

func TestSetPhase_SetsLastSyncedAt(t *testing.T) {
	for _, phase := range []string{phaseReady, phaseError, phaseDeleting} {
		status := &tsugav1alpha1.SyncStatus{}
		before := metav1.Now()
		setPhase(status, phase, "message")
		if status.Phase != phase || status.Message != "message" {
			t.Errorf("%s: got phase %q message %q", phase, status.Phase, status.Message)
		}
		if status.LastSyncedAt == nil || status.LastSyncedAt.Before(&before) {
			t.Errorf("%s: expected LastSyncedAt to be set to now, got %v", phase, status.LastSyncedAt)
		}
	}
}

func TestTsugaDashboardReconciler_Delete(t *testing.T) {
	now := metav1.Now()
	dashboard := newTestDashboard("test-dashboard", testNamespace)
	dashboard.Finalizers = []string{dashboardFinalizer}
	dashboard.DeletionTimestamp = &now
	dashboard.Status.ID = "dash-delete"
	scheme := testScheme(t)

	deleteCalled := false
	mockClient := &mockDashboardClient{
		deleteFn: func(_ context.Context, id string) error {
			deleteCalled = true
			if id != "dash-delete" {
				t.Fatalf("expected delete id dash-delete, got %q", id)
			}
			return nil
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(dashboard).
		WithStatusSubresource(dashboard).
		Build()

	reconciler := &TsugaDashboardReconciler{
		Client:      fakeClient,
		TsugaClient: mockClient,
	}

	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: dashboard.Name, Namespace: dashboard.Namespace}}
	if _, err := reconciler.Reconcile(context.Background(), req); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if !deleteCalled {
		t.Fatal("expected delete to be called")
	}
}

func TestTsugaDashboardReconciler_FailingDeleteWritesStatusOnce(t *testing.T) {
	now := metav1.Now()
	dashboard := newTestDashboard("test-dashboard", testNamespace)
	dashboard.Finalizers = []string{dashboardFinalizer}
	dashboard.DeletionTimestamp = &now
	dashboard.Status.ID = "dash-delete"
	scheme := testScheme(t)

	mockClient := &mockDashboardClient{
		deleteFn: func(context.Context, string) error {
			return &tsugaHTTPError{statusCode: 503, body: []byte("unavailable")}
		},
	}
	statusWrites := 0
	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(dashboard).
		WithStatusSubresource(dashboard).
		WithInterceptorFuncs(interceptor.Funcs{
			SubResourceUpdate: func(ctx context.Context, c client.Client, sub string, obj client.Object, opts ...client.SubResourceUpdateOption) error {
				statusWrites++
				return c.SubResource(sub).Update(ctx, obj, opts...)
			},
		}).
		Build()
	reconciler := &TsugaDashboardReconciler{Client: fakeClient, TsugaClient: mockClient}

	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: dashboard.Name, Namespace: dashboard.Namespace}}
	for range 3 {
		if _, err := reconciler.Reconcile(context.Background(), req); err == nil {
			t.Fatal("expected failing delete to return an error")
		}
	}
	// Each status write emits a watch event that skips backoff; only the
	// first attempt may write.
	if statusWrites != 1 {
		t.Fatalf("status writes=%d, want 1", statusWrites)
	}
}

func TestTsugaDashboardReconciler_DeleteDropsFinalizerWhenTagLookupRejected(t *testing.T) {
	now := metav1.Now()
	dashboard := newTestDashboard("test-dashboard", testNamespace)
	dashboard.Finalizers = []string{dashboardFinalizer}
	dashboard.DeletionTimestamp = &now

	// No deleteFn: a remote delete call would fail the reconcile.
	mockClient := &mockDashboardClient{
		findFn: func(context.Context, string, string) (string, error) {
			return "", &tsugaHTTPError{statusCode: 400, body: []byte(`{"error":{"message":"Bad Request"}}`)}
		},
	}
	fakeClient := fake.NewClientBuilder().
		WithScheme(testScheme(t)).
		WithObjects(dashboard).
		WithStatusSubresource(dashboard).
		Build()
	reconciler := &TsugaDashboardReconciler{Client: fakeClient, TsugaClient: mockClient}

	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: dashboard.Name, Namespace: dashboard.Namespace}}
	if _, err := reconciler.Reconcile(context.Background(), req); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	var remaining tsugav1alpha1.Dashboard
	if err := fakeClient.Get(context.Background(), req.NamespacedName, &remaining); !apierrors.IsNotFound(err) {
		t.Fatalf("expected the finalizer to be dropped and the object gone, got err=%v finalizers=%v", err, remaining.Finalizers)
	}
}

func TestTsugaDashboardReconciler_DeleteRetriesTransientTagLookupFailure(t *testing.T) {
	// 401/403 can clear (permission grant, token fix); dropping the finalizer
	// would orphan the remote copy.
	for _, code := range []int{401, 403, 503} {
		now := metav1.Now()
		dashboard := newTestDashboard("test-dashboard", testNamespace)
		dashboard.Finalizers = []string{dashboardFinalizer}
		dashboard.DeletionTimestamp = &now

		mockClient := &mockDashboardClient{
			findFn: func(context.Context, string, string) (string, error) {
				return "", &tsugaHTTPError{statusCode: code, body: []byte("unavailable")}
			},
		}
		fakeClient := fake.NewClientBuilder().
			WithScheme(testScheme(t)).
			WithObjects(dashboard).
			WithStatusSubresource(dashboard).
			Build()
		reconciler := &TsugaDashboardReconciler{Client: fakeClient, TsugaClient: mockClient}

		req := ctrl.Request{NamespacedName: types.NamespacedName{Name: dashboard.Name, Namespace: dashboard.Namespace}}
		if _, err := reconciler.Reconcile(context.Background(), req); err == nil {
			t.Fatalf("%d: expected the tag lookup to return an error and keep the finalizer", code)
		}
		var remaining tsugav1alpha1.Dashboard
		if err := fakeClient.Get(context.Background(), req.NamespacedName, &remaining); err != nil {
			t.Fatalf("%d: object should still exist: %v", code, err)
		}
	}
}

func newSyncedDashboard(lastSynced time.Time) *tsugav1alpha1.Dashboard {
	dashboard := newTestDashboard("test-dashboard", testNamespace)
	dashboard.Finalizers = []string{dashboardFinalizer}
	dashboard.Generation = 1
	dashboard.Status.ID = testDashboardID
	dashboard.Status.ObservedGeneration = 1
	dashboard.Status.Phase = phaseReady
	dashboard.Status.LastSyncedAt = &metav1.Time{Time: lastSynced}
	return dashboard
}

func TestTsugaDashboardReconciler_ResyncsAfterDriftInterval(t *testing.T) {
	dashboard := newSyncedDashboard(time.Now().Add(-driftResyncInterval - time.Minute))
	updated := false
	mockClient := &mockDashboardClient{
		updateFn: func(_ context.Context, id string, _ []byte) error {
			updated = id == testDashboardID
			return nil
		},
	}
	fakeClient := fake.NewClientBuilder().
		WithScheme(testScheme(t)).
		WithObjects(dashboard).
		WithStatusSubresource(dashboard).
		Build()
	reconciler := &TsugaDashboardReconciler{Client: fakeClient, TsugaClient: mockClient}

	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: dashboard.Name, Namespace: dashboard.Namespace}}
	result, err := reconciler.Reconcile(context.Background(), req)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if !updated {
		t.Fatal("expected a stale Ready dashboard to be re-pushed")
	}
	if result.RequeueAfter != driftResyncInterval {
		t.Fatalf("RequeueAfter=%v, want %v", result.RequeueAfter, driftResyncInterval)
	}
}

func TestTsugaDashboardReconciler_FreshSyncOnlySchedulesResync(t *testing.T) {
	dashboard := newSyncedDashboard(time.Now().Add(-time.Minute))
	fakeClient := fake.NewClientBuilder().
		WithScheme(testScheme(t)).
		WithObjects(dashboard).
		WithStatusSubresource(dashboard).
		Build()
	// No updateFn: any call to Update fails the reconcile.
	reconciler := &TsugaDashboardReconciler{Client: fakeClient, TsugaClient: &mockDashboardClient{}}

	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: dashboard.Name, Namespace: dashboard.Namespace}}
	result, err := reconciler.Reconcile(context.Background(), req)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if result.RequeueAfter <= 0 || result.RequeueAfter > driftResyncInterval {
		t.Fatalf("RequeueAfter=%v, want within (0, %v]", result.RequeueAfter, driftResyncInterval)
	}
}

func TestTsugaDashboardReconciler_ConcurrentSpecEditIsNotMarkedSynced(t *testing.T) {
	dashboard := newTestDashboard("test-dashboard", testNamespace)
	dashboard.Finalizers = []string{dashboardFinalizer}
	dashboard.Generation = 2
	dashboard.Status.ID = "dash-existing"
	dashboard.Status.ObservedGeneration = 1
	scheme := testScheme(t)

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(dashboard).
		WithStatusSubresource(dashboard).
		Build()
	key := types.NamespacedName{Name: dashboard.Name, Namespace: dashboard.Namespace}

	mockClient := &mockDashboardClient{
		updateFn: func(ctx context.Context, _ string, _ []byte) error {
			edited := &tsugav1alpha1.Dashboard{}
			if err := fakeClient.Get(ctx, key, edited); err != nil {
				t.Fatalf("get: %v", err)
			}
			edited.Spec.Name = "Edited mid-sync"
			edited.Generation = 3
			if err := fakeClient.Update(ctx, edited); err != nil {
				t.Fatalf("concurrent edit: %v", err)
			}
			return nil
		},
	}

	reconciler := &TsugaDashboardReconciler{Client: fakeClient, TsugaClient: mockClient}
	if _, err := reconciler.Reconcile(context.Background(), ctrl.Request{NamespacedName: key}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	updated := &tsugav1alpha1.Dashboard{}
	if err := fakeClient.Get(context.Background(), key, updated); err != nil {
		t.Fatalf("get updated dashboard: %v", err)
	}
	if updated.Generation != 3 {
		t.Fatalf("test setup: expected generation 3, got %d", updated.Generation)
	}
	if updated.Status.ObservedGeneration != 2 {
		t.Fatalf("expected observedGeneration 2 (the pushed spec), got %d", updated.Status.ObservedGeneration)
	}
}

func TestTsugaDashboardReconciler_DeleteFindsUnrecordedRemoteByTag(t *testing.T) {
	now := metav1.Now()
	dashboard := newTestDashboard("test-dashboard", testNamespace)
	dashboard.UID = "uid-orphan"
	dashboard.Finalizers = []string{dashboardFinalizer}
	dashboard.DeletionTimestamp = &now
	scheme := testScheme(t)

	var deletedID string
	mockClient := &mockDashboardClient{
		findFn: func(_ context.Context, key, value string) (string, error) {
			if key != ownerTagKey || value != "uid-orphan" {
				t.Fatalf("unexpected tag lookup %s=%s", key, value)
			}
			return "dash-orphan", nil
		},
		deleteFn: func(_ context.Context, id string) error {
			deletedID = id
			return nil
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(dashboard).
		WithStatusSubresource(dashboard).
		Build()

	reconciler := &TsugaDashboardReconciler{Client: fakeClient, TsugaClient: mockClient}
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: dashboard.Name, Namespace: dashboard.Namespace}}
	if _, err := reconciler.Reconcile(context.Background(), req); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if deletedID != "dash-orphan" {
		t.Fatalf("expected delete of dash-orphan, got %q", deletedID)
	}
}

func newStaleDashboardReconciler(t *testing.T, mockClient *mockDashboardClient) (*TsugaDashboardReconciler, ctrl.Request) {
	t.Helper()
	dashboard := newSyncedDashboard(time.Now().Add(-driftResyncInterval - time.Minute))
	fakeClient := fake.NewClientBuilder().
		WithScheme(testScheme(t)).
		WithObjects(dashboard).
		WithStatusSubresource(dashboard).
		Build()
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: dashboard.Name, Namespace: dashboard.Namespace}}
	return &TsugaDashboardReconciler{Client: fakeClient, TsugaClient: mockClient}, req
}

func getDashboard(t *testing.T, r *TsugaDashboardReconciler, req ctrl.Request) *tsugav1alpha1.Dashboard {
	t.Helper()
	current := &tsugav1alpha1.Dashboard{}
	if err := r.Get(context.Background(), req.NamespacedName, current); err != nil {
		t.Fatalf("get dashboard: %v", err)
	}
	return current
}

func TestTsugaDashboardReconciler_RetriesFailedResync(t *testing.T) {
	for _, code := range []int{503, 429, 401} {
		updates := 0
		reconciler, req := newStaleDashboardReconciler(t, &mockDashboardClient{
			updateFn: func(_ context.Context, _ string, _ []byte) error {
				updates++
				if updates == 1 {
					return &tsugaHTTPError{statusCode: code, body: []byte("unavailable")}
				}
				return nil
			},
		})

		_, _ = reconciler.Reconcile(context.Background(), req)
		if phase := getDashboard(t, reconciler, req).Status.Phase; phase != phaseError {
			t.Fatalf("%d: phase after failed resync=%q, want Error", code, phase)
		}

		result, err := reconciler.Reconcile(context.Background(), req)
		if err != nil {
			t.Fatalf("%d: retry reconcile: %v", code, err)
		}
		if updates != 2 {
			t.Fatalf("%d: expected the failed resync to be retried, got %d updates", code, updates)
		}
		if phase := getDashboard(t, reconciler, req).Status.Phase; phase != phaseReady {
			t.Fatalf("%d: phase after retry=%q, want Ready", code, phase)
		}
		if result.RequeueAfter != driftResyncInterval {
			t.Fatalf("%d: RequeueAfter=%v, want %v", code, result.RequeueAfter, driftResyncInterval)
		}
	}
}

func TestTsugaDashboardReconciler_RetriesFailedRecreateAfterResync404(t *testing.T) {
	creates := 0
	reconciler, req := newStaleDashboardReconciler(t, &mockDashboardClient{
		updateFn: func(_ context.Context, _ string, _ []byte) error {
			return &tsugaHTTPError{statusCode: 404, body: []byte("missing")}
		},
		createFn: func(_ context.Context, _ []byte) (string, error) {
			creates++
			if creates == 1 {
				return "", &tsugaHTTPError{statusCode: 503, body: []byte("unavailable")}
			}
			return "dash-recreated", nil
		},
	})

	_, _ = reconciler.Reconcile(context.Background(), req)
	if _, err := reconciler.Reconcile(context.Background(), req); err != nil {
		t.Fatalf("retry reconcile: %v", err)
	}

	current := getDashboard(t, reconciler, req)
	if current.Status.Phase != phaseReady || current.Status.ID != "dash-recreated" {
		t.Fatalf("got phase=%q id=%q, want Ready dash-recreated", current.Status.Phase, current.Status.ID)
	}
}

func TestTsugaDashboardReconciler_PermanentResyncErrorSettles(t *testing.T) {
	updates := 0
	reconciler, req := newStaleDashboardReconciler(t, &mockDashboardClient{
		updateFn: func(_ context.Context, _ string, _ []byte) error {
			updates++
			return &tsugaHTTPError{statusCode: 400, body: []byte("bad query")}
		},
	})

	// Re-reconcile while each pass still changes the object, as the watch would.
	lastRV := ""
	for range 5 {
		result, err := reconciler.Reconcile(context.Background(), req)
		if err != nil {
			t.Fatalf("reconcile: %v", err)
		}
		if result.RequeueAfter != 0 {
			t.Fatalf("expected no requeue for 400, got %v", result.RequeueAfter)
		}
		rv := getDashboard(t, reconciler, req).ResourceVersion
		if rv == lastRV {
			break
		}
		lastRV = rv
	}

	if updates != 2 {
		t.Fatalf("expected the error write to settle after 2 updates, got %d", updates)
	}
}
