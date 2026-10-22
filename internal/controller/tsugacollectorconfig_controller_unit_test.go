package controller

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/tsuga-dev/tsuga-operator/api/v1alpha1"
)

// newTestCollectorConfig returns a fully-enabled cluster TsugaCollectorConfig
// fixture used across the collectorconfig reconciler unit tests.
func newTestCollectorConfig() *v1alpha1.TsugaCollectorConfig {
	return &v1alpha1.TsugaCollectorConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
		Spec: v1alpha1.TsugaCollectorConfigSpec{
			Export:  &v1alpha1.ExportSpec{TokenSecretRef: v1alpha1.SecretKeyRef{Name: "s", Key: "k"}, Endpoint: "https://otlp.tsuga.com"},
			Agent:   v1alpha1.TelemetryToggles{Traces: true, Metrics: true, Logs: true},
			Gateway: v1alpha1.GatewayToggles{ClusterMetrics: true, KubernetesObjects: true},
		},
	}
}

const collectorConfigDefaultNamespace = "tsuga-operator-system"

// applyAsCreateOrUpdate works around the fake client's lack of server-side
// apply support (types.ApplyPatchType is rejected outright, see
// https://github.com/kubernetes/kubernetes/issues/115598) by translating an
// apply patch into a Create (object absent) or Update (object present)
// against the real fake client. Non-apply patches pass through unchanged.
func applyAsCreateOrUpdate() interceptor.Funcs {
	return interceptor.Funcs{
		Patch: func(ctx context.Context, cl client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
			if patch.Type() != types.ApplyPatchType {
				return cl.Patch(ctx, obj, patch, opts...)
			}
			existing := obj.DeepCopyObject().(client.Object)
			err := cl.Get(ctx, client.ObjectKeyFromObject(obj), existing)
			if apierrors.IsNotFound(err) {
				return cl.Create(ctx, obj)
			}
			if err != nil {
				return err
			}
			obj.SetResourceVersion(existing.GetResourceVersion())
			return cl.Update(ctx, obj)
		},
	}
}

func TestCollectorConfigReconcileCreatesCollectors(t *testing.T) {
	scheme := testScheme(t)
	cc := &v1alpha1.TsugaCollectorConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
		Spec: v1alpha1.TsugaCollectorConfigSpec{
			Export:  &v1alpha1.ExportSpec{TokenSecretRef: v1alpha1.SecretKeyRef{Name: "s", Key: "k"}, Endpoint: "https://otlp.tsuga.com"},
			Agent:   v1alpha1.TelemetryToggles{Traces: true, Metrics: true, Logs: true},
			Gateway: v1alpha1.GatewayToggles{ClusterMetrics: true, KubernetesObjects: true},
		},
	}
	cl := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(cc).WithStatusSubresource(cc).
		WithInterceptorFuncs(applyAsCreateOrUpdate()).
		Build()

	r := &TsugaCollectorConfigReconciler{Client: cl, Scheme: scheme}
	_, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "cluster"}})
	if err != nil {
		t.Fatal(err)
	}

	got := &v1alpha1.TsugaCollectorConfig{}
	if err := cl.Get(context.Background(), types.NamespacedName{Name: "cluster"}, got); err != nil {
		t.Fatal(err)
	}
	if got.Status.Phase != phaseReady {
		t.Fatalf("want Ready, got %q", got.Status.Phase)
	}
	// 2 collectors, their ServiceAccounts, and a ClusterRole and ClusterRoleBinding for each.
	if len(got.Status.ManagedResources) != 8 {
		t.Fatalf("want 8 managed resources, got %v", got.Status.ManagedResources)
	}

	for _, name := range []string{"tsuga-agent", "tsuga-gateway"} {
		collector := &unstructured.Unstructured{}
		collector.SetGroupVersionKind(otelCollectorGVK)
		key := types.NamespacedName{Name: name, Namespace: collectorConfigDefaultNamespace}
		if err := cl.Get(context.Background(), key, collector); err != nil {
			t.Fatalf("expected OpenTelemetryCollector %q to be persisted: %v", name, err)
		}
		if got := collector.GetNamespace(); got != collectorConfigDefaultNamespace {
			t.Fatalf("collector %q: want namespace %q, got %q", name, collectorConfigDefaultNamespace, got)
		}

		var ownedByConfig bool
		for _, ref := range collector.GetOwnerReferences() {
			if ref.Kind == "TsugaCollectorConfig" && ref.Controller != nil && *ref.Controller {
				ownedByConfig = true
				break
			}
		}
		if !ownedByConfig {
			t.Fatalf("collector %q: want a controller ownerReference to TsugaCollectorConfig, got %v", name, collector.GetOwnerReferences())
		}
	}
}

