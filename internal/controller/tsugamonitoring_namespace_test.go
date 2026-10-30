package controller

import (
	"context"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/tsuga-dev/tsuga-operator/api/v1alpha1"
)

// newNamespaceMonitoring builds an enabled TsugaMonitoring in Namespace mode.
func newNamespaceMonitoring(namespace string, languages ...string) *v1alpha1.TsugaMonitoring {
	return &v1alpha1.TsugaMonitoring{
		ObjectMeta: metav1.ObjectMeta{Name: "default", Namespace: namespace},
		Spec: v1alpha1.TsugaMonitoringSpec{
			Instrumentation: v1alpha1.InstrumentationSpec{Enabled: true, Languages: languages},
			Injection:       v1alpha1.InjectionNamespace,
		},
	}
}

func newNamespace(name string, annotations map[string]string) *corev1.Namespace {
	return &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name, Annotations: annotations}}
}

func namespaceModeReconciler(t *testing.T, m *v1alpha1.TsugaMonitoring, objs ...client.Object) (client.Client, *TsugaMonitoringReconciler, ctrl.Request) {
	t.Helper()
	scheme := monitoringTestScheme(t)
	cl := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(append([]client.Object{newClusterConfig(), m}, objs...)...).WithStatusSubresource(m).
		WithInterceptorFuncs(applyAsCreateOrUpdate()).
		Build()
	return cl, &TsugaMonitoringReconciler{Client: cl, Scheme: scheme}, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(m)}
}

func reconcileOK(t *testing.T, r *TsugaMonitoringReconciler, req ctrl.Request) {
	t.Helper()
	if _, err := r.Reconcile(context.Background(), req); err != nil {
		t.Fatal(err)
	}
}

func getNamespace(t *testing.T, cl client.Client, name string) *corev1.Namespace {
	t.Helper()
	ns := &corev1.Namespace{}
	if err := cl.Get(context.Background(), types.NamespacedName{Name: name}, ns); err != nil {
		t.Fatal(err)
	}
	return ns
}

