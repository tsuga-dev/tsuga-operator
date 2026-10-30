package controller

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/tsuga-dev/tsuga-operator/api/v1alpha1"
)

var _ = Describe("TsugaCollectorConfig prometheus validation", func() {
	newConfig := func(prometheus v1alpha1.PrometheusSpec) *v1alpha1.TsugaCollectorConfig {
		return &v1alpha1.TsugaCollectorConfig{
			ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
			Spec: v1alpha1.TsugaCollectorConfigSpec{
				ClusterName: "dev",
				Export: &v1alpha1.ExportSpec{
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
	It("rejects nodeLocal with the agent disabled", func() {
		cc := newConfig(v1alpha1.PrometheusSpec{Enabled: true, NodeLocal: ptr.To(true)})
		cc.Spec.Agent.Enabled = ptr.To(false)
		err := k8sClient.Create(context.Background(), cc)
		Expect(err).To(MatchError(ContainSubstring("prometheus.nodeLocal requires agent.enabled and agent.metrics")))
	})

	It("rejects nodeLocal with agent metrics off", func() {
		cc := newConfig(v1alpha1.PrometheusSpec{Enabled: true, NodeLocal: ptr.To(true)})
		cc.Spec.Agent.Metrics = false
		err := k8sClient.Create(context.Background(), cc)
		Expect(err).To(MatchError(ContainSubstring("prometheus.nodeLocal requires agent.enabled and agent.metrics")))
	})

	It("accepts nodeLocal while the scraper is disabled, whatever the agent", func() {
		cc := newConfig(v1alpha1.PrometheusSpec{
			Enabled:             false,
			NodeLocal:           ptr.To(true),
			AnnotationDiscovery: ptr.To(false),
		})
		cc.Spec.Agent.Metrics = false
		Expect(k8sClient.Create(context.Background(), cc)).To(Succeed())
		DeferCleanup(func() { Expect(k8sClient.Delete(context.Background(), cc)).To(Succeed()) })
	})

	It("rejects nodeLocal with annotationDiscovery false", func() {
		cc := newConfig(v1alpha1.PrometheusSpec{
			Enabled:             true,
			NodeLocal:           ptr.To(true),
			AnnotationDiscovery: ptr.To(false),
			TargetAllocator:     v1alpha1.TargetAllocatorSpec{Enabled: true},
			PrometheusCR:        v1alpha1.PrometheusCRSpec{Enabled: true},
		})
		err := k8sClient.Create(context.Background(), cc)
		Expect(err).To(MatchError(ContainSubstring("prometheus.nodeLocal scrapes pod annotations")))
	})

	It("rejects nodeLocal with an agent customConfig", func() {
		cc := newConfig(v1alpha1.PrometheusSpec{Enabled: true, NodeLocal: ptr.To(true)})
		cc.Spec.Agent.CustomConfig = &apiextensionsv1.JSON{Raw: []byte(`{"service":{"pipelines":{}}}`)}
		err := k8sClient.Create(context.Background(), cc)
		Expect(err).To(MatchError(ContainSubstring("prometheus.nodeLocal cannot be applied to an agent customConfig")))
	})

	It("rejects the per-node allocation strategy", func() {
		cc := newConfig(v1alpha1.PrometheusSpec{
			Enabled:         true,
			TargetAllocator: v1alpha1.TargetAllocatorSpec{Enabled: true, AllocationStrategy: "per-node"},
		})
		err := k8sClient.Create(context.Background(), cc)
		Expect(err).To(MatchError(ContainSubstring(`Unsupported value: "per-node"`)))
	})

	It("accepts nodeLocal with prometheusCR", func() {
		cc := newConfig(v1alpha1.PrometheusSpec{
			Enabled:         true,
			NodeLocal:       ptr.To(true),
			TargetAllocator: v1alpha1.TargetAllocatorSpec{Enabled: true},
			PrometheusCR:    v1alpha1.PrometheusCRSpec{Enabled: true},
		})
		Expect(k8sClient.Create(context.Background(), cc)).To(Succeed())
		DeferCleanup(func() { Expect(k8sClient.Delete(context.Background(), cc)).To(Succeed()) })
	})

	It("leaves annotationDiscovery unset when not given", func() {
		cc := newConfig(v1alpha1.PrometheusSpec{
			Enabled:         true,
			TargetAllocator: v1alpha1.TargetAllocatorSpec{Enabled: true},
			PrometheusCR:    v1alpha1.PrometheusCRSpec{Enabled: true},
		})
		Expect(k8sClient.Create(context.Background(), cc)).To(Succeed())
		DeferCleanup(func() { Expect(k8sClient.Delete(context.Background(), cc)).To(Succeed()) })
		got := &v1alpha1.TsugaCollectorConfig{}
		Expect(k8sClient.Get(context.Background(), client.ObjectKeyFromObject(cc), got)).To(Succeed())
		Expect(got.Spec.Prometheus.AnnotationDiscovery).To(BeNil())
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
				Export: &v1alpha1.ExportSpec{
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

	It("rejects a missing clusterName, which would export an empty k8s.cluster.name", func() {
		err := k8sClient.Create(context.Background(), newConfig(""))
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
				ClusterName: "dev",
				Export: &v1alpha1.ExportSpec{
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

func newValidConfig() *v1alpha1.TsugaCollectorConfig {
	return &v1alpha1.TsugaCollectorConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
		Spec: v1alpha1.TsugaCollectorConfigSpec{
			ClusterName: "dev",
			Export: &v1alpha1.ExportSpec{
				TokenSecretRef: v1alpha1.SecretKeyRef{Name: "s", Key: "k"},
				Endpoint:       "https://otlp.tsuga.com",
			},
		},
	}
}

func createCollectorConfig(cc *v1alpha1.TsugaCollectorConfig) error {
	err := k8sClient.Create(context.Background(), cc)
	if err == nil {
		DeferCleanup(func() { Expect(k8sClient.Delete(context.Background(), cc)).To(Succeed()) })
	}
	return err
}

var _ = Describe("TsugaCollectorConfig placement and ServiceAccount defaults", func() {
	It("defaults serviceAccount.create, rbac.create and agent.enabled to true", func() {
		cc := newValidConfig()
		Expect(k8sClient.Create(context.Background(), cc)).To(Succeed())
		DeferCleanup(func() { Expect(k8sClient.Delete(context.Background(), cc)).To(Succeed()) })
		got := &v1alpha1.TsugaCollectorConfig{}
		Expect(k8sClient.Get(context.Background(), client.ObjectKeyFromObject(cc), got)).To(Succeed())
		Expect(got.Spec.ServiceAccount.Create).To(HaveValue(BeTrue()))
		Expect(got.Spec.RBAC.Create).To(HaveValue(BeTrue()))
		Expect(got.Spec.Agent.Enabled).To(HaveValue(BeTrue()))
	})

	It("rejects serviceAccount.create: false without a name", func() {
		cc := newValidConfig()
		cc.Spec.ServiceAccount = v1alpha1.ServiceAccountSpec{Create: ptr.To(false)}
		err := k8sClient.Create(context.Background(), cc)
		Expect(err).To(MatchError(ContainSubstring("serviceAccount.name is required when serviceAccount.create is false")))
	})

	It("rejects a serviceAccount.name that is not a DNS-1123 subdomain", func() {
		cc := newValidConfig()
		cc.Spec.ServiceAccount = v1alpha1.ServiceAccountSpec{Create: ptr.To(false), Name: "My_SA"}
		err := k8sClient.Create(context.Background(), cc)
		Expect(err).To(MatchError(ContainSubstring("spec.serviceAccount.name")))
	})

	It("accepts serviceAccount.create: false with a name", func() {
		cc := newValidConfig()
		cc.Spec.ServiceAccount = v1alpha1.ServiceAccountSpec{Create: ptr.To(false), Name: "my-sa"}
		Expect(k8sClient.Create(context.Background(), cc)).To(Succeed())
		DeferCleanup(func() { Expect(k8sClient.Delete(context.Background(), cc)).To(Succeed()) })
	})
})

var _ = Describe("TsugaCollectorConfig tuning validation", func() {
	It("rejects an empty OTLP endpoint", func() {
		// The typed client drops an empty string (omitempty), so send it raw.
		u := &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": v1alpha1.GroupVersion.String(),
			"kind":       "TsugaCollectorConfig",
			"metadata":   map[string]any{"name": "cluster"},
			"spec": map[string]any{
				"clusterName": "dev",
				"agent":       map[string]any{"otlp": map[string]any{"grpcEndpoint": ""}},
				"gateway":     map[string]any{},
			},
		}}
		Expect(k8sClient.Create(context.Background(), u)).To(MatchError(ContainSubstring("spec.agent.otlp.grpcEndpoint")))
	})

	It("rejects batch max below size", func() {
		cc := newValidConfig()
		cc.Spec.Batch = v1alpha1.BatchSpec{SendBatchSize: ptr.To[int32](100), SendBatchMaxSize: ptr.To[int32](50)}
		Expect(createCollectorConfig(cc)).To(MatchError(ContainSubstring("batch.sendBatchMaxSize must be 0 or at least sendBatchSize")))
	})

	It("rejects a batch size above the built-in max", func() {
		cc := newValidConfig()
		cc.Spec.Batch = v1alpha1.BatchSpec{SendBatchSize: ptr.To[int32](10000)}
		Expect(createCollectorConfig(cc)).To(MatchError(ContainSubstring("an unset field counts as the built-in 5000")))
	})

	It("rejects a batch max below the built-in size", func() {
		cc := newValidConfig()
		cc.Spec.Batch = v1alpha1.BatchSpec{SendBatchMaxSize: ptr.To[int32](1000)}
		Expect(createCollectorConfig(cc)).To(MatchError(ContainSubstring("an unset field counts as the built-in 5000")))
	})

	It("accepts an unlimited batch max with a large size", func() {
		cc := newValidConfig()
		cc.Spec.Batch = v1alpha1.BatchSpec{SendBatchSize: ptr.To[int32](10000), SendBatchMaxSize: ptr.To[int32](0)}
		Expect(createCollectorConfig(cc)).To(Succeed())
	})

	DescribeTable("rejects an OTLP endpoint without a valid port",
		func(endpoint string) {
			cc := newValidConfig()
			cc.Spec.Agent.OTLP = v1alpha1.AgentOTLP{HTTPEndpoint: endpoint}
			Expect(createCollectorConfig(cc)).To(HaveOccurred())
		},
		Entry("port zero", "0.0.0.0:0"),
		Entry("env port", "${env:MY_POD_IP}:${env:PORT}"),
		Entry("port above 65535", "0.0.0.0:70000"),
	)

	It("accepts an OTLP endpoint with an env host and a port", func() {
		cc := newValidConfig()
		cc.Spec.Agent.OTLP = v1alpha1.AgentOTLP{HTTPEndpoint: "${env:MY_POD_IP}:4320"}
		Expect(createCollectorConfig(cc)).To(Succeed())
	})

	It("rejects a malformed interval", func() {
		cc := newValidConfig()
		cc.Spec.Agent.KubeletStats.CollectionInterval = "20 seconds"
		Expect(createCollectorConfig(cc)).To(MatchError(ContainSubstring("spec.agent.kubeletStats.collectionInterval")))
	})

	It("rejects a serviceNameEnvVar that is not an env var name", func() {
		cc := newValidConfig()
		cc.Spec.Profiling.ServiceNameEnvVar = "a\"b"
		Expect(createCollectorConfig(cc)).To(MatchError(ContainSubstring("spec.profiling.serviceNameEnvVar")))
	})
})

var _ = Describe("TsugaCollectorConfig prometheusCR validation", func() {
	newConfig := func(prometheus v1alpha1.PrometheusSpec) *v1alpha1.TsugaCollectorConfig {
		return &v1alpha1.TsugaCollectorConfig{
			ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
			Spec: v1alpha1.TsugaCollectorConfigSpec{
				ClusterName: "dev",
				Export: &v1alpha1.ExportSpec{
					TokenSecretRef: v1alpha1.SecretKeyRef{Name: "s", Key: "k"},
					Endpoint:       "https://otlp.tsuga.com",
				},
				Prometheus: prometheus,
			},
		}
	}

	It("rejects prometheusCR without the Target Allocator", func() {
		cc := newConfig(v1alpha1.PrometheusSpec{
			Enabled:      true,
			PrometheusCR: v1alpha1.PrometheusCRSpec{Enabled: true},
		})
		Expect(createCollectorConfig(cc)).To(MatchError(ContainSubstring(
			"prometheus.prometheusCR.enabled requires prometheus.targetAllocator.enabled: the Target Allocator is what watches ServiceMonitors and PodMonitors")))
	})

	It("accepts prometheusCR with the Target Allocator", func() {
		cc := newConfig(v1alpha1.PrometheusSpec{
			Enabled:         true,
			PrometheusCR:    v1alpha1.PrometheusCRSpec{Enabled: true},
			TargetAllocator: v1alpha1.TargetAllocatorSpec{Enabled: true},
		})
		Expect(createCollectorConfig(cc)).To(Succeed())
	})

	It("rejects a scraper with no discovery", func() {
		cc := newConfig(v1alpha1.PrometheusSpec{Enabled: true, AnnotationDiscovery: ptr.To(false)})
		Expect(createCollectorConfig(cc)).To(MatchError(ContainSubstring(
			"prometheus needs annotationDiscovery or prometheusCR.enabled, or the scraper has no targets")))
	})

	It("accepts no discovery when the scraper is off", func() {
		cc := newConfig(v1alpha1.PrometheusSpec{Enabled: false, AnnotationDiscovery: ptr.To(false)})
		Expect(createCollectorConfig(cc)).To(Succeed())
	})

	It("rejects allowNamespaces together with denyNamespaces", func() {
		cc := newConfig(v1alpha1.PrometheusSpec{
			Enabled:         true,
			TargetAllocator: v1alpha1.TargetAllocatorSpec{Enabled: true},
			PrometheusCR: v1alpha1.PrometheusCRSpec{
				Enabled:         true,
				AllowNamespaces: []string{"a"},
				DenyNamespaces:  []string{"b"},
			},
		})
		Expect(createCollectorConfig(cc)).To(MatchError(ContainSubstring(
			"prometheus.prometheusCR.allowNamespaces and denyNamespaces are mutually exclusive")))
	})

	It("rejects a malformed prometheusCR scrapeInterval", func() {
		cc := newConfig(v1alpha1.PrometheusSpec{
			Enabled:      true,
			PrometheusCR: v1alpha1.PrometheusCRSpec{ScrapeInterval: "30 seconds"},
		})
		Expect(createCollectorConfig(cc)).To(MatchError(ContainSubstring("spec.prometheus.prometheusCR.scrapeInterval")))
	})
})

var _ = Describe("TsugaCollectorConfig export validation", func() {
	newConfig := func(export *v1alpha1.ExportSpec) *v1alpha1.TsugaCollectorConfig {
		return &v1alpha1.TsugaCollectorConfig{
			ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
			Spec: v1alpha1.TsugaCollectorConfigSpec{
				ClusterName: "dev",
				Export:      export,
			},
		}
	}
	validExport := func() *v1alpha1.ExportSpec {
		return &v1alpha1.ExportSpec{
			TokenSecretRef: v1alpha1.SecretKeyRef{Name: "s", Key: "k"},
			Endpoint:       "https://otlp.tsuga.com",
		}
	}

	It("accepts a config without export", func() {
		Expect(createCollectorConfig(newConfig(nil))).To(Succeed())
	})

	It("rejects an unknown encoding", func() {
		export := validExport()
		export.Encoding = "xml"
		Expect(createCollectorConfig(newConfig(export))).To(MatchError(ContainSubstring("spec.export.encoding")))
	})

	It("rejects an unknown compression", func() {
		export := validExport()
		export.Compression = "brotli"
		Expect(createCollectorConfig(newConfig(export))).To(MatchError(ContainSubstring("spec.export.compression")))
	})
})

var _ = Describe("TsugaCollectorConfig enrichment validation", func() {
	newConfig := func() *v1alpha1.TsugaCollectorConfig {
		return &v1alpha1.TsugaCollectorConfig{
			ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
			Spec:       v1alpha1.TsugaCollectorConfigSpec{ClusterName: "dev"},
		}
	}
	withDetectors := func(detectors []string) *v1alpha1.TsugaCollectorConfig {
		cc := newConfig()
		cc.Spec.ResourceDetection = v1alpha1.ResourceDetectionSpec{Enabled: true, Detectors: detectors}
		return cc
	}

	It("rejects empty detectors", func() {
		// The typed client drops an empty slice (omitempty), so send it raw.
		u := &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": v1alpha1.GroupVersion.String(),
			"kind":       "TsugaCollectorConfig",
			"metadata":   map[string]any{"name": "cluster"},
			"spec": map[string]any{
				"clusterName":       "dev",
				"agent":             map[string]any{},
				"gateway":           map[string]any{},
				"resourceDetection": map[string]any{"enabled": true, "detectors": []any{}},
			},
		}}
		Expect(k8sClient.Create(context.Background(), u)).To(MatchError(ContainSubstring(
			"resourceDetection.detectors cannot be empty: the collector refuses to start with no detector")))
	})

	It("rejects ec2 before eks", func() {
		Expect(createCollectorConfig(withDetectors([]string{"env", "ec2", "eks"}))).To(MatchError(ContainSubstring(
			"resourceDetection.detectors must list ec2 after eks, or cloud.platform loses aws_eks")))
	})

	It("accepts eks then ec2", func() {
		Expect(createCollectorConfig(withDetectors([]string{"env", "eks", "ec2"}))).To(Succeed())
	})

	It("rejects an unknown detector", func() {
		Expect(createCollectorConfig(withDetectors([]string{"k8s_api"}))).To(MatchError(
			ContainSubstring("spec.resourceDetection.detectors")))
	})

	It("rejects a zero timeout", func() {
		cc := newConfig()
		cc.Spec.ResourceDetection = v1alpha1.ResourceDetectionSpec{Enabled: true, Timeout: "0s"}
		Expect(createCollectorConfig(cc)).To(MatchError(ContainSubstring("spec.resourceDetection.timeout")))
	})
})

var _ = Describe("TsugaCollectorConfig custom config validation", func() {
	It("round-trips extra components, pipeline names and customConfig", func() {
		cc := &v1alpha1.TsugaCollectorConfig{
			ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
			Spec:       v1alpha1.TsugaCollectorConfigSpec{ClusterName: "dev"},
		}
		receivers := `{"otlp/extra":{"protocols":{"grpc":{}}}}`
		custom := `{"receivers":{"otlp":{}},"service":{"pipelines":{}}}`
		cc.Spec.Agent.ExtraReceivers = &apiextensionsv1.JSON{Raw: []byte(receivers)}
		cc.Spec.Agent.PipelineReceivers.Logs = []string{"otlp/extra"}
		cc.Spec.Agent.ServiceExtensions = []string{"health_check"}
		cc.Spec.Gateway.CustomConfig = &apiextensionsv1.JSON{Raw: []byte(custom)}
		Expect(k8sClient.Create(context.Background(), cc)).To(Succeed())
		DeferCleanup(func() { Expect(k8sClient.Delete(context.Background(), cc)).To(Succeed()) })

		got := &v1alpha1.TsugaCollectorConfig{}
		Expect(k8sClient.Get(context.Background(), client.ObjectKeyFromObject(cc), got)).To(Succeed())
		Expect(got.Spec.Agent.ExtraReceivers.Raw).To(MatchJSON(receivers))
		Expect(got.Spec.Agent.PipelineReceivers.Logs).To(Equal([]string{"otlp/extra"}))
		Expect(got.Spec.Agent.ServiceExtensions).To(Equal([]string{"health_check"}))
		Expect(got.Spec.Gateway.CustomConfig.Raw).To(MatchJSON(custom))
	})
})
