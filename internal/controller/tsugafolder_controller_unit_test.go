package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	tsugav1alpha1 "github.com/tsuga-dev/tsuga-operator/api/v1alpha1"
)

func newTestMonitorFolder(name string) *tsugav1alpha1.MonitorFolder {
	return &tsugav1alpha1.MonitorFolder{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: testNamespace},
		Spec:       tsugav1alpha1.MonitorFolderSpec{Name: "Checkout", Owner: testOwner},
	}
}

// syncedMonitorFolder is a Ready folder whose spec generation was pushed.
func syncedMonitorFolder(name, id string) *tsugav1alpha1.MonitorFolder {
	folder := newTestMonitorFolder(name)
	folder.Finalizers = []string{monitorFolderFinalizer}
	folder.Generation = 1
	folder.Status.ID = id
	folder.Status.ObservedGeneration = 1
	folder.Status.Phase = phaseReady
	folder.Status.LastSyncedAt = &metav1.Time{Time: time.Now()}
	return folder
}

func folderReconcile(t *testing.T, objs []client.Object, tsuga TsugaResourceClient, name string) (*tsugav1alpha1.MonitorFolder, ctrl.Result, error) {
	t.Helper()
	fakeClient := fake.NewClientBuilder().
		WithScheme(testScheme(t)).
		WithObjects(objs...).
		WithStatusSubresource(objs...).
		Build()
	reconciler := &TsugaMonitorFolderReconciler{Client: fakeClient, TsugaClient: tsuga}
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: name, Namespace: testNamespace}}
	result, err := reconciler.Reconcile(context.Background(), req)
	updated := &tsugav1alpha1.MonitorFolder{}
	if getErr := fakeClient.Get(context.Background(), req.NamespacedName, updated); getErr != nil {
		t.Fatalf("get folder: %v", getErr)
	}
	return updated, result, err
}

// capturePayloads records every create/update body as a decoded map.
func capturePayloads(t *testing.T, bodies *[]map[string]any) *mockMonitorClient {
	decode := func(payload []byte) {
		var body map[string]any
		if err := json.Unmarshal(payload, &body); err != nil {
			t.Fatalf("payload is not valid json: %v", err)
		}
		*bodies = append(*bodies, body)
	}
	return &mockMonitorClient{
		createFn: func(_ context.Context, payload []byte) (string, error) {
			decode(payload)
			return "folder-new", nil
		},
		updateFn: func(_ context.Context, _ string, payload []byte) error {
			decode(payload)
			return nil
		},
	}
}

func TestMonitorFolderReconciler_CreateTopLevel(t *testing.T) {
	folder := newTestMonitorFolder("checkout")
	folder.Finalizers = []string{monitorFolderFinalizer}
	var bodies []map[string]any

	updated, _, err := folderReconcile(t, []client.Object{folder}, capturePayloads(t, &bodies), "checkout")
	if err != nil {
		t.Fatal(err)
	}
	if len(bodies) != 1 || bodies[0]["name"] != "Checkout" || bodies[0]["owner"] != testOwner {
		t.Fatalf("payloads = %v, want one create with name and owner", bodies)
	}
	if _, ok := bodies[0]["parentFolderId"]; ok {
		t.Errorf("top-level folder sent parentFolderId %v", bodies[0]["parentFolderId"])
	}
	if updated.Status.ID != "folder-new" || updated.Status.Phase != phaseReady {
		t.Errorf("status id=%q phase=%q, want folder-new Ready", updated.Status.ID, updated.Status.Phase)
	}
}

func TestMonitorFolderReconciler_UsesParentFolderRef(t *testing.T) {
	parent := syncedMonitorFolder("parent", "p1")
	child := newTestMonitorFolder("child")
	child.Finalizers = []string{monitorFolderFinalizer}
	child.Spec.ParentFolderRef = &tsugav1alpha1.LocalObjectReference{Name: "parent"}
	var bodies []map[string]any

	updated, _, err := folderReconcile(t, []client.Object{parent, child}, capturePayloads(t, &bodies), "child")
	if err != nil {
		t.Fatal(err)
	}
	if len(bodies) != 1 || bodies[0]["parentFolderId"] != "p1" {
		t.Fatalf("payloads = %v, want parentFolderId p1", bodies)
	}
	if updated.Status.ParentFolderID != "p1" {
		t.Errorf("status parentFolderId = %q, want p1", updated.Status.ParentFolderID)
	}
}