func getMonitoring(t *testing.T, cl client.Client, req ctrl.Request) *v1alpha1.TsugaMonitoring {
	t.Helper()
	m := &v1alpha1.TsugaMonitoring{}
	if err := cl.Get(context.Background(), req.NamespacedName, m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestNamespaceModeAnnotatesNamespaceNotWorkloads(t *testing.T) {
	dep := newTestDeployment("checkout", "payments")
	cl, r, req := namespaceModeReconciler(t, newNamespaceMonitoring("payments", "java", "python", "go"), newNamespace("payments", nil), dep)
	reconcileOK(t, r, req)

	ns := getNamespace(t, cl, "payments")
	for _, lang := range []string{"java", "python"} {
		if v := ns.Annotations[injectAnnotationPrefix+lang]; v != "default" {
			t.Errorf("namespace inject-%s = %q, want default", lang, v)
		}
	}
	if _, ok := ns.Annotations[injectAnnotationPrefix+"go"]; ok {
		t.Error("go needs a per-workload target exe and must not be injected in bulk")
	}
	if got := ns.Annotations[ownedLanguagesAnnotation]; got != "java,python" {
		t.Errorf("ownership record = %q, want java,python", got)
	}
	gotDep := getDeployment(t, cl, "checkout")
	if gotDep.ResourceVersion != dep.ResourceVersion || len(gotDep.Spec.Template.Annotations) != 0 {
		t.Errorf("Namespace mode must not touch pod templates, got %v", gotDep.Spec.Template.Annotations)
	}
	m := getMonitoring(t, cl, req)
	if m.Status.Phase != phaseReady || m.Status.InstrumentedWorkloads != 1 {
		t.Errorf("status = %s with %d workloads, want Ready with 1", m.Status.Phase, m.Status.InstrumentedWorkloads)
	}

	rv := ns.ResourceVersion
	reconcileOK(t, r, req)
	if getNamespace(t, cl, "payments").ResourceVersion != rv {
		t.Error("a steady-state reconcile must not patch the namespace")
	}
}

func TestNamespaceModeRefusedInCollectorNamespace(t *testing.T) {
	cl, r, req := namespaceModeReconciler(t, newNamespaceMonitoring("tsuga-operator-system", "java"), newNamespace("tsuga-operator-system", nil))
	reconcileOK(t, r, req)

	if _, ok := getNamespace(t, cl, "tsuga-operator-system").Annotations[injectAnnotationPrefix+"java"]; ok {
		t.Fatal("annotating the collector namespace would instrument the Tsuga collectors")
	}
	cond := meta.FindStatusCondition(getMonitoring(t, cl, req).Status.Conditions, "Ready")
	if cond == nil || cond.Status != metav1.ConditionFalse || cond.Reason != reasonCollectorNamespace {
		t.Fatalf("Ready condition = %+v, want False/%s", cond, reasonCollectorNamespace)
	}
}

func TestNamespaceModeConflictWithHandSetNamespaceAnnotation(t *testing.T) {
	ns := newNamespace("payments", map[string]string{injectAnnotationPrefix + "java": "other"})
	cl, r, req := namespaceModeReconciler(t, newNamespaceMonitoring("payments", "java"), ns, newTestDeployment("checkout", "payments"))
	reconcileOK(t, r, req)

	if v := getNamespace(t, cl, "payments").Annotations[injectAnnotationPrefix+"java"]; v != "other" {
		t.Fatalf("hand-set namespace annotation overwritten: %q", v)
	}
	cond := meta.FindStatusCondition(getMonitoring(t, cl, req).Status.Conditions, "Ready")
	if cond == nil || cond.Reason != reasonInjectConflict || !strings.Contains(cond.Message, "java") {
		t.Fatalf("Ready condition = %+v, want %s naming java", cond, reasonInjectConflict)
	}
	if n := getMonitoring(t, cl, req).Status.InstrumentedWorkloads; n != 0 {
		t.Fatalf("the namespace annotation points elsewhere, so nothing is instrumented by this CR; got %d", n)
	}
}

func TestNamespaceModeStripsOnDisable(t *testing.T) {
	ns := newNamespace("payments", map[string]string{injectAnnotationPrefix + "nodejs": "default"})
	cl, r, req := namespaceModeReconciler(t, newNamespaceMonitoring("payments", "java"), ns)
	reconcileOK(t, r, req)
	if getNamespace(t, cl, "payments").Annotations[injectAnnotationPrefix+"java"] != "default" {
		t.Fatal("setup: namespace should be annotated")
	}

	live := getMonitoring(t, cl, req)
	live.Spec.Instrumentation.Enabled = false
	if err := cl.Update(context.Background(), live); err != nil {
		t.Fatal(err)
	}
	reconcileOK(t, r, req)

	got := getNamespace(t, cl, "payments").Annotations
	if _, ok := got[injectAnnotationPrefix+"java"]; ok {
		t.Errorf("owned namespace annotation not stripped: %v", got)
	}
	if _, ok := got[ownedLanguagesAnnotation]; ok {
		t.Errorf("ownership record not cleared: %v", got)
	}
	if got[injectAnnotationPrefix+"nodejs"] != "default" {
		t.Errorf("hand-set namespace annotation must survive: %v", got)
	}
}

func TestNamespaceModeStripsOnDelete(t *testing.T) {
	cl, r, req := namespaceModeReconciler(t, newNamespaceMonitoring("payments", "java"), newNamespace("payments", nil))
	reconcileOK(t, r, req)
	if getNamespace(t, cl, "payments").Annotations[injectAnnotationPrefix+"java"] != "default" {
		t.Fatal("setup: namespace should be annotated")
	}
	if err := cl.Delete(context.Background(), getMonitoring(t, cl, req)); err != nil {
		t.Fatal(err)
	}
	reconcileOK(t, r, req)

	if got := getNamespace(t, cl, "payments").Annotations; got[injectAnnotationPrefix+"java"] != "" || got[ownedLanguagesAnnotation] != "" {
		t.Fatalf("deleting the TsugaMonitoring must strip its namespace annotations: %v", got)
	}
}

func TestSwitchWorkloadsToNamespaceDoesNotRoll(t *testing.T) {
	m := newNamespaceMonitoring("payments", "java")
	m.Spec.Injection = v1alpha1.InjectionWorkloads
	cl, r, req := namespaceModeReconciler(t, m, newNamespace("payments", nil), newTestDeployment("checkout", "payments"))
	reconcileOK(t, r, req)
	rolled := getDeployment(t, cl, "checkout")
	if rolled.Spec.Template.Annotations[injectAnnotationPrefix+"java"] != "default" {
		t.Fatal("setup: Workloads mode should annotate the template")
	}

	live := getMonitoring(t, cl, req)
	live.Spec.Injection = v1alpha1.InjectionNamespace
	if err := cl.Update(context.Background(), live); err != nil {
		t.Fatal(err)
	}
	reconcileOK(t, r, req)

	if got := getDeployment(t, cl, "checkout"); got.ResourceVersion != rolled.ResourceVersion {
		t.Errorf("switching to Namespace mode must not patch the template, got %v", got.Spec.Template.Annotations)
	}
	if getNamespace(t, cl, "payments").Annotations[injectAnnotationPrefix+"java"] != "default" {
		t.Error("namespace not annotated after the switch")
	}
}

func TestNamespaceModeStripsTemplateAnnotationForRemovedLanguage(t *testing.T) {
	dep := newTestDeployment("checkout", "payments")
	dep.Annotations = map[string]string{ownedLanguagesAnnotation: "java,python"}
	dep.Spec.Template.Annotations = map[string]string{injectAnnotationPrefix + "java": "default", injectAnnotationPrefix + "python": "default"}
	cl, r, req := namespaceModeReconciler(t, newNamespaceMonitoring("payments", "java"), newNamespace("payments", nil), dep)
	reconcileOK(t, r, req)

	got := getDeployment(t, cl, "checkout")
	if _, ok := got.Spec.Template.Annotations[injectAnnotationPrefix+"python"]; ok {
		t.Errorf("python is no longer wanted; its owned template annotation should go: %v", got.Spec.Template.Annotations)
	}
	if got.Spec.Template.Annotations[injectAnnotationPrefix+"java"] != "default" {
		t.Errorf("java is still wanted; stripping it would roll the workload: %v", got.Spec.Template.Annotations)
	}
}

func TestNamespaceModeOptOutNotCounted(t *testing.T) {
	dep := newTestDeployment("checkout", "payments")
	dep.Spec.Template.Annotations = map[string]string{injectAnnotationPrefix + "java": "false"}
	cl, r, req := namespaceModeReconciler(t, newNamespaceMonitoring("payments", "java"), newNamespace("payments", nil), dep, newTestDeployment("ledger", "payments"))
	reconcileOK(t, r, req)

	if n := getMonitoring(t, cl, req).Status.InstrumentedWorkloads; n != 1 {
		t.Fatalf("want only ledger counted (checkout opts out), got %d", n)
	}
}

func TestNamespaceEventEnqueuesNamespaceMonitoring(t *testing.T) {
	m := newNamespaceMonitoring("payments", "java")
	_, r, req := namespaceModeReconciler(t, m, newNamespace("payments", nil))
	reqs := r.namespaceToMonitorings(context.Background(), newNamespace("payments", nil))
	if len(reqs) != 1 || reqs[0] != req {
		t.Fatalf("namespaceToMonitorings = %v, want [%v]", reqs, req)
	}
}

// twoNamespaceMonitorings reconciles first, then second, in the payments
// namespace, both in Namespace mode.
func twoNamespaceMonitorings(t *testing.T, firstLangs, secondLangs []string) (client.Client, *TsugaMonitoringReconciler, ctrl.Request, ctrl.Request) {
	t.Helper()
	first := newNamespaceMonitoring("payments", firstLangs...)
	second := newNamespaceMonitoring("payments", secondLangs...)
	second.Name = "other"
	scheme := monitoringTestScheme(t)
	cl := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(newClusterConfig(), first, second, newNamespace("payments", nil)).WithStatusSubresource(first, second).
		WithInterceptorFuncs(applyAsCreateOrUpdate()).
		Build()
	r := &TsugaMonitoringReconciler{Client: cl, Scheme: scheme}
	firstReq := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(first)}
	secondReq := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(second)}
	reconcileOK(t, r, firstReq)
	reconcileOK(t, r, secondReq)
	return cl, r, firstReq, secondReq
}

