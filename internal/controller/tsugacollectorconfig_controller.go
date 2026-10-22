package controller

import (
	"context"
	"fmt"
	"slices"
	"time"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/meta"
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
//+kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=clusterroles;clusterrolebindings,verbs=patch;delete,resourceNames=tsuga-operator-collector-tsuga-agent;tsuga-operator-collector-tsuga-gateway;tsuga-operator-collector-tsuga-scraper;tsuga-operator-collector-tsuga-profiler
//+kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=clusterroles;clusterrolebindings,verbs=delete,resourceNames=tsuga-operator-collector
//+kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=roles;rolebindings,verbs=list;watch;create
//+kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=roles;rolebindings,verbs=patch;delete,resourceNames=tsuga-operator-collector-tsuga-scraper

// Same shape for ServiceAccounts: the Owns() informer needs list and watch,
// and create cannot be scoped by name.
//+kubebuilder:rbac:groups="",resources=serviceaccounts,verbs=list;watch;create
//+kubebuilder:rbac:groups="",resources=serviceaccounts,verbs=get;patch;delete,resourceNames=tsuga-agent;tsuga-gateway;tsuga-scraper;tsuga-profiler

// The markers below mirror the union of the collector ClusterRoles and Roles
// (see collection.RenderCollectorRBAC). Kubernetes forbids creating a role
// conferring permissions the creator does not itself hold, so the operator's
// manager ClusterRole must carry all of these for the escalation check to
// pass. Secrets are granted cluster-wide here because the collector namespace
// is configurable; the scraper itself only gets them in that namespace.
//+kubebuilder:rbac:groups="",resources=pods;namespaces;nodes;services;endpoints;events;replicationcontrollers;resourcequotas;persistentvolumes;persistentvolumeclaims,verbs=get;list;watch
//+kubebuilder:rbac:groups="",resources=nodes/stats;nodes/proxy,verbs=get
//+kubebuilder:rbac:groups=apps,resources=replicasets;deployments;daemonsets;statefulsets,verbs=get;list;watch
//+kubebuilder:rbac:groups=extensions,resources=replicasets,verbs=get;list;watch
//+kubebuilder:rbac:groups=batch,resources=jobs;cronjobs,verbs=get;list;watch
//+kubebuilder:rbac:groups=autoscaling,resources=horizontalpodautoscalers,verbs=get;list;watch
//+kubebuilder:rbac:groups=discovery.k8s.io,resources=endpointslices,verbs=get;list;watch
//+kubebuilder:rbac:groups=monitoring.coreos.com,resources=servicemonitors;podmonitors;probes;scrapeconfigs,verbs=get;list;watch
//+kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch

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

	desiredObjs := collection.RenderCollectorServiceAccounts(cfg)
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
	var result ctrl.Result
	ready, reason, message := true, reasonReconciled, fmt.Sprintf("%d resources applied", len(managed))
	if cfg.Prometheus.Enabled && cfg.Prometheus.TargetAllocator.Enabled && cfg.Prometheus.PrometheusCR.Enabled {
		installed, err := prometheusOperatorCRDsInstalled(r.RESTMapper())
		if err != nil {
			return r.fail(ctx, cc, reasonApplyFailed, err)
		}
		if !installed {
			ready, reason, message = false, reasonPrometheusCRDsMissing, prometheusCRDsMissingMessage
			result.RequeueAfter = prometheusCRDsRecheckInterval
		}
	}
	if !ready {
		desired.Phase, desired.Message = phaseError, message
	}
	setReady(&desired, cc.Generation, ready, reason, message)
	if collectionStatusEqual(cc.Status, desired) {
		return result, nil
	}
	now := metav1.Now()
	desired.LastSyncedAt = &now
	cc.Status = desired
	return result, r.Status().Update(ctx, cc)
}

// prometheusCRDsRecheckInterval exists because nothing watches CRD installs, so
// without it the missing-CRDs condition would stay stale after they appear.
const prometheusCRDsRecheckInterval = 5 * time.Minute

const prometheusCRDsMissingMessage = "prometheus.prometheusCR is enabled but the monitoring.coreos.com ServiceMonitor/PodMonitor CRDs are not installed; install the Prometheus Operator CRDs, then restart the tsuga-scraper-ta-targetallocator Deployment, or disable prometheusCR"

// prometheusOperatorCRDsInstalled reports whether the Target Allocator's
// ServiceMonitor or PodMonitor kind is served. The Target Allocator starts
// without those informers and does not recheck until restarted, so the
// condition surfaces it on our CR. One kind alone is enough to discover
// monitors, so only both missing counts.
func prometheusOperatorCRDsInstalled(mapper meta.RESTMapper) (bool, error) {
	for _, kind := range []string{"ServiceMonitor", "PodMonitor"} {
		_, err := mapper.RESTMapping(schema.GroupKind{Group: "monitoring.coreos.com", Kind: kind}, "v1")
		if err == nil {
			return true, nil
		}
		if !meta.IsNoMatchError(err) {
			return false, err
		}
	}
	return false, nil
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
	for _, gvk := range []schema.GroupVersionKind{collectorGVK, targetAllocatorGVK, clusterRoleGVK, clusterRoleBindingGVK, roleGVK, roleBindingGVK, serviceAccountGVK} {
		list := r.newChildList(gvk)
		if err := r.List(ctx, list); err != nil {
			return fmt.Errorf("listing %s: %w", gvk.Kind, err)
		}
		items, err := meta.ExtractList(list)
		if err != nil {
			return err
		}
		for _, item := range items {
			obj, ok := item.(client.Object)
			if !ok || !metav1.IsControlledBy(obj, cc) || keep[childKey{gvk, client.ObjectKeyFromObject(obj)}] {
				continue
			}
			if err := r.Delete(ctx, obj); client.IgnoreNotFound(err) != nil {
				return fmt.Errorf("pruning %s %s/%s: %w", gvk.Kind, obj.GetNamespace(), obj.GetName(), err)
			}
		}
	}
	return nil
}

// newChildList returns a typed list for kinds in the scheme, which the client
// serves from the informer cache, and an unstructured list for the OTel CRDs,
// which the scheme has no Go type for.
func (r *TsugaCollectorConfigReconciler) newChildList(gvk schema.GroupVersionKind) client.ObjectList {
	listGVK := gvk.GroupVersion().WithKind(gvk.Kind + "List")
	obj, err := r.Scheme.New(listGVK)
	if _, isUnstructured := obj.(runtime.Unstructured); err == nil && !isUnstructured {
		if list, ok := obj.(client.ObjectList); ok {
			return list
		}
	}
	list := &unstructured.UnstructuredList{}
	list.SetGroupVersionKind(listGVK)
	return list
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
	roleGVK               = rbacv1.SchemeGroupVersion.WithKind("Role")
	roleBindingGVK        = rbacv1.SchemeGroupVersion.WithKind("RoleBinding")
	serviceAccountGVK     = corev1.SchemeGroupVersion.WithKind("ServiceAccount")
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
		Owns(&rbacv1.Role{}).
		Owns(&rbacv1.RoleBinding{}).
		Owns(&corev1.ServiceAccount{}).
		Named("tsugacollectorconfig").
		Complete(r)
}
