package controller

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/tsuga-dev/tsuga-operator/api/v1alpha1"
)

var _ = Describe("TsugaCollectorConfig prometheus validation", func() {
	newConfig := func(prometheus v1alpha1.PrometheusSpec) *v1alpha1.TsugaCollectorConfig {
		return &v1alpha1.TsugaCollectorConfig{
			ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
			Spec: v1alpha1.TsugaCollectorConfigSpec{
				Export: v1alpha1.ExportSpec{
					TokenSecretRef: v1alpha1.SecretKeyRef{Name: "s", Key: "k"},
					Endpoint:       "https://otlp.tsuga.com",
				},
				Agent:      v1alpha1.TelemetryToggles{Traces: true, Metrics: true, Logs: true},
				Gateway:    v1alpha1.GatewayToggles{ClusterMetrics: true, KubernetesObjects: true},
				Prometheus: prometheus,
			},
		}
	}

	It("rejects replicas above one without the Target Allocator", func() {
		cc := newConfig(v1alpha1.PrometheusSpec{
			Enabled:  true,
			Replicas: 3,
		})
		err := k8sClient.Create(context.Background(), cc)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("requires prometheus.targetAllocator.enabled"))
	})

	It("accepts replicas above one with the Target Allocator", func() {
		cc := newConfig(v1alpha1.PrometheusSpec{
			Enabled:         true,
			Replicas:        3,
			TargetAllocator: v1alpha1.TargetAllocatorSpec{Enabled: true},
		})
		Expect(k8sClient.Create(context.Background(), cc)).To(Succeed())
		DeferCleanup(func() { Expect(k8sClient.Delete(context.Background(), cc)).To(Succeed()) })
	})

	It("accepts a config with no prometheus block at all", func() {
		cc := newConfig(v1alpha1.PrometheusSpec{})
		Expect(k8sClient.Create(context.Background(), cc)).To(Succeed())
		DeferCleanup(func() { Expect(k8sClient.Delete(context.Background(), cc)).To(Succeed()) })
	})
	It("rejects any name other than cluster", func() {
		cc := newConfig(v1alpha1.PrometheusSpec{})
		cc.Name = "second"
		err := k8sClient.Create(context.Background(), cc)
		Expect(err).To(MatchError(ContainSubstring("metadata.name must be 'cluster'")))
	})
})

var _ = Describe("TsugaCollectorConfig clusterName validation", func() {
	newConfig := func(clusterName string) *v1alpha1.TsugaCollectorConfig {
		return &v1alpha1.TsugaCollectorConfig{
			ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
			Spec: v1alpha1.TsugaCollectorConfigSpec{
				ClusterName: clusterName,
				Export: v1alpha1.ExportSpec{
					TokenSecretRef: v1alpha1.SecretKeyRef{Name: "s", Key: "k"},
					Endpoint:       "https://otlp.tsuga.com",
				},
			},
		}
	}

	It("rejects a clusterName that would break out of the YAML scalar", func() {
		err := k8sClient.Create(context.Background(), newConfig("prod\"\nexporters: {}"))
		Expect(err).To(MatchError(ContainSubstring("spec.clusterName")))
	})

	It("accepts a human-readable clusterName", func() {
		cc := newConfig("Prod EU-1")
		Expect(k8sClient.Create(context.Background(), cc)).To(Succeed())
		DeferCleanup(func() { Expect(k8sClient.Delete(context.Background(), cc)).To(Succeed()) })
	})
})

var _ = Describe("TsugaCollectorConfig export endpoint validation", func() {
	newConfig := func(endpoint string) *v1alpha1.TsugaCollectorConfig {
		return &v1alpha1.TsugaCollectorConfig{
			ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
			Spec: v1alpha1.TsugaCollectorConfigSpec{
				Export: v1alpha1.ExportSpec{
					TokenSecretRef: v1alpha1.SecretKeyRef{Name: "s", Key: "k"},
					Endpoint:       endpoint,
				},
			},
		}
	}

	It("rejects an endpoint that would break out of the YAML scalar", func() {
		err := k8sClient.Create(context.Background(), newConfig("https://otlp.tsuga.com\"\n    tls:\n      insecure_skip_verify: true"))
		Expect(err).To(MatchError(ContainSubstring("spec.export.endpoint")))
	})

	It("rejects a plain http endpoint, which would send the token in cleartext", func() {
		err := k8sClient.Create(context.Background(), newConfig("http://otlp.tsuga.com:4318"))
		Expect(err).To(MatchError(ContainSubstring("spec.export.endpoint")))
	})

	It("rejects an endpoint without an https scheme", func() {
		err := k8sClient.Create(context.Background(), newConfig("otlp.tsuga.com:4318"))
		Expect(err).To(MatchError(ContainSubstring("spec.export.endpoint")))
	})

	It("accepts an https endpoint with a port and path", func() {
		cc := newConfig("https://otlp.eu.tsuga.com:4318/otlp")
		Expect(k8sClient.Create(context.Background(), cc)).To(Succeed())
		DeferCleanup(func() { Expect(k8sClient.Delete(context.Background(), cc)).To(Succeed()) })
	})
})
