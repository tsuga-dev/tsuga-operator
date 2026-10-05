package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/go-logr/zapr"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	tsugav1alpha1 "github.com/tsuga-dev/tsuga-operator/api/v1alpha1"
)

// otelCollectorGVK is the GroupVersionKind of the OTel Operator's
// OpenTelemetryCollector CRD, used by the collection package to render
// unstructured collector objects. The fake client needs it registered
// (as unstructured) so server-side apply patches against it resolve. Reuses
// collectorGVK (defined in tsugacollectorconfig_controller.go) so the two
// stay in sync and the literal isn't duplicated for goconst.
var otelCollectorGVK = collectorGVK

const testMonitorID = "mon-123"

type mockMonitorClient struct {
	findFn   func(context.Context, string, string) (string, error)
	createFn func(context.Context, []byte) (string, error)
	updateFn func(context.Context, string, []byte) error
	deleteFn func(context.Context, string) error
}

func (m *mockMonitorClient) FindByTag(ctx context.Context, key, value string) (string, error) {
	if m.findFn == nil {
		return "", nil
	}
	return m.findFn(ctx, key, value)
}

func (m *mockMonitorClient) Create(ctx context.Context, payload []byte) (string, error) {
	if m.createFn == nil {
		return "", fmt.Errorf("unexpected create")
	}
	return m.createFn(ctx, payload)
}

func (m *mockMonitorClient) Update(ctx context.Context, id string, payload []byte) error {
	if m.updateFn == nil {
		return fmt.Errorf("unexpected update")
	}
	return m.updateFn(ctx, id, payload)
}

func (m *mockMonitorClient) Delete(ctx context.Context, id string) error {
	if m.deleteFn == nil {
		return fmt.Errorf("unexpected delete")
	}
	return m.deleteFn(ctx, id)
}

func testScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(s)
	_ = tsugav1alpha1.AddToScheme(s)

	listGVK := otelCollectorGVK.GroupVersion().WithKind(otelCollectorGVK.Kind + "List")
	s.AddKnownTypeWithName(otelCollectorGVK, &unstructured.Unstructured{})
	s.AddKnownTypeWithName(listGVK, &unstructured.UnstructuredList{})

	// The fake client rejects a kind the scheme does not know, and the
	// collectorconfig reconciler emits TargetAllocator objects alongside the
	// collectors.
	taListGVK := targetAllocatorGVK.GroupVersion().WithKind(targetAllocatorGVK.Kind + "List")
	s.AddKnownTypeWithName(targetAllocatorGVK, &unstructured.Unstructured{})
	s.AddKnownTypeWithName(taListGVK, &unstructured.UnstructuredList{})

	return s
}

func newTestMonitor(name, namespace string) *tsugav1alpha1.Monitor {
	obj := &tsugav1alpha1.Monitor{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec: tsugav1alpha1.MonitorSpec{
			Name:          "Test Monitor",
			Owner:         testOwner,
			Priority:      3,
			Permissions:   testPermsAll,
			Configuration: apiextensionsv1.JSON{Raw: []byte(`{"type":"metric","condition":{"formula":"q1","operator":"greater_than","threshold":1},"noDataBehavior":"resolve","timeframe":5,"groupByFields":[],"queries":[{"aggregate":{"type":"count"},"filter":"service:api"}]}`)},
		},
	}
	obj.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "observability.tsuga.com",
		Version: "v1alpha1",
		Kind:    "Monitor",
	})
	return obj
}

func TestTsugaMonitorReconciler_Create(t *testing.T) {
	monitor := newTestMonitor("test-monitor", testNamespace)
	scheme := testScheme(t)

	createCalled := false
	mockClient := &mockMonitorClient{
		createFn: func(_ context.Context, payload []byte) (string, error) {
			createCalled = true
			var body map[string]any
			if err := json.Unmarshal(payload, &body); err != nil {
				t.Fatalf("payload is not valid json: %v", err)
			}
			if body["name"] != "Test Monitor" {
				t.Fatalf("expected payload name Test Monitor, got %v", body["name"])
			}
			return testMonitorID, nil
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(monitor).
		WithStatusSubresource(monitor).
		Build()

	reconciler := &TsugaMonitorReconciler{
		Client:      fakeClient,
		TsugaClient: mockClient,
	}

	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: monitor.Name, Namespace: monitor.Namespace}}
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

	updated := &tsugav1alpha1.Monitor{}
	if err := fakeClient.Get(context.Background(), req.NamespacedName, updated); err != nil {
		t.Fatalf("get updated monitor: %v", err)
	}
	if updated.Status.ID != testMonitorID {
		t.Fatalf("expected status id mon-123, got %q", updated.Status.ID)
	}
}