func ownedByCollectorConfig(refs []metav1.OwnerReference) bool {
	for _, ref := range refs {
		if ref.Kind == "TsugaCollectorConfig" && ref.Controller != nil && *ref.Controller {
			return true
		}
	}
	return false
}

func TestCollectorConfigReconcileCreatesRBAC(t *testing.T) {
	scheme := testScheme(t)
	cc := newTestCollectorConfig()
	cl := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(cc).WithStatusSubresource(cc).
		WithInterceptorFuncs(applyAsCreateOrUpdate()).
		Build()

	r := &TsugaCollectorConfigReconciler{Client: cl, Scheme: scheme}
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "cluster"}}); err != nil {
		t.Fatal(err)
	}

	got := &v1alpha1.TsugaCollectorConfig{}
	if err := cl.Get(context.Background(), types.NamespacedName{Name: "cluster"}, got); err != nil {
		t.Fatal(err)
	}

	for collector, sa := range map[string]string{
		"tsuga-agent":   "tsuga-agent",
		"tsuga-gateway": "tsuga-gateway",
	} {
		name := "tsuga-operator-collector-" + collector
		role := &rbacv1.ClusterRole{}
		if err := cl.Get(context.Background(), types.NamespacedName{Name: name}, role); err != nil {
			t.Fatalf("expected ClusterRole %q to be persisted: %v", name, err)
		}
		if len(role.Rules) == 0 {
			t.Fatalf("ClusterRole %q should have rules", name)
		}
		if !ownedByCollectorConfig(role.GetOwnerReferences()) {
			t.Fatalf("ClusterRole %q: want controller ownerReference to TsugaCollectorConfig, got %v", name, role.GetOwnerReferences())
		}

		binding := &rbacv1.ClusterRoleBinding{}
		if err := cl.Get(context.Background(), types.NamespacedName{Name: name}, binding); err != nil {
			t.Fatalf("expected ClusterRoleBinding %q to be persisted: %v", name, err)
		}
		if !ownedByCollectorConfig(binding.GetOwnerReferences()) {
			t.Fatalf("ClusterRoleBinding %q: want controller ownerReference to TsugaCollectorConfig, got %v", name, binding.GetOwnerReferences())
		}
		want := rbacv1.Subject{Kind: rbacv1.ServiceAccountKind, Name: sa, Namespace: collectorConfigDefaultNamespace}
		if len(binding.Subjects) != 1 || binding.Subjects[0] != want {
			t.Fatalf("ClusterRoleBinding %q: want subjects [%v], got %v", name, want, binding.Subjects)
		}

		if !slices.Contains(got.Status.ManagedResources, name) {
			t.Fatalf("status.ManagedResources should include %q, got %v", name, got.Status.ManagedResources)
		}
	}
}

func TestCollectorConfigReconcileSteadyStateIsNoOp(t *testing.T) {
	scheme := testScheme(t)
	cc := newTestCollectorConfig()
	cl := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(cc).WithStatusSubresource(cc).
		WithInterceptorFuncs(applyAsCreateOrUpdate()).
		Build()

	r := &TsugaCollectorConfigReconciler{Client: cl, Scheme: scheme}
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "cluster"}}
	if _, err := r.Reconcile(context.Background(), req); err != nil {
		t.Fatal(err)
	}

	afterFirst := &v1alpha1.TsugaCollectorConfig{}
	if err := cl.Get(context.Background(), req.NamespacedName, afterFirst); err != nil {
		t.Fatal(err)
	}
	rv := afterFirst.GetResourceVersion()
	syncedAt := afterFirst.Status.LastSyncedAt

	if _, err := r.Reconcile(context.Background(), req); err != nil {
		t.Fatal(err)
	}

	afterSecond := &v1alpha1.TsugaCollectorConfig{}
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

