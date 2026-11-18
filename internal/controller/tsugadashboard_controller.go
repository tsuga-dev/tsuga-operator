package controller

import (
	"context"
	"time"

	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

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
//+kubebuilder:rbac:groups=observability.tsuga.com,resources=dashboardfolders,verbs=get;list;watch
//+kubebuilder:rbac:groups="",resources=events,verbs=create;patch

func (r *TsugaDashboardReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	reconciler := genericTsugaReconciler[*v1alpha1.Dashboard]{
		Client:        r.Client,
		adapter:       dashboardAdapter{KubeClient: r.Client, Teams: r.Teams},
		tsuga:         r.TsugaClient,
		recorder:      r.Recorder,
		checkInterval: r.DriftCheckInterval,
	}
	return reconciler.Reconcile(ctx, req)
}

// SetupWithManager watches DashboardFolders only when their CRD is served: a
// chart that lags the image lacks it, and watching it would fail cache sync.
func (r *TsugaDashboardReconciler) SetupWithManager(mgr ctrl.Manager) error {
	builder := ctrl.NewControllerManagedBy(mgr).For(&v1alpha1.Dashboard{})
	foldersServed, err := crdServed(mgr.GetRESTMapper(), v1alpha1.GroupVersion.WithKind("DashboardFolder"))
	if err != nil {
		return err
	}
	if foldersServed {
		builder = builder.Watches(&v1alpha1.DashboardFolder{}, handler.EnqueueRequestsFromMapFunc(r.folderToDashboards))
	} else {
		mgr.GetLogger().Info("DashboardFolder CRD not found; Dashboards are not re-synced when a referenced folder changes. Install this release's CRDs to enable it.")
	}
	return builder.Named("dashboard").Complete(r)
}

func (r *TsugaDashboardReconciler) folderToDashboards(ctx context.Context, obj client.Object) []reconcile.Request {
	var dashboards v1alpha1.DashboardList
	if err := r.List(ctx, &dashboards, client.InNamespace(obj.GetNamespace())); err != nil {
		logf.FromContext(ctx).Error(err, "folderToDashboards: failed to list Dashboards",
			"namespace", obj.GetNamespace(), "folder", obj.GetName())
		return nil
	}
	var reqs []reconcile.Request
	for _, d := range dashboards.Items {
		if d.Spec.FolderRef != nil && d.Spec.FolderRef.Name == obj.GetName() {
			reqs = append(reqs, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&d)})
		}
	}
	return reqs
}
