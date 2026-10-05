package controller

import (
	"context"
	"fmt"
	"slices"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/tsuga-dev/tsuga-operator/api/v1alpha1"
	"github.com/tsuga-dev/tsuga-operator/internal/collection"
)

// setupJobRecreateDelay gives a deleted setup Job time to leave the API
// server before a Job with the same name is created again.
const setupJobRecreateDelay = 2 * time.Second

// TsugaPostgresMonitoringReconciler reconciles TsugaPostgresMonitoring into a
// deployment-mode OpenTelemetryCollector plus, in managed provisioning mode,
// the setup Job that creates otel_monitor and the otel functions. It only
// reads Secret metadata to verify ownership; pods get every
// credential through secretKeyRef.
type TsugaPostgresMonitoringReconciler struct {
	client.Client
	APIReader client.Reader
	Scheme    *runtime.Scheme
}

//+kubebuilder:rbac:groups=observability.tsuga.com,resources=tsugapostgresmonitorings,verbs=get;list;watch
//+kubebuilder:rbac:groups=observability.tsuga.com,resources=tsugapostgresmonitorings/status,verbs=update
//+kubebuilder:rbac:groups=observability.tsuga.com,resources=tsugapostgresmonitorings/finalizers,verbs=update
//+kubebuilder:rbac:groups=batch,resources=jobs,verbs=get;list;watch;create;delete
//+kubebuilder:rbac:groups="",resources=secrets,verbs=create;get
//+kubebuilder:rbac:groups="",resources=configmaps,verbs=create;delete;get;patch

func (r *TsugaPostgresMonitoringReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	pg := &v1alpha1.TsugaPostgresMonitoring{}
	if err := r.Get(ctx, req.NamespacedName, pg); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	cluster := &v1alpha1.TsugaCollectorConfig{}
	if err := r.Get(ctx, types.NamespacedName{Name: clusterConfigName}, cluster); err != nil {
		return r.fail(ctx, pg, reasonClusterConfigMissing, err)
	}
	clusterCfg := collection.ResolveCollector(cluster.Spec)
	spec := collection.ResolvePostgres(pg.Spec)

	// ponytail: Create on every reconcile; on AlreadyExists, createIfAbsent
	// checks ownership with a metadata-only Get. RBAC cannot scope get to
	// metadata, so the manager holds cluster-wide Secret get for this.
	secret := collection.RenderPostgresMonitorSecret(pg.Name, pg.Namespace)
	secretCreated, err := r.createIfAbsent(ctx, pg, secret)
	if err != nil {
		return r.fail(ctx, pg, reasonApplyFailed, err)
	}

	var job *batchv1.Job
	if spec.Provisioning.Mode != collection.PostgresModeManual {
		job, err = collection.RenderPostgresSetupJob(pg.Name, pg.Namespace, spec)
		if err != nil {
			return r.fail(ctx, pg, reasonRenderFailed, err)
		}
		if err := r.checkAdminSecret(ctx, pg.Namespace, spec.Provisioning.AdminSecretRef.Name); err != nil {
			return r.fail(ctx, pg, reasonProvisioningFailed, err)
		}
		// A new monitor Secret means a new password. A Job that already ran set
		// the old one, so it is deleted and recreated to run ALTER USER again.
		//
		// ponytail: a crash or a failed Delete between the Secret Create and
		// this Delete still drops the re-run; recovery is deleting the
		// Secret again. A durable rotation marker closes it if that ever
		// bites.
		if secretCreated {
			deleted, err := r.deleteJob(ctx, pg, job)
			if err != nil {
				return r.fail(ctx, pg, reasonApplyFailed, err)
			}
			if deleted {
				return ctrl.Result{RequeueAfter: setupJobRecreateDelay}, nil
			}
		}
	}

	collector, err := collection.RenderPostgresCollector(pg.Name, pg.Namespace, spec,
		clusterCfg.Image, agentOTLPGRPCEndpoint(clusterCfg.CollectorNamespace))
	if err != nil {
		return r.fail(ctx, pg, reasonRenderFailed, err)
	}
	if err := r.apply(ctx, pg, collector); err != nil {
		return r.fail(ctx, pg, reasonApplyFailed, err)
	}
	managed := make([]string, 0, 4)
	managed = append(managed, secret.Name, collector.GetName())

	if spec.Provisioning.Mode == collection.PostgresModeManual {
		if err := r.deleteStaleJobs(ctx, pg, ""); err != nil {
			return r.fail(ctx, pg, reasonApplyFailed, err)
		}
		if err := r.deleteOwnedSetupConfigMap(ctx, pg); err != nil {
			return r.fail(ctx, pg, reasonApplyFailed, err)
		}
		return r.syncStatus(ctx, pg, managed, true, reasonReconciled, fmt.Sprintf(
			"provisioning is manual: create otel_monitor with the password in secret %s", secret.Name))
	}

	cm := collection.RenderPostgresSetupConfigMap(pg.Name, pg.Namespace)
	if err := r.apply(ctx, pg, cm); err != nil {
		return r.fail(ctx, pg, reasonApplyFailed, err)
	}
	managed = append(managed, cm.Name, job.Name)

	created, err := r.createIfAbsent(ctx, pg, job)
	if err != nil {
		return r.fail(ctx, pg, reasonApplyFailed, err)
	}
	if !created {
		if err := r.Get(ctx, client.ObjectKeyFromObject(job), job); err != nil {
			return r.fail(ctx, pg, reasonApplyFailed, err)
		}
	}
	if err := r.deleteStaleJobs(ctx, pg, job.Name); err != nil {
		return r.fail(ctx, pg, reasonApplyFailed, err)
	}

	ready, reason, message := setupJobOutcome(job)
	return r.syncStatus(ctx, pg, managed, ready, reason, message)
}