func TestNamespaceModeOverlappingLanguageDoesNotThrash(t *testing.T) {
	cl, r, firstReq, secondReq := twoNamespaceMonitorings(t, []string{"java"}, []string{"java"})
	rv := getNamespace(t, cl, "payments").ResourceVersion
	for range 2 {
		reconcileOK(t, r, firstReq)
		reconcileOK(t, r, secondReq)
	}

	ns := getNamespace(t, cl, "payments")
	if ns.Annotations[injectAnnotationPrefix+"java"] != "default" || ns.ResourceVersion != rv {
		t.Fatalf("want the first claim kept and the namespace left alone, got %v (rv %s -> %s)", ns.Annotations, rv, ns.ResourceVersion)
	}
	cond := meta.FindStatusCondition(getMonitoring(t, cl, secondReq).Status.Conditions, "Ready")
	if cond == nil || cond.Reason != reasonInjectConflict || !strings.Contains(cond.Message, "claimed by default") {
		t.Fatalf("want a conflict naming the first CR on the second, got %+v", cond)
	}
}

func TestNamespaceModeDisjointLanguagesKeepEachOthersAnnotations(t *testing.T) {
	cl, r, firstReq, _ := twoNamespaceMonitorings(t, []string{"java"}, []string{"python"})
	reconcileOK(t, r, firstReq)

	got := getNamespace(t, cl, "payments").Annotations
	if got[injectAnnotationPrefix+"java"] != "default" || got[injectAnnotationPrefix+"python"] != "other" {
		t.Fatalf("each CR should keep its own language on the namespace, got %v", got)
	}
}

func TestNamespaceModeLanguageTakenOverAfterOwnerDeleted(t *testing.T) {
	cl, r, firstReq, secondReq := twoNamespaceMonitorings(t, []string{"java"}, []string{"java"})
	if err := cl.Delete(context.Background(), getMonitoring(t, cl, firstReq)); err != nil {
		t.Fatal(err)
	}
	reconcileOK(t, r, firstReq)
	reconcileOK(t, r, secondReq)

	if v := getNamespace(t, cl, "payments").Annotations[injectAnnotationPrefix+"java"]; v != "other" {
		t.Fatalf("want the remaining CR to take java over, got %q", v)
	}
	if getMonitoring(t, cl, secondReq).Status.Phase != phaseReady {
		t.Fatal("want the remaining CR Ready once the conflict is gone")
	}
}
