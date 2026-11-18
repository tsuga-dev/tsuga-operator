package controller

import (
	"context"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/config"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	tsugav1alpha1 "github.com/tsuga-dev/tsuga-operator/api/v1alpha1"
)

var _ = Describe("Folder controllers setup", func() {
	It("registers the folder controllers and folder watches on a running manager", func() {
		ctx := context.Background()
		namespace := "folder-setup"
		Expect(k8sClient.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace}})).To(Succeed())

		skipNameValidation := true
		mgr, err := ctrl.NewManager(cfg, ctrl.Options{
			Scheme:     k8sClient.Scheme(),
			Metrics:    metricsserver.Options{BindAddress: "0"},
			Controller: config.Controller{SkipNameValidation: &skipNameValidation},
		})
		Expect(err).NotTo(HaveOccurred())
		Expect((&TsugaMonitorFolderReconciler{Client: mgr.GetClient(), TsugaClient: &noopMonitorClient{}}).SetupWithManager(mgr)).To(Succeed())
		Expect((&TsugaDashboardFolderReconciler{Client: mgr.GetClient(), TsugaClient: &noopMonitorClient{}}).SetupWithManager(mgr)).To(Succeed())
		Expect((&TsugaDashboardReconciler{Client: mgr.GetClient(), TsugaClient: &noopMonitorClient{}}).SetupWithManager(mgr)).To(Succeed())

		mgrCtx, mgrCancel := context.WithCancel(ctx)
		DeferCleanup(mgrCancel)
		go func() {
			defer GinkgoRecover()
			Expect(mgr.Start(mgrCtx)).To(Succeed())
		}()

		monitorFolder := &tsugav1alpha1.MonitorFolder{
			ObjectMeta: metav1.ObjectMeta{Name: "setup", Namespace: namespace},
			Spec:       tsugav1alpha1.MonitorFolderSpec{Name: "Setup", Owner: testOwner},
		}
		dashboardFolder := &tsugav1alpha1.DashboardFolder{
			ObjectMeta: metav1.ObjectMeta{Name: "setup", Namespace: namespace},
			Spec:       tsugav1alpha1.DashboardFolderSpec{Name: "Setup", Owner: testOwner},
		}
		Expect(k8sClient.Create(ctx, monitorFolder)).To(Succeed())
		Expect(k8sClient.Create(ctx, dashboardFolder)).To(Succeed())

		key := types.NamespacedName{Name: "setup", Namespace: namespace}
		Eventually(func(g Gomega) {
			mf := &tsugav1alpha1.MonitorFolder{}
			g.Expect(k8sClient.Get(ctx, key, mf)).To(Succeed())
			g.Expect(mf.Finalizers).To(ContainElement(monitorFolderFinalizer))
			df := &tsugav1alpha1.DashboardFolder{}
			g.Expect(k8sClient.Get(ctx, key, df)).To(Succeed())
			g.Expect(df.Finalizers).To(ContainElement(dashboardFolderFinalizer))
		}, 15*time.Second, 250*time.Millisecond).Should(Succeed())
	})
})