// agentOTLPGRPCEndpoint is the OTLP/gRPC address of the tsuga-agent Service,
// which the Postgres collector forwards to.
func agentOTLPGRPCEndpoint(collectorNamespace string) string {
	return fmt.Sprintf("%s.%s.svc.cluster.local:4317", agentCollectorServiceName, collectorNamespace)
}

// setupJobOutcome maps the setup Job's terminal condition onto the Ready
// condition. A Job with no terminal condition yet is pending.
func setupJobOutcome(job *batchv1.Job) (bool, string, string) {
	for _, c := range job.Status.Conditions {
		if c.Status != corev1.ConditionTrue {
			continue
		}
		switch c.Type {
		case batchv1.JobComplete:
			return true, reasonReconciled, fmt.Sprintf("setup job %s completed", job.Name)
		case batchv1.JobFailed:
			return false, reasonProvisioningFailed, fmt.Sprintf(
				"setup job %s failed: see kubectl logs -n %s job/%s", job.Name, job.Namespace, job.Name)
		}
	}
	return false, reasonProvisioningPending, fmt.Sprintf("setup job %s has not completed yet", job.Name)
}

// apply reads through APIReader because ConfigMaps are not watched.
func (r *TsugaPostgresMonitoringReconciler) apply(ctx context.Context, pg *v1alpha1.TsugaPostgresMonitoring, obj client.Object) error {
	reader := client.Reader(r.Client)
	if r.APIReader != nil {
		reader = r.APIReader
	}
	return applyControlled(ctx, r.Client, reader, r.Scheme, pg, obj)
}

// createIfAbsent creates obj owned by pg and reports whether it was created.
// An object that already exists is left untouched.
func (r *TsugaPostgresMonitoringReconciler) createIfAbsent(ctx context.Context, pg *v1alpha1.TsugaPostgresMonitoring, obj client.Object) (bool, error) {
	if err := controllerutil.SetControllerReference(pg, obj, r.Scheme); err != nil {
		return false, err
	}
	err := r.Create(ctx, obj)
	if apierrors.IsAlreadyExists(err) {
		reader := client.Reader(r.Client)
		if r.APIReader != nil {
			reader = r.APIReader
		}
		existing := obj.DeepCopyObject().(client.Object)
		if _, secret := obj.(*corev1.Secret); secret && r.APIReader != nil {
			metadata := &metav1.PartialObjectMetadata{}
			metadata.SetGroupVersionKind(corev1.SchemeGroupVersion.WithKind("Secret"))
			existing = metadata
		}
		if getErr := reader.Get(ctx, client.ObjectKeyFromObject(obj), existing); getErr != nil {
			return false, fmt.Errorf("checking existing %T %s/%s: %w", obj, obj.GetNamespace(), obj.GetName(), getErr)
		}
		if !metav1.IsControlledBy(existing, pg) {
			return false, fmt.Errorf("%T %s/%s already exists without a controller reference to TsugaPostgresMonitoring %s", obj, obj.GetNamespace(), obj.GetName(), pg.Name)
		}
		return false, nil
	}
	return err == nil, err
}

// checkAdminSecret requires the admin Secret to opt in with
// PostgresAdminSecretLabel. Only metadata is read. Secrets are not watched, so
// adding the label is picked up on the error backoff requeue.
func (r *TsugaPostgresMonitoringReconciler) checkAdminSecret(ctx context.Context, namespace, name string) error {
	reader := client.Reader(r.Client)
	if r.APIReader != nil {
		reader = r.APIReader
	}
	metadata := &metav1.PartialObjectMetadata{}
	metadata.SetGroupVersionKind(corev1.SchemeGroupVersion.WithKind("Secret"))
	if err := reader.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, metadata); err != nil {
		return fmt.Errorf("admin secret %s/%s: %w", namespace, name, err)
	}
	if metadata.Labels[collection.PostgresAdminSecretLabel] != "true" {
		return fmt.Errorf("admin secret %s/%s must be labeled %s=true to be used as adminSecretRef",
			namespace, name, collection.PostgresAdminSecretLabel)
	}
	return nil
}

