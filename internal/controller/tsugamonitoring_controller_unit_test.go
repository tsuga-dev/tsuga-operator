package controller

import (
	"context"
	"slices"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/tsuga-dev/tsuga-operator/api/v1alpha1"
)

// otelInstrumentationGVK is the GroupVersionKind of the OTel Operator's
// Instrumentation CRD, used by the collection package to render the
// unstructured object applied by this controller. Reuses instrumentationGVK
// (defined in tsugamonitoring_controller.go) so the two stay in sync and the
// literal isn't duplicated for goconst.
var otelInstrumentationGVK = instrumentationGVK

// monitoringTestScheme extends the shared testScheme with the Instrumentation
// GVK so the fake client can store/retrieve the unstructured object rendered
// by RenderInstrumentation.
func monitoringTestScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := testScheme(t)
	listGVK := otelInstrumentationGVK.GroupVersion().WithKind(otelInstrumentationGVK.Kind + "List")
	s.AddKnownTypeWithName(otelInstrumentationGVK, &unstructured.Unstructured{})
	s.AddKnownTypeWithName(listGVK, &unstructured.UnstructuredList{})
	return s
}

// wantAgentEndpoint is the expected in-cluster OTLP endpoint for a cluster
// TsugaCollectorConfig that leaves CollectorNamespace unset (defaults to
// tsuga-operator-system).
const wantAgentEndpoint = "http://tsuga-agent-collector.tsuga-operator-system.svc.cluster.local:4318"

func newTestDeployment(name, namespace string) *appsv1.Deployment {
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec: appsv1.DeploymentSpec{
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": name}},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": name}},
				Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: "example/app"}}},
			},
		},
	}
}

func TestMonitoringReconcileCreatesInstrumentation(t *testing.T) {
	scheme := monitoringTestScheme(t)
	cluster := &v1alpha1.TsugaCollectorConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
		Spec:       v1alpha1.TsugaCollectorConfigSpec{Export: v1alpha1.ExportSpec{Endpoint: "https://otlp.tsuga.com", TokenSecretRef: v1alpha1.SecretKeyRef{Name: "s", Key: "k"}}},
	}
	m := &v1alpha1.TsugaMonitoring{
		ObjectMeta: metav1.ObjectMeta{Name: "default", Namespace: "payments"},
		Spec:       v1alpha1.TsugaMonitoringSpec{Instrumentation: v1alpha1.InstrumentationSpec{Enabled: true, Languages: []string{"java"}}},
	}
	cl := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(cluster, m).WithStatusSubresource(m).
		WithInterceptorFuncs(applyAsCreateOrUpdate()).
		Build()

	r := &TsugaMonitoringReconciler{Client: cl, Scheme: scheme}
	_, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "default", Namespace: "payments"}})
	if err != nil {
		t.Fatal(err)
	}

	got := &v1alpha1.TsugaMonitoring{}
	if err := cl.Get(context.Background(), types.NamespacedName{Name: "default", Namespace: "payments"}, got); err != nil {
		t.Fatal(err)
	}
	if got.Status.Phase != phaseReady {
		t.Fatalf("want Ready, got %q", got.Status.Phase)
	}
	if len(got.Status.ManagedResources) != 1 || got.Status.ManagedResources[0] != "default" {
		t.Fatalf("want managed resources [default], got %v", got.Status.ManagedResources)
	}
	if got.Status.LastSyncedAt == nil {
		t.Fatal("want LastSyncedAt to be set")
	}

	inst := &unstructured.Unstructured{}
	inst.SetGroupVersionKind(otelInstrumentationGVK)
	if err := cl.Get(context.Background(), types.NamespacedName{Name: "default", Namespace: "payments"}, inst); err != nil {
		t.Fatalf("expected Instrumentation to be persisted: %v", err)
	}

	endpoint, _, _ := unstructured.NestedString(inst.Object, "spec", "exporter", "endpoint")
	if endpoint != wantAgentEndpoint {
		t.Fatalf("want exporter endpoint %q (in-cluster agent), got %q", wantAgentEndpoint, endpoint)
	}

	var ownedByMonitoring bool
	for _, ref := range inst.GetOwnerReferences() {
		if ref.Kind == "TsugaMonitoring" && ref.Controller != nil && *ref.Controller {
			ownedByMonitoring = true
			break
		}
	}
	if !ownedByMonitoring {
		t.Fatalf("Instrumentation: want a controller ownerReference to TsugaMonitoring, got %v", inst.GetOwnerReferences())
	}
}

func TestMonitoringClusterConfigEventEnqueuesDependents(t *testing.T) {
	cluster := newTestCluster()
	m1 := &v1alpha1.TsugaMonitoring{ObjectMeta: metav1.ObjectMeta{Name: "first", Namespace: "payments"}}
	m2 := &v1alpha1.TsugaMonitoring{ObjectMeta: metav1.ObjectMeta{Name: "second", Namespace: "orders"}}
	cl := fake.NewClientBuilder().WithScheme(monitoringTestScheme(t)).WithObjects(cluster, m1, m2).Build()
	r := &TsugaMonitoringReconciler{Client: cl}
	if got := r.clusterConfigToMonitorings(context.Background(), &v1alpha1.TsugaCollectorConfig{ObjectMeta: metav1.ObjectMeta{Name: "other"}}); len(got) != 0 {
		t.Fatalf("unrelated config enqueued %v", got)
	}
	got := r.clusterConfigToMonitorings(context.Background(), cluster)
	if len(got) != 2 {
		t.Fatalf("cluster config should enqueue both monitorings, got %v", got)
	}
	seen := map[types.NamespacedName]bool{}
	for _, req := range got {
		seen[req.NamespacedName] = true
	}
	if !seen[types.NamespacedName{Name: "first", Namespace: "payments"}] || !seen[types.NamespacedName{Name: "second", Namespace: "orders"}] {
		t.Fatalf("missing monitoring requests: %v", got)
	}
}

