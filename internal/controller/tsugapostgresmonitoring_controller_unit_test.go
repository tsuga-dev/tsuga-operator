package controller

import (
	"context"
	"slices"
	"strings"
	"testing"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	"github.com/tsuga-dev/tsuga-operator/api/v1alpha1"
	"github.com/tsuga-dev/tsuga-operator/internal/collection"
)

var pgKey = types.NamespacedName{Name: "orders-db", Namespace: "orders"}

func newTestPG(mode string) *v1alpha1.TsugaPostgresMonitoring {
	return &v1alpha1.TsugaPostgresMonitoring{
		ObjectMeta: metav1.ObjectMeta{Name: pgKey.Name, Namespace: pgKey.Namespace, UID: "pg-uid"},
		Spec: v1alpha1.TsugaPostgresMonitoringSpec{
			Host: "orders-pg-rw.orders.svc",
			Provisioning: v1alpha1.PostgresProvisioningSpec{
				Mode:           mode,
				AdminSecretRef: &corev1.LocalObjectReference{Name: "orders-pg-admin"},
			},
		},
	}
}

func newTestCluster() *v1alpha1.TsugaCollectorConfig {
	return &v1alpha1.TsugaCollectorConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
		Spec: v1alpha1.TsugaCollectorConfigSpec{Export: v1alpha1.ExportSpec{
			Endpoint: "https://otlp.tsuga.com", TokenSecretRef: v1alpha1.SecretKeyRef{Name: "s", Key: "k"},
		}},
	}
}

func newTestAdminSecret(labels map[string]string) *corev1.Secret {
	return &corev1.Secret{ObjectMeta: metav1.ObjectMeta{
		Name: "orders-pg-admin", Namespace: pgKey.Namespace, Labels: labels,
	}}
}

var adminOptIn = map[string]string{collection.PostgresAdminSecretLabel: "true"}

// newPGClient seeds an opted-in admin Secret unless objs already hold one.
func newPGClient(t *testing.T, objs ...client.Object) (client.Client, *TsugaPostgresMonitoringReconciler) {
	t.Helper()
	scheme := testScheme(t)
	if !slices.ContainsFunc(objs, func(o client.Object) bool { return o.GetName() == "orders-pg-admin" }) {
		objs = append(objs, newTestAdminSecret(adminOptIn))
	}
	cl := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(objs...).
		WithStatusSubresource(&v1alpha1.TsugaPostgresMonitoring{}, &batchv1.Job{}).
		WithInterceptorFuncs(applyAsCreateOrUpdate()).
		Build()
	return cl, &TsugaPostgresMonitoringReconciler{Client: cl, Scheme: scheme}
}

func reconcilePG(t *testing.T, r *TsugaPostgresMonitoringReconciler) ctrl.Result {
	t.Helper()
	res, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: pgKey})
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func pgReady(t *testing.T, cl client.Client) (*v1alpha1.TsugaPostgresMonitoring, *metav1.Condition) {
	t.Helper()
	got := &v1alpha1.TsugaPostgresMonitoring{}
	if err := cl.Get(context.Background(), pgKey, got); err != nil {
		t.Fatal(err)
	}
	cond := meta.FindStatusCondition(got.Status.Conditions, conditionReady)
	if cond == nil {
		t.Fatal("no Ready condition")
	}
	return got, cond
}

func setupJobs(t *testing.T, cl client.Client) []batchv1.Job {
	t.Helper()
	jobs := &batchv1.JobList{}
	if err := cl.List(context.Background(), jobs, client.InNamespace(pgKey.Namespace),
		client.MatchingLabels{collection.PostgresMonitoringLabel: pgKey.Name}); err != nil {
		t.Fatal(err)
	}
	return jobs.Items
}

