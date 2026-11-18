package controller

import (
	"context"
	"maps"
	"strings"
	"testing"
	"time"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	tsugav1alpha1 "github.com/tsuga-dev/tsuga-operator/api/v1alpha1"
)

const (
	driftRemote = `{"name":"Test Dashboard","owner":"team-platform"}`
	driftEdited = `{"name":"Edited in UI","owner":"team-platform"}`
)

func driftBaseline(t *testing.T, remote string, keys ...string) map[string]string {
	t.Helper()
	hashes, err := fieldHashes([]byte(remote), keys)
	if err != nil {
		t.Fatalf("fieldHashes: %v", err)
	}
	return hashes
}

// noDriftBaseline matches the mocks' default GET before any push ({}), so a
// resource carrying it passes the drift check and reaches the push path.
func noDriftBaseline(t *testing.T) map[string]string {
	t.Helper()
	return driftBaseline(t, `{}`, "name")
}

func newBaselinedDashboard(policy tsugav1alpha1.DriftPolicy, lastCheck time.Time, hashes map[string]string) *tsugav1alpha1.Dashboard {
	dashboard := newSyncedDashboard(time.Now().Add(-time.Hour))
	dashboard.Spec.DriftPolicy = policy
	dashboard.Status.RemoteFieldHashes = hashes
	if !lastCheck.IsZero() {
		dashboard.Status.LastDriftCheckAt = &metav1.Time{Time: lastCheck}
	}
	return dashboard
}

func newDriftFakeClient(t *testing.T, obj client.Object) (client.Client, *record.FakeRecorder) {
	t.Helper()
	fakeClient := fake.NewClientBuilder().
		WithScheme(testScheme(t)).
		WithObjects(obj).
		WithStatusSubresource(obj).
		Build()
	return fakeClient, record.NewFakeRecorder(10)
}

func newDriftDashboardReconciler(t *testing.T, dashboard *tsugav1alpha1.Dashboard, mock *mockDashboardClient) (*TsugaDashboardReconciler, ctrl.Request, *record.FakeRecorder) {
	t.Helper()
	fakeClient, recorder := newDriftFakeClient(t, dashboard)
	reconciler := &TsugaDashboardReconciler{Client: fakeClient, TsugaClient: mock, Recorder: recorder}
	return reconciler, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(dashboard)}, recorder
}

func remoteBody(body string) func(context.Context, string) ([]byte, error) {
	return func(context.Context, string) ([]byte, error) { return []byte(body), nil }
}

func drainEvents(recorder *record.FakeRecorder) []string {
	var events []string
	for {
		select {
		case e := <-recorder.Events:
			events = append(events, e)
		default:
			return events
		}
	}
}

func requireEvent(t *testing.T, events []string, parts ...string) {
	t.Helper()
	for _, e := range events {
		matched := true
		for _, p := range parts {
			matched = matched && strings.Contains(e, p)
		}
		if matched {
			return
		}
	}
	t.Fatalf("no event containing %q in %q", parts, events)
}

func TestDrift_NoBaselineRecordsWithoutPush(t *testing.T) {
	dashboard := newBaselinedDashboard(tsugav1alpha1.DriftPolicyOverwrite, time.Time{}, nil)
	// updatedAt is not in the payload, so it must stay out of the baseline.
	remote := `{"name":"Test Dashboard","owner":"team-platform","updatedAt":"2026-11-18T00:00:00Z"}`
	reconciler, req, recorder := newDriftDashboardReconciler(t, dashboard, &mockDashboardClient{getFn: remoteBody(remote)})

	result, err := reconciler.Reconcile(context.Background(), req)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if result.RequeueAfter != defaultDriftCheckInterval {
		t.Fatalf("RequeueAfter=%v, want %v", result.RequeueAfter, defaultDriftCheckInterval)
	}
	current := getDashboard(t, reconciler, req)
	hashes := current.Status.RemoteFieldHashes
	if hashes["name"] != driftBaseline(t, remote, "name")["name"] {
		t.Fatalf("name hash=%q, want the remote's", hashes["name"])
	}
	if _, ok := hashes["graphs"]; !ok {
		t.Fatalf("baseline keys must come from the payload, got %v", hashes)
	}
	if _, ok := hashes["updatedAt"]; ok {
		t.Fatalf("baseline must not include GET-only keys, got %v", hashes)
	}
	if current.Status.ObservedGeneration != 1 {
		t.Fatalf("observedGeneration=%d, want 1 (no push)", current.Status.ObservedGeneration)
	}
	if events := drainEvents(recorder); len(events) != 0 {
		t.Fatalf("expected no event, got %q", events)
	}
}