// deleteJob deletes a Job and its pods, reporting whether it existed.
func (r *TsugaPostgresMonitoringReconciler) deleteJob(ctx context.Context, pg *v1alpha1.TsugaPostgresMonitoring, job *batchv1.Job) (bool, error) {
	existing := &batchv1.Job{}
	if err := r.Get(ctx, client.ObjectKeyFromObject(job), existing); apierrors.IsNotFound(err) {
		return false, nil
	} else if err != nil {
		return false, err
	}
	if !metav1.IsControlledBy(existing, pg) {
		return false, fmt.Errorf("job %s/%s already exists without a controller reference to TsugaPostgresMonitoring %s", job.Namespace, job.Name, pg.Name)
	}
	err := r.Delete(ctx, existing, client.PropagationPolicy(metav1.DeletePropagationBackground))
	if apierrors.IsNotFound(err) {
		return false, nil
	}
	return err == nil, err
}

// deleteStaleJobs removes setup Jobs this CR controls whose name differs
// from current, i.e. Jobs rendered from other inputs.
func (r *TsugaPostgresMonitoringReconciler) deleteStaleJobs(ctx context.Context, pg *v1alpha1.TsugaPostgresMonitoring, current string) error {
	jobs := &batchv1.JobList{}
	if err := r.List(ctx, jobs, client.InNamespace(pg.Namespace),
		client.MatchingLabels{collection.PostgresMonitoringLabel: pg.Name}); err != nil {
		return err
	}
	for i := range jobs.Items {
		j := &jobs.Items[i]
		if j.Name == current || !metav1.IsControlledBy(j, pg) {
			continue
		}
		if _, err := r.deleteJob(ctx, pg, j); err != nil {
			return err
		}
	}
	return nil
}

func (r *TsugaPostgresMonitoringReconciler) deleteOwnedSetupConfigMap(ctx context.Context, pg *v1alpha1.TsugaPostgresMonitoring) error {
	cm := collection.RenderPostgresSetupConfigMap(pg.Name, pg.Namespace)
	reader := client.Reader(r.Client)
	if r.APIReader != nil {
		reader = r.APIReader
	}
	if err := reader.Get(ctx, client.ObjectKeyFromObject(cm), cm); err != nil {
		return client.IgnoreNotFound(err)
	}
	if !metav1.IsControlledBy(cm, pg) {
		return nil
	}
	return client.IgnoreNotFound(r.Delete(ctx, cm))
}

// syncStatus writes the status only when it differs on a meaningful field,
// so a steady-state reconcile does not retrigger the For() watch.
func (r *TsugaPostgresMonitoringReconciler) syncStatus(ctx context.Context, pg *v1alpha1.TsugaPostgresMonitoring, managed []string, ready bool, reason, message string) (ctrl.Result, error) {
	phase := phaseReady
	switch {
	case reason == reasonProvisioningPending:
		phase = phasePending
	case !ready:
		phase = phaseError
	}
	desired := v1alpha1.CollectionStatus{
		Phase:              phase,
		Message:            message,
		ObservedGeneration: pg.Generation,
		ManagedResources:   managed,
		Conditions:         slices.Clone(pg.Status.Conditions),
	}
	setReady(&desired, pg.Generation, ready, reason, message)
	if collectionStatusEqual(pg.Status, desired) {
		return ctrl.Result{}, nil
	}
	now := metav1.Now()
	desired.LastSyncedAt = &now
	pg.Status = desired
	return ctrl.Result{}, r.Status().Update(ctx, pg)
}

func (r *TsugaPostgresMonitoringReconciler) fail(ctx context.Context, pg *v1alpha1.TsugaPostgresMonitoring, reason string, cause error) (ctrl.Result, error) {
	pg.Status.Phase = phaseError
	pg.Status.Message = cause.Error()
	setReady(&pg.Status, pg.Generation, false, reason, cause.Error())
	if err := r.Status().Update(ctx, pg); err != nil {
		logf.FromContext(ctx).Error(err, "failed to record Error status", "reason", reason)
	}
	return ctrl.Result{}, cause
}

// SetupWithManager watches the owned collector and setup Jobs. Secrets and
// ConfigMaps are not watched: owning Secrets would cache every Secret in the
// cluster, and both are recreated on the CR's next reconcile anyway.
func (r *TsugaPostgresMonitoringReconciler) SetupWithManager(mgr ctrl.Manager) error {
	collector := &unstructured.Unstructured{}
	collector.SetGroupVersionKind(collectorGVK)
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.TsugaPostgresMonitoring{}).
		Owns(collector).
		Owns(&batchv1.Job{}).
		Watches(&v1alpha1.TsugaCollectorConfig{}, handler.EnqueueRequestsFromMapFunc(r.clusterConfigToPostgresMonitorings)).
		Named("tsugapostgresmonitoring").
		Complete(r)
}

func (r *TsugaPostgresMonitoringReconciler) clusterConfigToPostgresMonitorings(ctx context.Context, obj client.Object) []reconcile.Request {
	if obj.GetName() != clusterConfigName {
		return nil
	}
	var list v1alpha1.TsugaPostgresMonitoringList
	if err := r.List(ctx, &list); err != nil {
		logf.FromContext(ctx).Error(err, "clusterConfigToPostgresMonitorings: failed to list TsugaPostgresMonitorings")
		return nil
	}
	reqs := make([]reconcile.Request, 0, len(list.Items))
	for i := range list.Items {
		reqs = append(reqs, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&list.Items[i])})
	}
	return reqs
}