func TestCollectorConfigReconcileCreatesScraperAndTargetAllocator(t *testing.T) {
	scheme := testScheme(t)
	cc := newTestCollectorConfig()
	cc.Spec.Prometheus = v1alpha1.PrometheusSpec{
		Enabled:         true,
		ScrapeInterval:  "30s",
		Replicas:        2,
		TargetAllocator: v1alpha1.TargetAllocatorSpec{Enabled: true, AllocationStrategy: "consistent-hashing"},
	}
	cl := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(cc).
		WithStatusSubresource(cc).
		WithInterceptorFuncs(applyAsCreateOrUpdate()).
		Build()
	r := &TsugaCollectorConfigReconciler{Client: cl, Scheme: scheme}

	if _, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "cluster"},
	}); err != nil {
		t.Fatal(err)
	}

	scraper := &unstructured.Unstructured{}
	scraper.SetGroupVersionKind(collectorGVK)
	if err := cl.Get(context.Background(), types.NamespacedName{
		Name: "tsuga-scraper", Namespace: collectorConfigDefaultNamespace,
	}, scraper); err != nil {
		t.Fatalf("scraper collector not created: %v", err)
	}

	ta := &unstructured.Unstructured{}
	ta.SetGroupVersionKind(targetAllocatorGVK)
	if err := cl.Get(context.Background(), types.NamespacedName{
		Name: "tsuga-scraper-ta", Namespace: collectorConfigDefaultNamespace,
	}, ta); err != nil {
		t.Fatalf("target allocator not created: %v", err)
	}

	updated := &v1alpha1.TsugaCollectorConfig{}
	if err := cl.Get(context.Background(), types.NamespacedName{Name: "cluster"}, updated); err != nil {
		t.Fatal(err)
	}
	managed := map[string]bool{}
	for _, n := range updated.Status.ManagedResources {
		managed[n] = true
	}
	for _, want := range []string{"tsuga-scraper", "tsuga-scraper-ta"} {
		if !managed[want] {
			t.Fatalf("status.managedResources missing %q: %v", want, updated.Status.ManagedResources)
		}
	}
}