func TestDrift_NoBaselineWithPendingSpecChangePushesInSamePass(t *testing.T) {
	dashboard := newBaselinedDashboard(tsugav1alpha1.DriftPolicyWarn, time.Time{}, nil)
	dashboard.Generation = 2
	updates := 0
	reconciler, req, recorder := newDriftDashboardReconciler(t, dashboard, &mockDashboardClient{
		getFn: remoteBody(driftRemote),
		updateFn: func(context.Context, string, []byte) error {
			updates++
			return nil
		},
	})

	if _, err := reconciler.Reconcile(context.Background(), req); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if updates != 1 {
		t.Fatalf("updates=%d, want 1", updates)
	}
	current := getDashboard(t, reconciler, req)
	if current.Status.ObservedGeneration != 2 || current.Status.Phase != phaseReady {
		t.Fatalf("observedGeneration=%d phase=%q, want 2 and Ready", current.Status.ObservedGeneration, current.Status.Phase)
	}
	if events := drainEvents(recorder); len(events) != 0 {
		t.Fatalf("expected no event, got %q", events)
	}
}

func TestDrift_NoDriftNoPush(t *testing.T) {
	lastCheck := time.Now().Add(-defaultDriftCheckInterval - time.Minute)
	dashboard := newBaselinedDashboard(tsugav1alpha1.DriftPolicyOverwrite, lastCheck, driftBaseline(t, driftRemote, "name", "owner"))
	reconciler, req, recorder := newDriftDashboardReconciler(t, dashboard, &mockDashboardClient{getFn: remoteBody(driftRemote)})

	result, err := reconciler.Reconcile(context.Background(), req)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if result.RequeueAfter != defaultDriftCheckInterval {
		t.Fatalf("RequeueAfter=%v, want %v", result.RequeueAfter, defaultDriftCheckInterval)
	}
	current := getDashboard(t, reconciler, req)
	if current.Status.LastDriftCheckAt == nil || !current.Status.LastDriftCheckAt.After(lastCheck) {
		t.Fatalf("LastDriftCheckAt=%v, want after %v", current.Status.LastDriftCheckAt, lastCheck)
	}
	if events := drainEvents(recorder); len(events) != 0 {
		t.Fatalf("expected no event, got %q", events)
	}
}

func TestDrift_NotDueMakesNoCall(t *testing.T) {
	dashboard := newBaselinedDashboard(tsugav1alpha1.DriftPolicyOverwrite, time.Now().Add(-time.Minute), driftBaseline(t, driftRemote, "name"))
	reconciler, req, _ := newDriftDashboardReconciler(t, dashboard, &mockDashboardClient{
		getFn: func(context.Context, string) ([]byte, error) {
			t.Fatal("GET before the check is due")
			return nil, nil
		},
	})

	result, err := reconciler.Reconcile(context.Background(), req)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if result.RequeueAfter <= 0 || result.RequeueAfter > 9*time.Minute {
		t.Fatalf("RequeueAfter=%v, want within (0, 9m]", result.RequeueAfter)
	}
}

func TestDrift_OverwriteRevertsAndWarns(t *testing.T) {
	dashboard := newBaselinedDashboard(tsugav1alpha1.DriftPolicyOverwrite, time.Now().Add(-time.Hour), driftBaseline(t, driftRemote, "name", "owner"))
	updated := false
	mock := &mockDashboardClient{updateFn: func(context.Context, string, []byte) error {
		updated = true
		return nil
	}}
	mock.getFn = func(context.Context, string) ([]byte, error) {
		if updated {
			return mock.last, nil
		}
		return []byte(driftEdited), nil
	}
	reconciler, req, recorder := newDriftDashboardReconciler(t, dashboard, mock)

	if _, err := reconciler.Reconcile(context.Background(), req); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if !updated {
		t.Fatal("expected the drift to be reverted with an update")
	}
	requireEvent(t, drainEvents(recorder), "Warning DriftReverted", "name")
	current := getDashboard(t, reconciler, req)
	if current.Status.Phase != phaseReady {
		t.Fatalf("phase=%q, want Ready", current.Status.Phase)
	}
	keys, err := payloadKeys(mock.last)
	if err != nil {
		t.Fatalf("payloadKeys: %v", err)
	}
	if want := driftBaseline(t, string(mock.last), keys...); !maps.Equal(current.Status.RemoteFieldHashes, want) {
		t.Fatalf("hashes=%v, want the post-push GET %v", current.Status.RemoteFieldHashes, want)
	}
}

func TestDrift_WarnDetectsAndBlocks(t *testing.T) {
	baseline := driftBaseline(t, driftRemote, "name", "owner")
	dashboard := newBaselinedDashboard(tsugav1alpha1.DriftPolicyWarn, time.Now().Add(-time.Hour), baseline)
	mock := &mockDashboardClient{getFn: remoteBody(driftEdited)}
	reconciler, req, recorder := newDriftDashboardReconciler(t, dashboard, mock)

	if _, err := reconciler.Reconcile(context.Background(), req); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	current := getDashboard(t, reconciler, req)
	if current.Status.Phase != phaseDrifted {
		t.Fatalf("phase=%q, want Drifted", current.Status.Phase)
	}
	if !strings.Contains(current.Status.Message, "name") || !strings.Contains(current.Status.Message, "accept-remote") {
		t.Fatalf("message=%q, want the drifted field and the accept-remote hint", current.Status.Message)
	}
	requireEvent(t, drainEvents(recorder), "Warning DriftDetected", "name")
	if !maps.Equal(current.Status.RemoteFieldHashes, baseline) {
		t.Fatalf("hashes=%v, want the baseline kept", current.Status.RemoteFieldHashes)
	}

	mock.getFn = func(context.Context, string) ([]byte, error) {
		t.Fatal("GET on a repeat reconcile within the interval")
		return nil, nil
	}
	if _, err := reconciler.Reconcile(context.Background(), req); err != nil {
		t.Fatalf("second reconcile: %v", err)
	}
}