func TestMonitorFolderReconciler_ParentIDTakesPrecedence(t *testing.T) {
	parent := syncedMonitorFolder("parent", "p1")
	child := newTestMonitorFolder("child")
	child.Finalizers = []string{monitorFolderFinalizer}
	child.Spec.ParentFolderID = ptr.To("p-raw")
	child.Spec.ParentFolderRef = &tsugav1alpha1.LocalObjectReference{Name: "parent"}
	var bodies []map[string]any

	updated, _, err := folderReconcile(t, []client.Object{parent, child}, capturePayloads(t, &bodies), "child")
	if err != nil {
		t.Fatal(err)
	}
	if len(bodies) != 1 || bodies[0]["parentFolderId"] != "p-raw" {
		t.Fatalf("payloads = %v, want parentFolderId p-raw", bodies)
	}
	if updated.Status.ParentFolderID != "" {
		t.Errorf("status parentFolderId = %q, want empty for a raw ID", updated.Status.ParentFolderID)
	}
}

func TestMonitorFolderReconciler_UpdatesWhenParentRecreated(t *testing.T) {
	parent := syncedMonitorFolder("parent", "p-new")
	child := syncedMonitorFolder("child", "c1")
	child.Spec.ParentFolderRef = &tsugav1alpha1.LocalObjectReference{Name: "parent"}
	child.Status.ParentFolderID = "p-old"
	var bodies []map[string]any

	updated, _, err := folderReconcile(t, []client.Object{parent, child}, capturePayloads(t, &bodies), "child")
	if err != nil {
		t.Fatal(err)
	}
	if len(bodies) != 1 || bodies[0]["parentFolderId"] != "p-new" {
		t.Fatalf("payloads = %v, want one update with parentFolderId p-new", bodies)
	}
	if updated.Status.ParentFolderID != "p-new" {
		t.Errorf("status parentFolderId = %q, want p-new", updated.Status.ParentFolderID)
	}
}

func TestMonitorFolderReconciler_MissingParentReportsError(t *testing.T) {
	child := syncedMonitorFolder("child", "c1")
	child.Spec.ParentFolderRef = &tsugav1alpha1.LocalObjectReference{Name: "missing-parent"}

	updated, _, err := folderReconcile(t, []client.Object{child}, &mockMonitorClient{}, "child")
	if err == nil {
		t.Fatal("expected an unresolvable parentFolderRef to return an error")
	}
	if updated.Status.Phase != phaseError || !strings.Contains(updated.Status.Message, "missing-parent") {
		t.Fatalf("phase=%q message=%q, want Error naming the parent", updated.Status.Phase, updated.Status.Message)
	}
}

func TestMonitorFolderReconciler_ClientErrorDoesNotRequeueHot(t *testing.T) {
	child := syncedMonitorFolder("child", "c1")
	child.Generation = 2
	tsuga := &mockMonitorClient{updateFn: func(context.Context, string, []byte) error {
		return &tsugaHTTPError{statusCode: 400}
	}}

	updated, result, err := folderReconcile(t, []client.Object{child}, tsuga, "child")
	if err != nil || result.RequeueAfter != 0 {
		t.Fatalf("result=%+v err=%v, want no error and no requeue", result, err)
	}
	if updated.Status.Phase != phaseError {
		t.Errorf("phase = %q, want Error", updated.Status.Phase)
	}
}

func TestMonitorFolderReconciler_RevertsEditsMadeInTsuga(t *testing.T) {
	folder := syncedMonitorFolder("checkout", "f1")
	folder.Status.RemoteFieldHashes = driftBaseline(t, `{"name":"Checkout"}`, "name")
	updates := 0
	tsuga := &mockMonitorClient{
		getFn: remoteBody(`{"name":"Renamed in the UI"}`),
		updateFn: func(context.Context, string, []byte) error {
			updates++
			return nil
		},
	}

	if _, _, err := folderReconcile(t, []client.Object{folder}, tsuga, "checkout"); err != nil {
		t.Fatal(err)
	}
	if updates != 1 {
		t.Fatalf("updates = %d, want the UI rename reverted", updates)
	}
}

