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

// TsugaMonitorFolderReconciler reconciles MonitorFolder custom resources.
type TsugaMonitorFolderReconciler struct {
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

//+kubebuilder:rbac:groups=observability.tsuga.com,resources=monitorfolders,verbs=get;list;watch;patch
//+kubebuilder:rbac:groups=observability.tsuga.com,resources=monitorfolders/status,verbs=update

func (r *TsugaMonitorFolderReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	reconciler := genericTsugaReconciler[*v1alpha1.MonitorFolder]{
		Client:        r.Client,
		adapter:       &monitorFolderAdapter{KubeClient: r.Client, Teams: r.Teams},
		tsuga:         r.TsugaClient,
		recorder:      r.Recorder,
		checkInterval: r.DriftCheckInterval,
	}
	return reconciler.Reconcile(ctx, req)
}

// SetupWithManager registers nothing when the MonitorFolder CRD is not
// served: a chart that lags the image lacks it, and watching it would fail
// cache sync for the whole manager.
func (r *TsugaMonitorFolderReconciler) SetupWithManager(mgr ctrl.Manager) error {
	served, err := crdServed(mgr.GetRESTMapper(), v1alpha1.GroupVersion.WithKind("MonitorFolder"))
	if err != nil {
		return err
	}
	if !served {
		mgr.GetLogger().Info("MonitorFolder CRD not found; disabling the MonitorFolder controller. Install this release's CRDs to enable it.")
		return nil
	}
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.MonitorFolder{}).
		Watches(&v1alpha1.MonitorFolder{}, handler.EnqueueRequestsFromMapFunc(r.parentToChildren)).
		Named("monitorfolder").
		Complete(r)
}

func (r *TsugaMonitorFolderReconciler) parentToChildren(ctx context.Context, obj client.Object) []reconcile.Request {
	var folders v1alpha1.MonitorFolderList
	if err := r.List(ctx, &folders, client.InNamespace(obj.GetNamespace())); err != nil {
		logf.FromContext(ctx).Error(err, "parentToChildren: failed to list MonitorFolders",
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

type monitorFolderAdapter struct {
	KubeClient client.Client
	Teams      TeamResolver
}

func (a *monitorFolderAdapter) NewObject() *v1alpha1.MonitorFolder { return &v1alpha1.MonitorFolder{} }

// Policy is always Overwrite: folders have no driftPolicy, so edits made in
// Tsuga are reverted.
func (a *monitorFolderAdapter) Policy(*v1alpha1.MonitorFolder) v1alpha1.DriftPolicy { return "" }

func (a *monitorFolderAdapter) Finalizer() string { return monitorFolderFinalizer }

func (a *monitorFolderAdapter) Status(obj *v1alpha1.MonitorFolder) *v1alpha1.SyncStatus {
	return &obj.Status.SyncStatus
}

// InSync also compares the parent ID resolved from parentFolderRef so a
// recreated parent is pushed even though the generation did not change.
func (a *monitorFolderAdapter) InSync(ctx context.Context, obj *v1alpha1.MonitorFolder) bool {
	if obj.Status.ObservedGeneration != obj.Generation {
		return false
	}
	_, fromRef, err := a.resolvedSpec(ctx, obj)
	return err == nil && fromRef == obj.Status.ParentFolderID
}

func (a *monitorFolderAdapter) MarkSynced(latest, synced *v1alpha1.MonitorFolder) {
	latest.Status.ObservedGeneration = synced.Generation
	latest.Status.ParentFolderID = synced.Status.ParentFolderID
}

// Payload also records the parent ID resolved from parentFolderRef on obj so
// MarkSynced can persist what was pushed.
func (a *monitorFolderAdapter) Payload(ctx context.Context, obj *v1alpha1.MonitorFolder) ([]byte, error) {
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
	payload, err := invalidSpec(tsugamapper.MonitorFolderSpecToPayload(spec))
	if err != nil {
		return nil, err
	}
	obj.Status.ParentFolderID = fromRef
	return payload, nil
}

func (a *monitorFolderAdapter) resolvedSpec(ctx context.Context, obj *v1alpha1.MonitorFolder) (v1alpha1.MonitorFolderSpec, string, error) {
	spec := obj.Spec
	var fromRef string
	var err error
	spec.ParentFolderID, fromRef, err = resolveRef(ctx, a.KubeClient, obj, "parentFolderRef",
		spec.ParentFolderID, spec.ParentFolderRef, &v1alpha1.MonitorFolder{},
		func(f *v1alpha1.MonitorFolder) string { return f.Status.ID })
	return spec, fromRef, err
}

func (a *monitorFolderAdapter) OrphanBlockers(ctx context.Context, obj *v1alpha1.MonitorFolder) ([]string, error) {
	var items v1alpha1.MonitorList
	if err := a.KubeClient.List(ctx, &items, client.InNamespace(obj.Namespace)); err != nil {
		return nil, err
	}
	var folders v1alpha1.MonitorFolderList
	if err := a.KubeClient.List(ctx, &folders, client.InNamespace(obj.Namespace)); err != nil {
		return nil, err
	}
	var blockers []string
	for _, item := range items.Items {
		if pointsAt(item.Spec.FolderRef, item.Spec.FolderID, obj.Name, obj.Status.ID) {
			blockers = append(blockers, "Monitor/"+item.Name)
		}
	}
	for _, folder := range folders.Items {
		if folder.Name != obj.Name && pointsAt(folder.Spec.ParentFolderRef, folder.Spec.ParentFolderID, obj.Name, obj.Status.ID) {
			blockers = append(blockers, "MonitorFolder/"+folder.Name)
		}
	}
	slices.Sort(blockers)
	return blockers, nil
}