func TestDrift_WarnBlocksSpecChange(t *testing.T) {
	dashboard := newBaselinedDashboard(tsugav1alpha1.DriftPolicyWarn, time.Now().Add(-time.Hour), driftBaseline(t, driftRemote, "name", "owner"))
	dashboard.Generation = 2
	reconciler, req, _ := newDriftDashboardReconciler(t, dashboard, &mockDashboardClient{getFn: remoteBody(driftEdited)})

	if _, err := reconciler.Reconcile(context.Background(), req); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	first := getDashboard(t, reconciler, req)
	if first.Status.ObservedGeneration != 1 {
		t.Fatalf("observedGeneration=%d, want 1", first.Status.ObservedGeneration)
	}

	// The pending spec change forces a check on every pass; an unchanged
	// Drifted status must not be rewritten, or the watch would loop.
	if _, err := reconciler.Reconcile(context.Background(), req); err != nil {
		t.Fatalf("second reconcile: %v", err)
	}
	if rv := getDashboard(t, reconciler, req).ResourceVersion; rv != first.ResourceVersion {
		t.Fatalf("resourceVersion %s -> %s, want no status write", first.ResourceVersion, rv)
	}
}

func TestDrift_WarnClearsWhenUiReverted(t *testing.T) {
	dashboard := newBaselinedDashboard(tsugav1alpha1.DriftPolicyWarn, time.Now().Add(-time.Hour), driftBaseline(t, driftRemote, "name", "owner"))
	dashboard.Status.Phase = phaseDrifted
	reconciler, req, _ := newDriftDashboardReconciler(t, dashboard, &mockDashboardClient{getFn: remoteBody(driftRemote)})

	if _, err := reconciler.Reconcile(context.Background(), req); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if phase := getDashboard(t, reconciler, req).Status.Phase; phase != phaseReady {
		t.Fatalf("phase=%q, want Ready", phase)
	}
}

func TestDrift_SpecChangeWithoutDriftPushes(t *testing.T) {
	dashboard := newBaselinedDashboard(tsugav1alpha1.DriftPolicyWarn, time.Now().Add(-time.Minute), driftBaseline(t, driftRemote, "name", "owner"))
	dashboard.Generation = 2
	updated := false
	reconciler, req, _ := newDriftDashboardReconciler(t, dashboard, &mockDashboardClient{
		getFn: remoteBody(driftRemote),
		updateFn: func(context.Context, string, []byte) error {
			updated = true
			return nil
		},
	})

	if _, err := reconciler.Reconcile(context.Background(), req); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if !updated {
		t.Fatal("expected the spec change to be pushed")
	}
	if gen := getDashboard(t, reconciler, req).Status.ObservedGeneration; gen != 2 {
		t.Fatalf("observedGeneration=%d, want 2", gen)
	}
}

func TestDrift_RemoteDeleted(t *testing.T) {
	notFound := &tsugaHTTPError{statusCode: 404, body: []byte("missing")}

	t.Run("Overwrite", func(t *testing.T) {
		dashboard := newBaselinedDashboard(tsugav1alpha1.DriftPolicyOverwrite, time.Now().Add(-time.Hour), driftBaseline(t, driftRemote, "name"))
		created := false
		mock := &mockDashboardClient{createFn: func(context.Context, []byte) (string, error) {
			created = true
			return "dash-new", nil
		}}
		mock.getFn = func(context.Context, string) ([]byte, error) {
			if created {
				return mock.last, nil
			}
			return nil, notFound
		}
		reconciler, req, recorder := newDriftDashboardReconciler(t, dashboard, mock)

		if _, err := reconciler.Reconcile(context.Background(), req); err != nil {
			t.Fatalf("reconcile: %v", err)
		}
		if !created {
			t.Fatal("expected the deleted dashboard to be recreated")
		}
		requireEvent(t, drainEvents(recorder), "Warning DriftReverted", "deleted in Tsuga")
		if id := getDashboard(t, reconciler, req).Status.ID; id != "dash-new" {
			t.Fatalf("id=%q, want dash-new", id)
		}
	})

	t.Run("Warn", func(t *testing.T) {
		dashboard := newBaselinedDashboard(tsugav1alpha1.DriftPolicyWarn, time.Now().Add(-time.Hour), driftBaseline(t, driftRemote, "name"))
		reconciler, req, recorder := newDriftDashboardReconciler(t, dashboard, &mockDashboardClient{
			getFn: func(context.Context, string) ([]byte, error) { return nil, notFound },
		})

		if _, err := reconciler.Reconcile(context.Background(), req); err != nil {
			t.Fatalf("reconcile: %v", err)
		}
		if phase := getDashboard(t, reconciler, req).Status.Phase; phase != phaseDrifted {
			t.Fatalf("phase=%q, want Drifted", phase)
		}
		requireEvent(t, drainEvents(recorder), "Warning DriftDetected", "deleted in Tsuga")
	})
}

