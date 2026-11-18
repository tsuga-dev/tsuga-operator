package controller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/url"
	"slices"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/record"
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
	dashboardFinalizer       = "observability.tsuga.com/dashboard-finalizer"
	monitorFinalizer         = "observability.tsuga.com/monitor-finalizer"
	sloFinalizer             = "observability.tsuga.com/slo-finalizer"
	monitorFolderFinalizer   = "observability.tsuga.com/monitorfolder-finalizer"
	dashboardFolderFinalizer = "observability.tsuga.com/dashboardfolder-finalizer"
	rateLimitRequeueAfter    = 30 * time.Second

	// defaultDriftCheckInterval is how often a synced resource is compared
	// with Tsuga when the reconciler sets no checkInterval.
	defaultDriftCheckInterval = 10 * time.Minute

	// ownerTagKey tags every remote resource with its Kubernetes UID so a
	// create whose ID never reached status can be found and adopted.
	ownerTagKey = "tsuga-operator/uid"

	// maxTags is the Tsuga API limit on tags per dashboard, monitor and SLO.
	maxTags = 50

	// acceptRemoteAnnotation set to "true" makes the next reconcile take the
	// resource in Tsuga as the new drift baseline.
	acceptRemoteAnnotation = "observability.tsuga.com/accept-remote"

	phaseReady    = "Ready"
	phasePending  = "Pending"
	phaseError    = "Error"
	phaseDeleting = "Deleting"
	phaseDrifted  = "Drifted"
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
	// Policy returns the CR's drift policy; empty means Overwrite.
	Policy(T) v1alpha1.DriftPolicy
}

type genericTsugaReconciler[T client.Object] struct {
	client.Client
	adapter tsugaResourceAdapter[T]
	tsuga   TsugaResourceClient
	// recorder emits drift events; nil skips them.
	recorder record.EventRecorder
	// checkInterval is how often the remote resource is checked for drift;
	// 0 means defaultDriftCheckInterval.
	checkInterval time.Duration
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
	if obj.GetAnnotations()[acceptRemoteAnnotation] == "true" {
		remote, err := r.tsuga.Get(ctx, status.ID)
		if err == nil {
			return r.acceptRemote(ctx, obj, remote)
		}
		// A deleted resource cannot be accepted; the normal flow reports it.
		if !isNotFound(err) {
			return r.handleRemoteError(ctx, obj, err)
		}
	}

	// Error is not a reason to push: a failed spec push leaves InSync false
	// and a failed revert is found again as drift. It only makes the check
	// due, so a transient GET failure never re-PUTs an unchanged spec (which
	// would clear a monitor's snooze).
	pushNeeded := !r.adapter.InSync(ctx, obj)
	if !pushNeeded && status.Phase != phaseError && status.LastDriftCheckAt != nil {
		if wait := time.Until(status.LastDriftCheckAt.Add(r.interval())); wait > 0 {
			return ctrl.Result{RequeueAfter: wait}, nil
		}
	}

	remote, err := r.tsuga.Get(ctx, status.ID)
	deleted := isNotFound(err)
	if err != nil && !deleted {
		return r.handleRemoteError(ctx, obj, err)
	}
	if len(status.RemoteFieldHashes) == 0 && !pushNeeded {
		if deleted {
			return r.reportUnbaselinedDeletion(ctx, obj)
		}
		if err := r.recordBaseline(ctx, obj, remote, true); err != nil {
			return r.handleRemoteError(ctx, obj, err)
		}
		return ctrl.Result{RequeueAfter: r.interval()}, nil
	}

	var drifted []string
	switch {
	case deleted:
	case len(status.RemoteFieldHashes) == 0:
		// A fresh baseline has no drift, so the pending push runs now.
		if err := r.recordBaseline(ctx, obj, remote, false); err != nil {
			return r.handleRemoteError(ctx, obj, err)
		}
	default:
		if drifted, err = r.remoteDrift(ctx, obj, remote, pushNeeded); err != nil {
			return r.handleRemoteError(ctx, obj, err)
		}
	}
	if deleted || len(drifted) > 0 {
		return r.handleDrift(ctx, obj, deleted, drifted)
	}
	if pushNeeded {
		return r.push(ctx, obj, "Updated in Tsuga")
	}

	if err := r.updateStatus(ctx, obj, func(latest T) {
		status := r.adapter.Status(latest)
		status.LastDriftCheckAt = ptr.To(metav1.Now())
		if status.Phase == phaseDrifted || status.Phase == phaseError {
			setPhase(status, phaseReady, "Matches the CR in Tsuga")
		}
	}); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: r.interval()}, nil
}

