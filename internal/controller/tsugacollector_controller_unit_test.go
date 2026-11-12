package controller

import (
	"context"
	"slices"
	"strings"
	"testing"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/tsuga-dev/tsuga-operator/api/v1alpha1"
)

var (
	colKey      = types.NamespacedName{Name: "ndm", Namespace: "observability"}
	colChildKey = types.NamespacedName{Name: "tsuga-ndm", Namespace: "observability"}
)

func newTestTsugaCollector(mutate ...func(*v1alpha1.TsugaCollectorSpec)) *v1alpha1.TsugaCollector {
	c := &v1alpha1.TsugaCollector{
		ObjectMeta: metav1.ObjectMeta{Name: colKey.Name, Namespace: colKey.Namespace, UID: "col-uid"},
		Spec: v1alpha1.TsugaCollectorSpec{
			Collector: &apiextensionsv1.JSON{Raw: []byte(`{"config":{"receivers":{"netflow":{}},"service":{"pipelines":{"logs/netflow":{"receivers":["netflow"]}}}}}`)},
		},
	}
	for _, m := range mutate {
		m(&c.Spec)
	}
	return c
}

// newTestColCluster sets the agent signal toggles the CRD defaults to true.
func newTestColCluster() *v1alpha1.TsugaCollectorConfig {
	cluster := newTestCluster()
	cluster.Spec.Agent.Traces, cluster.Spec.Agent.Metrics, cluster.Spec.Agent.Logs = true, true, true
	return cluster
}

func newColClient(t *testing.T, objs ...client.Object) (client.Client, *TsugaCollectorReconciler) {
	t.Helper()
	scheme := testScheme(t)
	cl := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(objs...).
		WithStatusSubresource(&v1alpha1.TsugaCollector{}).
		WithInterceptorFuncs(applyAsCreateOrUpdate()).
		Build()
	return cl, &TsugaCollectorReconciler{Client: cl, Scheme: scheme}
}

func reconcileCol(t *testing.T, r *TsugaCollectorReconciler) error {
	t.Helper()
	_, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: colKey})
	return err
}

func colReady(t *testing.T, cl client.Client) (*v1alpha1.TsugaCollector, metav1.Condition) {
	t.Helper()
	got := &v1alpha1.TsugaCollector{}
	if err := cl.Get(context.Background(), colKey, got); err != nil {
		t.Fatal(err)
	}
	cond := meta.FindStatusCondition(got.Status.Conditions, "Ready")
	if cond == nil {
		t.Fatal("no Ready condition")
	}
	return got, *cond
}

func getColChild(cl client.Client) (*unstructured.Unstructured, error) {
	child := &unstructured.Unstructured{}
	child.SetGroupVersionKind(collectorGVK)
	return child, cl.Get(context.Background(), colChildKey, child)
}

func TestTsugaCollectorReconcileRendersOwnedCollector(t *testing.T) {
	cl, r := newColClient(t, newTestColCluster(), newTestTsugaCollector())
	if err := reconcileCol(t, r); err != nil {
		t.Fatal(err)
	}
	child, err := getColChild(cl)
	if err != nil {
		t.Fatal(err)
	}
	if !metav1.IsControlledBy(child, newTestTsugaCollector()) {
		t.Fatalf("child not controlled by the CR: %v", child.GetOwnerReferences())
	}
	endpoint, _, _ := unstructured.NestedString(child.Object, "spec", "config", "exporters", "otlp/tsuga-agent", "endpoint")
	if endpoint != "tsuga-agent-collector.tsuga-operator-system.svc.cluster.local:4317" {
		t.Fatalf("exporter endpoint = %q", endpoint)
	}
	got, cond := colReady(t, cl)
	if cond.Status != metav1.ConditionTrue || cond.Reason != reasonReconciled {
		t.Fatalf("Ready = %+v", cond)
	}
	if !slices.Equal(got.Status.ManagedResources, []string{"tsuga-ndm"}) {
		t.Fatalf("managed = %v", got.Status.ManagedResources)
	}
}

func TestTsugaCollectorReconcileClusterConfigMissing(t *testing.T) {
	cl, r := newColClient(t, newTestTsugaCollector())
	if err := reconcileCol(t, r); err == nil {
		t.Fatal("want an error without the cluster config")
	}
	if _, cond := colReady(t, cl); cond.Status != metav1.ConditionFalse || cond.Reason != reasonClusterConfigMissing {
		t.Fatalf("Ready = %+v", cond)
	}
}

func TestTsugaCollectorAgentDisabledRetiresCollector(t *testing.T) {
	cl, r := newColClient(t, newTestColCluster(), newTestTsugaCollector())
	if err := reconcileCol(t, r); err != nil {
		t.Fatal(err)
	}
	updateClusterSpec(t, cl, func(s *v1alpha1.TsugaCollectorConfigSpec) { s.Agent.Enabled = ptr.To(false) })
	if err := reconcileCol(t, r); err != nil {
		t.Fatal(err)
	}
	if _, err := getColChild(cl); !apierrors.IsNotFound(err) {
		t.Fatalf("want the owned collector deleted, got err=%v", err)
	}
	if _, cond := colReady(t, cl); cond.Status != metav1.ConditionFalse || cond.Reason != reasonAgentDisabled {
		t.Fatalf("Ready = %+v", cond)
	}
}