func TestMonitoringReconcileInjectsExistingWorkloads(t *testing.T) {
	scheme := monitoringTestScheme(t)
	cluster := &v1alpha1.TsugaCollectorConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
		Spec:       v1alpha1.TsugaCollectorConfigSpec{Export: v1alpha1.ExportSpec{Endpoint: "https://otlp.tsuga.com", TokenSecretRef: v1alpha1.SecretKeyRef{Name: "s", Key: "k"}}},
	}
	m := &v1alpha1.TsugaMonitoring{
		ObjectMeta: metav1.ObjectMeta{Name: "default", Namespace: "payments"},
		Spec: v1alpha1.TsugaMonitoringSpec{
			Instrumentation:         v1alpha1.InstrumentationSpec{Enabled: true, Languages: []string{"java"}},
			InjectExistingWorkloads: true,
		},
	}
	dep := newTestDeployment("checkout", "payments")
	cl := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(cluster, m, dep).WithStatusSubresource(m).
		WithInterceptorFuncs(applyAsCreateOrUpdate()).
		Build()

	r := &TsugaMonitoringReconciler{Client: cl, Scheme: scheme}
	_, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "default", Namespace: "payments"}})
	if err != nil {
		t.Fatal(err)
	}

	got := &v1alpha1.TsugaMonitoring{}
	if err := cl.Get(context.Background(), types.NamespacedName{Name: "default", Namespace: "payments"}, got); err != nil {
		t.Fatal(err)
	}
	if got.Status.Phase != phaseReady {
		t.Fatalf("want Ready, got %q", got.Status.Phase)
	}

	gotDep := &appsv1.Deployment{}
	if err := cl.Get(context.Background(), types.NamespacedName{Name: "checkout", Namespace: "payments"}, gotDep); err != nil {
		t.Fatal(err)
	}
	want := "default"
	if v := gotDep.Spec.Template.Annotations["instrumentation.opentelemetry.io/inject-java"]; v != want {
		t.Fatalf("want inject-java annotation %q, got %q (annotations: %v)", want, v, gotDep.Spec.Template.Annotations)
	}

	inst := &unstructured.Unstructured{}
	inst.SetGroupVersionKind(otelInstrumentationGVK)
	if err := cl.Get(context.Background(), types.NamespacedName{Name: "default", Namespace: "payments"}, inst); err != nil {
		t.Fatalf("expected Instrumentation to be persisted: %v", err)
	}
	endpoint, _, _ := unstructured.NestedString(inst.Object, "spec", "exporter", "endpoint")
	if endpoint != wantAgentEndpoint {
		t.Fatalf("want exporter endpoint %q, got %q", wantAgentEndpoint, endpoint)
	}

	var ownedByMonitoring bool
	for _, ref := range inst.GetOwnerReferences() {
		if ref.Kind == "TsugaMonitoring" && ref.Controller != nil && *ref.Controller {
			ownedByMonitoring = true
			break
		}
	}
	if !ownedByMonitoring {
		t.Fatalf("Instrumentation: want a controller ownerReference to TsugaMonitoring, got %v", inst.GetOwnerReferences())
	}
}

func TestMonitoringReconcileSkipsInjectionWhenDisabledOnSpec(t *testing.T) {
	scheme := monitoringTestScheme(t)
	cluster := &v1alpha1.TsugaCollectorConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
		Spec:       v1alpha1.TsugaCollectorConfigSpec{Export: v1alpha1.ExportSpec{Endpoint: "https://otlp.tsuga.com", TokenSecretRef: v1alpha1.SecretKeyRef{Name: "s", Key: "k"}}},
	}
	m := &v1alpha1.TsugaMonitoring{
		ObjectMeta: metav1.ObjectMeta{Name: "default", Namespace: "payments"},
		Spec: v1alpha1.TsugaMonitoringSpec{
			Instrumentation:         v1alpha1.InstrumentationSpec{Enabled: true, Languages: []string{"java"}},
			InjectExistingWorkloads: false,
		},
	}
	dep := newTestDeployment("checkout", "payments")
	cl := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(cluster, m, dep).WithStatusSubresource(m).
		WithInterceptorFuncs(applyAsCreateOrUpdate()).
		Build()

	r := &TsugaMonitoringReconciler{Client: cl, Scheme: scheme}
	_, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "default", Namespace: "payments"}})
	if err != nil {
		t.Fatal(err)
	}

	gotDep := &appsv1.Deployment{}
	if err := cl.Get(context.Background(), types.NamespacedName{Name: "checkout", Namespace: "payments"}, gotDep); err != nil {
		t.Fatal(err)
	}
	if len(gotDep.Spec.Template.Annotations) != 0 {
		t.Fatalf("want no annotations added when InjectExistingWorkloads is false, got %v", gotDep.Spec.Template.Annotations)
	}

	inst := &unstructured.Unstructured{}
	inst.SetGroupVersionKind(otelInstrumentationGVK)
	if err := cl.Get(context.Background(), types.NamespacedName{Name: "default", Namespace: "payments"}, inst); err != nil {
		t.Fatalf("expected Instrumentation to still be created: %v", err)
	}
}

