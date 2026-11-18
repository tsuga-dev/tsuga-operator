package controller

import (
	"context"
	"slices"
	"time"

	"k8s.io/client-go/tools/record"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/tsuga-dev/tsuga-operator/api/v1alpha1"
	tsugamapper "github.com/tsuga-dev/tsuga-operator/internal/tsuga"
)

// TsugaDashboardFolderReconciler reconciles DashboardFolder custom resources.
type TsugaDashboardFolderReconciler struct {
	client.Client

	TsugaClient TsugaResourceClient
	// Teams resolves spec.ownerName to a team ID.
	Teams TeamResolver
	// Recorder emits drift events; nil skips them.
	Recorder record.EventRecorder
	// DriftCheckInterval is how often the remote folder is checked for
	// drift; 0 means defaultDriftCheckInterval.
	DriftCheckInterval time.Duration
}

//+kubebuilder:rbac:groups=observability.tsuga.com,resources=dashboardfolders,verbs=get;list;watch;patch
//+kubebuilder:rbac:groups=observability.tsuga.com,resources=dashboardfolders/status,verbs=update

func (r *TsugaDashboardFolderReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	reconciler := genericTsugaReconciler[*v1alpha1.DashboardFolder]{
		Client:        r.Client,
		adapter:       &dashboardFolderAdapter{KubeClient: r.Client, Teams: r.Teams},
		tsuga:         r.TsugaClient,
		recorder:      r.Recorder,
		checkInterval: r.DriftCheckInterval,
	}
	return reconciler.Reconcile(ctx, req)
}

// SetupWithManager registers nothing when the DashboardFolder CRD is not
// served: a chart that lags the image lacks it, and watching it would fail
// cache sync for the whole manager.
func (r *TsugaDashboardFolderReconciler) SetupWithManager(mgr ctrl.Manager) error {
	served, err := crdServed(mgr.GetRESTMapper(), v1alpha1.GroupVersion.WithKind("DashboardFolder"))
	if err != nil {
		return err
	}
	if !served {
		mgr.GetLogger().Info("DashboardFolder CRD not found; disabling the DashboardFolder controller. Install this release's CRDs to enable it.")
		return nil
	}
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.DashboardFolder{}).
		Watches(&v1alpha1.DashboardFolder{}, handler.EnqueueRequestsFromMapFunc(r.parentToChildren)).
		Named("dashboardfolder").
		Complete(r)
}

func (r *TsugaDashboardFolderReconciler) parentToChildren(ctx context.Context, obj client.Object) []reconcile.Request {
	var folders v1alpha1.DashboardFolderList
	if err := r.List(ctx, &folders, client.InNamespace(obj.GetNamespace())); err != nil {
		logf.FromContext(ctx).Error(err, "parentToChildren: failed to list DashboardFolders",
			"namespace", obj.GetNamespace(), "folder", obj.GetName())
		return nil
	}
	var reqs []reconcile.Request
	for _, f := range folders.Items {
		if f.Spec.ParentFolderRef != nil && f.Spec.ParentFolderRef.Name == obj.GetName() {
			reqs = append(reqs, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&f)})
		}
	}
	return reqs
}

type dashboardFolderAdapter struct {
	KubeClient client.Client
	Teams      TeamResolver
}

func (a *dashboardFolderAdapter) NewObject() *v1alpha1.DashboardFolder {
	return &v1alpha1.DashboardFolder{}
}

// Policy is always Overwrite: folders have no driftPolicy, so edits made in
// Tsuga are reverted.
func (a *dashboardFolderAdapter) Policy(*v1alpha1.DashboardFolder) v1alpha1.DriftPolicy { return "" }

func (a *dashboardFolderAdapter) Finalizer() string { return dashboardFolderFinalizer }

func (a *dashboardFolderAdapter) Status(obj *v1alpha1.DashboardFolder) *v1alpha1.SyncStatus {
	return &obj.Status.SyncStatus
}

// InSync also compares the parent ID resolved from parentFolderRef so a
// recreated parent is pushed even though the generation did not change.
func (a *dashboardFolderAdapter) InSync(ctx context.Context, obj *v1alpha1.DashboardFolder) bool {
	if obj.Status.ObservedGeneration != obj.Generation {
		return false
	}
	_, fromRef, err := a.resolvedSpec(ctx, obj)
	return err == nil && fromRef == obj.Status.ParentFolderID
}

func (a *dashboardFolderAdapter) MarkSynced(latest, synced *v1alpha1.DashboardFolder) {
	latest.Status.ObservedGeneration = synced.Generation
	latest.Status.ParentFolderID = synced.Status.ParentFolderID
}

// Payload also records the parent ID resolved from parentFolderRef on obj so
// MarkSynced can persist what was pushed.
func (a *dashboardFolderAdapter) Payload(ctx context.Context, obj *v1alpha1.DashboardFolder) ([]byte, error) {
	spec, fromRef, err := a.resolvedSpec(ctx, obj)
	if err != nil {
		return nil, err
	}
	if spec.Tags, err = withOwnerTag(obj, spec.Tags); err != nil {
		return nil, err
	}
	if spec.Owner, err = resolveOwner(ctx, a.Teams, &obj.Status.SyncStatus, obj.Generation, spec.Owner, spec.OwnerName); err != nil {
		return nil, err
	}
	payload, err := invalidSpec(tsugamapper.DashboardFolderSpecToPayload(spec))
	if err != nil {
		return nil, err
	}
	obj.Status.ParentFolderID = fromRef
	return payload, nil
}

func (a *dashboardFolderAdapter) resolvedSpec(ctx context.Context, obj *v1alpha1.DashboardFolder) (v1alpha1.DashboardFolderSpec, string, error) {
	spec := obj.Spec
	var fromRef string
	var err error
	spec.ParentFolderID, fromRef, err = resolveRef(ctx, a.KubeClient, obj, "parentFolderRef",
		spec.ParentFolderID, spec.ParentFolderRef, &v1alpha1.DashboardFolder{},
		func(f *v1alpha1.DashboardFolder) string { return f.Status.ID })
	return spec, fromRef, err
}

func (a *dashboardFolderAdapter) OrphanBlockers(ctx context.Context, obj *v1alpha1.DashboardFolder) ([]string, error) {
	var items v1alpha1.DashboardList
	if err := a.KubeClient.List(ctx, &items, client.InNamespace(obj.Namespace)); err != nil {
		return nil, err
	}
	var folders v1alpha1.DashboardFolderList
	if err := a.KubeClient.List(ctx, &folders, client.InNamespace(obj.Namespace)); err != nil {
		return nil, err
	}
	var blockers []string
	for _, item := range items.Items {
		if pointsAt(item.Spec.FolderRef, item.Spec.FolderID, obj.Name, obj.Status.ID) {
			blockers = append(blockers, "Dashboard/"+item.Name)
		}
	}
	for _, folder := range folders.Items {
		if folder.Name != obj.Name && pointsAt(folder.Spec.ParentFolderRef, folder.Spec.ParentFolderID, obj.Name, obj.Status.ID) {
			blockers = append(blockers, "DashboardFolder/"+folder.Name)
		}
	}
	slices.Sort(blockers)
	return blockers, nil
}