func TestTsugaMonitorReconciler_UsesDashboardRef(t *testing.T) {
	monitor := newTestMonitor("test-monitor", testNamespace)
	monitor.Spec.DashboardRef = &tsugav1alpha1.LocalObjectReference{Name: "linked-dashboard"}
	dashboard := &tsugav1alpha1.Dashboard{
		ObjectMeta: metav1.ObjectMeta{Name: "linked-dashboard", Namespace: testNamespace},
		Status:     tsugav1alpha1.SyncStatus{ID: "dash-remote"},
	}
	scheme := testScheme(t)

	var receivedDashboardID string
	mockClient := &mockMonitorClient{
		createFn: func(_ context.Context, payload []byte) (string, error) {
			var body map[string]any
			if err := json.Unmarshal(payload, &body); err != nil {
				t.Fatalf("payload is not valid json: %v", err)
			}
			if rawID, ok := body["dashboardId"]; ok {
				receivedDashboardID, _ = rawID.(string)
			}
			return testMonitorID, nil
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(monitor, dashboard).
		WithStatusSubresource(monitor, dashboard).
		Build()

	reconciler := &TsugaMonitorReconciler{
		Client:      fakeClient,
		TsugaClient: mockClient,
	}

	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: monitor.Name, Namespace: monitor.Namespace}}
	if _, err := reconciler.Reconcile(context.Background(), req); err != nil {
		t.Fatalf("first reconcile: %v", err)
	}
	if _, err := reconciler.Reconcile(context.Background(), req); err != nil {
		t.Fatalf("second reconcile: %v", err)
	}
	if receivedDashboardID != "dash-remote" {
		t.Fatalf("expected resolved dashboardId dash-remote, got %q", receivedDashboardID)
	}
}

func TestTsugaMonitorReconciler_UpdateOnGenerationChange(t *testing.T) {
	monitor := newTestMonitor("test-monitor", testNamespace)
	monitor.Finalizers = []string{monitorFinalizer}
	monitor.Generation = 2
	monitor.Status.ID = "mon-existing"
	monitor.Status.ObservedGeneration = 1
	scheme := testScheme(t)

	updateCalled := false
	mockClient := &mockMonitorClient{
		updateFn: func(_ context.Context, id string, _ []byte) error {
			updateCalled = true
			if id != "mon-existing" {
				t.Fatalf("expected update id mon-existing, got %q", id)
			}
			return nil
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(monitor).
		WithStatusSubresource(monitor).
		Build()

	reconciler := &TsugaMonitorReconciler{
		Client:      fakeClient,
		TsugaClient: mockClient,
	}

	if _, err := reconciler.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: monitor.Name, Namespace: monitor.Namespace}}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if !updateCalled {
		t.Fatal("expected update to be called")
	}
}

func TestTsugaMonitorReconciler_RecreatesAfter404(t *testing.T) {
	monitor := newTestMonitor("test-monitor", testNamespace)
	monitor.Finalizers = []string{monitorFinalizer}
	monitor.Generation = 2
	monitor.Status.ID = "mon-missing"
	monitor.Status.ObservedGeneration = 1
	scheme := testScheme(t)

	createCalled := false
	mockClient := &mockMonitorClient{
		updateFn: func(_ context.Context, _ string, _ []byte) error {
			return &tsugaHTTPError{statusCode: 404, body: []byte("missing")}
		},
		createFn: func(_ context.Context, _ []byte) (string, error) {
			createCalled = true
			return "mon-recreated", nil
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(monitor).
		WithStatusSubresource(monitor).
		Build()

	reconciler := &TsugaMonitorReconciler{
		Client:      fakeClient,
		TsugaClient: mockClient,
	}

	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: monitor.Name, Namespace: monitor.Namespace}}
	if _, err := reconciler.Reconcile(context.Background(), req); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if !createCalled {
		t.Fatal("expected create after 404 on update")
	}
}

func TestTsugaMonitorReconciler_ClientErrorDoesNotBubble(t *testing.T) {
	monitor := newTestMonitor("test-monitor", testNamespace)
	monitor.Finalizers = []string{monitorFinalizer}
	scheme := testScheme(t)

	mockClient := &mockMonitorClient{
		createFn: func(_ context.Context, _ []byte) (string, error) {
			return "", &tsugaHTTPError{statusCode: 422, body: []byte("invalid")}
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(monitor).
		WithStatusSubresource(monitor).
		Build()

	reconciler := &TsugaMonitorReconciler{
		Client:      fakeClient,
		TsugaClient: mockClient,
	}

	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: monitor.Name, Namespace: monitor.Namespace}}
	if _, err := reconciler.Reconcile(context.Background(), req); err != nil {
		t.Fatalf("expected client error to be swallowed, got %v", err)
	}
}

