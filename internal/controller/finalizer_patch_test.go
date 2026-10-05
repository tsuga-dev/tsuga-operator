package controller

import (
	"context"
	"encoding/json"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// The finalizer write must leave spec untouched: a full typed write drops
// user-set empty lists and false booleans through omitempty, which bumps
// generation and shows up as drift in kubectl diff and GitOps tools.
// Objects are created unstructured, as kubectl does, so the empty values
// actually reach the API server.
var _ = Describe("Finalizer write", func() {
	ctx := context.Background()

	createRaw := func(manifest string) *unstructured.Unstructured {
		obj := &unstructured.Unstructured{}
		Expect(json.Unmarshal([]byte(manifest), &obj.Object)).To(Succeed())
		obj.SetNamespace(testNamespace)
		Expect(k8sClient.Create(ctx, obj)).To(Succeed())
		DeferCleanup(func() {
			latest := &unstructured.Unstructured{}
			latest.SetGroupVersionKind(obj.GroupVersionKind())
			if k8sClient.Get(ctx, types.NamespacedName{Namespace: obj.GetNamespace(), Name: obj.GetName()}, latest) != nil {
				return
			}
			latest.SetFinalizers(nil)
			_ = k8sClient.Update(ctx, latest)
			_ = k8sClient.Delete(ctx, latest)
		})
		return obj
	}

	// reconcileAndGet returns the reconcile error for the caller to check:
	// steps after the finalizer write may fail in envtest for some kinds.
	reconcileAndGet := func(obj *unstructured.Unstructured, reconcileFn func(context.Context, ctrl.Request) (ctrl.Result, error)) (*unstructured.Unstructured, error) {
		key := types.NamespacedName{Namespace: obj.GetNamespace(), Name: obj.GetName()}
		_, reconcileErr := reconcileFn(ctx, reconcile.Request{NamespacedName: key})
		got := &unstructured.Unstructured{}
		got.SetGroupVersionKind(obj.GroupVersionKind())
		Expect(k8sClient.Get(ctx, key, got)).To(Succeed())
		Expect(got.GetFinalizers()).NotTo(BeEmpty())
		Expect(got.GetGeneration()).To(Equal(int64(1)))
		return got, reconcileErr
	}

	It("keeps empty Dashboard filters and tags", func() {
		obj := createRaw(`{
			"apiVersion": "observability.tsuga.com/v1alpha1", "kind": "Dashboard",
			"metadata": {"name": "finalizer-patch-dashboard"},
			"spec": {"name": "d", "owner": "` + testOwner + `", "filters": [], "tags": [],
				"graphs": [{"id": "` + testGraphID + `", "visualization": {"type": "note", "note": "hi"}}]}
		}`)
		r := &TsugaDashboardReconciler{Client: k8sClient, TsugaClient: &noopDashboardClient{}}

		got, err := reconcileAndGet(obj, r.Reconcile)
		Expect(err).NotTo(HaveOccurred())
		Expect(got.Object["spec"]).To(HaveKey("filters"))
		Expect(got.Object["spec"]).To(HaveKey("tags"))
	})

	It("keeps empty SLO alerts", func() {
		obj := createRaw(`{
			"apiVersion": "observability.tsuga.com/v1alpha1", "kind": "SLO",
			"metadata": {"name": "finalizer-patch-slo"},
			"spec": {"name": "s", "owner": "` + testOwner + `", "permissions": "all",
				"target": 99.9, "timeframeDays": 30, "configuration": {"type": "event"}, "alerts": []}
		}`)
		r := &TsugaSLOReconciler{Client: k8sClient, TsugaClient: &noopDashboardClient{}}

		got, err := reconcileAndGet(obj, r.Reconcile)
		Expect(err).NotTo(HaveOccurred())
		Expect(got.Object["spec"]).To(HaveKey("alerts"))
	})

	It("keeps TsugaMonitoring injectExistingWorkloads false and empty languages", func() {
		obj := createRaw(`{
			"apiVersion": "observability.tsuga.com/v1alpha1", "kind": "TsugaMonitoring",
			"metadata": {"name": "finalizer-patch-monitoring"},
			"spec": {"injectExistingWorkloads": false, "instrumentation": {"enabled": false, "languages": []}}
		}`)
		r := &TsugaMonitoringReconciler{Client: k8sClient, Scheme: scheme.Scheme}

		// The Instrumentation CRD is absent in envtest, so the apply fails.
		got, _ := reconcileAndGet(obj, r.Reconcile)
		inject, found, err := unstructured.NestedBool(got.Object, "spec", "injectExistingWorkloads")
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(inject).To(BeFalse())
		Expect(got.Object["spec"].(map[string]any)["instrumentation"]).To(HaveKey("languages"))
	})
})