func (r *genericTsugaReconciler[T]) interval() time.Duration {
	if r.checkInterval > 0 {
		return r.checkInterval
	}
	return defaultDriftCheckInterval
}

// recordBaseline stores the remote hashes of a resource that has none (for
// example after an upgrade) without pushing, so an upgrade never reverts a UI
// edit. The caller never requeues immediately: on CRDs too old to keep the
// hashes the write is pruned, and an immediate requeue would GET in a loop.
// clearPhase moves a stale Drifted or Error back to Ready; it is off when a
// push follows, which sets the phase itself.
func (r *genericTsugaReconciler[T]) recordBaseline(ctx context.Context, obj T, remote []byte, clearPhase bool) error {
	hashes, err := r.remoteHashes(ctx, obj, remote)
	if err != nil {
		return err
	}
	return r.updateStatus(ctx, obj, func(latest T) {
		status := r.adapter.Status(latest)
		status.RemoteFieldHashes = hashes
		status.LastDriftCheckAt = ptr.To(metav1.Now())
		if clearPhase && (status.Phase == phaseDrifted || status.Phase == phaseError) {
			setPhase(status, phaseReady, "Matches the CR in Tsuga")
		}
	})
}

// reportUnbaselinedDeletion reports a resource deleted in Tsuga before any
// baseline was recorded. It is not recreated: after an upgrade that would
// revert a deletion made in the UI, and right after a create a lagging read
// would recreate a duplicate. A CR change recreates it.
func (r *genericTsugaReconciler[T]) reportUnbaselinedDeletion(ctx context.Context, obj T) (ctrl.Result, error) {
	logf.FromContext(ctx).Info("drift detected", "policy", r.adapter.Policy(obj), "deleted", true, "baseline", false)
	return r.markDrifted(ctx, obj, "deleted in Tsuga before the operator recorded a baseline; change the CR to recreate it")
}

// markDrifted emits DriftDetected and sets phase Drifted, keeping the baseline
// so the drift persists. An unchanged Drifted status is not rewritten: each
// write would retrigger the reconcile.
func (r *genericTsugaReconciler[T]) markDrifted(ctx context.Context, obj T, message string) (ctrl.Result, error) {
	r.event(obj, corev1.EventTypeWarning, "DriftDetected", message)
	if err := r.updateStatus(ctx, obj, func(latest T) {
		status := r.adapter.Status(latest)
		if status.Phase == phaseDrifted && status.Message == message {
			return
		}
		setPhase(status, phaseDrifted, message)
		status.LastDriftCheckAt = ptr.To(metav1.Now())
	}); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: r.interval()}, nil
}

// remoteDrift lists the managed fields of remote that differ from the
// baseline. With a push pending, fields already holding the desired value
// are left out.
func (r *genericTsugaReconciler[T]) remoteDrift(ctx context.Context, obj T, remote []byte, pushNeeded bool) ([]string, error) {
	baseline := r.adapter.Status(obj).RemoteFieldHashes
	current, err := fieldHashes(remote, slices.Collect(maps.Keys(baseline)))
	if err != nil {
		return nil, err
	}
	drifted := driftedFields(baseline, current)
	if pushNeeded && len(drifted) > 0 {
		drifted = r.withoutPushedFields(ctx, obj, remote, drifted)
	}
	return drifted, nil
}

// withoutPushedFields drops from drifted the fields where remote already holds
// the desired value, so a push whose status write failed is not taken for
// drift. It only ever removes fields; on error drifted is returned unchanged.
func (r *genericTsugaReconciler[T]) withoutPushedFields(ctx context.Context, obj T, remote []byte, drifted []string) []string {
	payload, err := r.adapter.Payload(ctx, obj)
	if err != nil {
		return drifted
	}
	var desired, current map[string]any
	if json.Unmarshal(payload, &desired) != nil || json.Unmarshal(remote, &current) != nil {
		return drifted
	}
	return slices.DeleteFunc(drifted, func(k string) bool { return holdsDesired(current[k], desired[k]) })
}