func TestMonitoringReconcileDisabledSkipsInstrumentation(t *testing.T) {
	scheme := monitoringTestScheme(t)
	m := &v1alpha1.TsugaMonitoring{
		ObjectMeta: metav1.ObjectMeta{Name: "default", Namespace: "payments"},
		Spec:       v1alpha1.TsugaMonitoringSpec{Instrumentation: v1alpha1.InstrumentationSpec{Enabled: false}, InjectExistingWorkloads: true},
	}
	dep := newTestDeployment("checkout", "payments")
	cl := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(m, dep).WithStatusSubresource(m).
		WithInterceptorFuncs(applyAsCreateOrUpdate()).
		Build()

	r := &TsugaMonitoringReconciler{Client: cl, Scheme: scheme}
	_, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "default", Namespace: "payments"}})
	if err != nil {
		t.Fatal(err)
	}

	got := &v1alpha1.TsugaMonitoring{}
	if err := cl.Get(context.Background(), types.NamespacedName{Name: "default", Namespace: "payments"}, got); err != nil {
		t.Fatal(err)
	}
	if got.Status.Phase != phaseReady {
		t.Fatalf("want Ready, got %q", got.Status.Phase)
	}
	if got.Status.Message != "instrumentation disabled" {
		t.Fatalf("want disabled message, got %q", got.Status.Message)
	}

	inst := &unstructured.Unstructured{}
	inst.SetGroupVersionKind(otelInstrumentationGVK)
	err = cl.Get(context.Background(), types.NamespacedName{Name: "default", Namespace: "payments"}, inst)
	if err == nil {
		t.Fatal("expected no Instrumentation to be created when disabled")
	}

	gotDep := &appsv1.Deployment{}
	if err := cl.Get(context.Background(), types.NamespacedName{Name: "checkout", Namespace: "payments"}, gotDep); err != nil {
		t.Fatal(err)
	}
	if len(gotDep.Spec.Template.Annotations) != 0 {
		t.Fatalf("want no annotations added when instrumentation is disabled, got %v", gotDep.Spec.Template.Annotations)
	}
}

func TestMonitoringInstrumentedWorkloadsReportsTotalAcrossReconciles(t *testing.T) {
	scheme := monitoringTestScheme(t)
	cluster := &v1alpha1.TsugaCollectorConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
		Spec:       v1alpha1.TsugaCollectorConfigSpec{Export: v1alpha1.ExportSpec{Endpoint: "https://otlp.tsuga.com", TokenSecretRef: v1alpha1.SecretKeyRef{Name: "s", Key: "k"}}},
	}
	m := &v1alpha1.TsugaMonitoring{
		ObjectMeta: metav1.ObjectMeta{Name: "default", Namespace: "payments"},
		Spec: v1alpha1.TsugaMonitoringSpec{
			Instrumentation:         v1alpha1.InstrumentationSpec{Enabled: true, Languages: []string{"java"}},
			InjectExistingWorkloads: true,
		},
	}
	dep := newTestDeployment("checkout", "payments")
	cl := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(cluster, m, dep).WithStatusSubresource(m).
		WithInterceptorFuncs(applyAsCreateOrUpdate()).
		Build()

	r := &TsugaMonitoringReconciler{Client: cl, Scheme: scheme}
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "default", Namespace: "payments"}}

	if _, err := r.Reconcile(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	got := &v1alpha1.TsugaMonitoring{}
	if err := cl.Get(context.Background(), req.NamespacedName, got); err != nil {
		t.Fatal(err)
	}
	if got.Status.InstrumentedWorkloads != 1 {
		t.Fatalf("after first reconcile: want 1 instrumented workload, got %d", got.Status.InstrumentedWorkloads)
	}
	if got.Status.Message != "" {
		t.Fatalf("want the count in a typed field and an empty message, got message %q", got.Status.Message)
	}

	// Second reconcile: the deployment is already annotated (not modified),
	// but the count must still reflect the total annotated workload, not 0.
	if _, err := r.Reconcile(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if err := cl.Get(context.Background(), req.NamespacedName, got); err != nil {
		t.Fatal(err)
	}
	if got.Status.InstrumentedWorkloads != 1 {
		t.Fatalf("after second reconcile: want 1 (total, not just modified), got %d", got.Status.InstrumentedWorkloads)
	}
}

// In opt-in mode the operator annotates nothing, so the count has to come from
// the annotations already on the workloads - otherwise a namespace an app team
// wired up by hand reports zero instrumented workloads.
func TestMonitoringCountsHandAnnotatedWorkloadsInOptInMode(t *testing.T) {
	scheme := monitoringTestScheme(t)
	cluster := &v1alpha1.TsugaCollectorConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
		Spec:       v1alpha1.TsugaCollectorConfigSpec{Export: v1alpha1.ExportSpec{Endpoint: "https://otlp.tsuga.com", TokenSecretRef: v1alpha1.SecretKeyRef{Name: "s", Key: "k"}}},
	}
	m := &v1alpha1.TsugaMonitoring{
		ObjectMeta: metav1.ObjectMeta{Name: "default", Namespace: "payments"},
		Spec: v1alpha1.TsugaMonitoringSpec{
			Instrumentation:         v1alpha1.InstrumentationSpec{Enabled: true, Languages: []string{"java"}},
			InjectExistingWorkloads: false,
		},
	}
	optedIn := newTestDeployment("checkout", "payments")
	optedIn.Spec.Template.Annotations = map[string]string{"instrumentation.opentelemetry.io/inject-java": "default"}
	otherCR := newTestDeployment("ledger", "payments")
	otherCR.Spec.Template.Annotations = map[string]string{"instrumentation.opentelemetry.io/inject-java": "some-other-monitoring"}
	bare := newTestDeployment("search", "payments")

	cl := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(cluster, m, optedIn, otherCR, bare).WithStatusSubresource(m).
		WithInterceptorFuncs(applyAsCreateOrUpdate()).
		Build()

	r := &TsugaMonitoringReconciler{Client: cl, Scheme: scheme}
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "default", Namespace: "payments"}}
	if _, err := r.Reconcile(context.Background(), req); err != nil {
		t.Fatal(err)
	}

	got := &v1alpha1.TsugaMonitoring{}
	if err := cl.Get(context.Background(), req.NamespacedName, got); err != nil {
		t.Fatal(err)
	}
	if got.Status.InstrumentedWorkloads != 1 {
		t.Fatalf("want 1 instrumented workload (only the one pointing at this CR), got %d", got.Status.InstrumentedWorkloads)
	}

	gotBare := &appsv1.Deployment{}
	if err := cl.Get(context.Background(), types.NamespacedName{Name: "search", Namespace: "payments"}, gotBare); err != nil {
		t.Fatal(err)
	}
	if len(gotBare.Spec.Template.Annotations) != 0 {
		t.Fatalf("opt-in mode must not annotate, got %v", gotBare.Spec.Template.Annotations)
	}
}