func TestDrift_FailedPushKeepsBaseline(t *testing.T) {
	baseline := driftBaseline(t, driftRemote, "name", "owner")
	dashboard := newBaselinedDashboard(tsugav1alpha1.DriftPolicyOverwrite, time.Now().Add(-time.Hour), baseline)
	reconciler, req, _ := newDriftDashboardReconciler(t, dashboard, &mockDashboardClient{
		getFn: remoteBody(driftEdited),
		updateFn: func(context.Context, string, []byte) error {
			return &tsugaHTTPError{statusCode: 500, body: []byte("boom")}
		},
	})

	_, _ = reconciler.Reconcile(context.Background(), req)
	current := getDashboard(t, reconciler, req)
	if current.Status.Phase != phaseError {
		t.Fatalf("phase=%q, want Error", current.Status.Phase)
	}
	if !maps.Equal(current.Status.RemoteFieldHashes, baseline) {
		t.Fatalf("hashes=%v, want the baseline kept", current.Status.RemoteFieldHashes)
	}
}

func TestMonitorDrift_WarnBlocks(t *testing.T) {
	monitor := newTestMonitor("test-monitor", testNamespace)
	monitor.Finalizers = []string{monitorFinalizer}
	monitor.Generation = 1
	monitor.Spec.DriftPolicy = tsugav1alpha1.DriftPolicyWarn
	monitor.Status.ID = testMonitorID
	monitor.Status.ObservedGeneration = 1
	monitor.Status.Phase = phaseReady
	monitor.Status.RemoteFieldHashes = driftBaseline(t, `{"name":"Test Monitor"}`, "name")
	fakeClient, recorder := newDriftFakeClient(t, monitor)
	reconciler := &TsugaMonitorReconciler{
		Client:      fakeClient,
		TsugaClient: &mockMonitorClient{getFn: remoteBody(`{"name":"Edited in UI"}`)},
		Recorder:    recorder,
	}
	req := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(monitor)}

	if _, err := reconciler.Reconcile(context.Background(), req); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	var current tsugav1alpha1.Monitor
	if err := fakeClient.Get(context.Background(), req.NamespacedName, &current); err != nil {
		t.Fatalf("get monitor: %v", err)
	}
	if current.Status.Phase != phaseDrifted {
		t.Fatalf("phase=%q, want Drifted", current.Status.Phase)
	}
	requireEvent(t, drainEvents(recorder), "Warning DriftDetected", "name")
}

func TestSLODrift_OverwriteReverts(t *testing.T) {
	slo := newTestSLO("test-slo", testNamespace)
	slo.Finalizers = []string{sloFinalizer}
	slo.Generation = 1
	slo.Status.ID = testSLOID
	slo.Status.ObservedGeneration = 1
	slo.Status.Phase = phaseReady
	slo.Status.RemoteFieldHashes = driftBaseline(t, `{"name":"Test SLO"}`, "name")
	updated := false
	mock := &mockSLOClient{updateFn: func(context.Context, string, []byte) error {
		updated = true
		return nil
	}}
	mock.getFn = func(context.Context, string) ([]byte, error) {
		if updated {
			return mock.last, nil
		}
		return []byte(`{"name":"Edited in UI"}`), nil
	}
	fakeClient, recorder := newDriftFakeClient(t, slo)
	reconciler := &TsugaSLOReconciler{Client: fakeClient, TsugaClient: mock, Recorder: recorder}
	req := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(slo)}

	if _, err := reconciler.Reconcile(context.Background(), req); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if !updated {
		t.Fatal("expected the drift to be reverted with an update")
	}
	requireEvent(t, drainEvents(recorder), "Warning DriftReverted", "name")
}

func TestDrift_PostPushGetFailureDoesNotFakeDrift(t *testing.T) {
	dashboard := newBaselinedDashboard(tsugav1alpha1.DriftPolicyWarn, time.Now().Add(-time.Minute), driftBaseline(t, driftRemote, "name", "owner"))
	dashboard.Generation = 2
	updates, gets := 0, 0
	mock := &mockDashboardClient{updateFn: func(context.Context, string, []byte) error {
		updates++
		return nil
	}}
	mock.getFn = func(context.Context, string) ([]byte, error) {
		gets++
		switch {
		case updates == 0:
			return []byte(driftRemote), nil
		case gets == 2:
			return nil, &tsugaHTTPError{statusCode: 500, body: []byte("boom")}
		default:
			return mock.last, nil
		}
	}
	reconciler, req, recorder := newDriftDashboardReconciler(t, dashboard, mock)

	_, _ = reconciler.Reconcile(context.Background(), req)
	current := getDashboard(t, reconciler, req)
	if current.Status.ObservedGeneration != 2 {
		t.Fatalf("observedGeneration=%d, want 2 (the push succeeded)", current.Status.ObservedGeneration)
	}
	if len(current.Status.RemoteFieldHashes) != 0 {
		t.Fatalf("hashes=%v, want cleared so the next pass re-baselines", current.Status.RemoteFieldHashes)
	}

	if _, err := reconciler.Reconcile(context.Background(), req); err != nil {
		t.Fatalf("second reconcile: %v", err)
	}
	current = getDashboard(t, reconciler, req)
	if len(current.Status.RemoteFieldHashes) == 0 {
		t.Fatal("expected a baseline to be recorded")
	}
	if current.Status.Phase != phaseReady {
		t.Fatalf("phase=%q, want Ready (the operator's own push is not drift)", current.Status.Phase)
	}
	for _, e := range drainEvents(recorder) {
		if strings.Contains(e, "DriftDetected") {
			t.Fatalf("unexpected event %q", e)
		}
	}
	if updates != 1 {
		t.Fatalf("updates=%d, want 1", updates)
	}
}

