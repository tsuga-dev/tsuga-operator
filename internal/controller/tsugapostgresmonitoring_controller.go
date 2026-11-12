package controller

import (
	"context"
	"fmt"
	"slices"
	"strings"
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

// setupJobRecreateDelay gives a deleted setup Job or collector time to leave
// the API server before one with the same name is created again.
const setupJobRecreateDelay = 2 * time.Second

// TsugaPostgresMonitoringReconciler reconciles TsugaPostgresMonitoring into a
// deployment-mode OpenTelemetryCollector plus, in managed provisioning mode,
// the setup Job that creates the monitoring role and the otel functions. Of
// CRs that would set different passwords for one role on one server, only the
// oldest is reconciled. It only reads Secret metadata, to verify ownership and
// opt-in labels; pods get every credential through secretKeyRef.
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
	if !collection.AgentEnabled(clusterCfg) {
		return r.retireCollector(ctx, pg, reasonAgentDisabled,
			"the cluster config disables the node agent, which database monitoring exports to")
	}
	port := collection.AgentOTLPPort(clusterCfg, "grpc")
	spec := collection.ResolvePostgres(pg.Spec)

	var all v1alpha1.TsugaPostgresMonitoringList
	if err := r.List(ctx, &all); err != nil {
		return r.fail(ctx, pg, reasonApplyFailed, err)
	}
	if winner := conflictWinner(pg, all.Items); winner != nil {
		return r.yieldToWinner(ctx, pg, spec, winner)
	}

	passwordSecret := collection.PostgresPasswordSecretName(spec, pg.Name)
	if err := r.checkCollectorSecrets(ctx, pg.Namespace, spec); err != nil {
		return r.fail(ctx, pg, reasonProvisioningFailed, err)
	}
	secretCreated := false
	if spec.PasswordSecretRef == nil {
		// ponytail: Create on every reconcile; on AlreadyExists, createIfAbsent
		// checks ownership with a metadata-only Get. RBAC cannot scope get to
		// metadata, so the manager holds cluster-wide Secret get for this.
		var err error
		secretCreated, err = r.createIfAbsent(ctx, pg, collection.RenderPostgresMonitorSecret(pg.Name, pg.Namespace))
		if err != nil {
			return r.fail(ctx, pg, reasonApplyFailed, err)
		}
	}

	var job *batchv1.Job
	if spec.Provisioning.Mode != collection.PostgresModeManual {
		var err error
		job, err = collection.RenderPostgresSetupJob(pg.Name, pg.Namespace, spec)
		if err != nil {
			return r.fail(ctx, pg, reasonRenderFailed, err)
		}
		if err := r.checkSecretLabel(ctx, pg.Namespace, spec.Provisioning.AdminSecretRef.Name,
			collection.PostgresAdminSecretLabel, "admin", "adminSecretRef"); err != nil {
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
		clusterCfg.Image, agentOTLPGRPCEndpoint(clusterCfg.CollectorNamespace, port))
	if err != nil {
		return r.fail(ctx, pg, reasonRenderFailed, err)
	}
	if deleted, err := deleteOnModeChange(ctx, r.Client, pg, collector); err != nil {
		return r.fail(ctx, pg, reasonApplyFailed, err)
	} else if deleted {
		return ctrl.Result{RequeueAfter: setupJobRecreateDelay}, nil
	}
	if err := r.apply(ctx, pg, collector); err != nil {
		return r.fail(ctx, pg, reasonApplyFailed, err)
	}
	managed := make([]string, 0, 4)
	if spec.PasswordSecretRef == nil {
		managed = append(managed, passwordSecret)
	}
	managed = append(managed, collector.GetName())
	var sidecarHint string
	if spec.Mode == collection.PostgresCollectorSidecar {
		sidecarHint = fmt.Sprintf("; add the pod annotation sidecar.opentelemetry.io/inject: %s to the database pod template", collector.GetName())
	}

	if spec.Provisioning.Mode == collection.PostgresModeManual {
		if err := r.deleteStaleJobs(ctx, pg, ""); err != nil {
			return r.fail(ctx, pg, reasonApplyFailed, err)
		}
		if err := r.deleteOwnedSetupConfigMap(ctx, pg); err != nil {
			return r.fail(ctx, pg, reasonApplyFailed, err)
		}
		return r.syncStatus(ctx, pg, managed, true, reasonReconciled, fmt.Sprintf(
			"provisioning is manual: create role %s with the password in secret %s%s", spec.Role, passwordSecret, sidecarHint))
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

	ready, reason, message := setupJobOutcome(job, spec.Role, passwordSecret)
	return r.syncStatus(ctx, pg, managed, ready, reason, message+sidecarHint)
}

// agentOTLPGRPCEndpoint is the OTLP/gRPC address of the tsuga-agent Service,
// which the Postgres collector forwards to.
func agentOTLPGRPCEndpoint(collectorNamespace string, port int32) string {
	return fmt.Sprintf("%s.%s.svc.cluster.local:%d", agentCollectorServiceName, collectorNamespace, port)
}

// retireCollector deletes the owned collector, since it has no agent to
// forward to, and reports Ready=False. The Secret and setup Job are left
// alone: they are inert without the collector.
func (r *TsugaPostgresMonitoringReconciler) retireCollector(ctx context.Context, pg *v1alpha1.TsugaPostgresMonitoring, reason, message string) (ctrl.Result, error) {
	if err := r.deleteOwnedCollector(ctx, pg); err != nil {
		return r.fail(ctx, pg, reasonApplyFailed, err)
	}
	return r.syncStatus(ctx, pg, nil, false, reason, message)
}

// yieldToWinner stops pg from touching a role another CR owns: no collector
// and no setup Job, so pg never overwrites the winner's password. The Secret
// is kept, so pg resumes cleanly once the conflict is gone.
func (r *TsugaPostgresMonitoringReconciler) yieldToWinner(ctx context.Context, pg *v1alpha1.TsugaPostgresMonitoring, spec v1alpha1.TsugaPostgresMonitoringSpec, winner *v1alpha1.TsugaPostgresMonitoring) (ctrl.Result, error) {
	if err := r.deleteOwnedCollector(ctx, pg); err != nil {
		return r.fail(ctx, pg, reasonApplyFailed, err)
	}
	if err := r.deleteStaleJobs(ctx, pg, ""); err != nil {
		return r.fail(ctx, pg, reasonApplyFailed, err)
	}
	return r.syncStatus(ctx, pg, nil, false, reasonRoleConflict, fmt.Sprintf(
		"role %s on %s:%d is already used by TsugaPostgresMonitoring %s/%s; set a distinct spec.role or share spec.passwordSecretRef",
		spec.Role, spec.Host, spec.Port, winner.Namespace, winner.Name))
}

func (r *TsugaPostgresMonitoringReconciler) deleteOwnedCollector(ctx context.Context, pg *v1alpha1.TsugaPostgresMonitoring) error {
	collector := &unstructured.Unstructured{}
	collector.SetGroupVersionKind(collectorGVK)
	key := types.NamespacedName{Name: pg.Name + "-pg", Namespace: pg.Namespace}
	if err := r.Get(ctx, key, collector); err != nil {
		return client.IgnoreNotFound(err)
	}
	if !metav1.IsControlledBy(collector, pg) {
		return nil
	}
	return client.IgnoreNotFound(r.Delete(ctx, collector))
}

// postgresServerKey identifies the role pg manages. A single-label host is a
// Service in pg's namespace, so it is qualified with it, and the .svc and
// .svc.cluster.local suffixes are dropped. Other DNS names for one server are
// not matched.
func postgresServerKey(pg *v1alpha1.TsugaPostgresMonitoring) string {
	spec := collection.ResolvePostgres(pg.Spec)
	host := strings.ToLower(spec.Host)
	if !strings.Contains(host, ".") {
		host += "." + pg.Namespace
	}
	host = strings.TrimSuffix(strings.TrimSuffix(host, ".svc.cluster.local"), ".svc")
	return fmt.Sprintf("%s:%d/%s", host, spec.Port, spec.Role)
}

// conflictWinner returns the CR that owns self's role on self's server, or
// nil when self does. CRs in one namespace naming the same passwordSecretRef
// set the same password, so they do not conflict. The oldest CR wins, ties
// broken by namespace/name.
func conflictWinner(self *v1alpha1.TsugaPostgresMonitoring, all []v1alpha1.TsugaPostgresMonitoring) *v1alpha1.TsugaPostgresMonitoring {
	key := postgresServerKey(self)
	var winner *v1alpha1.TsugaPostgresMonitoring
	for i := range all {
		other := &all[i]
		if other.DeletionTimestamp != nil ||
			(other.Namespace == self.Namespace && other.Name == self.Name) ||
			sharePasswordSecret(self, other) ||
			postgresServerKey(other) != key {
			continue
		}
		if winner == nil || olderPostgresMonitoring(other, winner) {
			winner = other
		}
	}
	if winner == nil || olderPostgresMonitoring(self, winner) {
		return nil
	}
	return winner
}

func sharePasswordSecret(a, b *v1alpha1.TsugaPostgresMonitoring) bool {
	return a.Namespace == b.Namespace && a.Spec.PasswordSecretRef != nil && b.Spec.PasswordSecretRef != nil &&
		a.Spec.PasswordSecretRef.Name == b.Spec.PasswordSecretRef.Name
}

func olderPostgresMonitoring(a, b *v1alpha1.TsugaPostgresMonitoring) bool {
	if !a.CreationTimestamp.Equal(&b.CreationTimestamp) {
		return a.CreationTimestamp.Before(&b.CreationTimestamp)
	}
	return a.Namespace+"/"+a.Name < b.Namespace+"/"+b.Name
}

// setupJobOutcome maps the setup Job's terminal condition onto the Ready
// condition. A Job with no terminal condition yet is pending.
func setupJobOutcome(job *batchv1.Job, role, passwordSecret string) (bool, string, string) {
	for _, c := range job.Status.Conditions {
		if c.Status != corev1.ConditionTrue {
			continue
		}
		switch c.Type {
		case batchv1.JobComplete:
			return true, reasonReconciled, fmt.Sprintf("setup job %s completed", job.Name)
		case batchv1.JobFailed:
			return false, reasonProvisioningFailed, fmt.Sprintf(
				"setup job %s failed: see kubectl logs -n %s job/%s (if role %s already exists and was not created by the operator, the password in secret %s must match it)",
				job.Name, job.Namespace, job.Name, role, passwordSecret)
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

// checkCollectorSecrets requires the credentials opt-in label on every Secret
// spec hands the collector: passwordSecretRef and the TLS volume's Secrets.
func (r *TsugaPostgresMonitoringReconciler) checkCollectorSecrets(ctx context.Context, namespace string, spec v1alpha1.TsugaPostgresMonitoringSpec) error {
	if spec.PasswordSecretRef != nil {
		if err := r.checkSecretLabel(ctx, namespace, spec.PasswordSecretRef.Name,
			collection.PostgresCredentialsSecretLabel, "password", "passwordSecretRef"); err != nil {
			return err
		}
	}
	for _, name := range collection.PostgresTLSSecretNames(spec) {
		if err := r.checkSecretLabel(ctx, namespace, name,
			collection.PostgresCredentialsSecretLabel, "tls", "tls.volume"); err != nil {
			return err
		}
	}
	return nil
}

// checkSecretLabel requires a Secret named in field to opt in with label=true;
// noun names the Secret in errors.
// Only metadata is read. Secrets are not watched, so adding the label is
// picked up on the error backoff requeue.
func (r *TsugaPostgresMonitoringReconciler) checkSecretLabel(ctx context.Context, namespace, name, label, noun, field string) error {
	reader := client.Reader(r.Client)
	if r.APIReader != nil {
		reader = r.APIReader
	}
	metadata := &metav1.PartialObjectMetadata{}
	metadata.SetGroupVersionKind(corev1.SchemeGroupVersion.WithKind("Secret"))
	if err := reader.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, metadata); err != nil {
		return fmt.Errorf("%s secret %s/%s: %w", noun, namespace, name, err)
	}
	if metadata.Labels[label] != "true" {
		return fmt.Errorf("%s secret %s/%s must be labeled %s=true to be used as %s",
			noun, namespace, name, label, field)
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

// SetupWithManager watches the owned collector and setup Jobs, and requeues
// the CRs sharing a role on one server when any of them changes, so a loser
// takes over once the winner is deleted or moves. Secrets and
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
		Watches(&v1alpha1.TsugaPostgresMonitoring{}, handler.EnqueueRequestsFromMapFunc(r.sameServerPostgresMonitorings)).
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

// sameServerPostgresMonitorings enqueues the other CRs sharing obj's server
// key. A delete event carries the last state, and the map handler maps both
// the old and new object on update, so rivals on a host obj left are requeued.
func (r *TsugaPostgresMonitoringReconciler) sameServerPostgresMonitorings(ctx context.Context, obj client.Object) []reconcile.Request {
	pg, ok := obj.(*v1alpha1.TsugaPostgresMonitoring)
	if !ok {
		return nil
	}
	var list v1alpha1.TsugaPostgresMonitoringList
	if err := r.List(ctx, &list); err != nil {
		logf.FromContext(ctx).Error(err, "sameServerPostgresMonitorings: failed to list TsugaPostgresMonitorings")
		return nil
	}
	key := postgresServerKey(pg)
	var reqs []reconcile.Request
	for i := range list.Items {
		other := &list.Items[i]
		if (other.Namespace == pg.Namespace && other.Name == pg.Name) ||
			postgresServerKey(other) != key {
			continue
		}
		reqs = append(reqs, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(other)})
	}
	return reqs
}
