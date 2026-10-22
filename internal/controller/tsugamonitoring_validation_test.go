package controller

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/tsuga-dev/tsuga-operator/api/v1alpha1"
)

var _ = Describe("TsugaMonitoring instrumentation validation", func() {
	newMonitoring := func(inst v1alpha1.InstrumentationSpec) *v1alpha1.TsugaMonitoring {
		return &v1alpha1.TsugaMonitoring{
			ObjectMeta: metav1.ObjectMeta{Name: "mon-validation", Namespace: "default"},
			Spec:       v1alpha1.TsugaMonitoringSpec{Instrumentation: inst},
		}
	}

	It("accepts the new languages", func() {
		m := newMonitoring(v1alpha1.InstrumentationSpec{Languages: []string{"go", "apache-httpd", "nginx"}})
		Expect(k8sClient.Create(context.Background(), m)).To(Succeed())
		DeferCleanup(func() { Expect(k8sClient.Delete(context.Background(), m)).To(Succeed()) })
	})

	It("rejects an unknown language", func() {
		m := newMonitoring(v1alpha1.InstrumentationSpec{Languages: []string{"ruby"}})
		err := k8sClient.Create(context.Background(), m)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("ruby"))
	})
})