func TestTsugaCollectorWithoutAgentTogglesIgnoresAgent(t *testing.T) {
	cluster := newTestColCluster()
	cluster.Spec.Agent.Enabled = ptr.To(false)
	cl, r := newColClient(t, cluster, newTestTsugaCollector(func(s *v1alpha1.TsugaCollectorSpec) {
		s.TsugaExporter, s.SelfTelemetry = ptr.To(false), ptr.To(false)
	}))
	if err := reconcileCol(t, r); err != nil {
		t.Fatal(err)
	}
	if _, err := getColChild(cl); err != nil {
		t.Fatalf("want the collector rendered, got err=%v", err)
	}
	if _, cond := colReady(t, cl); cond.Status != metav1.ConditionTrue {
		t.Fatalf("Ready = %+v", cond)
	}
}

func TestTsugaCollectorRenderFailureKeepsLastCollector(t *testing.T) {
	cl, r := newColClient(t, newTestColCluster(), newTestTsugaCollector())
	if err := reconcileCol(t, r); err != nil {
		t.Fatal(err)
	}
	updateClusterSpec(t, cl, func(s *v1alpha1.TsugaCollectorConfigSpec) { s.Agent.Logs = false })
	err := reconcileCol(t, r)
	if err == nil || !strings.Contains(err.Error(), "logs/netflow") {
		t.Fatalf("want a render error naming logs/netflow, got %v", err)
	}
	if _, cond := colReady(t, cl); cond.Status != metav1.ConditionFalse || cond.Reason != reasonRenderFailed {
		t.Fatalf("Ready = %+v", cond)
	}
	if _, err := getColChild(cl); err != nil {
		t.Fatalf("want the last good collector kept, got err=%v", err)
	}
}

func TestTsugaCollectorRejectsUnownedCollector(t *testing.T) {
	existing := &unstructured.Unstructured{}
	existing.SetGroupVersionKind(collectorGVK)
	existing.SetName(colChildKey.Name)
	existing.SetNamespace(colChildKey.Namespace)
	cl, r := newColClient(t, newTestColCluster(), newTestTsugaCollector(), existing)
	err := reconcileCol(t, r)
	if err == nil || !strings.Contains(err.Error(), "without a controller reference") {
		t.Fatalf("want an ownership error, got %v", err)
	}
	if _, cond := colReady(t, cl); cond.Reason != reasonApplyFailed {
		t.Fatalf("Ready = %+v", cond)
	}
	got, err := getColChild(cl)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.GetOwnerReferences()) != 0 {
		t.Fatalf("unowned collector adopted: %v", got.GetOwnerReferences())
	}
}

func TestTsugaCollectorSteadyStateKeepsStatus(t *testing.T) {
	cl, r := newColClient(t, newTestColCluster(), newTestTsugaCollector())
	if err := reconcileCol(t, r); err != nil {
		t.Fatal(err)
	}
	first, _ := colReady(t, cl)
	if err := reconcileCol(t, r); err != nil {
		t.Fatal(err)
	}
	second, _ := colReady(t, cl)
	if first.ResourceVersion != second.ResourceVersion {
		t.Fatalf("steady-state reconcile rewrote the CR: %s -> %s", first.ResourceVersion, second.ResourceVersion)
	}
}

func TestClusterConfigToTsugaCollectors(t *testing.T) {
	other := newTestTsugaCollector()
	other.Name, other.UID = "edge", "edge-uid"
	_, r := newColClient(t, newTestColCluster(), newTestTsugaCollector(), other)
	if got := r.clusterConfigToTsugaCollectors(context.Background(), &v1alpha1.TsugaCollectorConfig{ObjectMeta: metav1.ObjectMeta{Name: "other"}}); len(got) != 0 {
		t.Fatalf("non-cluster config mapped to %v", got)
	}
	got := r.clusterConfigToTsugaCollectors(context.Background(), newTestColCluster())
	if len(got) != 2 {
		t.Fatalf("want both TsugaCollectors, got %v", got)
	}
}

func TestTsugaCollectorModeChangeRecreatesCollector(t *testing.T) {
	cl, r := newColClient(t, newTestColCluster(), newTestTsugaCollector())
	if err := reconcileCol(t, r); err != nil {
		t.Fatal(err)
	}
	c := &v1alpha1.TsugaCollector{}
	if err := cl.Get(context.Background(), colKey, c); err != nil {
		t.Fatal(err)
	}
	c.Spec.Collector = &apiextensionsv1.JSON{Raw: []byte(`{"mode":"daemonset","config":{"receivers":{"netflow":{}},"service":{"pipelines":{"logs/netflow":{"receivers":["netflow"]}}}}}`)}
	if err := cl.Update(context.Background(), c); err != nil {
		t.Fatal(err)
	}

	res, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: colKey})
	if err != nil || res.RequeueAfter == 0 {
		t.Fatalf("want a requeue after deleting the collector, got %+v %v", res, err)
	}
	if _, err := getColChild(cl); !apierrors.IsNotFound(err) {
		t.Fatalf("collector with the old mode should be deleted, got err=%v", err)
	}
	if err := reconcileCol(t, r); err != nil {
		t.Fatal(err)
	}
	child, err := getColChild(cl)
	if err != nil {
		t.Fatal(err)
	}
	if mode, _, _ := unstructured.NestedString(child.Object, "spec", "mode"); mode != "daemonset" {
		t.Fatalf("mode = %q", mode)
	}
}
