package controller

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/tsuga-dev/tsuga-operator/api/v1alpha1"
	"github.com/tsuga-dev/tsuga-operator/internal/collection"
)

// agentCollectorServiceName is the Kubernetes Service name the OpenTelemetry
// Operator creates for the tsuga-agent OpenTelemetryCollector (the operator
// names it "<collector-name>-collector").
const agentCollectorServiceName = "tsuga-agent-collector"

// injectAnnotationPrefix is the OTel Operator annotation namespace used to
// opt a workload's pod template into auto-instrumentation.
const injectAnnotationPrefix = "instrumentation.opentelemetry.io/inject-"

// ownedLanguagesAnnotation records, on the workload itself, the comma-separated
// languages whose inject annotation this operator put on the workload's pod
// template. Removal is driven off this record rather than off the inject
// annotations themselves, so an annotation an app team set by hand is never
// stripped.
const ownedLanguagesAnnotation = "observability.tsuga.com/injected-languages"

// monitoringFinalizer holds a deleted TsugaMonitoring until its inject
// annotations are stripped; owner-reference GC removes the Instrumentation
// but knows nothing about annotations on workloads.
const monitoringFinalizer = "observability.tsuga.com/monitoring-finalizer"

// TsugaMonitoringReconciler reconciles TsugaMonitoring custom resources into
// a per-namespace OpenTelemetry Operator Instrumentation CR. Like the
// TsugaCollectorConfig controller it does not call the Tsuga API; it only
// manages Kubernetes objects.
type TsugaMonitoringReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

//+kubebuilder:rbac:groups=observability.tsuga.com,resources=tsugamonitorings,verbs=get;list;watch;patch
//+kubebuilder:rbac:groups=observability.tsuga.com,resources=tsugamonitorings/status,verbs=update
//+kubebuilder:rbac:groups=observability.tsuga.com,resources=tsugamonitorings/finalizers,verbs=update
//+kubebuilder:rbac:groups=opentelemetry.io,resources=instrumentations,verbs=get;list;watch;create;patch;delete
//+kubebuilder:rbac:groups=apps,resources=deployments;statefulsets;daemonsets,verbs=get;list;watch;patch

// agentOTLPEndpoint returns the in-cluster OTLP/HTTP endpoint of the
// tsuga-agent collector's Service in collectorNamespace. Auto-instrumented
// applications must export to this in-cluster agent - which holds the
// export auth token and enriches/batches telemetry - rather than directly
// to the external Tsuga endpoint.
func agentOTLPEndpoint(collectorNamespace string) string {
	return fmt.Sprintf("http://%s.%s.svc.cluster.local:4318", agentCollectorServiceName, collectorNamespace)
}