// acceptRemote takes remote as the new baseline, so a UI edit copied into the
// CR is no longer drift. ObservedGeneration is left alone: a pending CR change
// is still pushed on the next pass.
func (r *genericTsugaReconciler[T]) acceptRemote(ctx context.Context, obj T, remote []byte) (ctrl.Result, error) {
	hashes, err := r.remoteHashes(ctx, obj, remote)
	if err != nil {
		return r.handleRemoteError(ctx, obj, err)
	}
	if err := r.updateStatus(ctx, obj, func(latest T) {
		status := r.adapter.Status(latest)
		status.RemoteFieldHashes = hashes
		status.LastDriftCheckAt = ptr.To(metav1.Now())
		setPhase(status, phaseReady, "Accepted the resource in Tsuga as the drift baseline")
	}); err != nil {
		return ctrl.Result{}, err
	}
	r.event(obj, corev1.EventTypeNormal, "DriftAccepted", "accepted the resource in Tsuga as the drift baseline")
	if err := r.patchAnnotationAway(ctx, obj); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: time.Nanosecond}, nil
}

// remoteHashes hashes the fields of remote that obj's payload manages.
func (r *genericTsugaReconciler[T]) remoteHashes(ctx context.Context, obj T, remote []byte) (map[string]string, error) {
	payload, err := r.adapter.Payload(ctx, obj)
	if err != nil {
		return nil, err
	}
	return baselineHashes(payload, remote)
}

func (r *genericTsugaReconciler[T]) handleDrift(ctx context.Context, obj T, deleted bool, fields []string) (ctrl.Result, error) {
	what := "drifted fields: " + strings.Join(fields, ", ")
	if deleted {
		what = "deleted in Tsuga"
	}
	policy := r.adapter.Policy(obj)
	logf.FromContext(ctx).Info("drift detected", "policy", policy, "fields", fields, "deleted", deleted)

	if policy != v1alpha1.DriftPolicyWarn {
		r.event(obj, corev1.EventTypeWarning, "DriftReverted", what+"; reverting to the CR")
		if deleted {
			return r.createOrAdopt(ctx, obj, "Recreated in Tsuga")
		}
		return r.push(ctx, obj, "Reverted drift in Tsuga")
	}

	message := what + "; to keep the Tsuga changes, copy them into the CR and annotate it observability.tsuga.com/accept-remote=true"
	if deleted {
		message = what + "; set driftPolicy to Overwrite to recreate it"
	}
	return r.markDrifted(ctx, obj, message)
}

func (r *genericTsugaReconciler[T]) push(ctx context.Context, obj T, message string) (ctrl.Result, error) {
	payload, err := r.adapter.Payload(ctx, obj)
	if err == nil {
		err = r.tsuga.Update(ctx, r.adapter.Status(obj).ID, payload)
	}
	if isNotFound(err) {
		return r.createOrAdopt(ctx, obj, "Recreated in Tsuga")
	}
	if err != nil {
		return r.handleRemoteError(ctx, obj, err)
	}
	return r.recordPush(ctx, obj, payload, message)
}

// createOrAdopt adopts the remote resource tagged with obj's UID when a
// previous create succeeded but its ID never reached status, and creates one
// otherwise, so a failed status write does not leave a duplicate behind.
func (r *genericTsugaReconciler[T]) createOrAdopt(ctx context.Context, obj T, createdMessage string) (ctrl.Result, error) {
	id, err := r.tsuga.FindByTag(ctx, ownerTagKey, string(obj.GetUID()))
	if err != nil {
		return r.handleRemoteError(ctx, obj, err)
	}
	payload, err := r.adapter.Payload(ctx, obj)
	if err != nil {
		return r.handleRemoteError(ctx, obj, err)
	}

	message := createdMessage
	if id != "" {
		message = "Adopted existing resource in Tsuga"
		err = r.tsuga.Update(ctx, id, payload)
	} else {
		id, err = r.tsuga.Create(ctx, payload)
	}
	if err != nil {
		return r.handleRemoteError(ctx, obj, err)
	}
	r.adapter.Status(obj).ID = id
	return r.recordPush(ctx, obj, payload, message)
}

