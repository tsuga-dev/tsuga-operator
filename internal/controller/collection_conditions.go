package controller

import (
	"slices"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/tsuga-dev/tsuga-operator/api/v1alpha1"
)

// conditionReady is the single condition type the collection controllers
// publish. Per-stage conditions were considered and dropped: one Ready
// condition whose Reason names the failing stage answers "is this working, and
// if not why" without a second source of truth to keep consistent.
const conditionReady = "Ready"

// Reasons attached to the Ready condition. Each failure reason names the
// reconcile stage that produced it, so `kubectl describe` distinguishes a
// missing cluster config from a rejected apply.
const (
	reasonReconciled           = "Reconciled"
	reasonInstrumentationOff   = "InstrumentationDisabled"
	reasonClusterConfigMissing = "ClusterConfigMissing"
	reasonRenderFailed         = "RenderFailed"
	reasonApplyFailed          = "ApplyFailed"
	reasonWorkloadPatchFailed  = "WorkloadPatchFailed"
	reasonInjectConflict       = "InjectAnnotationConflict"
	reasonProvisioningPending  = "ProvisioningPending"
	reasonProvisioningFailed   = "ProvisioningFailed"
)

// setReady sets the Ready condition on status. Conditions already present are
// carried through meta.SetStatusCondition, which preserves LastTransitionTime
// when the status value is unchanged - without that, every reconcile would
// produce a new timestamp and defeat the steady-state no-op guard.
func setReady(status *v1alpha1.CollectionStatus, generation int64, ready bool, reason, message string) {
	condStatus := metav1.ConditionFalse
	if ready {
		condStatus = metav1.ConditionTrue
	}
	meta.SetStatusCondition(&status.Conditions, metav1.Condition{
		Type:               conditionReady,
		Status:             condStatus,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: generation,
	})
}

// conditionsEqual compares two condition sets on everything except
// LastTransitionTime, which is a timestamp rather than a meaningful change.
func conditionsEqual(a, b []metav1.Condition) bool {
	return slices.EqualFunc(a, b, func(x, y metav1.Condition) bool {
		x.LastTransitionTime, y.LastTransitionTime = metav1.Time{}, metav1.Time{}
		return x == y
	})
}
