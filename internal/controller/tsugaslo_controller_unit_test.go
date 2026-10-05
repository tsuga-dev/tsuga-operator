package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	tsugav1alpha1 "github.com/tsuga-dev/tsuga-operator/api/v1alpha1"
)

const testSLOID = "slo-123"

type mockSLOClient struct {
	findFn   func(context.Context, string, string) (string, error)
	createFn func(context.Context, []byte) (string, error)
	updateFn func(context.Context, string, []byte) error
	deleteFn func(context.Context, string) error
}

func (m *mockSLOClient) FindByTag(ctx context.Context, key, value string) (string, error) {
	if m.findFn == nil {
		return "", nil
	}
	return m.findFn(ctx, key, value)
}

func (m *mockSLOClient) Create(ctx context.Context, payload []byte) (string, error) {
	if m.createFn == nil {
		return "", fmt.Errorf("unexpected create")
	}
	return m.createFn(ctx, payload)
}

func (m *mockSLOClient) Update(ctx context.Context, id string, payload []byte) error {
	if m.updateFn == nil {
		return fmt.Errorf("unexpected update")
	}
	return m.updateFn(ctx, id, payload)
}

func (m *mockSLOClient) Delete(ctx context.Context, id string) error {
	if m.deleteFn == nil {
		return fmt.Errorf("unexpected delete")
	}
	return m.deleteFn(ctx, id)
}

func newTestSLO(name, namespace string) *tsugav1alpha1.SLO {
	obj := &tsugav1alpha1.SLO{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec: tsugav1alpha1.SLOSpec{
			Name:          "Test SLO",
			Owner:         testOwner,
			Permissions:   testPermsAll,
			Target:        99.9,
			TimeframeDays: 30,
			Configuration: apiextensionsv1.JSON{
				Raw: []byte(`{"type":"event","dataSource":"traces","noDataBehavior":"bad","goodQuery":{"queries":[{"aggregate":{"type":"count"},"filter":"service:api status:ok"}],"formula":"q1"},"totalQuery":{"queries":[{"aggregate":{"type":"count"},"filter":"service:api"}],"formula":"q1"}}`),
			},
		},
	}
	obj.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "observability.tsuga.com",
		Version: "v1alpha1",
		Kind:    "SLO",
	})
	return obj
}

// reconcileTwice runs the reconciler twice: the first pass only writes the
// finalizer, the second performs the remote call.
func reconcileTwice(t *testing.T, r *TsugaSLOReconciler, req ctrl.Request) ctrl.Result {
	t.Helper()
	if _, err := r.Reconcile(context.Background(), req); err != nil {
		t.Fatalf("first reconcile: %v", err)
	}
	res, err := r.Reconcile(context.Background(), req)
	if err != nil {
		t.Fatalf("second reconcile: %v", err)
	}
	return res
}

func TestTsugaSLOReconciler_Create(t *testing.T) {
	slo := newTestSLO("test-slo", testNamespace)
	scheme := testScheme(t)

	createCalled := false
	mockClient := &mockSLOClient{
		createFn: func(_ context.Context, payload []byte) (string, error) {
			createCalled = true
			var body map[string]any
			if err := json.Unmarshal(payload, &body); err != nil {
				t.Fatalf("payload is not valid json: %v", err)
			}
			if body["name"] != "Test SLO" {
				t.Fatalf("expected payload name Test SLO, got %v", body["name"])
			}
			if _, present := body["alerts"]; !present {
				t.Fatal("payload must carry the alerts key")
			}
			return testSLOID, nil
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(slo).
		WithStatusSubresource(slo).
		Build()

	reconciler := &TsugaSLOReconciler{Client: fakeClient, TsugaClient: mockClient}
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: slo.Name, Namespace: slo.Namespace}}

	if _, err := reconciler.Reconcile(context.Background(), req); err != nil {
		t.Fatalf("first reconcile: %v", err)
	}
	if createCalled {
		t.Fatal("create should not run before the finalizer is added")
	}

	if _, err := reconciler.Reconcile(context.Background(), req); err != nil {
		t.Fatalf("second reconcile: %v", err)
	}
	if !createCalled {
		t.Fatal("expected create to be called")
	}

	updated := &tsugav1alpha1.SLO{}
	if err := fakeClient.Get(context.Background(), req.NamespacedName, updated); err != nil {
		t.Fatalf("get updated slo: %v", err)
	}
	if updated.Status.ID != testSLOID {
		t.Fatalf("expected status id %q, got %q", testSLOID, updated.Status.ID)
	}
	if updated.Status.Phase != phaseReady {
		t.Fatalf("expected phase Ready, got %q", updated.Status.Phase)
	}
	if updated.Status.ObservedGeneration != updated.Generation {
		t.Fatalf("expected observedGeneration=%d, got %d", updated.Generation, updated.Status.ObservedGeneration)
	}
}