func TestDrift_TransientGetErrorDoesNotRepush(t *testing.T) {
	dashboard := newBaselinedDashboard(tsugav1alpha1.DriftPolicyOverwrite, time.Now().Add(-time.Hour), driftBaseline(t, driftRemote, "name", "owner"))
	failGet := true
	reconciler, req, _ := newDriftDashboardReconciler(t, dashboard, &mockDashboardClient{
		getFn: func(context.Context, string) ([]byte, error) {
			if failGet {
				return nil, &tsugaHTTPError{statusCode: 503, body: []byte("unavailable")}
			}
			return []byte(driftRemote), nil
		},
	})

	_, _ = reconciler.Reconcile(context.Background(), req)
	if phase := getDashboard(t, reconciler, req).Status.Phase; phase != phaseError {
		t.Fatalf("phase after failed GET=%q, want Error", phase)
	}

	// No updateFn: an Update call fails the reconcile.
	failGet = false
	if _, err := reconciler.Reconcile(context.Background(), req); err != nil {
		t.Fatalf("second reconcile: %v", err)
	}
	if phase := getDashboard(t, reconciler, req).Status.Phase; phase != phaseReady {
		t.Fatalf("phase=%q, want Ready", phase)
	}
}

func newAcceptRemoteDashboard(t *testing.T) *tsugav1alpha1.Dashboard {
	t.Helper()
	dashboard := newBaselinedDashboard(tsugav1alpha1.DriftPolicyWarn, time.Now().Add(-time.Minute), driftBaseline(t, driftRemote, "name", "owner"))
	dashboard.Status.Phase = phaseDrifted
	dashboard.Annotations = map[string]string{acceptRemoteAnnotation: "true"}
	return dashboard
}

func TestAcceptRemote_WarnRebaselinesAndRemovesAnnotation(t *testing.T) {
	dashboard := newAcceptRemoteDashboard(t)
	fakeClient := fake.NewClientBuilder().
		WithScheme(testScheme(t)).
		WithObjects(dashboard).
		WithStatusSubresource(dashboard).
		WithInterceptorFuncs(interceptor.Funcs{
			Update: func(context.Context, client.WithWatch, client.Object, ...client.UpdateOption) error {
				t.Fatal("full Update of the CR")
				return nil
			},
		}).
		Build()
	recorder := record.NewFakeRecorder(10)
	updates := 0
	reconciler := &TsugaDashboardReconciler{Client: fakeClient, Recorder: recorder, TsugaClient: &mockDashboardClient{
		getFn: remoteBody(driftEdited),
		updateFn: func(context.Context, string, []byte) error {
			updates++
			return nil
		},
	}}
	req := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(dashboard)}

	if _, err := reconciler.Reconcile(context.Background(), req); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	current := getDashboard(t, reconciler, req)
	payload, err := (dashboardAdapter{}).Payload(context.Background(), current)
	if err != nil {
		t.Fatalf("payload: %v", err)
	}
	keys, err := payloadKeys(payload)
	if err != nil {
		t.Fatalf("payloadKeys: %v", err)
	}
	if want := driftBaseline(t, driftEdited, keys...); !maps.Equal(current.Status.RemoteFieldHashes, want) {
		t.Fatalf("hashes=%v, want the remote's %v", current.Status.RemoteFieldHashes, want)
	}
	if current.Status.Phase != phaseReady {
		t.Fatalf("phase=%q, want Ready", current.Status.Phase)
	}
	requireEvent(t, drainEvents(recorder), "Normal DriftAccepted")
	if _, ok := current.Annotations[acceptRemoteAnnotation]; ok {
		t.Fatalf("annotations=%v, want %s removed", current.Annotations, acceptRemoteAnnotation)
	}
	if updates != 0 {
		t.Fatalf("updates=%d, want 0", updates)
	}
}

