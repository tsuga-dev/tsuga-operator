package controller

import (
	"context"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/tsuga-dev/tsuga-operator/api/v1alpha1"
)

var _ = Describe("TsugaPostgresMonitoring schema validation", func() {
	newPG := func(name string, mutate func(*v1alpha1.TsugaPostgresMonitoringSpec)) *v1alpha1.TsugaPostgresMonitoring {
		spec := v1alpha1.TsugaPostgresMonitoringSpec{
			Host: "orders-pg-rw.orders.svc",
			Provisioning: v1alpha1.PostgresProvisioningSpec{
				AdminSecretRef: &corev1.LocalObjectReference{Name: "orders-pg-admin"},
			},
		}
		if mutate != nil {
			mutate(&spec)
		}
		return &v1alpha1.TsugaPostgresMonitoring{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: testNamespace},
			Spec:       spec,
		}
	}

	It("accepts a managed CR and applies the defaults", func() {
		pg := newPG("pg-defaults", nil)
		Expect(k8sClient.Create(context.Background(), pg)).To(Succeed())
		Expect(pg.Spec.Port).To(Equal(int32(5432)))
		Expect(pg.Spec.Database).To(Equal("postgres"))
		Expect(pg.Spec.SSLMode).To(Equal("require"))
		Expect(pg.Spec.Provisioning.Mode).To(Equal("managed"))
		Expect(k8sClient.Delete(context.Background(), pg)).To(Succeed())
	})

	It("accepts manual provisioning without an admin secret", func() {
		pg := newPG("pg-manual", func(s *v1alpha1.TsugaPostgresMonitoringSpec) {
			s.Provisioning = v1alpha1.PostgresProvisioningSpec{Mode: "manual"}
		})
		Expect(k8sClient.Create(context.Background(), pg)).To(Succeed())
		Expect(k8sClient.Delete(context.Background(), pg)).To(Succeed())
	})

	It("rejects managed provisioning without an admin secret", func() {
		err := k8sClient.Create(context.Background(), newPG("pg-no-admin", func(s *v1alpha1.TsugaPostgresMonitoringSpec) {
			s.Provisioning = v1alpha1.PostgresProvisioningSpec{Mode: "managed"}
		}))
		Expect(err).To(MatchError(ContainSubstring("adminSecretRef is required")))
	})

	It("rejects an empty provisioning block without admin secret", func() {
		err := k8sClient.Create(context.Background(), newPG("pg-empty", func(s *v1alpha1.TsugaPostgresMonitoringSpec) {
			s.Provisioning = v1alpha1.PostgresProvisioningSpec{}
		}))
		Expect(err).To(MatchError(ContainSubstring("adminSecretRef is required")))
	})

	It("rejects managed provisioning with an empty admin secret name", func() {
		err := k8sClient.Create(context.Background(), newPG("pg-empty-admin-name", func(s *v1alpha1.TsugaPostgresMonitoringSpec) {
			s.Provisioning = v1alpha1.PostgresProvisioningSpec{
				Mode:           "managed",
				AdminSecretRef: &corev1.LocalObjectReference{},
			}
		}))
		Expect(err).To(MatchError(ContainSubstring("adminSecretRef is required")))
	})

	It("rejects a truly omitted provisioning key", func() {
		obj := &unstructured.Unstructured{}
		obj.SetAPIVersion("observability.tsuga.com/v1alpha1")
		obj.SetKind("TsugaPostgresMonitoring")
		obj.SetName("pg-omitted")
		obj.SetNamespace(testNamespace)
		err := unstructured.SetNestedField(obj.Object, "orders-pg-rw.orders.svc", "spec", "host")
		Expect(err).NotTo(HaveOccurred())
		err = k8sClient.Create(context.Background(), obj)
		Expect(err).To(MatchError(ContainSubstring("adminSecretRef is required")))
	})

	It("rejects a host that could inject DSN keys", func() {
		for _, host := range []string{"db password=x", "db\nx", "db:5432", "", "orders..svc", "orders.-svc"} {
			err := k8sClient.Create(context.Background(), newPG("pg-bad-host", func(s *v1alpha1.TsugaPostgresMonitoringSpec) {
				s.Host = host
			}))
			Expect(err).To(HaveOccurred(), "host %q", host)
		}
	})

	It("rejects a database name that could inject DSN keys", func() {
		err := k8sClient.Create(context.Background(), newPG("pg-bad-db", func(s *v1alpha1.TsugaPostgresMonitoringSpec) {
			s.Database = "postgres sslmode=disable"
		}))
		Expect(err).To(HaveOccurred())
	})

	It("rejects an sslMode outside the enum", func() {
		err := k8sClient.Create(context.Background(), newPG("pg-bad-ssl", func(s *v1alpha1.TsugaPostgresMonitoringSpec) {
			s.SSLMode = "verify-full"
		}))
		Expect(err).To(HaveOccurred())
	})

	It("rejects a name too long for the setup Job's pod label", func() {
		err := k8sClient.Create(context.Background(), newPG(strings.Repeat("a", 44), nil))
		Expect(err).To(MatchError(ContainSubstring("43 characters")))
	})

	It("accepts a name at the 43 character limit", func() {
		pg := newPG(strings.Repeat("a", 43), nil)
		Expect(k8sClient.Create(context.Background(), pg)).To(Succeed())
		Expect(k8sClient.Delete(context.Background(), pg)).To(Succeed())
	})
})