func TestCollectorConfigReconcilePrunesChildrenNoLongerDesired(t *testing.T) {
	scheme := testScheme(t)
	cc := newTestCollectorConfig()
	cc.UID = "cc-uid"
	cc.Spec.Prometheus = v1alpha1.PrometheusSpec{
		Enabled:         true,
		ScrapeInterval:  "30s",
		Replicas:        1,
		TargetAllocator: v1alpha1.TargetAllocatorSpec{Enabled: true, AllocationStrategy: "consistent-hashing"},
	}
	// Owned by another controller: must survive the prune.
	foreign := &unstructured.Unstructured{}
	foreign.SetGroupVersionKind(collectorGVK)
	foreign.SetName("someone-else")
	foreign.SetNamespace(collectorConfigDefaultNamespace)
	foreignRole := &rbacv1.ClusterRole{ObjectMeta: metav1.ObjectMeta{Name: "someone-else"}}
	// The shared role granted to every collector before RBAC was split per
	// ServiceAccount: an upgrade must remove it.
	isController := true
	legacyRole := &rbacv1.ClusterRole{ObjectMeta: metav1.ObjectMeta{
		Name: "tsuga-operator-collector",
		OwnerReferences: []metav1.OwnerReference{{
			APIVersion: v1alpha1.GroupVersion.String(), Kind: "TsugaCollectorConfig",
			Name: cc.Name, UID: cc.UID, Controller: &isController,
		}},
	}}
	legacyBinding := &rbacv1.ClusterRoleBinding{
		ObjectMeta: *legacyRole.ObjectMeta.DeepCopy(),
		RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: legacyRole.Name},
	}
	cl := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(cc, foreign, foreignRole, legacyRole, legacyBinding).WithStatusSubresource(cc).
		WithInterceptorFuncs(applyAsCreateOrUpdate()).
		Build()
	r := &TsugaCollectorConfigReconciler{Client: cl, Scheme: scheme}
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "cluster"}}
	if _, err := r.Reconcile(context.Background(), req); err != nil {
		t.Fatal(err)
	}

	current := &v1alpha1.TsugaCollectorConfig{}
	if err := cl.Get(context.Background(), req.NamespacedName, current); err != nil {
		t.Fatal(err)
	}
	current.Spec.Gateway = v1alpha1.GatewayToggles{}
	current.Spec.Prometheus = v1alpha1.PrometheusSpec{}
	current.Spec.CollectorNamespace = "moved"
	if err := cl.Update(context.Background(), current); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Reconcile(context.Background(), req); err != nil {
		t.Fatal(err)
	}

	exists := func(gvk schema.GroupVersionKind, ns, name string) bool {
		obj := &unstructured.Unstructured{}
		obj.SetGroupVersionKind(gvk)
		err := cl.Get(context.Background(), types.NamespacedName{Namespace: ns, Name: name}, obj)
		if err != nil && !apierrors.IsNotFound(err) {
			t.Fatal(err)
		}
		return err == nil
	}
	for _, name := range []string{"tsuga-agent", "tsuga-gateway", "tsuga-scraper"} {
		if exists(collectorGVK, collectorConfigDefaultNamespace, name) {
			t.Errorf("collector %s in old namespace not pruned", name)
		}
	}
	if exists(targetAllocatorGVK, collectorConfigDefaultNamespace, "tsuga-scraper-ta") {
		t.Error("target allocator not pruned")
	}
	if !exists(collectorGVK, "moved", "tsuga-agent") {
		t.Error("agent not created in new namespace")
	}
	if !exists(collectorGVK, collectorConfigDefaultNamespace, "someone-else") {
		t.Error("collector not owned by the config was pruned")
	}

	roleGVK := rbacv1.SchemeGroupVersion.WithKind("ClusterRole")
	bindingGVK := rbacv1.SchemeGroupVersion.WithKind("ClusterRoleBinding")
	for _, name := range []string{"tsuga-operator-collector", "tsuga-operator-collector-tsuga-gateway", "tsuga-operator-collector-tsuga-scraper"} {
		if exists(roleGVK, "", name) || exists(bindingGVK, "", name) {
			t.Errorf("RBAC %s not pruned", name)
		}
	}
	if !exists(roleGVK, "", "tsuga-operator-collector-tsuga-agent") || !exists(bindingGVK, "", "tsuga-operator-collector-tsuga-agent") {
		t.Error("agent RBAC missing")
	}
	if !exists(roleGVK, "", "someone-else") {
		t.Error("ClusterRole not owned by the config was pruned")
	}
}

func TestCollectorConfigSetsReadyCondition(t *testing.T) {
	scheme := testScheme(t)
	cc := newTestCollectorConfig()
	cc.Generation = 2
	cl := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(cc).WithStatusSubresource(cc).
		WithInterceptorFuncs(applyAsCreateOrUpdate()).
		Build()

	r := &TsugaCollectorConfigReconciler{Client: cl, Scheme: scheme}
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "cluster"}}
	if _, err := r.Reconcile(context.Background(), req); err != nil {
		t.Fatal(err)
	}

	got := &v1alpha1.TsugaCollectorConfig{}
	if err := cl.Get(context.Background(), req.NamespacedName, got); err != nil {
		t.Fatal(err)
	}
	cond := meta.FindStatusCondition(got.Status.Conditions, conditionReady)
	if cond == nil || cond.Status != metav1.ConditionTrue || cond.Reason != reasonReconciled {
		t.Fatalf("want True/%s, got %v", reasonReconciled, cond)
	}
	if cond.ObservedGeneration != 2 {
		t.Fatalf("want observedGeneration 2, got %d", cond.ObservedGeneration)
	}

	// A second pass must not rewrite status: SetStatusCondition keeps
	// LastTransitionTime stable, and the equality check ignores it.
	rv := got.GetResourceVersion()
	if _, err := r.Reconcile(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	afterSecond := &v1alpha1.TsugaCollectorConfig{}
	if err := cl.Get(context.Background(), req.NamespacedName, afterSecond); err != nil {
		t.Fatal(err)
	}
	if afterSecond.GetResourceVersion() != rv {
		t.Fatalf("steady-state reconcile must not write status: resourceVersion %q -> %q", rv, afterSecond.GetResourceVersion())
	}
}