func finishJob(t *testing.T, cl client.Client, job batchv1.Job, condition batchv1.JobConditionType) {
	t.Helper()
	job.Status.Conditions = []batchv1.JobCondition{{Type: condition, Status: corev1.ConditionTrue}}
	if err := cl.Status().Update(context.Background(), &job); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresReconcileManagedCreatesChildrenAndWaitsForJob(t *testing.T) {
	cl, r := newPGClient(t, newTestCluster(), newTestPG("managed"))
	reconcilePG(t, r)

	ctx := context.Background()
	if err := cl.Get(ctx, types.NamespacedName{Name: "orders-db-pg-monitor", Namespace: "orders"}, &corev1.Secret{}); err != nil {
		t.Fatalf("monitor secret: %v", err)
	}
	if err := cl.Get(ctx, types.NamespacedName{Name: "orders-db-pg-setup", Namespace: "orders"}, &corev1.ConfigMap{}); err != nil {
		t.Fatalf("setup configmap: %v", err)
	}
	collector := &unstructured.Unstructured{}
	collector.SetGroupVersionKind(collectorGVK)
	if err := cl.Get(ctx, types.NamespacedName{Name: "orders-db-pg", Namespace: "orders"}, collector); err != nil {
		t.Fatalf("collector: %v", err)
	}
	endpoint, _, _ := unstructured.NestedString(collector.Object, "spec", "config", "exporters", "otlp", "endpoint")
	if endpoint != "tsuga-agent-collector.tsuga-operator-system.svc.cluster.local:4317" {
		t.Fatalf("collector exports to %q", endpoint)
	}
	if n := len(setupJobs(t, cl)); n != 1 {
		t.Fatalf("want 1 setup job, got %d", n)
	}

	got, cond := pgReady(t, cl)
	if cond.Status != metav1.ConditionFalse || cond.Reason != reasonProvisioningPending || got.Status.Phase != phasePending {
		t.Fatalf("want pending, got %s/%s phase %s", cond.Status, cond.Reason, got.Status.Phase)
	}
}

func TestPostgresReconcileRequiresAdminSecretOptIn(t *testing.T) {
	cases := map[string]client.Object{
		"unlabeled": newTestAdminSecret(nil),
		"not true":  newTestAdminSecret(map[string]string{collection.PostgresAdminSecretLabel: "false"}),
		// a non-Secret with the admin name stops newPGClient seeding one
		"missing": &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "orders-pg-admin", Namespace: pgKey.Namespace}},
	}
	for name, admin := range cases {
		t.Run(name, func(t *testing.T) {
			cl, r := newPGClient(t, newTestCluster(), newTestPG("managed"), admin)
			if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: pgKey}); err == nil {
				t.Fatal("want an error for an admin Secret that did not opt in")
			}
			if n := len(setupJobs(t, cl)); n != 0 {
				t.Fatalf("want no setup job, got %d", n)
			}
			got, cond := pgReady(t, cl)
			if cond.Reason != reasonProvisioningFailed || got.Status.Phase != phaseError {
				t.Fatalf("want ProvisioningFailed/Error, got %s/%s", cond.Reason, got.Status.Phase)
			}
		})
	}
}

func TestPostgresClusterConfigEventEnqueuesDependents(t *testing.T) {
	cluster := newTestCluster()
	pg1 := newTestPG("manual")
	pg2 := newTestPG("manual")
	pg2.Name, pg2.Namespace = "inventory", "warehouse"
	cl := fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(cluster, pg1, pg2).Build()
	r := &TsugaPostgresMonitoringReconciler{Client: cl}
	if got := r.clusterConfigToPostgresMonitorings(context.Background(), &v1alpha1.TsugaCollectorConfig{ObjectMeta: metav1.ObjectMeta{Name: "other"}}); len(got) != 0 {
		t.Fatalf("unrelated config enqueued %v", got)
	}
	got := r.clusterConfigToPostgresMonitorings(context.Background(), cluster)
	if len(got) != 2 {
		t.Fatalf("cluster config should enqueue both Postgres monitorings, got %v", got)
	}
	seen := map[types.NamespacedName]bool{}
	for _, req := range got {
		seen[req.NamespacedName] = true
	}
	if !seen[pgKey] || !seen[types.NamespacedName{Name: "inventory", Namespace: "warehouse"}] {
		t.Fatalf("missing Postgres monitoring requests: %v", got)
	}
}

func TestPostgresReconcileReportsJobOutcome(t *testing.T) {
	for _, tc := range []struct {
		condition  batchv1.JobConditionType
		wantStatus metav1.ConditionStatus
		wantReason string
		wantPhase  string
	}{
		{batchv1.JobComplete, metav1.ConditionTrue, reasonReconciled, phaseReady},
		{batchv1.JobFailed, metav1.ConditionFalse, reasonProvisioningFailed, phaseError},
	} {
		t.Run(string(tc.condition), func(t *testing.T) {
			cl, r := newPGClient(t, newTestCluster(), newTestPG("managed"))
			reconcilePG(t, r)
			finishJob(t, cl, setupJobs(t, cl)[0], tc.condition)
			reconcilePG(t, r)

			got, cond := pgReady(t, cl)
			if cond.Status != tc.wantStatus || cond.Reason != tc.wantReason || got.Status.Phase != tc.wantPhase {
				t.Fatalf("got %s/%s phase %s", cond.Status, cond.Reason, got.Status.Phase)
			}
			if tc.condition == batchv1.JobFailed && !strings.Contains(cond.Message, "kubectl logs -n orders job/") {
				t.Fatalf("failure message should point at the job logs: %q", cond.Message)
			}
		})
	}
}