func TestMonitoringSetsReadyConditionOnSuccess(t *testing.T) {
	scheme := monitoringTestScheme(t)
	cluster := &v1alpha1.TsugaCollectorConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
		Spec:       v1alpha1.TsugaCollectorConfigSpec{Export: v1alpha1.ExportSpec{Endpoint: "https://otlp.tsuga.com", TokenSecretRef: v1alpha1.SecretKeyRef{Name: "s", Key: "k"}}},
	}
	m := &v1alpha1.TsugaMonitoring{
		ObjectMeta: metav1.ObjectMeta{Name: "default", Namespace: "payments", Generation: 4},
		Spec:       v1alpha1.TsugaMonitoringSpec{Instrumentation: v1alpha1.InstrumentationSpec{Enabled: true, Languages: []string{"java"}}},
	}
	cl := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(cluster, m).WithStatusSubresource(m).
		WithInterceptorFuncs(applyAsCreateOrUpdate()).
		Build()

	r := &TsugaMonitoringReconciler{Client: cl, Scheme: scheme}
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "default", Namespace: "payments"}}
	if _, err := r.Reconcile(context.Background(), req); err != nil {
		t.Fatal(err)
	}

	got := &v1alpha1.TsugaMonitoring{}
	if err := cl.Get(context.Background(), req.NamespacedName, got); err != nil {
		t.Fatal(err)
	}
	cond := meta.FindStatusCondition(got.Status.Conditions, conditionReady)
	if cond == nil {
		t.Fatalf("want a %s condition, got %v", conditionReady, got.Status.Conditions)
	}
	if cond.Status != metav1.ConditionTrue || cond.Reason != reasonReconciled {
		t.Fatalf("want True/%s, got %s/%s", reasonReconciled, cond.Status, cond.Reason)
	}
	if cond.ObservedGeneration != 4 {
		t.Fatalf("want observedGeneration 4, got %d", cond.ObservedGeneration)
	}
}

func TestMonitoringSetsReadyFalseWhenClusterConfigMissing(t *testing.T) {
	scheme := monitoringTestScheme(t)
	m := &v1alpha1.TsugaMonitoring{
		ObjectMeta: metav1.ObjectMeta{Name: "default", Namespace: "payments"},
		Spec:       v1alpha1.TsugaMonitoringSpec{Instrumentation: v1alpha1.InstrumentationSpec{Enabled: true, Languages: []string{"java"}}},
	}
	cl := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(m).WithStatusSubresource(m).
		WithInterceptorFuncs(applyAsCreateOrUpdate()).
		Build()

	r := &TsugaMonitoringReconciler{Client: cl, Scheme: scheme}
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "default", Namespace: "payments"}}
	if _, err := r.Reconcile(context.Background(), req); err == nil {
		t.Fatal("want an error when the cluster TsugaCollectorConfig is absent")
	}

	got := &v1alpha1.TsugaMonitoring{}
	if err := cl.Get(context.Background(), req.NamespacedName, got); err != nil {
		t.Fatal(err)
	}
	if got.Status.Phase != phaseError {
		t.Fatalf("want Error, got %q", got.Status.Phase)
	}
	cond := meta.FindStatusCondition(got.Status.Conditions, conditionReady)
	if cond == nil || cond.Status != metav1.ConditionFalse || cond.Reason != reasonClusterConfigMissing {
		t.Fatalf("want False/%s, got %v", reasonClusterConfigMissing, cond)
	}
	if cond.Message == "" {
		t.Fatal("want the cause on the condition message")
	}
}

func TestMonitoringSteadyStateIsNoOp(t *testing.T) {
	scheme := monitoringTestScheme(t)
	cluster := &v1alpha1.TsugaCollectorConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
		Spec:       v1alpha1.TsugaCollectorConfigSpec{Export: v1alpha1.ExportSpec{Endpoint: "https://otlp.tsuga.com", TokenSecretRef: v1alpha1.SecretKeyRef{Name: "s", Key: "k"}}},
	}
	m := &v1alpha1.TsugaMonitoring{
		ObjectMeta: metav1.ObjectMeta{Name: "default", Namespace: "payments"},
		Spec:       v1alpha1.TsugaMonitoringSpec{Instrumentation: v1alpha1.InstrumentationSpec{Enabled: true, Languages: []string{"java"}}},
	}
	cl := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(cluster, m).WithStatusSubresource(m).
		WithInterceptorFuncs(applyAsCreateOrUpdate()).
		Build()

	r := &TsugaMonitoringReconciler{Client: cl, Scheme: scheme}
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "default", Namespace: "payments"}}

	if _, err := r.Reconcile(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	afterFirst := &v1alpha1.TsugaMonitoring{}
	if err := cl.Get(context.Background(), req.NamespacedName, afterFirst); err != nil {
		t.Fatal(err)
	}
	rv := afterFirst.GetResourceVersion()
	syncedAt := afterFirst.Status.LastSyncedAt

	if _, err := r.Reconcile(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	afterSecond := &v1alpha1.TsugaMonitoring{}
	if err := cl.Get(context.Background(), req.NamespacedName, afterSecond); err != nil {
		t.Fatal(err)
	}
	if afterSecond.GetResourceVersion() != rv {
		t.Fatalf("steady-state reconcile must not write status: resourceVersion changed %q -> %q", rv, afterSecond.GetResourceVersion())
	}
	if syncedAt == nil || afterSecond.Status.LastSyncedAt == nil || !afterSecond.Status.LastSyncedAt.Equal(syncedAt) {
		t.Fatalf("steady-state reconcile must not bump LastSyncedAt: %v -> %v", syncedAt, afterSecond.Status.LastSyncedAt)
	}
}

func TestAgentOTLPEndpoint(t *testing.T) {
	got := agentOTLPEndpoint("tsuga-operator-system")
	if got != wantAgentEndpoint {
		t.Fatalf("want %q, got %q", wantAgentEndpoint, got)
	}
}

// newMonitoring builds an enabled TsugaMonitoring in inject-everything mode.
func newMonitoring(languages ...string) *v1alpha1.TsugaMonitoring {
	return &v1alpha1.TsugaMonitoring{
		ObjectMeta: metav1.ObjectMeta{Name: "default", Namespace: "payments"},
		Spec: v1alpha1.TsugaMonitoringSpec{
			Instrumentation:         v1alpha1.InstrumentationSpec{Enabled: true, Languages: languages},
			InjectExistingWorkloads: true,
		},
	}
}

func newClusterConfig() *v1alpha1.TsugaCollectorConfig {
	return &v1alpha1.TsugaCollectorConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
		Spec:       v1alpha1.TsugaCollectorConfigSpec{Export: v1alpha1.ExportSpec{Endpoint: "https://otlp.tsuga.com", TokenSecretRef: v1alpha1.SecretKeyRef{Name: "s", Key: "k"}}},
	}
}