func TestCollectorConfigReconcileRejectsUnownedExistingChild(t *testing.T) {
	scheme := testScheme(t)
	cc := newTestCollectorConfig()
	byHelm := &rbacv1.ClusterRole{ObjectMeta: metav1.ObjectMeta{Name: "tsuga-operator-collector-tsuga-agent"}}
	cl := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(cc, byHelm).WithStatusSubresource(cc).
		WithInterceptorFuncs(applyAsCreateOrUpdate()).
		Build()

	r := &TsugaCollectorConfigReconciler{Client: cl, Scheme: scheme}
	_, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "cluster"}})
	if err == nil || !strings.Contains(err.Error(), "without a controller reference") {
		t.Fatalf("want an ownership error, got %v", err)
	}
	got := &rbacv1.ClusterRole{}
	if err := cl.Get(context.Background(), client.ObjectKeyFromObject(byHelm), got); err != nil {
		t.Fatal(err)
	}
	if len(got.OwnerReferences) != 0 {
		t.Fatalf("unowned ClusterRole must not be adopted, got %v", got.OwnerReferences)
	}
}

func reconcileCluster(t *testing.T, r *TsugaCollectorConfigReconciler) {
	t.Helper()
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "cluster"}}); err != nil {
		t.Fatal(err)
	}
}

func updateClusterSpec(t *testing.T, cl client.Client, mutate func(*v1alpha1.TsugaCollectorConfigSpec)) {
	t.Helper()
	cc := &v1alpha1.TsugaCollectorConfig{}
	if err := cl.Get(context.Background(), types.NamespacedName{Name: "cluster"}, cc); err != nil {
		t.Fatal(err)
	}
	mutate(&cc.Spec)
	if err := cl.Update(context.Background(), cc); err != nil {
		t.Fatal(err)
	}
}

func TestCollectorConfigReconcileCreatesAndPrunesServiceAccounts(t *testing.T) {
	scheme := testScheme(t)
	cc := newTestCollectorConfig()
	cc.Spec.Profiling.Enabled = true
	cl := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(cc).WithStatusSubresource(cc).
		WithInterceptorFuncs(applyAsCreateOrUpdate()).
		Build()
	r := &TsugaCollectorConfigReconciler{Client: cl, Scheme: scheme}

	reconcileCluster(t, r)
	profilerKey := types.NamespacedName{Name: "tsuga-profiler", Namespace: collectorConfigDefaultNamespace}
	sa := &corev1.ServiceAccount{}
	if err := cl.Get(context.Background(), profilerKey, sa); err != nil {
		t.Fatalf("expected ServiceAccount tsuga-profiler: %v", err)
	}
	if !ownedByCollectorConfig(sa.GetOwnerReferences()) {
		t.Fatalf("ServiceAccount must be controlled by the config, got %v", sa.GetOwnerReferences())
	}

	updateClusterSpec(t, cl, func(s *v1alpha1.TsugaCollectorConfigSpec) { s.Profiling.Enabled = false })
	reconcileCluster(t, r)
	if err := cl.Get(context.Background(), profilerKey, &corev1.ServiceAccount{}); !apierrors.IsNotFound(err) {
		t.Fatalf("profiler ServiceAccount must be pruned, got %v", err)
	}
	agentKey := types.NamespacedName{Name: "tsuga-agent", Namespace: collectorConfigDefaultNamespace}
	if err := cl.Get(context.Background(), agentKey, &corev1.ServiceAccount{}); err != nil {
		t.Fatalf("agent ServiceAccount must stay: %v", err)
	}

	updateClusterSpec(t, cl, func(s *v1alpha1.TsugaCollectorConfigSpec) {
		s.ServiceAccount = v1alpha1.ServiceAccountSpec{Create: ptr.To(false), Name: "my-sa"}
	})
	reconcileCluster(t, r)
	if err := cl.Get(context.Background(), agentKey, &corev1.ServiceAccount{}); !apierrors.IsNotFound(err) {
		t.Fatalf("create: false must prune operator ServiceAccounts, got %v", err)
	}
}