func (r *TsugaMonitoringReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	m := &v1alpha1.TsugaMonitoring{}
	if err := r.Get(ctx, req.NamespacedName, m); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	deleting := !m.DeletionTimestamp.IsZero()
	if deleting && !controllerutil.ContainsFinalizer(m, monitoringFinalizer) {
		return ctrl.Result{}, nil
	}
	// Finalizer writes are patches: a full Update re-serializes spec through
	// omitempty, dropping languages: [] and flipping a user-set
	// injectExistingWorkloads: false back to its default of true.
	if !deleting && !controllerutil.ContainsFinalizer(m, monitoringFinalizer) {
		patch := client.MergeFromWithOptions(m.DeepCopy(), client.MergeFromWithOptimisticLock{})
		controllerutil.AddFinalizer(m, monitoringFinalizer)
		if err := r.Patch(ctx, m, patch); err != nil {
			return ctrl.Result{}, err
		}
	}
	if deleting || !m.Spec.Instrumentation.Enabled {
		// Still walk the workloads: with instrumentation off (or the CR
		// going away) the wanted annotation set is empty, so this pass
		// strips the inject annotations this CR added and leaves the
		// workloads uninstrumented on their next roll.
		instrumented, _, err := r.reconcileWorkloads(ctx, m)
		if err != nil {
			return r.fail(ctx, m, reasonWorkloadPatchFailed, err)
		}
		if err := r.deleteOwnedInstrumentation(ctx, m); err != nil {
			return r.fail(ctx, m, reasonApplyFailed, err)
		}
		if deleting {
			patch := client.MergeFromWithOptions(m.DeepCopy(), client.MergeFromWithOptimisticLock{})
			controllerutil.RemoveFinalizer(m, monitoringFinalizer)
			return ctrl.Result{}, r.Patch(ctx, m, patch)
		}
		desired := v1alpha1.TsugaMonitoringStatus{
			CollectionStatus: v1alpha1.CollectionStatus{
				Phase:              phaseReady,
				Message:            "instrumentation disabled",
				ObservedGeneration: m.Generation,
				Conditions:         slices.Clone(m.Status.Conditions),
			},
			InstrumentedWorkloads: instrumented,
		}
		setReady(&desired.CollectionStatus, m.Generation, true, reasonInstrumentationOff, "instrumentation is disabled on spec")
		return r.syncStatus(ctx, m, desired)
	}

	cluster := &v1alpha1.TsugaCollectorConfig{}
	if err := r.Get(ctx, types.NamespacedName{Name: clusterConfigName}, cluster); err != nil {
		return r.fail(ctx, m, reasonClusterConfigMissing, err)
	}
	collectorCfg := collection.ResolveCollector(cluster.Spec)
	endpoint := agentOTLPEndpoint(collectorCfg.CollectorNamespace)

	inst := collection.RenderInstrumentation(m.Namespace, m.Name, m.Spec.Instrumentation, endpoint)
	if err := applyControlled(ctx, r.Client, r.Client, r.Scheme, m, inst); err != nil {
		return r.fail(ctx, m, reasonApplyFailed, err)
	}

	instrumented, conflicts, err := r.reconcileWorkloads(ctx, m)
	if err != nil {
		return r.fail(ctx, m, reasonWorkloadPatchFailed, err)
	}

	desired := v1alpha1.TsugaMonitoringStatus{
		CollectionStatus: v1alpha1.CollectionStatus{
			Phase:              phaseReady,
			ObservedGeneration: m.Generation,
			ManagedResources:   []string{inst.GetName()},
			Conditions:         slices.Clone(m.Status.Conditions),
		},
		InstrumentedWorkloads: instrumented,
	}
	if len(conflicts) > 0 {
		// Not returned as an error: nothing a retry can fix, and the
		// workload watch re-enqueues this CR once the annotation is freed.
		msg := "inject annotations already set elsewhere: " + strings.Join(conflicts, ", ")
		desired.Phase = phaseError
		desired.Message = msg
		setReady(&desired.CollectionStatus, m.Generation, false, reasonInjectConflict, msg)
		return r.syncStatus(ctx, m, desired)
	}
	setReady(&desired.CollectionStatus, m.Generation, true, reasonReconciled,
		fmt.Sprintf("%d workloads instrumented", instrumented))

	return r.syncStatus(ctx, m, desired)
}

// syncStatus writes desired onto the TsugaMonitoring status only when it
// differs from the current status on a meaningful field (LastSyncedAt is
// ignored). When it does differ, LastSyncedAt is refreshed. A steady-state
// reconcile is therefore a true no-op, which prevents the For() watch from
// retriggering in a hot loop.
func (r *TsugaMonitoringReconciler) syncStatus(ctx context.Context, m *v1alpha1.TsugaMonitoring, desired v1alpha1.TsugaMonitoringStatus) (ctrl.Result, error) {
	if m.Status.InstrumentedWorkloads == desired.InstrumentedWorkloads &&
		collectionStatusEqual(m.Status.CollectionStatus, desired.CollectionStatus) {
		return ctrl.Result{}, nil
	}
	now := metav1.Now()
	desired.LastSyncedAt = &now
	m.Status = desired
	return ctrl.Result{}, r.Status().Update(ctx, m)
}

