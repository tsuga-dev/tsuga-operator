package controller

import (
	"context"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/tsuga-dev/tsuga-operator/api/v1alpha1"
)

const sslVerifyFull = "verify-full"

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
		Expect(pg.Spec.Role).To(Equal("otel_monitor"))
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
			s.SSLMode = "verify-ca"
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

	secretTLS := func(tls v1alpha1.PostgresTLSSpec) func(*v1alpha1.TsugaPostgresMonitoringSpec) {
		return func(s *v1alpha1.TsugaPostgresMonitoringSpec) {
			s.SSLMode = sslVerifyFull
			s.TLS = &tls
		}
	}
	caVolume := v1alpha1.PostgresTLSVolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: "ca"}}

	acceptPG := func(name string, mutate func(*v1alpha1.TsugaPostgresMonitoringSpec)) {
		pg := newPG(name, mutate)
		Expect(k8sClient.Create(context.Background(), pg)).To(Succeed())
		Expect(k8sClient.Delete(context.Background(), pg)).To(Succeed())
	}
	rejectPG := func(name, msg string, mutate func(*v1alpha1.TsugaPostgresMonitoringSpec)) {
		err := k8sClient.Create(context.Background(), newPG(name, mutate))
		Expect(err).To(MatchError(ContainSubstring(msg)))
	}

	It("accepts verify-full with a CA from a secret", func() {
		acceptPG("pg-tls-ca", secretTLS(v1alpha1.PostgresTLSSpec{Volume: caVolume, CAFile: "ca.crt"}))
	})

	It("accepts manual verify-full without tls (system roots)", func() {
		acceptPG("pg-tls-system", func(s *v1alpha1.TsugaPostgresMonitoringSpec) {
			s.SSLMode = sslVerifyFull
			s.Provisioning = v1alpha1.PostgresProvisioningSpec{Mode: "manual"}
		})
	})

	It("rejects managed verify-full without caFile", func() {
		rejectPG("pg-tls-managed-system", "tls.caFile is required for verify-full", func(s *v1alpha1.TsugaPostgresMonitoringSpec) {
			s.SSLMode = sslVerifyFull
		})
		rejectPG("pg-tls-managed-cert", "tls.caFile is required for verify-full", secretTLS(v1alpha1.PostgresTLSSpec{Volume: caVolume, CertFile: "tls.crt", KeyFile: "tls.key"}))
	})

	It("accepts require with a client cert only", func() {
		acceptPG("pg-tls-client", func(s *v1alpha1.TsugaPostgresMonitoringSpec) {
			s.TLS = &v1alpha1.PostgresTLSSpec{Volume: caVolume, CertFile: "tls.crt", KeyFile: "tls.key"}
		})
	})

	It("rejects verify-ca", func() {
		rejectPG("pg-verify-ca", "Unsupported value", func(s *v1alpha1.TsugaPostgresMonitoringSpec) { s.SSLMode = "verify-ca" })
	})

	It("rejects tls with sslMode disable", func() {
		rejectPG("pg-tls-disable", "tls requires sslMode", func(s *v1alpha1.TsugaPostgresMonitoringSpec) {
			s.SSLMode = "disable"
			s.TLS = &v1alpha1.PostgresTLSSpec{Volume: caVolume, CAFile: "ca.crt"}
		})
	})

	It("rejects caFile with sslMode require", func() {
		rejectPG("pg-ca-require", "tls.caFile requires sslMode verify-full", func(s *v1alpha1.TsugaPostgresMonitoringSpec) {
			s.TLS = &v1alpha1.PostgresTLSSpec{Volume: caVolume, CAFile: "ca.crt"}
		})
	})

	It("rejects certFile without keyFile and the reverse", func() {
		rejectPG("pg-cert-only", "tls.certFile and tls.keyFile must be set together", secretTLS(v1alpha1.PostgresTLSSpec{Volume: caVolume, CertFile: "tls.crt"}))
		rejectPG("pg-key-only", "tls.certFile and tls.keyFile must be set together", secretTLS(v1alpha1.PostgresTLSSpec{Volume: caVolume, CAFile: "ca.crt", KeyFile: "tls.key"}))
	})

	It("rejects tls with neither caFile nor certFile", func() {
		rejectPG("pg-tls-empty", "tls requires caFile or certFile", secretTLS(v1alpha1.PostgresTLSSpec{Volume: caVolume}))
	})

	It("rejects a tls volume with two sources", func() {
		rejectPG("pg-two-sources", "exactly one", secretTLS(v1alpha1.PostgresTLSSpec{
			Volume: v1alpha1.PostgresTLSVolumeSource{
				Secret:    &corev1.SecretVolumeSource{SecretName: "ca"},
				ConfigMap: &corev1.ConfigMapVolumeSource{LocalObjectReference: corev1.LocalObjectReference{Name: "ca"}},
			},
			CAFile: "ca.crt",
		}))
	})

	It("rejects a tls volume with no source", func() {
		rejectPG("pg-no-source", "exactly one", secretTLS(v1alpha1.PostgresTLSSpec{CAFile: "ca.crt"}))
	})

	It("rejects a file path with .. or an absolute path", func() {
		for _, p := range []string{"../x", "a/../x", "/ca.crt"} {
			err := k8sClient.Create(context.Background(), newPG("pg-bad-path", secretTLS(v1alpha1.PostgresTLSSpec{Volume: caVolume, CAFile: p})))
			Expect(err).To(HaveOccurred(), "path %q", p)
		}
		err := k8sClient.Create(context.Background(), newPG("pg-bad-cert-path", func(s *v1alpha1.TsugaPostgresMonitoringSpec) {
			s.TLS = &v1alpha1.PostgresTLSSpec{Volume: caVolume, CertFile: "../tls.crt", KeyFile: "tls.key"}
		}))
		Expect(err).To(HaveOccurred())
		err = k8sClient.Create(context.Background(), newPG("pg-bad-key-path", func(s *v1alpha1.TsugaPostgresMonitoringSpec) {
			s.TLS = &v1alpha1.PostgresTLSSpec{Volume: caVolume, CertFile: "tls.crt", KeyFile: "/tls.key"}
		}))
		Expect(err).To(HaveOccurred())
	})

	It("rejects a role that is not a plain identifier", func() {
		for _, r := range []string{"Bad-Role", "a;b"} {
			err := k8sClient.Create(context.Background(), newPG("pg-bad-role", func(s *v1alpha1.TsugaPostgresMonitoringSpec) { s.Role = r }))
			Expect(err).To(HaveOccurred(), "role %q", r)
		}
	})

	It("rejects changing role after creation", func() {
		pg := newPG("pg-role-immutable", func(s *v1alpha1.TsugaPostgresMonitoringSpec) { s.Role = "a_mon" })
		Expect(k8sClient.Create(context.Background(), pg)).To(Succeed())
		pg.Spec.Role = "b_mon"
		Expect(k8sClient.Update(context.Background(), pg)).To(MatchError(ContainSubstring("role is immutable")))
		Expect(k8sClient.Delete(context.Background(), pg)).To(Succeed())
	})

	It("accepts passwordSecretRef", func() {
		acceptPG("pg-password-ref", func(s *v1alpha1.TsugaPostgresMonitoringSpec) {
			s.PasswordSecretRef = &corev1.LocalObjectReference{Name: "pg-monitor-password"}
		})
	})

	sidecar := func(mutate func(*v1alpha1.TsugaPostgresMonitoringSpec)) func(*v1alpha1.TsugaPostgresMonitoringSpec) {
		return func(s *v1alpha1.TsugaPostgresMonitoringSpec) {
			s.Mode = "sidecar"
			if mutate != nil {
				mutate(s)
			}
		}
	}

	It("accepts sidecar mode with sslMode require", func() {
		acceptPG("pg-sidecar", sidecar(nil))
	})

	It("rejects tls and verify-full in sidecar mode", func() {
		rejectPG("pg-sidecar-tls", "sidecar mode supports sslMode disable or require without tls",
			sidecar(secretTLS(v1alpha1.PostgresTLSSpec{Volume: caVolume, CAFile: "ca.crt"})))
		rejectPG("pg-sidecar-verify", "sidecar mode supports sslMode disable or require without tls",
			sidecar(func(s *v1alpha1.TsugaPostgresMonitoringSpec) {
				s.SSLMode = sslVerifyFull
				s.Provisioning = v1alpha1.PostgresProvisioningSpec{Mode: "manual"}
			}))
	})

	It("rejects nodeSelector and tolerations in sidecar mode", func() {
		rejectPG("pg-sidecar-ns", "do not apply in sidecar mode", sidecar(func(s *v1alpha1.TsugaPostgresMonitoringSpec) {
			s.NodeSelector = map[string]string{"a": "b"}
		}))
		rejectPG("pg-sidecar-tol", "do not apply in sidecar mode", sidecar(func(s *v1alpha1.TsugaPostgresMonitoringSpec) {
			s.Tolerations = []corev1.Toleration{{Key: "a", Operator: corev1.TolerationOpExists}}
		}))
	})

	It("rejects extraExtensions and serviceExtensions in sidecar mode", func() {
		rejectPG("pg-sidecar-ext", "not supported in sidecar mode", sidecar(func(s *v1alpha1.TsugaPostgresMonitoringSpec) {
			s.ExtraExtensions = &apiextensionsv1.JSON{Raw: []byte(`{"health_check/x":{}}`)}
		}))
		rejectPG("pg-sidecar-svcext", "not supported in sidecar mode", sidecar(func(s *v1alpha1.TsugaPostgresMonitoringSpec) {
			s.ServiceExtensions = []string{"foo"}
		}))
	})
})
