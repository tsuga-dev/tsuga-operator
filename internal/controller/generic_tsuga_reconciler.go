package controller

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/retry"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/tsuga-dev/tsuga-operator/api/v1alpha1"
	tsugamapper "github.com/tsuga-dev/tsuga-operator/internal/tsuga"
)

const (
	dashboardFinalizer    = "observability.tsuga.com/dashboard-finalizer"
	monitorFinalizer      = "observability.tsuga.com/monitor-finalizer"
	sloFinalizer          = "observability.tsuga.com/slo-finalizer"
	rateLimitRequeueAfter = 30 * time.Second

	// driftResyncInterval re-pushes a Ready resource's spec so edits or
	// deletions made in the Tsuga UI are reverted or recreated. Only kinds
	// whose update is side-effect free opt in: a monitor update clears its
	// snooze and an SLO update recreates its alerts with new IDs.
	driftResyncInterval = 10 * time.Minute

	// ownerTagKey tags every remote resource with its Kubernetes UID so a
	// create whose ID never reached status can be found and adopted.
	ownerTagKey = "tsuga-operator/uid"

	// maxTags is the Tsuga API limit on tags per dashboard, monitor and SLO.
	maxTags = 50

	phaseReady    = "Ready"
	phasePending  = "Pending"
	phaseError    = "Error"
	phaseDeleting = "Deleting"
)

type tsugaResourceAdapter[T client.Object] interface {
	NewObject() T
	Finalizer() string
	Status(T) *v1alpha1.SyncStatus
	InSync(context.Context, T) bool
	// MarkSynced records on latest what the successful create or update of synced pushed.
	MarkSynced(latest, synced T)
	// Payload returns the Tsuga API request body for obj, owner tag included.
	Payload(context.Context, T) ([]byte, error)
}

type genericTsugaReconciler[T client.Object] struct {
	client.Client
	adapter tsugaResourceAdapter[T]
	tsuga   TsugaResourceClient
	// resync enables the driftResyncInterval re-push.
	resync bool
}

func (r *genericTsugaReconciler[T]) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	obj := r.adapter.NewObject()
	if err := r.Get(ctx, req.NamespacedName, obj); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if !obj.GetDeletionTimestamp().IsZero() {
		return r.handleDelete(ctx, obj)
	}

	if !slices.Contains(obj.GetFinalizers(), r.adapter.Finalizer()) {
		if err := r.patchFinalizers(ctx, obj, controllerutil.AddFinalizer); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	}

	if r.adapter.Status(obj).ID == "" {
		return r.createOrAdopt(ctx, obj, "Created in Tsuga")
	}

	status := r.adapter.Status(obj)
	// Error means the last push failed, whatever triggered it, so it is
	// retried even when the spec is in sync (e.g. a failed drift resync).
	if !r.adapter.InSync(ctx, obj) || status.Phase == phaseError || (r.resync && resyncDue(status)) {
		if err := r.updateRemote(ctx, obj); err != nil {
			if isNotFound(err) {
				return r.createOrAdopt(ctx, obj, "Recreated in Tsuga")
			}
			return r.handleRemoteError(ctx, obj, err)
		}
		if err := r.updateStatus(ctx, obj, func(latest T) {
			r.adapter.MarkSynced(latest, obj)
			setPhase(r.adapter.Status(latest), phaseReady, "Updated in Tsuga")
		}); err != nil {
			return ctrl.Result{}, err
		}
		return r.resyncResult(), nil
	}

	if !r.resync || status.Phase != phaseReady || status.LastSyncedAt == nil {
		return ctrl.Result{}, nil
	}
	return ctrl.Result{RequeueAfter: time.Until(status.LastSyncedAt.Add(driftResyncInterval))}, nil
}

func (r *genericTsugaReconciler[T]) resyncResult() ctrl.Result {
	if !r.resync {
		return ctrl.Result{}
	}
	return ctrl.Result{RequeueAfter: driftResyncInterval}
}

func resyncDue(status *v1alpha1.SyncStatus) bool {
	return status.Phase == phaseReady &&
		(status.LastSyncedAt == nil || time.Since(status.LastSyncedAt.Time) >= driftResyncInterval)
}

