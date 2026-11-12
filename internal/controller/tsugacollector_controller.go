package controller

import (
	"context"
	"slices"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/tsuga-dev/tsuga-operator/api/v1alpha1"
	"github.com/tsuga-dev/tsuga-operator/internal/collection"
)

// TsugaCollectorReconciler renders a TsugaCollector into an owned
// OpenTelemetryCollector named tsuga-<name>.
type TsugaCollectorReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

//+kubebuilder:rbac:groups=observability.tsuga.com,resources=tsugacollectors,verbs=get;list;watch
//+kubebuilder:rbac:groups=observability.tsuga.com,resources=tsugacollectors/status,verbs=update
//+kubebuilder:rbac:groups=observability.tsuga.com,resources=tsugacollectors/finalizers,verbs=update

func (r *TsugaCollectorReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	c := &v1alpha1.TsugaCollector{}
	if err := r.Get(ctx, req.NamespacedName, c); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	cluster := &v1alpha1.TsugaCollectorConfig{}
	if err := r.Get(ctx, types.NamespacedName{Name: clusterConfigName}, cluster); err != nil {
		return r.fail(ctx, c, reasonClusterConfigMissing, err)
	}
	clusterCfg := collection.ResolveCollector(cluster.Spec)
	needsAgent := ptr.Deref(c.Spec.TsugaExporter, true) || ptr.Deref(c.Spec.SelfTelemetry, true)
	if needsAgent && !collection.AgentEnabled(clusterCfg) {
		return r.retire(ctx, c)
	}

	ns := clusterCfg.CollectorNamespace
	collector, err := collection.RenderCustomCollector(c.Name, c.Namespace, c.Spec, clusterCfg,
		agentOTLPGRPCEndpoint(ns, collection.AgentOTLPPort(clusterCfg, "grpc")),
		agentOTLPEndpoint(ns, collection.AgentOTLPPort(clusterCfg, "http")))
	if err != nil {
		return r.fail(ctx, c, reasonRenderFailed, err)
	}
	if deleted, err := deleteOnModeChange(ctx, r.Client, c, collector); err != nil {
		return r.fail(ctx, c, reasonApplyFailed, err)
	} else if deleted {
		return ctrl.Result{RequeueAfter: setupJobRecreateDelay}, nil
	}
	if err := applyControlled(ctx, r.Client, r.Client, r.Scheme, c, collector); err != nil {
		return r.fail(ctx, c, reasonApplyFailed, err)
	}
	return r.syncStatus(ctx, c, []string{collector.GetName()}, true, reasonReconciled, "")
}

// retire deletes the owned collector, which has no agent to export to.
func (r *TsugaCollectorReconciler) retire(ctx context.Context, c *v1alpha1.TsugaCollector) (ctrl.Result, error) {
	collector := &unstructured.Unstructured{}
	collector.SetGroupVersionKind(collectorGVK)
	key := types.NamespacedName{Name: collection.CustomCollectorName(c.Name), Namespace: c.Namespace}
	if err := r.Get(ctx, key, collector); err != nil {
		if !apierrors.IsNotFound(err) {
			return r.fail(ctx, c, reasonApplyFailed, err)
		}
	} else if metav1.IsControlledBy(collector, c) {
		if err := r.Delete(ctx, collector); client.IgnoreNotFound(err) != nil {
			return r.fail(ctx, c, reasonApplyFailed, err)
		}
	}
	return r.syncStatus(ctx, c, nil, false, reasonAgentDisabled,
		"the cluster config disables the node agent, which this collector exports to")
}

// syncStatus writes the status only when it differs on a meaningful field,
// so a steady-state reconcile does not retrigger the For() watch.
func (r *TsugaCollectorReconciler) syncStatus(ctx context.Context, c *v1alpha1.TsugaCollector, managed []string, ready bool, reason, message string) (ctrl.Result, error) {
	phase := phaseReady
	if !ready {
		phase = phaseError
	}
	desired := v1alpha1.CollectionStatus{
		Phase:              phase,
		Message:            message,
		ObservedGeneration: c.Generation,
		ManagedResources:   managed,
		Conditions:         slices.Clone(c.Status.Conditions),
	}
	setReady(&desired, c.Generation, ready, reason, message)
	if collectionStatusEqual(c.Status, desired) {
		return ctrl.Result{}, nil
	}
	now := metav1.Now()
	desired.LastSyncedAt = &now
	c.Status = desired
	return ctrl.Result{}, r.Status().Update(ctx, c)
}

func (r *TsugaCollectorReconciler) fail(ctx context.Context, c *v1alpha1.TsugaCollector, reason string, cause error) (ctrl.Result, error) {
	c.Status.Phase = phaseError
	c.Status.Message = cause.Error()
	setReady(&c.Status, c.Generation, false, reason, cause.Error())
	if err := r.Status().Update(ctx, c); err != nil {
		logf.FromContext(ctx).Error(err, "failed to record Error status", "reason", reason)
	}
	return ctrl.Result{}, cause
}

// SetupWithManager re-renders every TsugaCollector when the cluster config
// changes: it decides the agent endpoint, image and accepted signals.
func (r *TsugaCollectorReconciler) SetupWithManager(mgr ctrl.Manager) error {
	collector := &unstructured.Unstructured{}
	collector.SetGroupVersionKind(collectorGVK)
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.TsugaCollector{}).
		Owns(collector).
		Watches(&v1alpha1.TsugaCollectorConfig{}, handler.EnqueueRequestsFromMapFunc(r.clusterConfigToTsugaCollectors)).
		Named("tsugacollector").
		Complete(r)
}

func (r *TsugaCollectorReconciler) clusterConfigToTsugaCollectors(ctx context.Context, obj client.Object) []reconcile.Request {
	if obj.GetName() != clusterConfigName {
		return nil
	}
	var list v1alpha1.TsugaCollectorList
	if err := r.List(ctx, &list); err != nil {
		logf.FromContext(ctx).Error(err, "clusterConfigToTsugaCollectors: failed to list TsugaCollectors")
		return nil
	}
	reqs := make([]reconcile.Request, 0, len(list.Items))
	for i := range list.Items {
		reqs = append(reqs, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&list.Items[i])})
	}
	return reqs
}