func TestTsugaMonitorReconciler_RateLimitRequeues(t *testing.T) {
	monitor := newTestMonitor("test-monitor", testNamespace)
	monitor.Finalizers = []string{monitorFinalizer}
	scheme := testScheme(t)

	mockClient := &mockMonitorClient{
		createFn: func(_ context.Context, _ []byte) (string, error) {
			return "", &tsugaHTTPError{statusCode: 429, body: []byte("rate limited")}
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(monitor).
		WithStatusSubresource(monitor).
		Build()

	reconciler := &TsugaMonitorReconciler{
		Client:      fakeClient,
		TsugaClient: mockClient,
	}

	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: monitor.Name, Namespace: monitor.Namespace}}
	result, err := reconciler.Reconcile(context.Background(), req)
	if err != nil {
		t.Fatalf("expected rate limit to requeue without error, got %v", err)
	}
	if result.RequeueAfter != rateLimitRequeueAfter {
		t.Fatalf("expected requeueAfter %s, got %s", rateLimitRequeueAfter, result.RequeueAfter)
	}
}

func TestMonitorAdapter_ResolvedSpec_DashboardIDTakesPrecedence(t *testing.T) {
	dashID := "explicit-id"
	obj := &tsugav1alpha1.Monitor{
		ObjectMeta: metav1.ObjectMeta{Name: "test-monitor", Namespace: testNamespace},
		Spec: tsugav1alpha1.MonitorSpec{
			DashboardID:  &dashID,
			DashboardRef: &tsugav1alpha1.LocalObjectReference{Name: "some-dashboard"},
		},
	}

	adapter := &monitorAdapter{}
	resolved, err := adapter.resolvedSpec(context.Background(), obj)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resolved.DashboardID == nil || *resolved.DashboardID != dashID {
		t.Errorf("expected dashboardID %q to win, got %v", dashID, resolved.DashboardID)
	}
}

func TestMonitorAdapter_ResolvedSpec_BothSet_LogsWarning(t *testing.T) {
	core, logs := observer.New(zapcore.InfoLevel)
	logger := zapr.NewLogger(zap.New(core))
	ctx := logf.IntoContext(context.Background(), logger)

	dashID := "explicit-id"
	obj := &tsugav1alpha1.Monitor{
		ObjectMeta: metav1.ObjectMeta{Name: "test-monitor", Namespace: testNamespace},
		Spec: tsugav1alpha1.MonitorSpec{
			DashboardID:  &dashID,
			DashboardRef: &tsugav1alpha1.LocalObjectReference{Name: "some-dashboard"},
		},
	}

	adapter := &monitorAdapter{}
	if _, err := adapter.resolvedSpec(ctx, obj); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if logs.Len() == 0 {
		t.Fatal("expected a warning log to be emitted when both dashboardId and dashboardRef are set")
	}
	found := false
	for _, entry := range logs.All() {
		if strings.Contains(entry.Message, "dashboardRef") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected log containing 'dashboardRef', got: %v", logs.All())
	}
}

func TestTsugaMonitorReconciler_Delete(t *testing.T) {
	now := metav1.Now()
	monitor := newTestMonitor("test-monitor", testNamespace)
	monitor.Finalizers = []string{monitorFinalizer}
	monitor.DeletionTimestamp = &now
	monitor.Status.ID = "mon-delete"
	scheme := testScheme(t)

	deleteCalled := false
	mockClient := &mockMonitorClient{
		deleteFn: func(_ context.Context, id string) error {
			deleteCalled = true
			if id != "mon-delete" {
				t.Fatalf("expected delete id mon-delete, got %q", id)
			}
			return nil
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(monitor).
		WithStatusSubresource(monitor).
		Build()

	reconciler := &TsugaMonitorReconciler{
		Client:      fakeClient,
		TsugaClient: mockClient,
	}

	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: monitor.Name, Namespace: monitor.Namespace}}
	if _, err := reconciler.Reconcile(context.Background(), req); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if !deleteCalled {
		t.Fatal("expected delete to be called")
	}
}

