package controller

import (
	"context"
	"fmt"
	"slices"

	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/apiutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/tsuga-dev/tsuga-operator/api/v1alpha1"
	"github.com/tsuga-dev/tsuga-operator/internal/collection"
)

// fieldManager is the field owner used for server-side apply of the OTel
// collector resources managed by this controller.
const fieldManager = "tsuga-operator"

// TsugaCollectorConfigReconciler reconciles TsugaCollectorConfig custom
// resources into OpenTelemetryCollector CRs. Unlike the Dashboard/Monitor
// controllers it does not call the Tsuga API; it only manages Kubernetes
// objects.
type TsugaCollectorConfigReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

//+kubebuilder:rbac:groups=observability.tsuga.com,resources=tsugacollectorconfigs,verbs=get;list;watch
//+kubebuilder:rbac:groups=observability.tsuga.com,resources=tsugacollectorconfigs/status,verbs=update
// Children carry a controller reference with blockOwnerDeletion, which the
// OwnerReferencesPermissionEnforcement admission plugin (on in OpenShift)
// only allows with update on the owner's finalizers.
//+kubebuilder:rbac:groups=observability.tsuga.com,resources=tsugacollectorconfigs/finalizers,verbs=update
//+kubebuilder:rbac:groups=opentelemetry.io,resources=opentelemetrycollectors,verbs=get;list;watch;create;patch;delete
//+kubebuilder:rbac:groups=opentelemetry.io,resources=targetallocators,verbs=get;list;watch;create;patch;delete

// Top-level create cannot be restricted by resourceNames. Apply of a missing
// RBAC object is a create; patch and delete remain scoped to rendered names.
// Delete also covers the legacy shared "tsuga-operator-collector" objects,
// which are pruned but never applied, so they get no patch.
// list and watch stay unscoped: the Owns() informers need both.
//+kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=clusterroles;clusterrolebindings,verbs=list;watch
//+kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=clusterroles;clusterrolebindings,verbs=create
//+kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=clusterroles;clusterrolebindings,verbs=patch;delete,resourceNames=tsuga-operator-collector-tsuga-agent;tsuga-operator-collector-tsuga-gateway;tsuga-operator-collector-tsuga-scraper
//+kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=clusterroles;clusterrolebindings,verbs=delete,resourceNames=tsuga-operator-collector

// The markers below mirror the union of the collector ClusterRoles (see
// collection.RenderCollectorRBAC). Kubernetes forbids creating a ClusterRole
// conferring permissions the creator does not itself hold, so the operator's
// manager ClusterRole must carry all of these for the escalation check to
// pass.
//+kubebuilder:rbac:groups="",resources=pods;namespaces;nodes;services;endpoints;replicationcontrollers;resourcequotas;persistentvolumes;persistentvolumeclaims,verbs=get;list;watch
//+kubebuilder:rbac:groups="",resources=nodes/stats;nodes/proxy,verbs=get
//+kubebuilder:rbac:groups=apps,resources=replicasets;deployments;daemonsets;statefulsets,verbs=get;list;watch
//+kubebuilder:rbac:groups=extensions,resources=replicasets,verbs=get;list;watch
//+kubebuilder:rbac:groups=batch,resources=jobs;cronjobs,verbs=get;list;watch
//+kubebuilder:rbac:groups=autoscaling,resources=horizontalpodautoscalers,verbs=get;list;watch
//+kubebuilder:rbac:groups=discovery.k8s.io,resources=endpointslices,verbs=get;list;watch

func (r *TsugaCollectorConfigReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	cc := &v1alpha1.TsugaCollectorConfig{}
	if err := r.Get(ctx, req.NamespacedName, cc); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	cfg := collection.ResolveCollector(cc.Spec)
	objs, err := collection.RenderCollectors(cfg)
	if err != nil {
		// Rendering is deterministic on the spec, so only a spec change can
		// fix it; report it without requeueing forever. The previous
		// collectors keep running until then. A failed status write is still
		// returned so the error gets recorded on retry.
		return ctrl.Result{}, r.recordFailure(ctx, cc, reasonRenderFailed, err)
	}

	desiredObjs := make([]client.Object, 0, len(objs))
	for _, o := range objs {
		desiredObjs = append(desiredObjs, o)
	}
	desiredObjs = append(desiredObjs, collection.RenderCollectorRBAC(cfg)...)

	managed := make([]string, 0, len(desiredObjs))
	for _, o := range desiredObjs {
		// The cached read is safe: every child kind is watched through Owns().
		if err := applyControlled(ctx, r.Client, r.Client, r.Scheme, cc, o); err != nil {
			return r.fail(ctx, cc, reasonApplyFailed, err)
		}
		managed = append(managed, o.GetName())
	}
	if err := r.pruneChildren(ctx, cc, desiredObjs); err != nil {
		return r.fail(ctx, cc, reasonApplyFailed, err)
	}

	desired := v1alpha1.CollectionStatus{
		Phase:              phaseReady,
		Message:            "",
		ObservedGeneration: cc.Generation,
		ManagedResources:   managed,
		Conditions:         slices.Clone(cc.Status.Conditions),
	}
	setReady(&desired, cc.Generation, true, reasonReconciled,
		fmt.Sprintf("%d resources applied", len(managed)))
	if collectionStatusEqual(cc.Status, desired) {
		return ctrl.Result{}, nil
	}
	now := metav1.Now()
	desired.LastSyncedAt = &now
	cc.Status = desired
	return ctrl.Result{}, r.Status().Update(ctx, cc)
}