func TestDashboardFolderReconciler_UsesParentFolderRef(t *testing.T) {
	parent := &tsugav1alpha1.DashboardFolder{
		ObjectMeta: metav1.ObjectMeta{Name: "parent", Namespace: testNamespace},
		Spec:       tsugav1alpha1.DashboardFolderSpec{Name: "Parent", Owner: testOwner},
		Status:     tsugav1alpha1.DashboardFolderStatus{SyncStatus: tsugav1alpha1.SyncStatus{ID: "p1"}},
	}
	child := &tsugav1alpha1.DashboardFolder{
		ObjectMeta: metav1.ObjectMeta{Name: "child", Namespace: testNamespace, Finalizers: []string{dashboardFolderFinalizer}},
		Spec: tsugav1alpha1.DashboardFolderSpec{
			Name: "Child", Owner: testOwner,
			ParentFolderRef: &tsugav1alpha1.LocalObjectReference{Name: "parent"},
		},
	}
	var bodies []map[string]any
	fakeClient := fake.NewClientBuilder().
		WithScheme(testScheme(t)).
		WithObjects(parent, child).
		WithStatusSubresource(parent, child).
		Build()
	reconciler := &TsugaDashboardFolderReconciler{Client: fakeClient, TsugaClient: capturePayloads(t, &bodies)}
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "child", Namespace: testNamespace}}

	if _, err := reconciler.Reconcile(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if len(bodies) != 1 || bodies[0]["parentFolderId"] != "p1" {
		t.Fatalf("payloads = %v, want parentFolderId p1", bodies)
	}
}

func TestFolderMapFuncs_EnqueueReferrers(t *testing.T) {
	parent := syncedMonitorFolder("checkout", "f1")
	child := newTestMonitorFolder("child")
	child.Spec.ParentFolderRef = &tsugav1alpha1.LocalObjectReference{Name: "checkout"}
	unrelated := newTestMonitorFolder("other")
	filed := newTestMonitor("filed", testNamespace)
	filed.Spec.FolderRef = &tsugav1alpha1.LocalObjectReference{Name: "checkout"}
	unfiled := newTestMonitor("unfiled", testNamespace)
	fakeClient := fake.NewClientBuilder().
		WithScheme(testScheme(t)).
		WithObjects(parent, child, unrelated, filed, unfiled).
		Build()
	ctx := context.Background()

	children := (&TsugaMonitorFolderReconciler{Client: fakeClient}).parentToChildren(ctx, parent)
	if len(children) != 1 || children[0].Name != "child" {
		t.Errorf("parentToChildren = %v, want only child", children)
	}
	monitors := (&TsugaMonitorReconciler{Client: fakeClient}).folderToMonitors(ctx, parent)
	if len(monitors) != 1 || monitors[0].Name != "filed" {
		t.Errorf("folderToMonitors = %v, want only filed", monitors)
	}
}

// notEmptyBody is the Tsuga API response to deleting a non-empty monitor
// folder, as returned by the API on 2026-11-18.
const notEmptyBody = `{"requestId":"r","error":{"message":"Monitor folder (ID: f1) is not empty. Remove its monitors and subfolders before deleting it.","statusCode":400,"code":"BAD_REQUEST"}}`

func deletingMonitorFolder(name, id string) *tsugav1alpha1.MonitorFolder {
	folder := syncedMonitorFolder(name, id)
	folder.DeletionTimestamp = &metav1.Time{Time: time.Now()}
	return folder
}

// deleteReconcile reconciles a deleting folder; found is false once the
// finalizer is removed and the fake client drops the object.
func deleteReconcile(t *testing.T, objs []client.Object, deleteErr error, name string) (folder *tsugav1alpha1.MonitorFolder, found bool, result ctrl.Result, deletes int, err error) {
	t.Helper()
	return deleteReconcileSeq(t, objs, []error{deleteErr}, name)
}

// deleteReconcileSeq answers successive remote deletes with deleteErrs,
// repeating the last one.
func deleteReconcileSeq(t *testing.T, objs []client.Object, deleteErrs []error, name string) (folder *tsugav1alpha1.MonitorFolder, found bool, result ctrl.Result, deletes int, err error) {
	t.Helper()
	fakeClient := fake.NewClientBuilder().
		WithScheme(testScheme(t)).
		WithObjects(objs...).
		WithStatusSubresource(objs...).
		Build()
	tsuga := &mockMonitorClient{deleteFn: func(context.Context, string) error {
		deletes++
		return deleteErrs[min(deletes, len(deleteErrs))-1]
	}}
	reconciler := &TsugaMonitorFolderReconciler{Client: fakeClient, TsugaClient: tsuga}
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: name, Namespace: testNamespace}}
	result, err = reconciler.Reconcile(context.Background(), req)
	folder = &tsugav1alpha1.MonitorFolder{}
	getErr := fakeClient.Get(context.Background(), req.NamespacedName, folder)
	return folder, getErr == nil, result, deletes, err
}