func getDeployment(t *testing.T, cl client.Client, name string) *appsv1.Deployment {
	t.Helper()
	got := &appsv1.Deployment{}
	if err := cl.Get(context.Background(), types.NamespacedName{Name: name, Namespace: "payments"}, got); err != nil {
		t.Fatal(err)
	}
	return got
}

func TestMonitoringStripsInjectAnnotationsWhenDisabled(t *testing.T) {
	scheme := monitoringTestScheme(t)
	m := newMonitoring("java")
	dep := newTestDeployment("checkout", "payments")
	byHand := newTestDeployment("ledger", "payments")
	byHand.Spec.Template.Annotations = map[string]string{"instrumentation.opentelemetry.io/inject-java": "default"}

	cl := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(newClusterConfig(), m, dep, byHand).WithStatusSubresource(m).
		WithInterceptorFuncs(applyAsCreateOrUpdate()).
		Build()

	r := &TsugaMonitoringReconciler{Client: cl, Scheme: scheme}
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "default", Namespace: "payments"}}
	if _, err := r.Reconcile(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if v := getDeployment(t, cl, "checkout").Spec.Template.Annotations[injectAnnotationPrefix+"java"]; v != "default" {
		t.Fatalf("setup: want the operator to annotate checkout, got %q", v)
	}

	live := &v1alpha1.TsugaMonitoring{}
	if err := cl.Get(context.Background(), req.NamespacedName, live); err != nil {
		t.Fatal(err)
	}
	live.Spec.Instrumentation.Enabled = false
	if err := cl.Update(context.Background(), live); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Reconcile(context.Background(), req); err != nil {
		t.Fatal(err)
	}

	gotDep := getDeployment(t, cl, "checkout")
	if _, ok := gotDep.Spec.Template.Annotations[injectAnnotationPrefix+"java"]; ok {
		t.Fatalf("want the operator-added inject annotation stripped, got %v", gotDep.Spec.Template.Annotations)
	}
	if _, ok := gotDep.Annotations[ownedLanguagesAnnotation]; ok {
		t.Fatalf("want the ownership record cleared, got %v", gotDep.Annotations)
	}

	gotByHand := getDeployment(t, cl, "ledger")
	if v := gotByHand.Spec.Template.Annotations[injectAnnotationPrefix+"java"]; v != "default" {
		t.Fatalf("want the hand-set inject annotation to survive, got %q (annotations: %v)", v, gotByHand.Spec.Template.Annotations)
	}
}

func TestMonitoringStripsInjectAnnotationForRemovedLanguage(t *testing.T) {
	scheme := monitoringTestScheme(t)
	m := newMonitoring("java", "python")
	dep := newTestDeployment("checkout", "payments")

	cl := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(newClusterConfig(), m, dep).WithStatusSubresource(m).
		WithInterceptorFuncs(applyAsCreateOrUpdate()).
		Build()

	r := &TsugaMonitoringReconciler{Client: cl, Scheme: scheme}
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "default", Namespace: "payments"}}
	if _, err := r.Reconcile(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if v := getDeployment(t, cl, "checkout").Annotations[ownedLanguagesAnnotation]; v != "java,python" {
		t.Fatalf("setup: want ownership record %q, got %q", "java,python", v)
	}

	live := &v1alpha1.TsugaMonitoring{}
	if err := cl.Get(context.Background(), req.NamespacedName, live); err != nil {
		t.Fatal(err)
	}
	live.Spec.Instrumentation.Languages = []string{"java"}
	if err := cl.Update(context.Background(), live); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Reconcile(context.Background(), req); err != nil {
		t.Fatal(err)
	}

	gotDep := getDeployment(t, cl, "checkout")
	if v := gotDep.Spec.Template.Annotations[injectAnnotationPrefix+"java"]; v != "default" {
		t.Fatalf("want inject-java kept, got %q", v)
	}
	if _, ok := gotDep.Spec.Template.Annotations[injectAnnotationPrefix+"python"]; ok {
		t.Fatalf("want inject-python stripped, got %v", gotDep.Spec.Template.Annotations)
	}
	if v := gotDep.Annotations[ownedLanguagesAnnotation]; v != "java" {
		t.Fatalf("want ownership record %q, got %q", "java", v)
	}
}

