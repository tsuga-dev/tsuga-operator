package controller

import (
	"context"
	"time"

	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/tsuga-dev/tsuga-operator/api/v1alpha1"
)

// TsugaDashboardReconciler reconciles Dashboard custom resources.
type TsugaDashboardReconciler struct {
	client.Client

	TsugaClient TsugaResourceClient
	// Teams resolves spec.ownerName to a team ID.
	Teams TeamResolver
	// Recorder emits drift events; nil skips them.
	Recorder record.EventRecorder
	// DriftCheckInterval is how often the remote resource is checked for
	// drift; 0 means defaultDriftCheckInterval.
	DriftCheckInterval time.Duration
}

//+kubebuilder:rbac:groups=observability.tsuga.com,resources=dashboards,verbs=get;list;watch;patch
//+kubebuilder:rbac:groups=observability.tsuga.com,resources=dashboards/status,verbs=update
//+kubebuilder:rbac:groups="",resources=events,verbs=create;patch

func (r *TsugaDashboardReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	reconciler := genericTsugaReconciler[*v1alpha1.Dashboard]{
		Client:        r.Client,
		adapter:       dashboardAdapter{teams: r.Teams},
		tsuga:         r.TsugaClient,
		recorder:      r.Recorder,
		checkInterval: r.DriftCheckInterval,
	}
	return reconciler.Reconcile(ctx, req)
}

func (r *TsugaDashboardReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.Dashboard{}).
		Named("dashboard").
		Complete(r)
}