// pruneChildren deletes collectors, target allocators and collector RBAC
// controlled by cc that are absent from desired, whether disabled, in another
// namespace, or left over from an earlier RBAC layout.
func (r *TsugaCollectorConfigReconciler) pruneChildren(ctx context.Context, cc *v1alpha1.TsugaCollectorConfig, desired []client.Object) error {
	type childKey struct {
		schema.GroupVersionKind
		client.ObjectKey
	}
	keep := make(map[childKey]bool, len(desired))
	for _, o := range desired {
		gvk, err := apiutil.GVKForObject(o, r.Scheme)
		if err != nil {
			return err
		}
		keep[childKey{gvk, client.ObjectKeyFromObject(o)}] = true
	}
	for _, gvk := range []schema.GroupVersionKind{collectorGVK, targetAllocatorGVK, clusterRoleGVK, clusterRoleBindingGVK} {
		list := &unstructured.UnstructuredList{}
		list.SetGroupVersionKind(gvk.GroupVersion().WithKind(gvk.Kind + "List"))
		if err := r.List(ctx, list); err != nil {
			return fmt.Errorf("listing %s: %w", gvk.Kind, err)
		}
		for i := range list.Items {
			obj := &list.Items[i]
			if !metav1.IsControlledBy(obj, cc) || keep[childKey{gvk, client.ObjectKeyFromObject(obj)}] {
				continue
			}
			if err := r.Delete(ctx, obj); client.IgnoreNotFound(err) != nil {
				return fmt.Errorf("pruning %s %s/%s: %w", gvk.Kind, obj.GetNamespace(), obj.GetName(), err)
			}
		}
	}
	return nil
}

func (r *TsugaCollectorConfigReconciler) fail(ctx context.Context, cc *v1alpha1.TsugaCollectorConfig, reason string, cause error) (ctrl.Result, error) {
	if err := r.recordFailure(ctx, cc, reason, cause); err != nil {
		logf.FromContext(ctx).Error(err, "failed to record Error status", "reason", reason)
	}
	return ctrl.Result{}, cause
}

// recordFailure writes the Error status for cause and returns the status
// write error, if any.
func (r *TsugaCollectorConfigReconciler) recordFailure(ctx context.Context, cc *v1alpha1.TsugaCollectorConfig, reason string, cause error) error {
	cc.Status.Phase = phaseError
	cc.Status.Message = cause.Error()
	setReady(&cc.Status, cc.Generation, false, reason, cause.Error())
	return r.Status().Update(ctx, cc)
}

// collectorGVK is the GroupVersionKind of the OTel Operator's
// OpenTelemetryCollector CRD that RenderCollectors produces. SetupWithManager
// uses it to watch owned collectors so drift (edits or deletes) re-triggers
// reconciliation.
// otelAPIGroup is the API group of the OpenTelemetry Operator's CRDs.
const otelAPIGroup = "opentelemetry.io"

var collectorGVK = schema.GroupVersionKind{
	Group:   otelAPIGroup,
	Version: "v1beta1",
	Kind:    "OpenTelemetryCollector",
}

// targetAllocatorGVK is the GroupVersionKind of the OTel Operator's standalone
// TargetAllocator CRD, which RenderCollectors emits alongside the scraper
// collector. Still v1alpha1: it has not graduated with OpenTelemetryCollector.
var targetAllocatorGVK = schema.GroupVersionKind{
	Group:   otelAPIGroup,
	Version: "v1alpha1",
	Kind:    "TargetAllocator",
}

var (
	clusterRoleGVK        = rbacv1.SchemeGroupVersion.WithKind("ClusterRole")
	clusterRoleBindingGVK = rbacv1.SchemeGroupVersion.WithKind("ClusterRoleBinding")
)

func (r *TsugaCollectorConfigReconciler) SetupWithManager(mgr ctrl.Manager) error {
	collector := &unstructured.Unstructured{}
	collector.SetGroupVersionKind(collectorGVK)
	allocator := &unstructured.Unstructured{}
	allocator.SetGroupVersionKind(targetAllocatorGVK)
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.TsugaCollectorConfig{}).
		Owns(collector).
		Owns(allocator).
		Owns(&rbacv1.ClusterRole{}).
		Owns(&rbacv1.ClusterRoleBinding{}).
		Named("tsugacollectorconfig").
		Complete(r)
}