// reconcileWorkloads walks the Deployments, StatefulSets, and DaemonSets in
// the TsugaMonitoring's namespace and returns how many of them are instrumented
// by this CR. When InjectExistingWorkloads is set it also annotates them on the
// way past.
//
// The count is read from the annotations present on each pod template rather
// than from what this pass wrote, which makes it correct in both modes: in
// opt-in mode the operator annotates nothing, yet workloads an app team
// annotated by hand are still counted, and in inject-everything mode a
// steady-state pass that patches nothing still reports the full total.
//
// Jobs and CronJobs are intentionally not handled here - a Job's pod
// template is immutable after creation, so annotating an existing Job
// cannot trigger injection into its (already-running or already-completed)
// pods.
//
// Annotations are removed as well as added: when instrumentation is disabled,
// InjectExistingWorkloads is turned off, or a language is dropped from the
// spec, the now-unwanted inject annotations this CR added are stripped, so the
// workload comes back uninstrumented on its next roll.
//
// Several TsugaMonitorings may share a namespace. An inject annotation that
// already points at another existing TsugaMonitoring is left alone, and the
// skipped languages are returned as sorted "lang (claimed by name)" entries;
// overwriting it instead would have the two CRs take turns rolling the
// workload forever.
func (r *TsugaMonitoringReconciler) reconcileWorkloads(ctx context.Context, m *v1alpha1.TsugaMonitoring) (int32, []string, error) {
	var want map[string]string
	if m.DeletionTimestamp.IsZero() && m.Spec.Instrumentation.Enabled && m.Spec.InjectExistingWorkloads {
		want = make(map[string]string, len(m.Spec.Instrumentation.Languages))
		for _, lang := range m.Spec.Instrumentation.Languages {
			want[injectAnnotationPrefix+lang] = m.Name
		}
	}

	monitorings := &v1alpha1.TsugaMonitoringList{}
	if err := r.List(ctx, monitorings, client.InNamespace(m.Namespace)); err != nil {
		return 0, nil, err
	}
	others := map[string]struct{}{}
	for i := range monitorings.Items {
		if name := monitorings.Items[i].Name; name != m.Name {
			others[name] = struct{}{}
		}
	}
	conflicts := map[string]struct{}{}

	var instrumented int32

	deployments := &appsv1.DeploymentList{}
	if err := r.List(ctx, deployments, client.InNamespace(m.Namespace)); err != nil {
		return instrumented, nil, err
	}
	for i := range deployments.Items {
		d := &deployments.Items[i]
		n, err := r.syncWorkload(ctx, m, d, &d.Spec.Template.ObjectMeta, want, others, conflicts)
		instrumented += n
		if err != nil {
			return instrumented, nil, err
		}
	}

	statefulSets := &appsv1.StatefulSetList{}
	if err := r.List(ctx, statefulSets, client.InNamespace(m.Namespace)); err != nil {
		return instrumented, nil, err
	}
	for i := range statefulSets.Items {
		s := &statefulSets.Items[i]
		n, err := r.syncWorkload(ctx, m, s, &s.Spec.Template.ObjectMeta, want, others, conflicts)
		instrumented += n
		if err != nil {
			return instrumented, nil, err
		}
	}

	daemonSets := &appsv1.DaemonSetList{}
	if err := r.List(ctx, daemonSets, client.InNamespace(m.Namespace)); err != nil {
		return instrumented, nil, err
	}
	for i := range daemonSets.Items {
		ds := &daemonSets.Items[i]
		n, err := r.syncWorkload(ctx, m, ds, &ds.Spec.Template.ObjectMeta, want, others, conflicts)
		instrumented += n
		if err != nil {
			return instrumented, nil, err
		}
	}

	return instrumented, slices.Sorted(maps.Keys(conflicts)), nil
}

// syncWorkload brings one workload's inject annotations in line with want and
// reports whether that workload is instrumented by m. The patch is idempotent:
// a workload is only patched when an annotation has to be added, changed, or
// removed, so already-correct workloads are not needlessly rolled.
func (r *TsugaMonitoringReconciler) syncWorkload(
	ctx context.Context,
	m *v1alpha1.TsugaMonitoring,
	obj client.Object,
	tmpl *metav1.ObjectMeta,
	want map[string]string,
	others, conflicts map[string]struct{},
) (int32, error) {
	orig := obj.DeepCopyObject().(client.Object)
	if syncInjectAnnotations(obj, tmpl, m, want, others, conflicts) {
		if err := r.Patch(ctx, obj, client.MergeFrom(orig)); err != nil {
			return 0, err
		}
	}
	if !instrumentedBy(tmpl, m) {
		return 0, nil
	}
	return 1, nil
}