func TestPostgresReconcileKeepsExistingMonitorSecret(t *testing.T) {
	pg := newTestPG("managed")
	existing := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "orders-db-pg-monitor", Namespace: "orders"},
		Data:       map[string][]byte{"password": []byte("keep-me")},
	}
	if err := controllerutil.SetControllerReference(pg, existing, testScheme(t)); err != nil {
		t.Fatal(err)
	}
	cl, r := newPGClient(t, newTestCluster(), pg, existing)
	r.APIReader = cl
	reconcilePG(t, r)
	reconcilePG(t, r)

	got := &corev1.Secret{}
	if err := cl.Get(context.Background(), client.ObjectKeyFromObject(existing), got); err != nil {
		t.Fatal(err)
	}
	if string(got.Data["password"]) != "keep-me" || len(got.StringData) != 0 {
		t.Fatalf("monitor secret was overwritten: %+v", got)
	}
}

func TestPostgresReconcileRejectsForeignExistingSecret(t *testing.T) {
	existing := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "orders-db-pg-monitor", Namespace: "orders"}}
	cl, r := newPGClient(t, newTestCluster(), newTestPG("managed"), existing)
	_, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: pgKey})
	if err == nil || !strings.Contains(err.Error(), "without a controller reference") {
		t.Fatalf("want an ownership error, got %v", err)
	}
	if jobs := setupJobs(t, cl); len(jobs) != 0 {
		t.Fatalf("foreign Secret must stop provisioning; got %d jobs", len(jobs))
	}
}

func TestPostgresReconcileRejectsForeignExistingJob(t *testing.T) {
	pg := newTestPG("managed")
	spec := collection.ResolvePostgres(pg.Spec)
	job, err := collection.RenderPostgresSetupJob(pg.Name, pg.Namespace, spec)
	if err != nil {
		t.Fatal(err)
	}
	cl, r := newPGClient(t, newTestCluster(), pg, job)
	_, err = r.Reconcile(context.Background(), ctrl.Request{NamespacedName: pgKey})
	if err == nil || !strings.Contains(err.Error(), "without a controller reference") {
		t.Fatalf("want an ownership error, got %v", err)
	}
	if err := cl.Get(context.Background(), client.ObjectKeyFromObject(job), &batchv1.Job{}); err != nil {
		t.Fatalf("foreign Job must remain untouched: %v", err)
	}
}

func TestPostgresReconcileRerunsJobForNewPassword(t *testing.T) {
	cl, r := newPGClient(t, newTestCluster(), newTestPG("managed"))
	reconcilePG(t, r)
	job := setupJobs(t, cl)[0]
	finishJob(t, cl, job, batchv1.JobComplete)
	reconcilePG(t, r)

	ctx := context.Background()
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "orders-db-pg-monitor", Namespace: "orders"}}
	if err := cl.Delete(ctx, secret); err != nil {
		t.Fatal(err)
	}

	if res := reconcilePG(t, r); res.RequeueAfter == 0 {
		t.Fatal("want a requeue after deleting the stale setup job")
	}
	if err := cl.Get(ctx, client.ObjectKeyFromObject(&job), &batchv1.Job{}); !apierrors.IsNotFound(err) {
		t.Fatalf("the completed job should be deleted so it re-runs with the new password, got %v", err)
	}

	reconcilePG(t, r)
	jobs := setupJobs(t, cl)
	if len(jobs) != 1 || jobs[0].Name != job.Name || len(jobs[0].Status.Conditions) != 0 {
		t.Fatalf("want a fresh job named %s, got %+v", job.Name, jobs)
	}
	if _, cond := pgReady(t, cl); cond.Reason != reasonProvisioningPending {
		t.Fatalf("status must not keep reporting the old job's success, got %s", cond.Reason)
	}
}