// createOrAdopt adopts the remote resource tagged with obj's UID when a
// previous create succeeded but its ID never reached status, and creates one
// otherwise, so a failed status write does not leave a duplicate behind.
func (r *genericTsugaReconciler[T]) createOrAdopt(ctx context.Context, obj T, createdMessage string) (ctrl.Result, error) {
	id, err := r.tsuga.FindByTag(ctx, ownerTagKey, string(obj.GetUID()))
	if err != nil {
		return r.handleRemoteError(ctx, obj, err)
	}

	message := createdMessage
	if id != "" {
		message = "Adopted existing resource in Tsuga"
		r.adapter.Status(obj).ID = id
		err = r.updateRemote(ctx, obj)
	} else {
		id, err = r.createRemote(ctx, obj)
	}
	if err != nil {
		return r.handleRemoteError(ctx, obj, err)
	}

	if err := r.updateStatus(ctx, obj, func(latest T) {
		r.adapter.Status(latest).ID = id
		r.adapter.MarkSynced(latest, obj)
		setPhase(r.adapter.Status(latest), phaseReady, message)
	}); err != nil {
		return ctrl.Result{}, err
	}
	return r.resyncResult(), nil
}

func (r *genericTsugaReconciler[T]) createRemote(ctx context.Context, obj T) (string, error) {
	payload, err := r.adapter.Payload(ctx, obj)
	if err != nil {
		return "", err
	}
	return r.tsuga.Create(ctx, payload)
}

func (r *genericTsugaReconciler[T]) updateRemote(ctx context.Context, obj T) error {
	payload, err := r.adapter.Payload(ctx, obj)
	if err != nil {
		return err
	}
	return r.tsuga.Update(ctx, r.adapter.Status(obj).ID, payload)
}

// withOwnerTag replaces any spec tag using the owner key with the UID tag, and
// rejects specs left with no room for it under the API tag limit.
func withOwnerTag(obj client.Object, tags []v1alpha1.ResourceTag) ([]v1alpha1.ResourceTag, error) {
	tags = slices.DeleteFunc(slices.Clone(tags), func(t v1alpha1.ResourceTag) bool { return t.Key == ownerTagKey })
	if len(tags) >= maxTags {
		return nil, fmt.Errorf("%w: tags: at most %d allowed, as the operator adds the %q tag",
			errInvalidSpec, maxTags-1, ownerTagKey)
	}
	return append(tags, v1alpha1.ResourceTag{Key: ownerTagKey, Value: string(obj.GetUID())}), nil
}

func setPhase(status *v1alpha1.SyncStatus, phase, message string) {
	now := metav1.Now()
	status.Phase = phase
	status.Message = message
	status.LastSyncedAt = &now
}

func (r *genericTsugaReconciler[T]) handleDelete(ctx context.Context, obj T) (ctrl.Result, error) {
	if !slices.Contains(obj.GetFinalizers(), r.adapter.Finalizer()) {
		return ctrl.Result{}, nil
	}

	if err := r.updateStatus(ctx, obj, func(latest T) {
		status := r.adapter.Status(latest)
		// Rewriting Deleting on every retry would bump LastSyncedAt, emit a
		// watch event and bypass the workqueue backoff.
		if status.Phase == phaseDeleting {
			return
		}
		setPhase(status, phaseDeleting, "Deleting remote resource")
	}); err != nil {
		logf.FromContext(ctx).Error(err, "failed to record Deleting phase; continuing with delete")
	}

	remoteID := r.adapter.Status(obj).ID
	if remoteID == "" {
		// A create whose ID never reached status is still tagged with the UID.
		id, err := r.tsuga.FindByTag(ctx, ownerTagKey, string(obj.GetUID()))
		switch {
		case err == nil:
			remoteID = id
		case isClientError(err) || isNotFound(err):
			// a permanent rejection (400/422) would pin the finalizer
			// forever. Drop it; a remote copy, if one exists, stays findable by
			// its UID tag.
			logf.FromContext(ctx).Error(err, "tag lookup rejected; removing finalizer without a remote delete",
				"uid", obj.GetUID())
		default:
			return ctrl.Result{}, err
		}
	}
	if remoteID != "" {
		if err := r.tsuga.Delete(ctx, remoteID); err != nil && !isNotFound(err) {
			return ctrl.Result{}, err
		}
	}

	if err := r.patchFinalizers(ctx, obj, controllerutil.RemoveFinalizer); err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

func (r *genericTsugaReconciler[T]) handleRemoteError(ctx context.Context, obj T, err error) (ctrl.Result, error) {
	var httpErr *tsugaHTTPError
	if errors.As(err, &httpErr) {
		logf.FromContext(ctx).Info("tsuga API request failed", "statusCode", httpErr.statusCode, "requestId", httpErr.requestID())
	}

	if statusErr := r.updateStatus(ctx, obj, func(latest T) {
		status := r.adapter.Status(latest)
		if status.Phase == phaseError && status.Message == err.Error() {
			return
		}
		setPhase(status, phaseError, err.Error())
	}); statusErr != nil {
		return ctrl.Result{}, statusErr
	}

	if isRetryableClientError(err) {
		return ctrl.Result{RequeueAfter: rateLimitRequeueAfter}, nil
	}

	if isClientError(err) || errors.Is(err, errInvalidSpec) {
		return ctrl.Result{}, nil
	}

	return ctrl.Result{}, err
}

// patchFinalizers sends only metadata.finalizers. A full Update would
// re-serialize spec through the Go types, whose omitempty drops user-set
// empty lists and bumps generation. The merge patch replaces the whole
// finalizers list, so the optimistic lock makes a concurrent finalizer edit
// conflict and retry on a fresh read instead of being overwritten.
func (r *genericTsugaReconciler[T]) patchFinalizers(ctx context.Context, obj T, mutate func(client.Object, string) bool) error {
	key := client.ObjectKeyFromObject(obj)

	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		latest := r.adapter.NewObject()
		if err := r.Get(ctx, key, latest); err != nil {
			return err
		}
		patch := client.MergeFromWithOptions(latest.DeepCopyObject().(T), client.MergeFromWithOptimisticLock{})
		if !mutate(latest, r.adapter.Finalizer()) {
			return nil
		}
		return r.Patch(ctx, latest, patch)
	})
}