// instrumentedBy reports whether a pod template carries an OTel inject
// annotation pointing at this TsugaMonitoring's Instrumentation CR. A bare
// "true" is deliberately not matched: it selects whichever Instrumentation the
// OTel Operator considers the namespace default, which need not be this one.
func instrumentedBy(tmpl *metav1.ObjectMeta, m *v1alpha1.TsugaMonitoring) bool {
	for k, v := range tmpl.Annotations {
		if strings.HasPrefix(k, injectAnnotationPrefix) && injectTarget(v, m.Namespace) == m.Name {
			return true
		}
	}
	return false
}

// injectTarget returns the Instrumentation name an inject annotation value
// in namespace ns refers to, accepting both "name" and "ns/name".
func injectTarget(v, ns string) string {
	if name, ok := strings.CutPrefix(v, ns+"/"); ok {
		return name
	}
	return v
}

// syncInjectAnnotations sets the wanted inject annotations on the pod template
// and strips the operator-owned ones that want does not contain, keeping the
// ownership record on obj in step. An annotation that carries the wanted value
// before the operator touches it is left alone and not claimed, so a hand-set
// annotation the operator happens to agree with survives a disable.
//
// The ownership record is per workload, not per CR: it says the operator set
// a language, not which TsugaMonitoring did. So an annotation is only stripped
// while it points at m, and one pointing at another TsugaMonitoring in others
// is never overwritten; that language goes into conflicts instead. Likewise a
// differing value the operator did not set (a hand-set name, "true", "false")
// is never overwritten, so it is never claimed and later stripped.
// Returns true when the workload needs a patch.
func syncInjectAnnotations(
	obj client.Object,
	tmpl *metav1.ObjectMeta,
	m *v1alpha1.TsugaMonitoring,
	want map[string]string,
	others, conflicts map[string]struct{},
) bool {
	owned := parseOwnedLanguages(obj.GetAnnotations()[ownedLanguagesAnnotation])
	changed := false

	for k, v := range want {
		if injectTarget(tmpl.Annotations[k], m.Namespace) == v {
			continue
		}
		lang := strings.TrimPrefix(k, injectAnnotationPrefix)
		target := injectTarget(tmpl.Annotations[k], m.Namespace)
		if _, claimed := others[target]; claimed {
			conflicts[fmt.Sprintf("%s (claimed by %s)", lang, target)] = struct{}{}
			continue
		}
		if _, isOwned := owned[lang]; !isOwned && tmpl.Annotations[k] != "" {
			conflicts[fmt.Sprintf("%s (set by hand on %s)", lang, obj.GetName())] = struct{}{}
			continue
		}
		if tmpl.Annotations == nil {
			tmpl.Annotations = map[string]string{}
		}
		tmpl.Annotations[k] = v
		owned[lang] = struct{}{}
		changed = true
	}

	for lang := range owned {
		if _, ok := want[injectAnnotationPrefix+lang]; ok {
			continue
		}
		if injectTarget(tmpl.Annotations[injectAnnotationPrefix+lang], m.Namespace) != m.Name {
			continue
		}
		delete(tmpl.Annotations, injectAnnotationPrefix+lang)
		delete(owned, lang)
		changed = true
	}

	if !changed {
		return false
	}
	setOwnedLanguages(obj, owned)
	return true
}

func parseOwnedLanguages(v string) map[string]struct{} {
	owned := map[string]struct{}{}
	for _, lang := range strings.Split(v, ",") {
		if lang = strings.TrimSpace(lang); lang != "" {
			owned[lang] = struct{}{}
		}
	}
	return owned
}

func setOwnedLanguages(obj client.Object, owned map[string]struct{}) {
	ann := obj.GetAnnotations()
	if len(owned) == 0 {
		delete(ann, ownedLanguagesAnnotation)
		obj.SetAnnotations(ann)
		return
	}
	if ann == nil {
		ann = map[string]string{}
	}
	ann[ownedLanguagesAnnotation] = strings.Join(slices.Sorted(maps.Keys(owned)), ",")
	obj.SetAnnotations(ann)
}