// A workload an app team annotated before the operator ever saw it must not be
// claimed just because the operator wants the same annotation, or a later
// disable would strip it.
func TestMonitoringDoesNotClaimPreexistingInjectAnnotation(t *testing.T) {
	scheme := monitoringTestScheme(t)
	m := newMonitoring("java")
	dep := newTestDeployment("checkout", "payments")
	dep.Spec.Template.Annotations = map[string]string{injectAnnotationPrefix + "java": "default"}

	cl := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(newClusterConfig(), m, dep).WithStatusSubresource(m).
		WithInterceptorFuncs(applyAsCreateOrUpdate()).
		Build()

	r := &TsugaMonitoringReconciler{Client: cl, Scheme: scheme}
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "default", Namespace: "payments"}}
	if _, err := r.Reconcile(context.Background(), req); err != nil {
		t.Fatal(err)
	}

	gotDep := getDeployment(t, cl, "checkout")
	if _, ok := gotDep.Annotations[ownedLanguagesAnnotation]; ok {
		t.Fatalf("want no ownership claim over a pre-existing annotation, got %v", gotDep.Annotations)
	}
}

// A namespace-qualified annotation resolving to this Instrumentation already
// matches; it is neither a conflict nor rewritten.
func TestMonitoringAcceptsNamespaceQualifiedInjectAnnotation(t *testing.T) {
	scheme := monitoringTestScheme(t)
	m := newMonitoring("java")
	dep := newTestDeployment("checkout", "payments")
	dep.Spec.Template.Annotations = map[string]string{injectAnnotationPrefix + "java": "payments/default"}

	cl := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(newClusterConfig(), m, dep).WithStatusSubresource(m).
		WithInterceptorFuncs(applyAsCreateOrUpdate()).
		Build()

	r := &TsugaMonitoringReconciler{Client: cl, Scheme: scheme}
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "default", Namespace: "payments"}}
	if _, err := r.Reconcile(context.Background(), req); err != nil {
		t.Fatal(err)
	}

	live := &v1alpha1.TsugaMonitoring{}
	if err := cl.Get(context.Background(), req.NamespacedName, live); err != nil {
		t.Fatal(err)
	}
	if cond := meta.FindStatusCondition(live.Status.Conditions, conditionReady); cond == nil || cond.Reason == reasonInjectConflict {
		t.Fatalf("want no inject conflict, got %v", cond)
	}
	if v := getDeployment(t, cl, "checkout").Spec.Template.Annotations[injectAnnotationPrefix+"java"]; v != "payments/default" {
		t.Fatalf("want the annotation left as is, got %q", v)
	}
}

// A hand-set inject annotation with a different value must be reported as a
// conflict, not overwritten: overwriting would claim it, roll the workload,
// and a later disable would strip it.
func TestMonitoringDoesNotOverwriteHandSetInjectAnnotation(t *testing.T) {
	for _, handSet := range []string{"my-instr", "true", "false"} {
		t.Run(handSet, func(t *testing.T) {
			scheme := monitoringTestScheme(t)
			m := newMonitoring("java")
			dep := newTestDeployment("checkout", "payments")
			dep.Spec.Template.Annotations = map[string]string{injectAnnotationPrefix + "java": handSet}

			cl := fake.NewClientBuilder().WithScheme(scheme).
				WithObjects(newClusterConfig(), m, dep).WithStatusSubresource(m).
				WithInterceptorFuncs(applyAsCreateOrUpdate()).
				Build()

			r := &TsugaMonitoringReconciler{Client: cl, Scheme: scheme}
			req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "default", Namespace: "payments"}}
			if _, err := r.Reconcile(context.Background(), req); err != nil {
				t.Fatal(err)
			}
			rv := getDeployment(t, cl, "checkout").ResourceVersion

			live := &v1alpha1.TsugaMonitoring{}
			if err := cl.Get(context.Background(), req.NamespacedName, live); err != nil {
				t.Fatal(err)
			}
			cond := meta.FindStatusCondition(live.Status.Conditions, conditionReady)
			if cond == nil || cond.Reason != reasonInjectConflict || !strings.Contains(cond.Message, "checkout") {
				t.Fatalf("want a %s condition naming the workload, got %v", reasonInjectConflict, cond)
			}

			live.Spec.Instrumentation.Enabled = false
			if err := cl.Update(context.Background(), live); err != nil {
				t.Fatal(err)
			}
			if _, err := r.Reconcile(context.Background(), req); err != nil {
				t.Fatal(err)
			}

			gotDep := getDeployment(t, cl, "checkout")
			if v := gotDep.Spec.Template.Annotations[injectAnnotationPrefix+"java"]; v != handSet {
				t.Fatalf("want the hand-set value %q kept, got %q", handSet, v)
			}
			if _, ok := gotDep.Annotations[ownedLanguagesAnnotation]; ok {
				t.Fatalf("want no ownership claim, got %v", gotDep.Annotations)
			}
			if gotDep.ResourceVersion != rv {
				t.Fatalf("want the workload never patched, resourceVersion %q -> %q", rv, gotDep.ResourceVersion)
			}
		})
	}
}

// An annotation the operator owns is re-asserted when its value drifts.
func TestMonitoringReassertsOwnedInjectAnnotation(t *testing.T) {
	scheme := monitoringTestScheme(t)
	m := newMonitoring("java")
	dep := newTestDeployment("checkout", "payments")
	dep.Annotations = map[string]string{ownedLanguagesAnnotation: "java"}
	dep.Spec.Template.Annotations = map[string]string{injectAnnotationPrefix + "java": "stale"}

	cl := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(newClusterConfig(), m, dep).WithStatusSubresource(m).
		WithInterceptorFuncs(applyAsCreateOrUpdate()).
		Build()

	r := &TsugaMonitoringReconciler{Client: cl, Scheme: scheme}
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "default", Namespace: "payments"}}
	if _, err := r.Reconcile(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if v := getDeployment(t, cl, "checkout").Spec.Template.Annotations[injectAnnotationPrefix+"java"]; v != "default" {
		t.Fatalf("want the owned annotation re-asserted, got %q", v)
	}
}