func TestTsugaMonitorReconciler_UpdatesWhenReferencedDashboardRecreated(t *testing.T) {
	monitor := newTestMonitor("test-monitor", testNamespace)
	monitor.Finalizers = []string{monitorFinalizer}
	monitor.Generation = 1
	monitor.Spec.DashboardRef = &tsugav1alpha1.LocalObjectReference{Name: "linked-dashboard"}
	monitor.Status.ID = "mon-existing"
	monitor.Status.ObservedGeneration = 1
	monitor.Status.DashboardID = "dash-old"
	dashboard := &tsugav1alpha1.Dashboard{
		ObjectMeta: metav1.ObjectMeta{Name: "linked-dashboard", Namespace: testNamespace},
		Status:     tsugav1alpha1.SyncStatus{ID: "dash-new"},
	}
	scheme := testScheme(t)

	updateCalls := 0
	var receivedDashboardID string
	mockClient := &mockMonitorClient{
		updateFn: func(_ context.Context, _ string, payload []byte) error {
			updateCalls++
			var body map[string]any
			if err := json.Unmarshal(payload, &body); err != nil {
				t.Fatalf("payload is not valid json: %v", err)
			}
			receivedDashboardID, _ = body["dashboardId"].(string)
			return nil
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(monitor, dashboard).
		WithStatusSubresource(monitor, dashboard).
		Build()

	reconciler := &TsugaMonitorReconciler{
		Client:      fakeClient,
		TsugaClient: mockClient,
	}

	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: monitor.Name, Namespace: monitor.Namespace}}
	if _, err := reconciler.Reconcile(context.Background(), req); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if updateCalls != 1 || receivedDashboardID != "dash-new" {
		t.Fatalf("expected one update with dashboardId dash-new, got %d calls with %q", updateCalls, receivedDashboardID)
	}

	updated := &tsugav1alpha1.Monitor{}
	if err := fakeClient.Get(context.Background(), req.NamespacedName, updated); err != nil {
		t.Fatalf("get updated monitor: %v", err)
	}
	if updated.Status.DashboardID != "dash-new" {
		t.Fatalf("expected status dashboardId dash-new, got %q", updated.Status.DashboardID)
	}

	if _, err := reconciler.Reconcile(context.Background(), req); err != nil {
		t.Fatalf("steady-state reconcile: %v", err)
	}
	if updateCalls != 1 {
		t.Fatalf("expected no update in steady state, got %d calls", updateCalls)
	}
}

func TestTsugaMonitorReconciler_DeletedReferencedDashboardReportsError(t *testing.T) {
	monitor := newTestMonitor("test-monitor", testNamespace)
	monitor.Finalizers = []string{monitorFinalizer}
	monitor.Generation = 1
	monitor.Spec.DashboardRef = &tsugav1alpha1.LocalObjectReference{Name: "deleted-dashboard"}
	monitor.Status.ID = "mon-existing"
	monitor.Status.ObservedGeneration = 1
	monitor.Status.DashboardID = "dash-gone"
	monitor.Status.Phase = phaseReady
	monitor.Status.LastSyncedAt = &metav1.Time{Time: time.Now()}

	fakeClient := fake.NewClientBuilder().
		WithScheme(testScheme(t)).
		WithObjects(monitor).
		WithStatusSubresource(monitor).
		Build()
	reconciler := &TsugaMonitorReconciler{Client: fakeClient, TsugaClient: &mockMonitorClient{}}

	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: monitor.Name, Namespace: monitor.Namespace}}
	if _, err := reconciler.Reconcile(context.Background(), req); err == nil {
		t.Fatal("expected an unresolvable dashboardRef to return an error")
	}
	updated := &tsugav1alpha1.Monitor{}
	if err := fakeClient.Get(context.Background(), req.NamespacedName, updated); err != nil {
		t.Fatalf("get monitor: %v", err)
	}
	if updated.Status.Phase != phaseError || !strings.Contains(updated.Status.Message, "deleted-dashboard") {
		t.Fatalf("phase=%q message=%q, want Error naming the dashboard", updated.Status.Phase, updated.Status.Message)
	}
}

func TestTsugaMonitorReconciler_DoesNotResync(t *testing.T) {
	// A monitor update clears its snooze, so a synced monitor is never
	// re-pushed on a timer.
	monitor := newTestMonitor("test-monitor", testNamespace)
	monitor.Finalizers = []string{monitorFinalizer}
	monitor.Generation = 1
	monitor.Status.ID = "mon-existing"
	monitor.Status.ObservedGeneration = 1
	monitor.Status.Phase = phaseReady
	monitor.Status.LastSyncedAt = &metav1.Time{Time: time.Now().Add(-driftResyncInterval - time.Hour)}

	fakeClient := fake.NewClientBuilder().
		WithScheme(testScheme(t)).
		WithObjects(monitor).
		WithStatusSubresource(monitor).
		Build()
	// No updateFn: an Update call fails the reconcile.
	reconciler := &TsugaMonitorReconciler{Client: fakeClient, TsugaClient: &mockMonitorClient{}}

	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: monitor.Name, Namespace: monitor.Namespace}}
	result, err := reconciler.Reconcile(context.Background(), req)
	if err != nil || result.RequeueAfter != 0 {
		t.Fatalf("result=%+v err=%v, want no update and no requeue", result, err)
	}
}