func (r *genericTsugaReconciler[T]) updateStatus(ctx context.Context, obj T, mutate func(T)) error {
	key := client.ObjectKeyFromObject(obj)

	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		latest := r.adapter.NewObject()
		if err := r.Get(ctx, key, latest); err != nil {
			return err
		}

		before := latest.DeepCopyObject()
		mutate(latest)
		// Skipping no-op writes keeps a repeated permanent error from
		// emitting a watch event that re-runs the same failing API call.
		if equality.Semantic.DeepEqual(before, latest) {
			return nil
		}

		return r.Status().Update(ctx, latest)
	})
}

type dashboardAdapter struct{}

func (dashboardAdapter) NewObject() *v1alpha1.Dashboard { return &v1alpha1.Dashboard{} }

func (dashboardAdapter) Finalizer() string { return dashboardFinalizer }

func (dashboardAdapter) Status(obj *v1alpha1.Dashboard) *v1alpha1.SyncStatus { return &obj.Status }

func (dashboardAdapter) InSync(_ context.Context, obj *v1alpha1.Dashboard) bool {
	return obj.Status.ObservedGeneration == obj.Generation
}

func (dashboardAdapter) MarkSynced(latest, synced *v1alpha1.Dashboard) {
	latest.Status.ObservedGeneration = synced.Generation
}

func (dashboardAdapter) Payload(_ context.Context, obj *v1alpha1.Dashboard) ([]byte, error) {
	spec := obj.Spec
	tags, err := withOwnerTag(obj, spec.Tags)
	if err != nil {
		return nil, err
	}
	spec.Tags = tags
	return invalidSpec(tsugamapper.DashboardSpecToPayload(spec))
}

type monitorAdapter struct {
	KubeClient client.Client
}

func (a *monitorAdapter) NewObject() *v1alpha1.Monitor { return &v1alpha1.Monitor{} }

func (a *monitorAdapter) Finalizer() string { return monitorFinalizer }

func (a *monitorAdapter) Status(obj *v1alpha1.Monitor) *v1alpha1.SyncStatus {
	return &obj.Status.SyncStatus
}

// InSync also compares the resolved dashboard ID so a recreated referenced
// Dashboard is pushed even though the Monitor generation did not change.
func (a *monitorAdapter) InSync(ctx context.Context, obj *v1alpha1.Monitor) bool {
	if obj.Status.ObservedGeneration != obj.Generation {
		return false
	}
	spec, err := a.resolvedSpec(ctx, obj)
	if err != nil {
		// An unresolvable ref (Dashboard deleted or not synced yet) goes through
		// Payload so its error reaches status instead of reporting Ready.
		return false
	}
	return ptr.Deref(spec.DashboardID, "") == obj.Status.DashboardID
}