// recordPush records a successful push of obj with the hashes of a GET made
// right after it; a create or update response can differ from a read, so it
// is never used. When that GET fails the push is still recorded, with no
// baseline, so the next pass re-baselines instead of taking the operator's
// own push for drift.
func (r *genericTsugaReconciler[T]) recordPush(ctx context.Context, obj T, payload []byte, message string) (ctrl.Result, error) {
	remote, getErr := r.tsuga.Get(ctx, r.adapter.Status(obj).ID)
	var hashes map[string]string
	if getErr == nil {
		hashes, getErr = baselineHashes(payload, remote)
	}
	r.adapter.Status(obj).RemoteFieldHashes = hashes

	if err := r.updateStatus(ctx, obj, func(latest T) {
		r.copyPushed(latest, obj)
		if getErr == nil {
			setPhase(r.adapter.Status(latest), phaseReady, message)
		}
	}); err != nil {
		return ctrl.Result{}, err
	}
	if getErr != nil {
		return r.handleRemoteError(ctx, obj, getErr)
	}
	return ctrl.Result{RequeueAfter: r.interval()}, nil
}

// baselineHashes hashes the fields of remote that the payload manages. Keys
// come from the payload, never from remote, so server-only fields such as a
// monitor's pauseUntil are not compared.
func baselineHashes(payload, remote []byte) (map[string]string, error) {
	keys, err := payloadKeys(payload)
	if err != nil {
		return nil, err
	}
	return fieldHashes(remote, keys)
}

// copyPushed records on latest what the successful push of synced wrote.
func (r *genericTsugaReconciler[T]) copyPushed(latest, synced T) {
	r.adapter.MarkSynced(latest, synced)
	status := r.adapter.Status(latest)
	status.ID = r.adapter.Status(synced).ID
	status.OwnerID = r.adapter.Status(synced).OwnerID
	status.RemoteFieldHashes = r.adapter.Status(synced).RemoteFieldHashes
	status.LastDriftCheckAt = ptr.To(metav1.Now())
}

func (r *genericTsugaReconciler[T]) event(obj T, eventType, reason, message string) {
	if r.recorder != nil {
		r.recorder.Event(obj, eventType, reason, message)
	}
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

// resolveOwner returns the team ID to push as owner: owner itself, or the ID
// of the team named ownerName, recorded in status.OwnerID. Teams cannot be
// renamed, so the recorded ID is reused until the spec changes, and drift
// reverts and error retries make no team lookup.
func resolveOwner(ctx context.Context, teams TeamResolver, status *v1alpha1.SyncStatus, generation int64, owner, ownerName string) (string, error) {
	if ownerName == "" {
		status.OwnerID = ""
		return owner, nil
	}
	if status.OwnerID != "" && status.ObservedGeneration == generation {
		return status.OwnerID, nil
	}
	id, err := teams.TeamID(ctx, ownerName)
	if err != nil {
		return "", err
	}
	status.OwnerID = id
	return id, nil
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
			return r.deleteFailed(ctx, obj, deleteMessage(err), err)
		}
	}
	if remoteID != "" {
		if err := r.tsuga.Delete(ctx, remoteID); err != nil && !isNotFound(err) {
			blocker, orphanable := any(r.adapter).(orphanBlocker[T])
			if !orphanable || obj.GetAnnotations()[orphanIfNotEmptyAnnotation] != annotationTrue || !isFolderNotEmpty(err) {
				return r.deleteFailed(ctx, obj, deleteMessage(err), err)
			}
			blockers, listErr := blocker.OrphanBlockers(ctx, obj)
			if listErr != nil {
				return ctrl.Result{}, listErr
			}
			if len(blockers) > 0 {
				message := fmt.Sprintf("waiting for %s to be deleted before orphaning: %s", strings.Join(blockers, ", "), deleteMessage(err))
				return r.deleteFailed(ctx, obj, message, err)
			}
			// The last referrer may have been deleted in Tsuga while the first
			// call was in flight, leaving the folder empty: confirm before
			// orphaning so a teardown still deletes it.
			switch retryErr := r.tsuga.Delete(ctx, remoteID); {
			case retryErr == nil || isNotFound(retryErr):
			case isFolderNotEmpty(retryErr):
				logf.FromContext(ctx).Info("leaving the folder in Tsuga: it is not empty and no resource in the namespace references it",
					"name", obj.GetName(), "namespace", obj.GetNamespace(), "tsugaID", remoteID)
			default:
				return r.deleteFailed(ctx, obj, deleteMessage(retryErr), retryErr)
			}
		}
	}

	if err := r.patchFinalizers(ctx, obj, controllerutil.RemoveFinalizer); err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