func TestPostgresReconcileDeletesStaleJobs(t *testing.T) {
	scheme := testScheme(t)
	pg := newTestPG("managed")
	stale := &batchv1.Job{ObjectMeta: metav1.ObjectMeta{
		Name: "orders-db-pg-setup-0000000000", Namespace: "orders",
		Labels: map[string]string{collection.PostgresMonitoringLabel: pg.Name},
	}}
	if err := controllerutil.SetControllerReference(pg, stale, scheme); err != nil {
		t.Fatal(err)
	}
	foreign := &batchv1.Job{ObjectMeta: metav1.ObjectMeta{
		Name: "someone-elses-job", Namespace: "orders",
		Labels: map[string]string{collection.PostgresMonitoringLabel: pg.Name},
	}}
	cl, r := newPGClient(t, newTestCluster(), pg, stale, foreign)
	reconcilePG(t, r)

	ctx := context.Background()
	if err := cl.Get(ctx, client.ObjectKeyFromObject(stale), &batchv1.Job{}); !apierrors.IsNotFound(err) {
		t.Fatalf("stale job should be deleted, got %v", err)
	}
	if err := cl.Get(ctx, client.ObjectKeyFromObject(foreign), &batchv1.Job{}); err != nil {
		t.Fatalf("a job this CR does not control must be left alone: %v", err)
	}
}

func TestPostgresReconcileManualSkipsProvisioning(t *testing.T) {
	cl, r := newPGClient(t, newTestCluster(), newTestPG("manual"))
	reconcilePG(t, r)

	if n := len(setupJobs(t, cl)); n != 0 {
		t.Fatalf("manual mode created %d jobs", n)
	}
	err := cl.Get(context.Background(), types.NamespacedName{Name: "orders-db-pg-setup", Namespace: "orders"}, &corev1.ConfigMap{})
	if !apierrors.IsNotFound(err) {
		t.Fatalf("manual mode should not render the setup configmap, got %v", err)
	}
	got, cond := pgReady(t, cl)
	if cond.Status != metav1.ConditionTrue || got.Status.Phase != phaseReady || !strings.Contains(cond.Message, "orders-db-pg-monitor") {
		t.Fatalf("got %s phase %s message %q", cond.Status, got.Status.Phase, cond.Message)
	}
}

func TestPostgresReconcileSwitchToManualDeletesSetupResources(t *testing.T) {
	cl, r := newPGClient(t, newTestCluster(), newTestPG("managed"))
	reconcilePG(t, r)
	if len(setupJobs(t, cl)) != 1 {
		t.Fatal("managed mode should create a setup Job")
	}
	pg := &v1alpha1.TsugaPostgresMonitoring{}
	if err := cl.Get(context.Background(), pgKey, pg); err != nil {
		t.Fatal(err)
	}
	pg.Spec.Provisioning.Mode = "manual"
	if err := cl.Update(context.Background(), pg); err != nil {
		t.Fatal(err)
	}
	reconcilePG(t, r)
	if jobs := setupJobs(t, cl); len(jobs) != 0 {
		t.Fatalf("manual mode left %d setup Jobs", len(jobs))
	}
	key := types.NamespacedName{Name: "orders-db-pg-setup", Namespace: "orders"}
	if err := cl.Get(context.Background(), key, &corev1.ConfigMap{}); !apierrors.IsNotFound(err) {
		t.Fatalf("manual mode left setup ConfigMap: %v", err)
	}
}

func TestPostgresReconcileMissingClusterConfig(t *testing.T) {
	cl, r := newPGClient(t, newTestPG("managed"))
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: pgKey}); err == nil {
		t.Fatal("want an error without the cluster TsugaCollectorConfig")
	}
	if _, cond := pgReady(t, cl); cond.Reason != reasonClusterConfigMissing {
		t.Fatalf("reason = %s", cond.Reason)
	}
}

func TestPostgresReconcileRejectsUnownedExistingConfigMap(t *testing.T) {
	existing := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "orders-db-pg-setup", Namespace: "orders"}}
	cl, r := newPGClient(t, newTestCluster(), newTestPG("managed"), existing)
	_, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: pgKey})
	if err == nil || !strings.Contains(err.Error(), "without a controller reference") {
		t.Fatalf("want an ownership error, got %v", err)
	}
	got := &corev1.ConfigMap{}
	if err := cl.Get(context.Background(), client.ObjectKeyFromObject(existing), got); err != nil {
		t.Fatal(err)
	}
	if len(got.OwnerReferences) != 0 {
		t.Fatalf("unowned ConfigMap must not be adopted, got %v", got.OwnerReferences)
	}
}
