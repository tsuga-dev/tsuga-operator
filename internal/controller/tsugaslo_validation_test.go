package controller

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/tsuga-dev/tsuga-operator/api/v1alpha1"
)

var _ = Describe("SLO schema validation", func() {
	eventConfiguration := func() apiextensionsv1.JSON {
		return apiextensionsv1.JSON{Raw: []byte(`{"type":"event","dataSource":"traces","noDataBehavior":"bad","goodQuery":{"queries":[{"aggregate":{"type":"count"},"filter":"service:api status:ok"}],"formula":"q1"},"totalQuery":{"queries":[{"aggregate":{"type":"count"},"filter":"service:api"}],"formula":"q1"}}`)}
	}

	newSLO := func(name string, mutate func(*v1alpha1.SLOSpec)) *v1alpha1.SLO {
		spec := v1alpha1.SLOSpec{
			Name:          "Checkout availability",
			Configuration: eventConfiguration(),
			Target:        99.9,
			TimeframeDays: 30,
			Owner:         testOwner,
			Permissions:   testPermsAll,
		}
		if mutate != nil {
			mutate(&spec)
		}
		return &v1alpha1.SLO{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: testNamespace},
			Spec:       spec,
		}
	}

	It("accepts an event SLO", func() {
		slo := newSLO("schema-event", nil)
		Expect(k8sClient.Create(context.Background(), slo)).To(Succeed())
		Expect(k8sClient.Delete(context.Background(), slo)).To(Succeed())
	})

	It("accepts a time SLO with alerts", func() {
		slo := newSLO("schema-time", func(s *v1alpha1.SLOSpec) {
			s.Configuration = apiextensionsv1.JSON{Raw: []byte(`{"type":"time","dataSource":"metrics","noDataBehavior":"ignore","sliceSizeMinutes":60,"threshold":{"operator":"less_than_or_equal","value":250},"query":{"queries":[{"aggregate":{"type":"percentile","field":"duration","percentile":95},"filter":"service:api"}],"formula":"q1"}}`)}
			s.Alerts = []v1alpha1.SLOAlert{
				{Priority: 2, Configuration: apiextensionsv1.JSON{Raw: []byte(`{"type":"burn-rate","burnRate":14.4}`)}},
			}
		})
		Expect(k8sClient.Create(context.Background(), slo)).To(Succeed())
		Expect(k8sClient.Delete(context.Background(), slo)).To(Succeed())
	})

	It("rejects a target at the exclusive upper bound", func() {
		err := k8sClient.Create(context.Background(), newSLO("schema-target-100", func(s *v1alpha1.SLOSpec) {
			s.Target = 100
		}))
		Expect(err).To(HaveOccurred())
	})

	It("rejects a timeframe outside the supported set", func() {
		err := k8sClient.Create(context.Background(), newSLO("schema-timeframe-14", func(s *v1alpha1.SLOSpec) {
			s.TimeframeDays = 14
		}))
		Expect(err).To(HaveOccurred())
	})

	It("rejects an unknown permissions value", func() {
		err := k8sClient.Create(context.Background(), newSLO("schema-perms", func(s *v1alpha1.SLOSpec) {
			s.Permissions = "everyone"
		}))
		Expect(err).To(HaveOccurred())
	})

	It("rejects an alert priority above five", func() {
		err := k8sClient.Create(context.Background(), newSLO("schema-priority", func(s *v1alpha1.SLOSpec) {
			s.Alerts = []v1alpha1.SLOAlert{
				{Priority: 6, Configuration: apiextensionsv1.JSON{Raw: []byte(`{"type":"burn-rate","burnRate":2}`)}},
			}
		}))
		Expect(err).To(HaveOccurred())
	})
})
