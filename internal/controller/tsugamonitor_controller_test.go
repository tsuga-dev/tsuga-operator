/*
Copyright 2026 Tsuga.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/config"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	tsugav1alpha1 "github.com/tsuga-dev/tsuga-operator/api/v1alpha1"
)

// noopMonitorClient is a minimal stub that prevents panics in the scaffold integration test.
type noopMonitorClient struct{}

func (n *noopMonitorClient) FindByTag(context.Context, string, string) (string, error) {
	return "", nil
}

func (n *noopMonitorClient) Create(ctx context.Context, payload []byte) (string, error) {
	return "test-id", nil
}
func (n *noopMonitorClient) Update(ctx context.Context, id string, payload []byte) error {
	return nil
}
func (n *noopMonitorClient) Delete(ctx context.Context, id string) error { return nil }

var _ = Describe("Monitor Controller", func() {
	Context("When reconciling a resource", func() {
		const resourceName = "test-resource"

		ctx := context.Background()

		typeNamespacedName := types.NamespacedName{
			Name:      resourceName,
			Namespace: testNamespace,
		}
		monitor := &tsugav1alpha1.Monitor{}

		BeforeEach(func() {
			By("creating the custom resource for the Kind Monitor")
			err := k8sClient.Get(ctx, typeNamespacedName, monitor)
			if err != nil && errors.IsNotFound(err) {
				resource := &tsugav1alpha1.Monitor{
					ObjectMeta: metav1.ObjectMeta{
						Name:      resourceName,
						Namespace: testNamespace,
					},
					Spec: tsugav1alpha1.MonitorSpec{
						Name:          "Monitor Test",
						Owner:         testOwner,
						Priority:      1,
						Permissions:   testPermsAll,
						Configuration: apiextensionsv1.JSON{Raw: []byte(`{"type":"metric","condition":{"formula":"q1","operator":"greater_than","threshold":1},"noDataBehavior":"resolve","timeframe":5,"groupByFields":[],"queries":[{"aggregate":{"type":"count"},"filter":"service:api"}]}`)},
					},
				}
				Expect(k8sClient.Create(ctx, resource)).To(Succeed())
			}
		})

		AfterEach(func() {
			resource := &tsugav1alpha1.Monitor{}
			err := k8sClient.Get(ctx, typeNamespacedName, resource)
			Expect(err).NotTo(HaveOccurred())

			By("Cleanup the specific resource instance Monitor")
			Expect(k8sClient.Delete(ctx, resource)).To(Succeed())
		})
		It("should successfully reconcile the resource", func() {
			By("Reconciling the created resource")
			controllerReconciler := &TsugaMonitorReconciler{
				Client:      k8sClient,
				TsugaClient: &noopMonitorClient{},
			}

			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())
		})
	})
})

var _ = Describe("Monitor Controller watches Dashboard", func() {
	const (
		dashboardName = "watch-test-dashboard"
		monitorName   = "watch-test-monitor"
		namespace     = testNamespace
	)

	ctx := context.Background()

	dashboardKey := types.NamespacedName{Name: dashboardName, Namespace: namespace}
	monitorKey := types.NamespacedName{Name: monitorName, Namespace: namespace}

	AfterEach(func() {
		monitor := &tsugav1alpha1.Monitor{}
		if err := k8sClient.Get(ctx, monitorKey, monitor); err == nil {
			Expect(k8sClient.Delete(ctx, monitor)).To(Succeed())
		}

		dashboard := &tsugav1alpha1.Dashboard{}
		if err := k8sClient.Get(ctx, dashboardKey, dashboard); err == nil {
			Expect(k8sClient.Delete(ctx, dashboard)).To(Succeed())
		}
	})

	It("should reconcile a Monitor to Ready after its referenced Dashboard is synced", func() {
		By("creating a Dashboard without a status ID (not yet synced)")
		dashboard := &tsugav1alpha1.Dashboard{
			ObjectMeta: metav1.ObjectMeta{
				Name:      dashboardName,
				Namespace: namespace,
			},
			Spec: tsugav1alpha1.DashboardSpec{
				Name:  "Watch Test Dashboard",
				Owner: testOwner,
				Graphs: []tsugav1alpha1.DashboardGraph{
					{
						ID:            testGraphID,
						Visualization: apiextensionsv1.JSON{Raw: []byte(`{"type":"note","note":"hello"}`)},
					},
				},
			},
		}
		Expect(k8sClient.Create(ctx, dashboard)).To(Succeed())

		By("creating a Monitor referencing the Dashboard via dashboardRef")
		monitor := &tsugav1alpha1.Monitor{
			ObjectMeta: metav1.ObjectMeta{
				Name:      monitorName,
				Namespace: namespace,
			},
			Spec: tsugav1alpha1.MonitorSpec{
				Name:          "Watch Test Monitor",
				Owner:         testOwner,
				Priority:      1,
				Permissions:   testPermsAll,
				Configuration: apiextensionsv1.JSON{Raw: []byte(`{"type":"metric","condition":{"formula":"q1","operator":"greater_than","threshold":1},"noDataBehavior":"resolve","timeframe":5,"groupByFields":[],"queries":[{"aggregate":{"type":"count"},"filter":"service:api"}]}`)},
				DashboardRef:  &tsugav1alpha1.LocalObjectReference{Name: dashboardName},
			},
		}
		Expect(k8sClient.Create(ctx, monitor)).To(Succeed())

		By("starting the Monitor controller with a running manager")
		mgr, err := ctrl.NewManager(cfg, ctrl.Options{
			Scheme: k8sClient.Scheme(),
		})
		Expect(err).NotTo(HaveOccurred())

		reconciler := &TsugaMonitorReconciler{
			Client:      mgr.GetClient(),
			TsugaClient: &noopMonitorClient{},
		}
		Expect(reconciler.SetupWithManager(mgr)).To(Succeed())

		mgrCtx, mgrCancel := context.WithCancel(ctx)
		go func() {
			_ = mgr.Start(mgrCtx)
		}()
		DeferCleanup(mgrCancel)

		By("verifying a Dashboard event maps to the Monitor that references it")
		Eventually(func() []reconcile.Request {
			return reconciler.dashboardToMonitors(ctx, dashboard)
		}, 15*time.Second, 250*time.Millisecond).Should(ConsistOf(reconcile.Request{NamespacedName: monitorKey}))

		By("verifying the Monitor reaches Error phase (Dashboard not yet synced)")
		Eventually(func(g Gomega) {
			m := &tsugav1alpha1.Monitor{}
			g.Expect(k8sClient.Get(ctx, monitorKey, m)).To(Succeed())
			g.Expect(m.Status.Phase).To(Equal(phaseError))
		}, 15*time.Second, 250*time.Millisecond).Should(Succeed())

		By("patching the Dashboard's status to simulate a sync (adding a remote ID)")
		dashboardLatest := &tsugav1alpha1.Dashboard{}
		Expect(k8sClient.Get(ctx, dashboardKey, dashboardLatest)).To(Succeed())
		dashboardLatest.Status.ID = "dash-remote-synced"
		Expect(k8sClient.Status().Update(ctx, dashboardLatest)).To(Succeed())

		By("verifying the Monitor is automatically reconciled to Ready without any manual touch")
		Eventually(func(g Gomega) {
			m := &tsugav1alpha1.Monitor{}
			g.Expect(k8sClient.Get(ctx, monitorKey, m)).To(Succeed())
			g.Expect(m.Status.Phase).To(Equal(phaseReady))
		}, 15*time.Second, 250*time.Millisecond).Should(Succeed())
	})
})

var _ = Describe("Monitor Controller on a persistent Tsuga API error", func() {
	const namespace = "remote-error-loop"

	BeforeEach(func() {
		ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace}}
		if err := k8sClient.Create(ctx, ns); err != nil && !errors.IsAlreadyExists(err) {
			Expect(err).NotTo(HaveOccurred())
		}
	})

	// The Tsuga API puts a fresh requestId in every error body. If that leaks
	// into status.message, each failure is a status change, the status write
	// re-triggers the Monitor watch, and workqueue backoff never applies.
	DescribeTable("does not hot-loop against the API",
		func(name string, statusCode int) {
			var creates atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.URL.Path == "/v1/monitors/query" {
					_, _ = w.Write([]byte(`{"requestId":"q","data":[]}`))
					return
				}
				n := creates.Add(1)
				w.WriteHeader(statusCode)
				_, _ = fmt.Fprintf(w, `{"requestId":"req-%d","error":{"message":"Team (ID: does-not-exist) not found","statusCode":%d,"code":"RESOURCE_NOT_FOUND"}}`, n, statusCode)
			}))
			DeferCleanup(srv.Close)
			monitors, _, _ := NewTsugaClients(srv.URL, "token")

			skipNameValidation := true
			mgr, err := ctrl.NewManager(cfg, ctrl.Options{
				Scheme:     k8sClient.Scheme(),
				Cache:      cache.Options{DefaultNamespaces: map[string]cache.Config{namespace: {}}},
				Metrics:    metricsserver.Options{BindAddress: "0"},
				Controller: config.Controller{SkipNameValidation: &skipNameValidation},
			})
			Expect(err).NotTo(HaveOccurred())
			Expect((&TsugaMonitorReconciler{Client: mgr.GetClient(), TsugaClient: monitors}).SetupWithManager(mgr)).To(Succeed())

			mgrCtx, mgrCancel := context.WithCancel(ctx)
			mgrDone := make(chan struct{})
			go func() {
				defer close(mgrDone)
				_ = mgr.Start(mgrCtx)
			}()
			DeferCleanup(func() {
				mgrCancel()
				<-mgrDone
			})

			key := types.NamespacedName{Name: name, Namespace: namespace}
			monitor := &tsugav1alpha1.Monitor{
				ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
				Spec: tsugav1alpha1.MonitorSpec{
					Name:          "Remote Error Monitor",
					Owner:         "does-not-exist",
					Priority:      1,
					Permissions:   testPermsAll,
					Configuration: apiextensionsv1.JSON{Raw: []byte(`{"type":"metric","condition":{"formula":"q1","operator":"greater_than","threshold":1},"noDataBehavior":"resolve","timeframe":5,"groupByFields":[],"queries":[{"aggregate":{"type":"count"},"filter":"service:api"}]}`)},
				},
			}
			Expect(k8sClient.Create(ctx, monitor)).To(Succeed())
			DeferCleanup(func() {
				m := &tsugav1alpha1.Monitor{}
				Expect(k8sClient.Get(ctx, key, m)).To(Succeed())
				Expect(k8sClient.Delete(ctx, m)).To(Succeed())
				Eventually(func() bool {
					return errors.IsNotFound(k8sClient.Get(ctx, key, &tsugav1alpha1.Monitor{}))
				}, 15*time.Second, 100*time.Millisecond).Should(BeTrue())
			})

			Eventually(func(g Gomega) {
				m := &tsugav1alpha1.Monitor{}
				g.Expect(k8sClient.Get(ctx, key, m)).To(Succeed())
				g.Expect(m.Status.Phase).To(Equal(phaseError))
			}, 15*time.Second, 100*time.Millisecond).Should(Succeed())

			const window = 3 * time.Second
			start := creates.Load()
			time.Sleep(window)
			calls := creates.Load() - start
			AddReportEntry(fmt.Sprintf("Tsuga create calls in %s after a %d", window, statusCode), calls)
			Expect(calls).To(BeNumerically("<", 15), "the reconciler retried the failing create %d times in %s", calls, window)
		},
		Entry("404 retried with backoff", "remote-error-404", http.StatusNotFound),
		Entry("400 not requeued", "remote-error-400", http.StatusBadRequest),
	)
})