func TestAcceptRemote_ThenPendingSpecChangePushes(t *testing.T) {
	dashboard := newAcceptRemoteDashboard(t)
	dashboard.Generation = 2
	updates := 0
	mock := &mockDashboardClient{updateFn: func(context.Context, string, []byte) error {
		updates++
		return nil
	}}
	mock.getFn = func(context.Context, string) ([]byte, error) {
		if updates > 0 {
			return mock.last, nil
		}
		return []byte(driftEdited), nil
	}
	reconciler, req, _ := newDriftDashboardReconciler(t, dashboard, mock)

	if _, err := reconciler.Reconcile(context.Background(), req); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	accepted := getDashboard(t, reconciler, req)
	if updates != 0 {
		t.Fatalf("updates=%d after the accept, want 0", updates)
	}
	if accepted.Status.ObservedGeneration != 1 {
		t.Fatalf("observedGeneration=%d after the accept, want 1", accepted.Status.ObservedGeneration)
	}

	if _, err := reconciler.Reconcile(context.Background(), req); err != nil {
		t.Fatalf("second reconcile: %v", err)
	}
	if updates != 1 {
		t.Fatalf("updates=%d, want 1", updates)
	}
	if gen := getDashboard(t, reconciler, req).Status.ObservedGeneration; gen != 2 {
		t.Fatalf("observedGeneration=%d, want 2", gen)
	}
}

func TestAcceptRemote_RemoteDeletedKeepsAnnotation(t *testing.T) {
	dashboard := newAcceptRemoteDashboard(t)
	reconciler, req, _ := newDriftDashboardReconciler(t, dashboard, &mockDashboardClient{
		getFn: func(context.Context, string) ([]byte, error) {
			return nil, &tsugaHTTPError{statusCode: 404, body: []byte("missing")}
		},
	})

	if _, err := reconciler.Reconcile(context.Background(), req); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	current := getDashboard(t, reconciler, req)
	if current.Annotations[acceptRemoteAnnotation] != "true" {
		t.Fatalf("annotations=%v, want %s kept", current.Annotations, acceptRemoteAnnotation)
	}
	if current.Status.Phase != phaseDrifted {
		t.Fatalf("phase=%q, want Drifted", current.Status.Phase)
	}
}

func TestAcceptRemote_IgnoredWithoutID(t *testing.T) {
	dashboard := newAcceptRemoteDashboard(t)
	dashboard.Status = tsugav1alpha1.DashboardStatus{}
	created := false
	reconciler, req, _ := newDriftDashboardReconciler(t, dashboard, &mockDashboardClient{
		createFn: func(context.Context, []byte) (string, error) {
			created = true
			return "dash-new", nil
		},
	})

	if _, err := reconciler.Reconcile(context.Background(), req); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if !created {
		t.Fatal("expected the normal create path")
	}
	current := getDashboard(t, reconciler, req)
	if current.Status.ID != "dash-new" {
		t.Fatalf("id=%q, want dash-new", current.Status.ID)
	}
	if current.Annotations[acceptRemoteAnnotation] != "true" {
		t.Fatalf("annotations=%v, want %s kept", current.Annotations, acceptRemoteAnnotation)
	}
}

func TestDrift_PrunedStatusDoesNotHotLoop(t *testing.T) {
	dashboard := newBaselinedDashboard(tsugav1alpha1.DriftPolicyOverwrite, time.Time{}, nil)
	// Old CRDs without the drift fields: the apiserver prunes them on write.
	fakeClient := fake.NewClientBuilder().
		WithScheme(testScheme(t)).
		WithObjects(dashboard).
		WithStatusSubresource(dashboard).
		WithInterceptorFuncs(interceptor.Funcs{
			SubResourceUpdate: func(ctx context.Context, c client.Client, sub string, obj client.Object, opts ...client.SubResourceUpdateOption) error {
				if d, ok := obj.(*tsugav1alpha1.Dashboard); ok {
					d.Status.RemoteFieldHashes = nil
					d.Status.LastDriftCheckAt = nil
				}
				return c.SubResource(sub).Update(ctx, obj, opts...)
			},
		}).
		Build()
	gets := 0
	reconciler := &TsugaDashboardReconciler{Client: fakeClient, Recorder: record.NewFakeRecorder(10), TsugaClient: &mockDashboardClient{
		getFn: func(context.Context, string) ([]byte, error) {
			gets++
			return []byte(driftRemote), nil
		},
	}}
	req := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(dashboard)}

	for pass := 1; pass <= 2; pass++ {
		before := gets
		result, err := reconciler.Reconcile(context.Background(), req)
		if err != nil {
			t.Fatalf("pass %d: reconcile: %v", pass, err)
		}
		if result.RequeueAfter < defaultDriftCheckInterval-time.Second {
			t.Fatalf("pass %d: RequeueAfter=%v, want about %v", pass, result.RequeueAfter, defaultDriftCheckInterval)
		}
		if n := gets - before; n > 1 {
			t.Fatalf("pass %d: %d GETs, want at most 1", pass, n)
		}
	}
}