// deleteOwnedInstrumentation removes the Instrumentation CR this
// TsugaMonitoring controls, so a hand-annotated workload cannot keep using it
// after instrumentation is disabled. A same-named Instrumentation that m does
// not control is left alone.
func (r *TsugaMonitoringReconciler) deleteOwnedInstrumentation(ctx context.Context, m *v1alpha1.TsugaMonitoring) error {
	inst := &unstructured.Unstructured{}
	inst.SetGroupVersionKind(instrumentationGVK)
	if err := r.Get(ctx, types.NamespacedName{Name: m.Name, Namespace: m.Namespace}, inst); err != nil {
		return client.IgnoreNotFound(err)
	}
	if !metav1.IsControlledBy(inst, m) {
		return nil
	}
	return client.IgnoreNotFound(r.Delete(ctx, inst))
}

func (r *TsugaMonitoringReconciler) fail(ctx context.Context, m *v1alpha1.TsugaMonitoring, reason string, cause error) (ctrl.Result, error) {
	m.Status.Phase = phaseError
	m.Status.Message = cause.Error()
	setReady(&m.Status.CollectionStatus, m.Generation, false, reason, cause.Error())
	if err := r.Status().Update(ctx, m); err != nil {
		logf.FromContext(ctx).Error(err, "failed to record Error status", "reason", reason)
	}
	return ctrl.Result{}, cause
}

// instrumentationGVK is the GroupVersionKind of the OTel Operator's
// Instrumentation CRD that RenderInstrumentation produces. SetupWithManager
// uses it to watch owned Instrumentation objects so drift (edits or
// deletes) re-triggers reconciliation.
var instrumentationGVK = schema.GroupVersionKind{
	Group:   otelAPIGroup,
	Version: "v1alpha1",
	Kind:    "Instrumentation",
}

func (r *TsugaMonitoringReconciler) SetupWithManager(mgr ctrl.Manager) error {
	instrumentation := &unstructured.Unstructured{}
	instrumentation.SetGroupVersionKind(instrumentationGVK)
	// Workload watches annotate Deployments/StatefulSets/DaemonSets created
	// after the TsugaMonitoring was reconciled. GenerationChanged skips
	// status-only churn; creates always pass.
	enqueue := handler.EnqueueRequestsFromMapFunc(r.workloadToMonitorings)
	onSpecChange := builder.WithPredicates(predicate.GenerationChangedPredicate{})
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.TsugaMonitoring{}).
		Owns(instrumentation).
		Watches(&v1alpha1.TsugaCollectorConfig{}, handler.EnqueueRequestsFromMapFunc(r.clusterConfigToMonitorings)).
		Watches(&appsv1.Deployment{}, enqueue, onSpecChange).
		Watches(&appsv1.StatefulSet{}, enqueue, onSpecChange).
		Watches(&appsv1.DaemonSet{}, enqueue, onSpecChange).
		Named("tsugamonitoring").
		Complete(r)
}

func (r *TsugaMonitoringReconciler) clusterConfigToMonitorings(ctx context.Context, obj client.Object) []reconcile.Request {
	if obj.GetName() != clusterConfigName {
		return nil
	}
	var list v1alpha1.TsugaMonitoringList
	if err := r.List(ctx, &list); err != nil {
		logf.FromContext(ctx).Error(err, "clusterConfigToMonitorings: failed to list TsugaMonitorings")
		return nil
	}
	reqs := make([]reconcile.Request, 0, len(list.Items))
	for i := range list.Items {
		reqs = append(reqs, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&list.Items[i])})
	}
	return reqs
}

func (r *TsugaMonitoringReconciler) workloadToMonitorings(ctx context.Context, obj client.Object) []reconcile.Request {
	var list v1alpha1.TsugaMonitoringList
	if err := r.List(ctx, &list, client.InNamespace(obj.GetNamespace())); err != nil {
		logf.FromContext(ctx).Error(err, "workloadToMonitorings: failed to list TsugaMonitorings",
			"namespace", obj.GetNamespace(), "workload", obj.GetName())
		return nil
	}
	reqs := make([]reconcile.Request, 0, len(list.Items))
	for i := range list.Items {
		reqs = append(reqs, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&list.Items[i])})
	}
	return reqs
}