// deleteMessage is err's text for status, except for transport errors, whose
// text changes on every attempt (local port): a changing message would write
// status, emit a watch event and bypass the workqueue backoff each retry.
func deleteMessage(err error) string {
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return fmt.Sprintf("%s %s: request to Tsuga failed", urlErr.Op, urlErr.URL)
	}
	return err.Error()
}

// orphanIfNotEmptyAnnotation, set to "true" on a folder, removes the
// finalizer without deleting the Tsuga folder when Tsuga refuses the delete
// because the folder is not empty and nothing in the namespace references it.
const (
	orphanIfNotEmptyAnnotation = "observability.tsuga.com/orphan-if-not-empty"
	annotationTrue             = "true"
)

// orphanBlocker is implemented by adapters whose remote object may be left in
// Tsuga under orphanIfNotEmptyAnnotation.
type orphanBlocker[T client.Object] interface {
	// OrphanBlockers lists, as sorted Kind/name, the resources in obj's
	// namespace that still point at it.
	OrphanBlockers(ctx context.Context, obj T) ([]string, error)
}

// pointsAt reports whether a raw-ID-or-reference field targets the folder
// named name with Tsuga ID id.
func pointsAt(ref *v1alpha1.LocalObjectReference, rawID *string, name, id string) bool {
	return (ref != nil && ref.Name == name) || (id != "" && ptr.Deref(rawID, "") == id)
}

