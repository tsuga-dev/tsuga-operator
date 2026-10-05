package controller

import (
	"context"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/tsuga-dev/tsuga-operator/api/v1alpha1"
)

// TsugaDashboardReconciler reconciles Dashboard custom resources.
type TsugaDashboardReconciler struct {
	client.Client

	TsugaClient TsugaResourceClient
}

//+kubebuilder:rbac:groups=observability.tsuga.com,resources=dashboards,verbs=get;list;watch;patch
//+kubebuilder:rbac:groups=observability.tsuga.com,resources=dashboards/status,verbs=update

func (r *TsugaDashboardReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	reconciler := genericTsugaReconciler[*v1alpha1.Dashboard]{
		Client:  r.Client,
		adapter: dashboardAdapter{},
		tsuga:   r.TsugaClient,
		resync:  true,
	}
	return reconciler.Reconcile(ctx, req)
}

func (r *TsugaDashboardReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.Dashboard{}).
		Named("dashboard").
		Complete(r)
}