func TestDeleteClientError_ShowsReasonAndRequeuesSteadily(t *testing.T) {
	folder := deletingMonitorFolder("checkout", "f1")

	updated, found, result, _, err := deleteReconcile(t, []client.Object{folder},
		&tsugaHTTPError{statusCode: 400, body: []byte(notEmptyBody)}, "checkout")
	if err != nil || result.RequeueAfter != rateLimitRequeueAfter {
		t.Fatalf("result=%+v err=%v, want a steady %s requeue", result, err, rateLimitRequeueAfter)
	}
	if !found || !slices.Contains(updated.Finalizers, monitorFolderFinalizer) {
		t.Fatal("finalizer must be kept while Tsuga refuses the delete")
	}
	if updated.Status.Phase != phaseDeleting || !strings.Contains(updated.Status.Message, "is not empty") {
		t.Errorf("phase=%q message=%q, want Deleting with the API reason", updated.Status.Phase, updated.Status.Message)
	}
}

func TestDeleteServerError_KeepsBackoff(t *testing.T) {
	folder := deletingMonitorFolder("checkout", "f1")

	_, found, result, _, err := deleteReconcile(t, []client.Object{folder}, &tsugaHTTPError{statusCode: 503}, "checkout")
	if err == nil || result.RequeueAfter != 0 {
		t.Fatalf("result=%+v err=%v, want the error returned for workqueue backoff", result, err)
	}
	if !found {
		t.Fatal("finalizer must be kept on a server error")
	}
}

func orphanableMonitorFolder() *tsugav1alpha1.MonitorFolder {
	folder := deletingMonitorFolder("checkout", "f1")
	folder.Annotations = map[string]string{orphanIfNotEmptyAnnotation: "true"}
	return folder
}

func notEmptyErr() error { return &tsugaHTTPError{statusCode: 400, body: []byte(notEmptyBody)} }

func TestOrphanIfNotEmpty_NoReferrers_RemovesFinalizerWithoutDelete(t *testing.T) {
	_, found, _, deletes, err := deleteReconcile(t, []client.Object{orphanableMonitorFolder()}, notEmptyErr(), "checkout")
	if err != nil {
		t.Fatal(err)
	}
	if found {
		t.Fatal("expected the finalizer removed so the resource is gone")
	}
	if deletes != 2 {
		t.Errorf("remote delete calls = %d, want 2 (a retry confirms it is still not empty)", deletes)
	}
}

func TestOrphanIfNotEmpty_WithoutAnnotation_Waits(t *testing.T) {
	_, found, _, _, _ := deleteReconcile(t, []client.Object{deletingMonitorFolder("checkout", "f1")}, notEmptyErr(), "checkout")
	if !found {
		t.Fatal("a folder without the annotation must not be orphaned")
	}
}

func TestOrphanIfNotEmpty_OtherValue_Waits(t *testing.T) {
	folder := orphanableMonitorFolder()
	folder.Annotations[orphanIfNotEmptyAnnotation] = "yes"
	_, found, _, _, _ := deleteReconcile(t, []client.Object{folder}, notEmptyErr(), "checkout")
	if !found {
		t.Fatal(`only the value "true" may orphan`)
	}
}

func TestOrphanIfNotEmpty_DeletingReferrerBlocks(t *testing.T) {
	monitor := newTestMonitor("checkout-errors", testNamespace)
	monitor.Spec.FolderRef = &tsugav1alpha1.LocalObjectReference{Name: "checkout"}
	monitor.Finalizers = []string{monitorFinalizer}
	monitor.DeletionTimestamp = &metav1.Time{Time: time.Now()}

	updated, found, result, _, err := deleteReconcile(t, []client.Object{orphanableMonitorFolder(), monitor}, notEmptyErr(), "checkout")
	if err != nil || result.RequeueAfter != rateLimitRequeueAfter {
		t.Fatalf("result=%+v err=%v, want a steady requeue", result, err)
	}
	if !found {
		t.Fatal("a folder whose monitor is still being deleted must not be orphaned")
	}
	if !strings.Contains(updated.Status.Message, "Monitor/checkout-errors") {
		t.Errorf("message = %q, want it to name the blocking monitor", updated.Status.Message)
	}
}

func TestOrphanIfNotEmpty_LiveReferrerBlocks(t *testing.T) {
	monitor := newTestMonitor("checkout-errors", testNamespace)
	monitor.Spec.FolderRef = &tsugav1alpha1.LocalObjectReference{Name: "checkout"}

	_, found, _, _, _ := deleteReconcile(t, []client.Object{orphanableMonitorFolder(), monitor}, notEmptyErr(), "checkout")
	if !found {
		t.Fatal("a folder still referenced by a live monitor must not be orphaned")
	}
}