func TestDrift_OwnPushNotReportedAsDrift(t *testing.T) {
	// The baseline predates a push whose status write failed: Tsuga already
	// holds the new spec, but ObservedGeneration was never advanced.
	dashboard := newBaselinedDashboard(tsugav1alpha1.DriftPolicyWarn, time.Now().Add(-time.Minute), driftBaseline(t, `{"name":"Old name"}`, "name"))
	dashboard.Generation = 2
	pushed, err := (dashboardAdapter{}).Payload(context.Background(), dashboard.DeepCopy())
	if err != nil {
		t.Fatalf("payload: %v", err)
	}
	updates := 0
	reconciler, req, recorder := newDriftDashboardReconciler(t, dashboard, &mockDashboardClient{
		getFn: remoteBody(string(pushed)),
		updateFn: func(context.Context, string, []byte) error {
			updates++
			return nil
		},
	})

	if _, err := reconciler.Reconcile(context.Background(), req); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	for _, e := range drainEvents(recorder) {
		if strings.Contains(e, "DriftDetected") {
			t.Fatalf("unexpected event %q", e)
		}
	}
	current := getDashboard(t, reconciler, req)
	if current.Status.Phase == phaseDrifted {
		t.Fatalf("phase=%q, the operator's own push must not count as drift", current.Status.Phase)
	}
	if current.Status.ObservedGeneration != 2 {
		t.Fatalf("observedGeneration=%d, want 2", current.Status.ObservedGeneration)
	}
	if updates != 1 {
		t.Fatalf("updates=%d, want 1 (the pending spec is pushed)", updates)
	}
}

func TestDrift_NoBaselineRemoteDeletedDoesNotRecreate(t *testing.T) {
	for _, policy := range []tsugav1alpha1.DriftPolicy{tsugav1alpha1.DriftPolicyOverwrite, tsugav1alpha1.DriftPolicyWarn} {
		t.Run(string(policy), func(t *testing.T) {
			dashboard := newBaselinedDashboard(policy, time.Time{}, nil)
			reconciler, req, recorder := newDriftDashboardReconciler(t, dashboard, &mockDashboardClient{
				getFn: func(context.Context, string) ([]byte, error) {
					return nil, &tsugaHTTPError{statusCode: 404, body: []byte("missing")}
				},
				createFn: func(context.Context, []byte) (string, error) {
					t.Fatal("create without a baseline or a spec change")
					return "", nil
				},
			})

			result, err := reconciler.Reconcile(context.Background(), req)
			if err != nil {
				t.Fatalf("reconcile: %v", err)
			}
			if result.RequeueAfter != defaultDriftCheckInterval {
				t.Fatalf("RequeueAfter=%v, want %v", result.RequeueAfter, defaultDriftCheckInterval)
			}
			current := getDashboard(t, reconciler, req)
			if current.Status.Phase != phaseDrifted {
				t.Fatalf("phase=%q, want Drifted", current.Status.Phase)
			}
			if !strings.Contains(current.Status.Message, "before the operator recorded a baseline") {
				t.Fatalf("message=%q, want the no-baseline deletion message", current.Status.Message)
			}
			requireEvent(t, drainEvents(recorder), "Warning DriftDetected", "deleted in Tsuga")
		})
	}
}

func TestDrift_NoBaselineRemoteDeletedRecreatesOnSpecChange(t *testing.T) {
	dashboard := newBaselinedDashboard(tsugav1alpha1.DriftPolicyOverwrite, time.Time{}, nil)
	dashboard.Generation = 2
	created := false
	mock := &mockDashboardClient{createFn: func(context.Context, []byte) (string, error) {
		created = true
		return "dash-new", nil
	}}
	mock.getFn = func(context.Context, string) ([]byte, error) {
		if created {
			return mock.last, nil
		}
		return nil, &tsugaHTTPError{statusCode: 404, body: []byte("missing")}
	}
	reconciler, req, _ := newDriftDashboardReconciler(t, dashboard, mock)

	if _, err := reconciler.Reconcile(context.Background(), req); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if !created {
		t.Fatal("expected the spec change to recreate the dashboard")
	}
	current := getDashboard(t, reconciler, req)
	if current.Status.ID != "dash-new" || current.Status.ObservedGeneration != 2 {
		t.Fatalf("id=%q observedGeneration=%d, want dash-new and 2", current.Status.ID, current.Status.ObservedGeneration)
	}
}

func TestAcceptRemote_UnderOverwrite(t *testing.T) {
	dashboard := newAcceptRemoteDashboard(t)
	dashboard.Spec.DriftPolicy = tsugav1alpha1.DriftPolicyOverwrite
	updates := 0
	reconciler, req, recorder := newDriftDashboardReconciler(t, dashboard, &mockDashboardClient{
		getFn: remoteBody(driftEdited),
		updateFn: func(context.Context, string, []byte) error {
			updates++
			return nil
		},
	})

	if _, err := reconciler.Reconcile(context.Background(), req); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	current := getDashboard(t, reconciler, req)
	if current.Status.RemoteFieldHashes["name"] != driftBaseline(t, driftEdited, "name")["name"] {
		t.Fatalf("hashes=%v, want the remote's", current.Status.RemoteFieldHashes)
	}
	if current.Status.Phase != phaseReady {
		t.Fatalf("phase=%q, want Ready", current.Status.Phase)
	}
	if _, ok := current.Annotations[acceptRemoteAnnotation]; ok {
		t.Fatalf("annotations=%v, want %s removed", current.Annotations, acceptRemoteAnnotation)
	}
	requireEvent(t, drainEvents(recorder), "Normal DriftAccepted")
	if updates != 0 {
		t.Fatalf("updates=%d, want 0", updates)
	}

	if _, err := reconciler.Reconcile(context.Background(), req); err != nil {
		t.Fatalf("second reconcile: %v", err)
	}
	if updates != 0 {
		t.Fatalf("updates=%d after the next pass, want 0 (accepted remote is not reverted)", updates)
	}
}