func TestTsugaSLOReconciler_RecreatesOnNotFound(t *testing.T) {
	slo := newTestSLO("gone-slo", testNamespace)
	slo.Generation = 2
	slo.Status.ID = "stale-id"
	slo.Status.ObservedGeneration = 1
	slo.Finalizers = []string{sloFinalizer}
	scheme := testScheme(t)

	mockClient := &mockSLOClient{
		updateFn: func(context.Context, string, []byte) error {
			return &tsugaHTTPError{statusCode: 404, body: []byte("not found")}
		},
		createFn: func(context.Context, []byte) (string, error) {
			return "fresh-id", nil
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(slo).
		WithStatusSubresource(slo).
		Build()

	reconciler := &TsugaSLOReconciler{Client: fakeClient, TsugaClient: mockClient}
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: slo.Name, Namespace: slo.Namespace}}

	if _, err := reconciler.Reconcile(context.Background(), req); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	updated := &tsugav1alpha1.SLO{}
	if err := fakeClient.Get(context.Background(), req.NamespacedName, updated); err != nil {
		t.Fatalf("get updated slo: %v", err)
	}
	if updated.Status.ID != "fresh-id" {
		t.Fatalf("expected the SLO to be recreated with a fresh id, got %q", updated.Status.ID)
	}
	if updated.Status.Phase != phaseReady {
		t.Fatalf("expected phase Ready, got %q", updated.Status.Phase)
	}
}

func TestTsugaSLOReconciler_ClientErrorIsTerminal(t *testing.T) {
	slo := newTestSLO("bad-slo", testNamespace)
	scheme := testScheme(t)

	mockClient := &mockSLOClient{
		createFn: func(context.Context, []byte) (string, error) {
			return "", &tsugaHTTPError{statusCode: 400, body: []byte("bad request")}
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(slo).
		WithStatusSubresource(slo).
		Build()

	reconciler := &TsugaSLOReconciler{Client: fakeClient, TsugaClient: mockClient}
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: slo.Name, Namespace: slo.Namespace}}

	res := reconcileTwice(t, reconciler, req)
	if res.RequeueAfter != 0 {
		t.Fatalf("a 4xx must not requeue, got RequeueAfter=%v", res.RequeueAfter)
	}

	updated := &tsugav1alpha1.SLO{}
	if err := fakeClient.Get(context.Background(), req.NamespacedName, updated); err != nil {
		t.Fatalf("get updated slo: %v", err)
	}
	if updated.Status.Phase != phaseError {
		t.Fatalf("expected phase Error, got %q", updated.Status.Phase)
	}
}

func TestTsugaSLOReconciler_RateLimitRequeues(t *testing.T) {
	slo := newTestSLO("throttled-slo", testNamespace)
	scheme := testScheme(t)

	mockClient := &mockSLOClient{
		createFn: func(context.Context, []byte) (string, error) {
			return "", &tsugaHTTPError{statusCode: 429, body: []byte("slow down")}
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(slo).
		WithStatusSubresource(slo).
		Build()

	reconciler := &TsugaSLOReconciler{Client: fakeClient, TsugaClient: mockClient}
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: slo.Name, Namespace: slo.Namespace}}

	res := reconcileTwice(t, reconciler, req)
	if res.RequeueAfter != rateLimitRequeueAfter {
		t.Fatalf("expected RequeueAfter=%v, got %v", rateLimitRequeueAfter, res.RequeueAfter)
	}
}

func TestTsugaSLOReconciler_DeleteRemovesFinalizer(t *testing.T) {
	now := metav1.Now()
	slo := newTestSLO("doomed-slo", testNamespace)
	slo.Status.ID = testSLOID
	slo.Finalizers = []string{sloFinalizer}
	slo.DeletionTimestamp = &now
	scheme := testScheme(t)

	deletedID := ""
	mockClient := &mockSLOClient{
		deleteFn: func(_ context.Context, id string) error {
			deletedID = id
			return nil
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(slo).
		WithStatusSubresource(slo).
		Build()

	reconciler := &TsugaSLOReconciler{Client: fakeClient, TsugaClient: mockClient}
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: slo.Name, Namespace: slo.Namespace}}

	if _, err := reconciler.Reconcile(context.Background(), req); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if deletedID != testSLOID {
		t.Fatalf("expected the remote SLO %q to be deleted, got %q", testSLOID, deletedID)
	}

	// Removing the last finalizer lets the fake client complete the deletion,
	// so the object is gone rather than merely finalizer-free.
	remaining := &tsugav1alpha1.SLO{}
	err := fakeClient.Get(context.Background(), req.NamespacedName, remaining)
	if err != nil && !errors.IsNotFound(err) {
		t.Fatalf("unexpected error getting the SLO: %v", err)
	}
	if err == nil && len(remaining.Finalizers) != 0 {
		t.Fatalf("expected the finalizer to be removed, got %v", remaining.Finalizers)
	}
}