func TestCollectorConfigReconcilePrunesRBACWhenRBACCreateOff(t *testing.T) {
	scheme := testScheme(t)
	cc := newTestCollectorConfig()
	cl := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(cc).WithStatusSubresource(cc).
		WithInterceptorFuncs(applyAsCreateOrUpdate()).
		Build()
	r := &TsugaCollectorConfigReconciler{Client: cl, Scheme: scheme}

	reconcileCluster(t, r)
	roleKey := types.NamespacedName{Name: "tsuga-operator-collector-tsuga-agent"}
	if err := cl.Get(context.Background(), roleKey, &rbacv1.ClusterRole{}); err != nil {
		t.Fatalf("expected ClusterRole before rbac.create is turned off: %v", err)
	}

	updateClusterSpec(t, cl, func(s *v1alpha1.TsugaCollectorConfigSpec) { s.RBAC.Create = ptr.To(false) })
	reconcileCluster(t, r)
	if err := cl.Get(context.Background(), roleKey, &rbacv1.ClusterRole{}); !apierrors.IsNotFound(err) {
		t.Fatalf("rbac.create: false must prune the ClusterRole, got %v", err)
	}
	if err := cl.Get(context.Background(), roleKey, &rbacv1.ClusterRoleBinding{}); !apierrors.IsNotFound(err) {
		t.Fatalf("rbac.create: false must prune the ClusterRoleBinding, got %v", err)
	}
}

func TestCollectorConfigReconcilePrunesScraperSecretsRole(t *testing.T) {
	scheme := testScheme(t)
	cc := prometheusCRCollectorConfig()
	cl := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(cc).WithStatusSubresource(cc).
		WithInterceptorFuncs(applyAsCreateOrUpdate()).
		Build()
	r := &TsugaCollectorConfigReconciler{Client: cl, Scheme: scheme}
	exists := func(namespace string) bool {
		key := types.NamespacedName{Name: "tsuga-operator-collector-tsuga-scraper", Namespace: namespace}
		roleErr := cl.Get(context.Background(), key, &rbacv1.Role{})
		bindingErr := cl.Get(context.Background(), key, &rbacv1.RoleBinding{})
		for _, err := range []error{roleErr, bindingErr} {
			if err != nil && !apierrors.IsNotFound(err) {
				t.Fatal(err)
			}
		}
		return roleErr == nil && bindingErr == nil
	}

	reconcileCluster(t, r)
	if !exists(collectorConfigDefaultNamespace) {
		t.Fatal("prometheusCR must render the scraper secrets Role and RoleBinding")
	}

	updateClusterSpec(t, cl, func(s *v1alpha1.TsugaCollectorConfigSpec) { s.CollectorNamespace = "observability" })
	reconcileCluster(t, r)
	if exists(collectorConfigDefaultNamespace) || !exists("observability") {
		t.Fatal("a collector namespace change must move the scraper secrets Role and RoleBinding")
	}

	updateClusterSpec(t, cl, func(s *v1alpha1.TsugaCollectorConfigSpec) { s.Prometheus.PrometheusCR.Enabled = false })
	reconcileCluster(t, r)
	if exists("observability") {
		t.Fatal("prometheusCR off must prune the scraper secrets Role and RoleBinding")
	}
}

func prometheusCRCollectorConfig() *v1alpha1.TsugaCollectorConfig {
	cc := newTestCollectorConfig()
	cc.Spec.Prometheus.Enabled = true
	cc.Spec.Prometheus.TargetAllocator.Enabled = true
	cc.Spec.Prometheus.PrometheusCR.Enabled = true
	return cc
}

func reconcileForReadyCondition(t *testing.T, cc *v1alpha1.TsugaCollectorConfig, mapper func(meta.RESTMapper) meta.RESTMapper) (client.Client, ctrl.Result, *metav1.Condition) {
	t.Helper()
	scheme := testScheme(t)
	b := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(cc).WithStatusSubresource(cc).
		WithInterceptorFuncs(applyAsCreateOrUpdate())
	if mapper != nil {
		b = b.WithRESTMapper(mapper(fake.NewClientBuilder().WithScheme(scheme).Build().RESTMapper()))
	}
	cl := b.Build()
	r := &TsugaCollectorConfigReconciler{Client: cl, Scheme: scheme}
	res, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: cc.Name}})
	if err != nil {
		t.Fatal(err)
	}
	got := &v1alpha1.TsugaCollectorConfig{}
	if err := cl.Get(context.Background(), types.NamespacedName{Name: cc.Name}, got); err != nil {
		t.Fatal(err)
	}
	return cl, res, meta.FindStatusCondition(got.Status.Conditions, conditionReady)
}