func TestOrphanIfNotEmpty_RawIDReferrerAndChildFolderBlock(t *testing.T) {
	monitor := newTestMonitor("by-id", testNamespace)
	monitor.Spec.FolderID = ptr.To("f1")
	child := newTestMonitorFolder("child")
	child.Spec.ParentFolderRef = &tsugav1alpha1.LocalObjectReference{Name: "checkout"}
	unrelated := newTestMonitor("elsewhere", testNamespace)
	unrelated.Spec.FolderID = ptr.To("f-other")

	updated, found, _, _, _ := deleteReconcile(t, []client.Object{orphanableMonitorFolder(), monitor, child, unrelated}, notEmptyErr(), "checkout")
	if !found {
		t.Fatal("referrers by raw ID or parentFolderRef must block orphaning")
	}
	for _, want := range []string{"Monitor/by-id", "MonitorFolder/child"} {
		if !strings.Contains(updated.Status.Message, want) {
			t.Errorf("message = %q, want it to name %s", updated.Status.Message, want)
		}
	}
	if strings.Contains(updated.Status.Message, "elsewhere") {
		t.Errorf("message = %q names an unrelated monitor", updated.Status.Message)
	}
}

func TestDashboardFolderOrphanIfNotEmpty_NoReferrers_RemovesFinalizer(t *testing.T) {
	folder := &tsugav1alpha1.DashboardFolder{
		ObjectMeta: metav1.ObjectMeta{
			Name: "checkout", Namespace: testNamespace,
			Finalizers:        []string{dashboardFolderFinalizer},
			DeletionTimestamp: &metav1.Time{Time: time.Now()},
			Annotations:       map[string]string{orphanIfNotEmptyAnnotation: "true"},
		},
		Spec:   tsugav1alpha1.DashboardFolderSpec{Name: "Checkout", Owner: testOwner},
		Status: tsugav1alpha1.DashboardFolderStatus{SyncStatus: tsugav1alpha1.SyncStatus{ID: "f1"}},
	}
	fakeClient := fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(folder).WithStatusSubresource(folder).Build()
	tsuga := &mockMonitorClient{deleteFn: func(context.Context, string) error { return notEmptyErr() }}
	reconciler := &TsugaDashboardFolderReconciler{Client: fakeClient, TsugaClient: tsuga}
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "checkout", Namespace: testNamespace}}

	if _, err := reconciler.Reconcile(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if err := fakeClient.Get(context.Background(), req.NamespacedName, &tsugav1alpha1.DashboardFolder{}); err == nil {
		t.Fatal("expected the finalizer removed so the resource is gone")
	}
}

// The last managed child can finish deleting while the folder's first delete
// is in flight: the folder is then empty and must be deleted, not orphaned.
func TestOrphanIfNotEmpty_RetriesDeleteOnceReferrersAreGone(t *testing.T) {
	_, found, _, deletes, err := deleteReconcileSeq(t, []client.Object{orphanableMonitorFolder()}, []error{notEmptyErr(), nil}, "checkout")
	if err != nil || found {
		t.Fatalf("found=%v err=%v, want the resource gone", found, err)
	}
	if deletes != 2 {
		t.Errorf("remote delete calls = %d, want 2", deletes)
	}
}

// A network error message carries a fresh local port on every attempt; a
// status write per attempt would emit a watch event and bypass backoff.
func TestDeleteNetworkError_WritesStableStatus(t *testing.T) {
	folder := deletingMonitorFolder("checkout", "f1")
	attempt := 0
	netErr := func() error {
		attempt++
		return &url.Error{Op: "Delete", URL: "https://api.tsuga.com/v1/monitor-folders/f1",
			Err: fmt.Errorf("read tcp 10.0.0.1:%d->1.2.3.4:443: connection reset by peer", 50000+attempt)}
	}
	statusWrites := 0
	fakeClient := fake.NewClientBuilder().
		WithScheme(testScheme(t)).
		WithObjects(folder).
		WithStatusSubresource(folder).
		WithInterceptorFuncs(interceptor.Funcs{
			SubResourceUpdate: func(ctx context.Context, c client.Client, sub string, obj client.Object, opts ...client.SubResourceUpdateOption) error {
				statusWrites++
				return c.SubResource(sub).Update(ctx, obj, opts...)
			},
		}).
		Build()
	reconciler := &TsugaMonitorFolderReconciler{Client: fakeClient, TsugaClient: &mockMonitorClient{
		deleteFn: func(context.Context, string) error { return netErr() },
	}}
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "checkout", Namespace: testNamespace}}
	for range 3 {
		if _, err := reconciler.Reconcile(context.Background(), req); err == nil {
			t.Fatal("expected the network error returned for backoff")
		}
	}
	if statusWrites != 1 {
		t.Fatalf("status writes=%d, want 1", statusWrites)
	}
}