func TestMonitoringDeletesOwnedInstrumentationWhenDisabled(t *testing.T) {
	scheme := monitoringTestScheme(t)
	m := newMonitoring("java")
	cl := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(newClusterConfig(), m).WithStatusSubresource(m).
		WithInterceptorFuncs(applyAsCreateOrUpdate()).
		Build()

	r := &TsugaMonitoringReconciler{Client: cl, Scheme: scheme}
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "default", Namespace: "payments"}}
	if _, err := r.Reconcile(context.Background(), req); err != nil {
		t.Fatal(err)
	}

	live := &v1alpha1.TsugaMonitoring{}
	if err := cl.Get(context.Background(), req.NamespacedName, live); err != nil {
		t.Fatal(err)
	}
	live.Spec.Instrumentation.Enabled = false
	if err := cl.Update(context.Background(), live); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Reconcile(context.Background(), req); err != nil {
		t.Fatal(err)
	}

	inst := &unstructured.Unstructured{}
	inst.SetGroupVersionKind(otelInstrumentationGVK)
	if err := cl.Get(context.Background(), req.NamespacedName, inst); !apierrors.IsNotFound(err) {
		t.Fatalf("want the owned Instrumentation deleted, got err=%v", err)
	}
	got := &v1alpha1.TsugaMonitoring{}
	if err := cl.Get(context.Background(), req.NamespacedName, got); err != nil {
		t.Fatal(err)
	}
	if len(got.Status.ManagedResources) != 0 {
		t.Fatalf("want ManagedResources cleared, got %v", got.Status.ManagedResources)
	}
}

func TestMonitoringKeepsUnownedInstrumentationWhenDisabled(t *testing.T) {
	scheme := monitoringTestScheme(t)
	m := newMonitoring("java")
	m.Spec.Instrumentation.Enabled = false
	byHand := &unstructured.Unstructured{}
	byHand.SetGroupVersionKind(otelInstrumentationGVK)
	byHand.SetName("default")
	byHand.SetNamespace("payments")
	cl := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(m, byHand).WithStatusSubresource(m).
		Build()

	r := &TsugaMonitoringReconciler{Client: cl, Scheme: scheme}
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "default", Namespace: "payments"}}
	if _, err := r.Reconcile(context.Background(), req); err != nil {
		t.Fatal(err)
	}

	inst := &unstructured.Unstructured{}
	inst.SetGroupVersionKind(otelInstrumentationGVK)
	if err := cl.Get(context.Background(), req.NamespacedName, inst); err != nil {
		t.Fatalf("want the hand-made Instrumentation kept, got %v", err)
	}
}

func TestMonitoringWorkloadEventEnqueuesNamespaceMonitoring(t *testing.T) {
	scheme := monitoringTestScheme(t)
	m := &v1alpha1.TsugaMonitoring{ObjectMeta: metav1.ObjectMeta{Name: "default", Namespace: "payments"}}
	other := &v1alpha1.TsugaMonitoring{ObjectMeta: metav1.ObjectMeta{Name: "default", Namespace: "other"}}
	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(m, other).Build()
	r := &TsugaMonitoringReconciler{Client: cl, Scheme: scheme}

	reqs := r.workloadToMonitorings(context.Background(), newTestDeployment("new-app", "payments"))
	if len(reqs) != 1 || reqs[0].NamespacedName != (types.NamespacedName{Name: "default", Namespace: "payments"}) {
		t.Fatalf("expected one request for payments/default, got %v", reqs)
	}
}

// Deleting the CR must strip the inject annotations it added, exactly as
// disabling it does: otherwise workloads keep pointing at an Instrumentation
// that garbage collection is about to remove.
func TestMonitoringDeleteStripsInjectAnnotationsAndRemovesFinalizer(t *testing.T) {
	scheme := monitoringTestScheme(t)
	m := newMonitoring("java")
	dep := newTestDeployment("checkout", "payments")

	cl := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(newClusterConfig(), m, dep).WithStatusSubresource(m).
		WithInterceptorFuncs(applyAsCreateOrUpdate()).
		Build()

	r := &TsugaMonitoringReconciler{Client: cl, Scheme: scheme}
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "default", Namespace: "payments"}}
	if _, err := r.Reconcile(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if v := getDeployment(t, cl, "checkout").Spec.Template.Annotations[injectAnnotationPrefix+"java"]; v != "default" {
		t.Fatalf("setup: want the operator to annotate checkout, got %q", v)
	}

	live := &v1alpha1.TsugaMonitoring{}
	if err := cl.Get(context.Background(), req.NamespacedName, live); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(live.Finalizers, monitoringFinalizer) {
		t.Fatalf("want finalizer %q on the CR, got %v", monitoringFinalizer, live.Finalizers)
	}
	if err := cl.Delete(context.Background(), live); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Reconcile(context.Background(), req); err != nil {
		t.Fatal(err)
	}

	gotDep := getDeployment(t, cl, "checkout")
	if _, ok := gotDep.Spec.Template.Annotations[injectAnnotationPrefix+"java"]; ok {
		t.Fatalf("want the inject annotation stripped on delete, got %v", gotDep.Spec.Template.Annotations)
	}
	if _, ok := gotDep.Annotations[ownedLanguagesAnnotation]; ok {
		t.Fatalf("want the ownership record cleared on delete, got %v", gotDep.Annotations)
	}
	if err := cl.Get(context.Background(), req.NamespacedName, &v1alpha1.TsugaMonitoring{}); !apierrors.IsNotFound(err) {
		t.Fatalf("want the CR gone once the finalizer is removed, got err=%v", err)
	}
}