func (a *monitorAdapter) MarkSynced(latest, synced *v1alpha1.Monitor) {
	latest.Status.ObservedGeneration = synced.Generation
	latest.Status.DashboardID = synced.Status.DashboardID
}

// Payload also records the resolved dashboard ID on obj so MarkSynced can
// persist what was pushed.
func (a *monitorAdapter) Payload(ctx context.Context, obj *v1alpha1.Monitor) ([]byte, error) {
	spec, err := a.resolvedSpec(ctx, obj)
	if err != nil {
		return nil, err
	}

	if spec.Tags, err = withOwnerTag(obj, spec.Tags); err != nil {
		return nil, err
	}
	payload, err := invalidSpec(tsugamapper.MonitorSpecToPayload(spec))
	if err != nil {
		return nil, err
	}

	obj.Status.DashboardID = ptr.Deref(spec.DashboardID, "")
	return payload, nil
}

func (a *monitorAdapter) resolvedSpec(ctx context.Context, obj *v1alpha1.Monitor) (v1alpha1.MonitorSpec, error) {
	spec := obj.Spec
	if spec.DashboardID != nil {
		if spec.DashboardRef != nil {
			logf.FromContext(ctx).Info("both dashboardId and dashboardRef are set on Monitor; dashboardId takes precedence and dashboardRef is ignored",
				"monitor", obj.Name, "namespace", obj.Namespace)
		}
		return spec, nil
	}
	if spec.DashboardRef == nil {
		return spec, nil
	}

	var dashboard v1alpha1.Dashboard
	key := client.ObjectKey{Namespace: obj.Namespace, Name: spec.DashboardRef.Name}
	if err := a.KubeClient.Get(ctx, key, &dashboard); err != nil {
		return v1alpha1.MonitorSpec{}, fmt.Errorf("resolve dashboardRef %q: %w", spec.DashboardRef.Name, err)
	}
	if dashboard.Status.ID == "" {
		return v1alpha1.MonitorSpec{}, errors.New("referenced dashboard has not been synced to Tsuga yet")
	}

	spec.DashboardID = &dashboard.Status.ID
	return spec, nil
}

// collectionStatusEqual reports whether two CollectionStatus values match on
// the fields that reflect a meaningful change (Phase, Message,
// ObservedGeneration, ManagedResources, Conditions). LastSyncedAt is
// deliberately ignored so a steady-state reconcile that would only bump the
// timestamp is treated as a no-op, which stops the For() watch from
// retriggering in a hot loop.
func collectionStatusEqual(a, b v1alpha1.CollectionStatus) bool {
	return a.Phase == b.Phase &&
		a.Message == b.Message &&
		a.ObservedGeneration == b.ObservedGeneration &&
		slices.Equal(a.ManagedResources, b.ManagedResources) &&
		conditionsEqual(a.Conditions, b.Conditions)
}

type sloAdapter struct{}

func (sloAdapter) NewObject() *v1alpha1.SLO { return &v1alpha1.SLO{} }

func (sloAdapter) Finalizer() string { return sloFinalizer }

func (sloAdapter) Status(obj *v1alpha1.SLO) *v1alpha1.SyncStatus { return &obj.Status }

func (sloAdapter) InSync(_ context.Context, obj *v1alpha1.SLO) bool {
	return obj.Status.ObservedGeneration == obj.Generation
}

func (sloAdapter) MarkSynced(latest, synced *v1alpha1.SLO) {
	latest.Status.ObservedGeneration = synced.Generation
}

func (sloAdapter) Payload(_ context.Context, obj *v1alpha1.SLO) ([]byte, error) {
	spec := obj.Spec
	tags, err := withOwnerTag(obj, spec.Tags)
	if err != nil {
		return nil, err
	}
	spec.Tags = tags
	return invalidSpec(tsugamapper.SLOSpecToPayload(spec))
}

// errInvalidSpec marks a spec the mapper rejects: retrying cannot fix it,
// only a spec change can.
var errInvalidSpec = errors.New("invalid spec")

func invalidSpec(payload []byte, err error) ([]byte, error) {
	if err != nil {
		return nil, fmt.Errorf("%w: %w", errInvalidSpec, err)
	}
	return payload, nil
}
