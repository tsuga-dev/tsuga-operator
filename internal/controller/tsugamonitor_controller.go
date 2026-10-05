package controller

import (
	"context"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/tsuga-dev/tsuga-operator/api/v1alpha1"
)

// TsugaMonitorReconciler reconciles Monitor custom resources.
type TsugaMonitorReconciler struct {
	client.Client

	TsugaClient TsugaResourceClient
}

//+kubebuilder:rbac:groups=observability.tsuga.com,resources=monitors,verbs=get;list;watch;patch
//+kubebuilder:rbac:groups=observability.tsuga.com,resources=monitors/status,verbs=update
//+kubebuilder:rbac:groups=observability.tsuga.com,resources=dashboards,verbs=get;list;watch

func (r *TsugaMonitorReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	reconciler := genericTsugaReconciler[*v1alpha1.Monitor]{
		Client:  r.Client,
		adapter: &monitorAdapter{KubeClient: r.Client},
		tsuga:   r.TsugaClient,
	}
	return reconciler.Reconcile(ctx, req)
}

func (r *TsugaMonitorReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.Monitor{}).
		Watches(
			&v1alpha1.Dashboard{},
			handler.EnqueueRequestsFromMapFunc(r.dashboardToMonitors),
		).
		Named("monitor").
		Complete(r)
}

func (r *TsugaMonitorReconciler) dashboardToMonitors(ctx context.Context, obj client.Object) []reconcile.Request {
	var monitorList v1alpha1.MonitorList
	if err := r.List(ctx, &monitorList, client.InNamespace(obj.GetNamespace())); err != nil {
		logf.FromContext(ctx).Error(err, "dashboardToMonitors: failed to list Monitors",
			"namespace", obj.GetNamespace(), "dashboard", obj.GetName())
		return nil
	}
	var reqs []reconcile.Request
	for _, m := range monitorList.Items {
		if m.Spec.DashboardRef != nil && m.Spec.DashboardRef.Name == obj.GetName() {
			reqs = append(reqs, reconcile.Request{
				NamespacedName: client.ObjectKeyFromObject(&m),
			})
		}
	}
	return reqs
}