// Two CRs in one namespace wanting the same language must not take turns
// overwriting the annotation: each patch rolls the workload, which re-enqueues
// both CRs, forever. The first claim wins and the second reports the conflict.
func TestMonitoringOverlappingLanguageDoesNotThrash(t *testing.T) {
	scheme := monitoringTestScheme(t)
	first := newMonitoring("java")
	second := newMonitoring("java")
	second.Name = "other"
	dep := newTestDeployment("checkout", "payments")

	cl := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(newClusterConfig(), first, second, dep).WithStatusSubresource(first, second).
		WithInterceptorFuncs(applyAsCreateOrUpdate()).
		Build()

	r := &TsugaMonitoringReconciler{Client: cl, Scheme: scheme}
	firstReq := ctrl.Request{NamespacedName: types.NamespacedName{Name: "default", Namespace: "payments"}}
	secondReq := ctrl.Request{NamespacedName: types.NamespacedName{Name: "other", Namespace: "payments"}}
	if _, err := r.Reconcile(context.Background(), firstReq); err != nil {
		t.Fatal(err)
	}
	rv := getDeployment(t, cl, "checkout").ResourceVersion

	for range 2 {
		if _, err := r.Reconcile(context.Background(), secondReq); err != nil {
			t.Fatal(err)
		}
		if _, err := r.Reconcile(context.Background(), firstReq); err != nil {
			t.Fatal(err)
		}
	}

	gotDep := getDeployment(t, cl, "checkout")
	if v := gotDep.Spec.Template.Annotations[injectAnnotationPrefix+"java"]; v != "default" {
		t.Fatalf("want the first claim kept, got %q", v)
	}
	if gotDep.ResourceVersion != rv {
		t.Fatalf("want the workload left alone after the first claim, resourceVersion %q -> %q", rv, gotDep.ResourceVersion)
	}

	got := &v1alpha1.TsugaMonitoring{}
	if err := cl.Get(context.Background(), secondReq.NamespacedName, got); err != nil {
		t.Fatal(err)
	}
	cond := meta.FindStatusCondition(got.Status.Conditions, conditionReady)
	if cond == nil || cond.Status != metav1.ConditionFalse || cond.Reason != reasonInjectConflict {
		t.Fatalf("want False/%s on the second CR, got %v", reasonInjectConflict, cond)
	}
	if !strings.Contains(cond.Message, "java") || !strings.Contains(cond.Message, "default") {
		t.Fatalf("want the conflict message to name the language and the claiming CR, got %q", cond.Message)
	}
}

// Two CRs with disjoint languages share the workload's ownership record; one
// must not strip the other's annotation because it is absent from its own spec.
func TestMonitoringDisjointLanguagesKeepEachOthersAnnotations(t *testing.T) {
	scheme := monitoringTestScheme(t)
	first := newMonitoring("java")
	second := newMonitoring("python")
	second.Name = "other"
	dep := newTestDeployment("checkout", "payments")

	cl := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(newClusterConfig(), first, second, dep).WithStatusSubresource(first, second).
		WithInterceptorFuncs(applyAsCreateOrUpdate()).
		Build()

	r := &TsugaMonitoringReconciler{Client: cl, Scheme: scheme}
	firstReq := ctrl.Request{NamespacedName: types.NamespacedName{Name: "default", Namespace: "payments"}}
	secondReq := ctrl.Request{NamespacedName: types.NamespacedName{Name: "other", Namespace: "payments"}}
	for _, req := range []ctrl.Request{firstReq, secondReq} {
		if _, err := r.Reconcile(context.Background(), req); err != nil {
			t.Fatal(err)
		}
	}
	rv := getDeployment(t, cl, "checkout").ResourceVersion
	for _, req := range []ctrl.Request{firstReq, secondReq} {
		if _, err := r.Reconcile(context.Background(), req); err != nil {
			t.Fatal(err)
		}
	}

	gotDep := getDeployment(t, cl, "checkout")
	if v := gotDep.Spec.Template.Annotations[injectAnnotationPrefix+"java"]; v != "default" {
		t.Fatalf("want inject-java kept for the first CR, got %q (annotations: %v)", v, gotDep.Spec.Template.Annotations)
	}
	if v := gotDep.Spec.Template.Annotations[injectAnnotationPrefix+"python"]; v != "other" {
		t.Fatalf("want inject-python kept for the second CR, got %q (annotations: %v)", v, gotDep.Spec.Template.Annotations)
	}
	if gotDep.ResourceVersion != rv {
		t.Fatalf("want steady state once both CRs applied, resourceVersion %q -> %q", rv, gotDep.ResourceVersion)
	}
}

func TestMonitoringReconcileRejectsUnownedExistingInstrumentation(t *testing.T) {
	scheme := monitoringTestScheme(t)
	m := newMonitoring("java")
	byHand := &unstructured.Unstructured{}
	byHand.SetGroupVersionKind(otelInstrumentationGVK)
	byHand.SetName("default")
	byHand.SetNamespace("payments")
	cl := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(newTestCollectorConfig(), m, byHand).WithStatusSubresource(m).
		WithInterceptorFuncs(applyAsCreateOrUpdate()).
		Build()

	r := &TsugaMonitoringReconciler{Client: cl, Scheme: scheme}
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "default", Namespace: "payments"}}
	if _, err := r.Reconcile(context.Background(), req); err == nil || !strings.Contains(err.Error(), "without a controller reference") {
		t.Fatalf("want an ownership error, got %v", err)
	}
	inst := &unstructured.Unstructured{}
	inst.SetGroupVersionKind(otelInstrumentationGVK)
	if err := cl.Get(context.Background(), req.NamespacedName, inst); err != nil {
		t.Fatal(err)
	}
	if refs := inst.GetOwnerReferences(); len(refs) != 0 {
		t.Fatalf("hand-made Instrumentation must not be adopted, got %v", refs)
	}
}