func TestDrift_BaselineClearsStaleDrifted(t *testing.T) {
	dashboard := newBaselinedDashboard(tsugav1alpha1.DriftPolicyOverwrite, time.Time{}, nil)
	dashboard.Status.Phase = phaseDrifted
	dashboard.Status.Message = "deleted in Tsuga before the operator recorded a baseline; change the CR to recreate it"
	mock := &mockDashboardClient{
		getFn:    remoteBody(driftRemote),
		updateFn: func(context.Context, string, []byte) error { t.Fatal("unexpected update"); return nil },
		createFn: func(context.Context, []byte) (string, error) { t.Fatal("unexpected create"); return "", nil },
	}
	reconciler, req, _ := newDriftDashboardReconciler(t, dashboard, mock)

	if _, err := reconciler.Reconcile(context.Background(), req); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	current := getDashboard(t, reconciler, req)
	if current.Status.Phase != phaseReady {
		t.Fatalf("phase=%q, want Ready", current.Status.Phase)
	}
	if len(current.Status.RemoteFieldHashes) == 0 {
		t.Fatal("expected a baseline to be recorded")
	}
}

func TestDrift_PrunedStatusBaselineClearsStaleError(t *testing.T) {
	dashboard := newBaselinedDashboard(tsugav1alpha1.DriftPolicyOverwrite, time.Time{}, nil)
	dashboard.Status.Phase = phaseError
	fakeClient := fake.NewClientBuilder().
		WithScheme(testScheme(t)).
		WithObjects(dashboard).
		WithStatusSubresource(dashboard).
		WithInterceptorFuncs(interceptor.Funcs{
			SubResourceUpdate: func(ctx context.Context, c client.Client, sub string, obj client.Object, opts ...client.SubResourceUpdateOption) error {
				if d, ok := obj.(*tsugav1alpha1.Dashboard); ok {
					d.Status.RemoteFieldHashes = nil
					d.Status.LastDriftCheckAt = nil
				}
				return c.SubResource(sub).Update(ctx, obj, opts...)
			},
		}).
		Build()
	reconciler := &TsugaDashboardReconciler{Client: fakeClient, Recorder: record.NewFakeRecorder(10), TsugaClient: &mockDashboardClient{
		getFn: remoteBody(driftRemote),
	}}
	req := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(dashboard)}

	if _, err := reconciler.Reconcile(context.Background(), req); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if phase := getDashboard(t, reconciler, req).Status.Phase; phase != phaseReady {
		t.Fatalf("phase=%q, want Ready", phase)
	}
}

func TestSLODrift_OwnPushWithServerAlertIDsNotReportedAsDrift(t *testing.T) {
	// Tsuga recreates SLO alerts on every update and returns them with an id
	// and sloId the payload never carries.
	slo := newTestSLO("test-slo", testNamespace)
	slo.Finalizers = []string{sloFinalizer}
	slo.Generation = 2
	slo.Spec.DriftPolicy = tsugav1alpha1.DriftPolicyWarn
	slo.Spec.Alerts = []tsugav1alpha1.SLOAlert{
		{Priority: 2, Configuration: apiextensionsv1.JSON{Raw: []byte(`{"type":"burn-rate","burnRate":14.4}`)}},
	}
	slo.Status.ID = testSLOID
	slo.Status.ObservedGeneration = 1
	slo.Status.Phase = phaseReady
	slo.Status.LastDriftCheckAt = &metav1.Time{Time: time.Now().Add(-time.Minute)}
	slo.Status.RemoteFieldHashes = driftBaseline(t,
		`{"name":"Test SLO","alerts":[{"id":"old","sloId":"`+testSLOID+`","priority":1,"configuration":{"type":"burn-rate","burnRate":2}}]}`,
		"name", "alerts")
	pushed, err := (sloAdapter{}).Payload(context.Background(), slo.DeepCopy())
	if err != nil {
		t.Fatalf("payload: %v", err)
	}
	remote := strings.Replace(string(pushed), `"alerts":[{`, `"alerts":[{"id":"new","sloId":"`+testSLOID+`",`, 1)
	updates := 0
	mock := &mockSLOClient{updateFn: func(context.Context, string, []byte) error {
		updates++
		return nil
	}}
	mock.getFn = remoteBody(remote)
	fakeClient, recorder := newDriftFakeClient(t, slo)
	reconciler := &TsugaSLOReconciler{Client: fakeClient, TsugaClient: mock, Recorder: recorder}
	req := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(slo)}

	if _, err := reconciler.Reconcile(context.Background(), req); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	for _, e := range drainEvents(recorder) {
		if strings.Contains(e, "DriftDetected") {
			t.Fatalf("unexpected event %q", e)
		}
	}
	if updates != 1 {
		t.Fatalf("updates=%d, want 1 (the pending spec is pushed)", updates)
	}
}