func TestCollectorConfigReportsMissingPrometheusCRDs(t *testing.T) {
	cl, res, cond := reconcileForReadyCondition(t, prometheusCRCollectorConfig(), nil)
	if res.RequeueAfter != 5*time.Minute {
		t.Fatalf("RequeueAfter = %v, want 5m", res.RequeueAfter)
	}

	if cond == nil || cond.Status != metav1.ConditionFalse || cond.Reason != reasonPrometheusCRDsMissing {
		t.Fatalf("want Ready=False/%s, got %+v", reasonPrometheusCRDsMissing, cond)
	}
	const wantMsg = "prometheus.prometheusCR is enabled but the monitoring.coreos.com ServiceMonitor/PodMonitor CRDs are not installed; install the Prometheus Operator CRDs, then restart the tsuga-scraper-ta-targetallocator Deployment, or disable prometheusCR"
	if cond.Message != wantMsg {
		t.Fatalf("message = %q", cond.Message)
	}
	got := &v1alpha1.TsugaCollectorConfig{}
	if err := cl.Get(context.Background(), types.NamespacedName{Name: "cluster"}, got); err != nil {
		t.Fatal(err)
	}
	if got.Status.Phase != phaseError || got.Status.Message != wantMsg {
		t.Fatalf("phase=%q message=%q, want %q with the condition message", got.Status.Phase, got.Status.Message, phaseError)
	}
	ta := &unstructured.Unstructured{}
	ta.SetGroupVersionKind(targetAllocatorGVK)
	if err := cl.Get(context.Background(), types.NamespacedName{Name: "tsuga-scraper-ta", Namespace: collectorConfigDefaultNamespace}, ta); err != nil {
		t.Fatalf("children should still be applied: %v", err)
	}
}

func TestCollectorConfigReadyWithPrometheusCR(t *testing.T) {
	cases := []struct {
		name      string
		kinds     []string
		scraperOn bool
	}{
		{"both CRDs present", []string{"ServiceMonitor", "PodMonitor"}, true},
		{"only ServiceMonitor present", []string{"ServiceMonitor"}, true},
		{"scraper off", nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cc := prometheusCRCollectorConfig()
			cc.Spec.Prometheus.Enabled = tc.scraperOn
			var mapper func(meta.RESTMapper) meta.RESTMapper
			if tc.kinds != nil {
				mapper = func(base meta.RESTMapper) meta.RESTMapper {
					gv := schema.GroupVersion{Group: "monitoring.coreos.com", Version: "v1"}
					m := meta.NewDefaultRESTMapper([]schema.GroupVersion{gv})
					for _, k := range tc.kinds {
						m.Add(gv.WithKind(k), meta.RESTScopeNamespace)
					}
					return meta.MultiRESTMapper{base, m}
				}
			}
			_, res, cond := reconcileForReadyCondition(t, cc, mapper)
			if res.RequeueAfter != 0 {
				t.Fatalf("RequeueAfter = %v, want 0", res.RequeueAfter)
			}
			if cond == nil || cond.Status != metav1.ConditionTrue || cond.Reason != reasonReconciled {
				t.Fatalf("want Ready=True/%s, got %+v", reasonReconciled, cond)
			}
		})
	}
}

func TestCollectorConfigMissingCRDsSteadyStateKeepsStatus(t *testing.T) {
	scheme := testScheme(t)
	cc := prometheusCRCollectorConfig()
	cl := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(cc).WithStatusSubresource(cc).
		WithInterceptorFuncs(applyAsCreateOrUpdate()).
		Build()
	r := &TsugaCollectorConfigReconciler{Client: cl, Scheme: scheme}
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: cc.Name}}

	resourceVersion := func() string {
		got := &v1alpha1.TsugaCollectorConfig{}
		if err := cl.Get(context.Background(), req.NamespacedName, got); err != nil {
			t.Fatal(err)
		}
		return got.ResourceVersion
	}
	if _, err := r.Reconcile(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	first := resourceVersion()
	res, err := r.Reconcile(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if res.RequeueAfter != 5*time.Minute {
		t.Fatalf("RequeueAfter = %v, want 5m", res.RequeueAfter)
	}
	if second := resourceVersion(); second != first {
		t.Fatalf("status rewritten on steady state: resourceVersion %s -> %s", first, second)
	}
}