// deleteFailed records phase Deleting and why the remote delete failed while
// keeping the finalizer. A permanent client error (such as a folder that is not empty) is
// retried on a steady interval instead of the growing workqueue backoff, so
// the delete completes soon after the cause is cleared in Tsuga.
func (r *genericTsugaReconciler[T]) deleteFailed(ctx context.Context, obj T, message string, err error) (ctrl.Result, error) {
	if statusErr := r.updateStatus(ctx, obj, func(latest T) {
		status := r.adapter.Status(latest)
		// Rewriting the same message on every retry would bump LastSyncedAt,
		// emit a watch event and bypass the workqueue backoff.
		if status.Phase == phaseDeleting && status.Message == message {
			return
		}
		setPhase(status, phaseDeleting, message)
	}); statusErr != nil {
		return ctrl.Result{}, statusErr
	}
	if isClientError(err) {
		return ctrl.Result{RequeueAfter: rateLimitRequeueAfter}, nil
	}
	return ctrl.Result{}, err
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

// patchAnnotationAway removes the accept-remote annotation with a metadata
// merge patch, for the same reasons as patchFinalizers.
func (r *genericTsugaReconciler[T]) patchAnnotationAway(ctx context.Context, obj T) error {
	key := client.ObjectKeyFromObject(obj)

	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		latest := r.adapter.NewObject()
		if err := r.Get(ctx, key, latest); err != nil {
			return err
		}
		annotations := latest.GetAnnotations()
		if _, ok := annotations[acceptRemoteAnnotation]; !ok {
			return nil
		}
		patch := client.MergeFromWithOptions(latest.DeepCopyObject().(T), client.MergeFromWithOptimisticLock{})
		delete(annotations, acceptRemoteAnnotation)
		latest.SetAnnotations(annotations)
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

type dashboardAdapter struct {
	KubeClient client.Client
	Teams      TeamResolver
}

func (dashboardAdapter) NewObject() *v1alpha1.Dashboard { return &v1alpha1.Dashboard{} }

func (dashboardAdapter) Finalizer() string { return dashboardFinalizer }

func (dashboardAdapter) Status(obj *v1alpha1.Dashboard) *v1alpha1.SyncStatus {
	return &obj.Status.SyncStatus
}

// InSync also compares the folder ID resolved from folderRef so a recreated
// referenced folder is pushed even though the generation did not change.
func (a dashboardAdapter) InSync(ctx context.Context, obj *v1alpha1.Dashboard) bool {
	if obj.Status.ObservedGeneration != obj.Generation {
		return false
	}
	_, folderFromRef, err := a.resolvedSpec(ctx, obj)
	return err == nil && folderFromRef == obj.Status.FolderID
}

func (dashboardAdapter) Policy(obj *v1alpha1.Dashboard) v1alpha1.DriftPolicy {
	return obj.Spec.DriftPolicy
}

func (dashboardAdapter) MarkSynced(latest, synced *v1alpha1.Dashboard) {
	latest.Status.ObservedGeneration = synced.Generation
	latest.Status.FolderID = synced.Status.FolderID
}

// Payload also records the folder ID resolved from folderRef on obj so
// MarkSynced can persist what was pushed.
func (a dashboardAdapter) Payload(ctx context.Context, obj *v1alpha1.Dashboard) ([]byte, error) {
	spec, folderFromRef, err := a.resolvedSpec(ctx, obj)
	if err != nil {
		return nil, err
	}
	if spec.Tags, err = withOwnerTag(obj, spec.Tags); err != nil {
		return nil, err
	}
	if spec.Owner, err = resolveOwner(ctx, a.Teams, &obj.Status.SyncStatus, obj.Generation, spec.Owner, spec.OwnerName); err != nil {
		return nil, err
	}
	payload, err := invalidSpec(tsugamapper.DashboardSpecToPayload(spec))
	if err != nil {
		return nil, err
	}
	obj.Status.FolderID = folderFromRef
	return payload, nil
}

func (a dashboardAdapter) resolvedSpec(ctx context.Context, obj *v1alpha1.Dashboard) (v1alpha1.DashboardSpec, string, error) {
	spec := obj.Spec
	var folderFromRef string
	var err error
	spec.FolderID, folderFromRef, err = resolveRef(ctx, a.KubeClient, obj, "folderRef",
		spec.FolderID, spec.FolderRef, &v1alpha1.DashboardFolder{},
		func(f *v1alpha1.DashboardFolder) string { return f.Status.ID })
	return spec, folderFromRef, err
}

type monitorAdapter struct {
	KubeClient client.Client
	Teams      TeamResolver
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
	spec, folderFromRef, err := a.resolvedSpec(ctx, obj)
	if err != nil {
		// An unresolvable ref (Dashboard or folder deleted or not synced yet)
		// goes through Payload so its error reaches status instead of
		// reporting Ready.
		return false
	}
	return ptr.Deref(spec.DashboardID, "") == obj.Status.DashboardID && folderFromRef == obj.Status.FolderID
}

func (a *monitorAdapter) Policy(obj *v1alpha1.Monitor) v1alpha1.DriftPolicy {
	return obj.Spec.DriftPolicy
}

func (a *monitorAdapter) MarkSynced(latest, synced *v1alpha1.Monitor) {
	latest.Status.ObservedGeneration = synced.Generation
	latest.Status.DashboardID = synced.Status.DashboardID
	latest.Status.FolderID = synced.Status.FolderID
}

// Payload also records the resolved dashboard ID on obj so MarkSynced can
// persist what was pushed.
func (a *monitorAdapter) Payload(ctx context.Context, obj *v1alpha1.Monitor) ([]byte, error) {
	spec, folderFromRef, err := a.resolvedSpec(ctx, obj)
	if err != nil {
		return nil, err
	}

	if spec.Tags, err = withOwnerTag(obj, spec.Tags); err != nil {
		return nil, err
	}
	if spec.Owner, err = resolveOwner(ctx, a.Teams, &obj.Status.SyncStatus, obj.Generation, spec.Owner, spec.OwnerName); err != nil {
		return nil, err
	}
	payload, err := invalidSpec(tsugamapper.MonitorSpecToPayload(spec))
	if err != nil {
		return nil, err
	}

	obj.Status.DashboardID = ptr.Deref(spec.DashboardID, "")
	obj.Status.FolderID = folderFromRef
	return payload, nil
}

// resolvedSpec also returns the folder ID resolved from folderRef ("" when
// folderId or neither is set).
func (a *monitorAdapter) resolvedSpec(ctx context.Context, obj *v1alpha1.Monitor) (v1alpha1.MonitorSpec, string, error) {
	spec, err := a.resolvedDashboard(ctx, obj)
	if err != nil {
		return v1alpha1.MonitorSpec{}, "", err
	}
	var folderFromRef string
	spec.FolderID, folderFromRef, err = resolveRef(ctx, a.KubeClient, obj, "folderRef",
		spec.FolderID, spec.FolderRef, &v1alpha1.MonitorFolder{},
		func(f *v1alpha1.MonitorFolder) string { return f.Status.ID })
	if err != nil {
		return v1alpha1.MonitorSpec{}, "", err
	}
	return spec, folderFromRef, nil
}

func (a *monitorAdapter) resolvedDashboard(ctx context.Context, obj *v1alpha1.Monitor) (v1alpha1.MonitorSpec, error) {
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

type sloAdapter struct {
	teams TeamResolver
}

func (sloAdapter) NewObject() *v1alpha1.SLO { return &v1alpha1.SLO{} }

func (sloAdapter) Finalizer() string { return sloFinalizer }

func (sloAdapter) Status(obj *v1alpha1.SLO) *v1alpha1.SyncStatus { return &obj.Status }

func (sloAdapter) InSync(_ context.Context, obj *v1alpha1.SLO) bool {
	return obj.Status.ObservedGeneration == obj.Generation
}

func (sloAdapter) Policy(obj *v1alpha1.SLO) v1alpha1.DriftPolicy {
	return obj.Spec.DriftPolicy
}

func (sloAdapter) MarkSynced(latest, synced *v1alpha1.SLO) {
	latest.Status.ObservedGeneration = synced.Generation
}

func (a sloAdapter) Payload(ctx context.Context, obj *v1alpha1.SLO) ([]byte, error) {
	spec := obj.Spec
	tags, err := withOwnerTag(obj, spec.Tags)
	if err != nil {
		return nil, err
	}
	spec.Tags = tags
	if spec.Owner, err = resolveOwner(ctx, a.teams, &obj.Status, obj.Generation, spec.Owner, spec.OwnerName); err != nil {
		return nil, err
	}
	return invalidSpec(tsugamapper.SLOSpecToPayload(spec))
}

// errInvalidSpec marks a spec the mapper rejects: retrying cannot fix it,
// only a spec change can.
var errInvalidSpec = errors.New("invalid spec")

// resolveRef returns the Tsuga ID to push for a field that takes either a raw
// ID or a same-namespace reference, and the ID resolved from the reference
// ("" when the raw ID or neither is set). The raw ID takes precedence.
func resolveRef[T client.Object](ctx context.Context, reader client.Reader, obj client.Object, field string,
	rawID *string, ref *v1alpha1.LocalObjectReference, target T, syncedID func(T) string,
) (pushID *string, fromRef string, err error) {
	if rawID != nil {
		if ref != nil {
			logf.FromContext(ctx).Info("both an ID and "+field+" are set; the ID takes precedence and "+field+" is ignored",
				"name", obj.GetName(), "namespace", obj.GetNamespace())
		}
		return rawID, "", nil
	}
	if ref == nil {
		return nil, "", nil
	}
	if err := reader.Get(ctx, client.ObjectKey{Namespace: obj.GetNamespace(), Name: ref.Name}, target); err != nil {
		return nil, "", fmt.Errorf("resolve %s %q: %w", field, ref.Name, err)
	}
	id := syncedID(target)
	if id == "" {
		return nil, "", fmt.Errorf("%s %q has not been synced to Tsuga yet", field, ref.Name)
	}
	return &id, id, nil
}

func invalidSpec(payload []byte, err error) ([]byte, error) {
	if err != nil {
		return nil, fmt.Errorf("%w: %w", errInvalidSpec, err)
	}
	return payload, nil
}
