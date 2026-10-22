package collection

import (
	"context"
	"os"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	"sigs.k8s.io/yaml"

	"github.com/tsuga-dev/tsuga-operator/api/v1alpha1"
)

// otelCRDs returns the CustomResourceDefinitions in the pinned OpenTelemetry
// Operator release bundle. The rest of the bundle — the operator Deployment,
// its RBAC, its webhooks — has no bearing on whether a render matches the
// schema, so it is dropped.
func otelCRDs(t *testing.T, path string) []*apiextensionsv1.CustomResourceDefinition {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read operator bundle: %v", err)
	}
	var out []*apiextensionsv1.CustomResourceDefinition
	for _, doc := range strings.Split(string(raw), "\n---\n") {
		crd := &apiextensionsv1.CustomResourceDefinition{}
		if err := yaml.Unmarshal([]byte(doc), crd); err != nil || crd.Kind != "CustomResourceDefinition" {
			continue
		}
		// The bundle converts between CRD versions through a webhook whose CA
		// cert-manager injects. Neither runs here, and the renders only use
		// versions the API server stores directly, so conversion is dropped.
		crd.Spec.Conversion = &apiextensionsv1.CustomResourceConversion{Strategy: apiextensionsv1.NoneConverter}
		out = append(out, crd)
	}
	if len(out) == 0 {
		t.Fatalf("no CustomResourceDefinition found in %s", path)
	}
	return out
}

// TestRendersValidateAgainstPinnedOperatorCRDs submits every custom resource
// this package renders to an API server carrying the CRDs of the OpenTelemetry
// Operator release the install path pins. The renders are `unstructured`, so
// nothing about them is checked at compile time: a schema change upstream
// otherwise surfaces on a customer's cluster at apply time. Strict field
// validation makes a field that upstream removed an error rather than a
// silently pruned one.
func TestRendersValidateAgainstPinnedOperatorCRDs(t *testing.T) {
	bundle := os.Getenv("OTEL_OPERATOR_BUNDLE")
	if bundle == "" {
		t.Skip("OTEL_OPERATOR_BUNDLE unset; run `make test` or `make otel-bundle`")
	}

	env := &envtest.Environment{CRDs: otelCRDs(t, bundle)}
	restCfg, err := env.Start()
	if err != nil {
		t.Fatalf("start test API server: %v", err)
	}
	t.Cleanup(func() {
		if err := env.Stop(); err != nil {
			t.Errorf("stop test API server: %v", err)
		}
	})

	c, err := client.New(restCfg, client.Options{})
	if err != nil {
		t.Fatalf("build client: %v", err)
	}

	ctx := context.Background()
	cfg := everythingEnabled()
	if err := c.Create(ctx, &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: cfg.CollectorNamespace},
	}); err != nil {
		t.Fatalf("create namespace: %v", err)
	}

	objs, err := RenderCollectors(cfg)
	if err != nil {
		t.Fatalf("render collectors: %v", err)
	}
	inst, err := RenderInstrumentation(
		cfg.CollectorNamespace,
		"tsuga-instrumentation",
		v1alpha1.InstrumentationSpec{Languages: []string{"java", "nodejs", "python", "dotnet"}},
		cfg.Export.Endpoint, "",
	)
	if err != nil {
		t.Fatalf("render instrumentation: %v", err)
	}
	fullInst, err := RenderInstrumentation(
		cfg.CollectorNamespace,
		"tsuga-instrumentation-full",
		v1alpha1.InstrumentationSpec{
			Languages: []string{"java", "nodejs", "python", "dotnet", "go", "apache-httpd", "nginx"},
			Spec:      &apiextensionsv1.JSON{Raw: []byte(`{"sampler":{"type":"parentbased_traceidratio","argument":"0.25"},"env":[{"name":"A","value":"1"}],"nginx":{"attrs":[{"name":"NginxModuleTraceAsError","value":"OFF"}]}}`)},
		},
		cfg.Export.Endpoint, "http://tsuga-agent-collector.tsuga-system.svc.cluster.local:4317",
	)
	if err != nil {
		t.Fatalf("render full instrumentation: %v", err)
	}
	objs = append(objs, inst, fullInst)
	pgCollector, err := RenderPostgresCollector("orders-db", cfg.CollectorNamespace,
		ResolvePostgres(v1alpha1.TsugaPostgresMonitoringSpec{Host: "orders-pg-rw.orders.svc"}),
		"", "tsuga-agent-collector."+cfg.CollectorNamespace+".svc.cluster.local:4317")
	if err != nil {
		t.Fatalf("render postgres collector: %v", err)
	}
	objs = append(objs, pgCollector)

	for _, obj := range objs {
		if err := c.Create(ctx, obj, client.DryRunAll, client.FieldValidation(metav1.FieldValidationStrict)); err != nil {
			t.Errorf("%s/%s rejected by the pinned CRD schema: %v", obj.GetKind(), obj.GetName(), err)
		}
	}
}

// everythingEnabled is effective() with the scraper and its allocator turned
// on, so one pass renders every custom resource this package knows how to build.
func everythingEnabled() v1alpha1.TsugaCollectorConfigSpec {
	cfg := effective()
	cfg.Prometheus = ResolveCollector(v1alpha1.TsugaCollectorConfigSpec{
		Prometheus: v1alpha1.PrometheusSpec{
			Enabled:         true,
			Replicas:        2,
			TargetAllocator: v1alpha1.TargetAllocatorSpec{Enabled: true},
		},
	}).Prometheus
	cfg.Prometheus.PrometheusCR = v1alpha1.PrometheusCRSpec{
		Enabled:                         true,
		ServiceMonitorSelector:          &metav1.LabelSelector{MatchLabels: map[string]string{"team": "payments"}},
		PodMonitorSelector:              &metav1.LabelSelector{MatchLabels: map[string]string{"team": "payments"}},
		ServiceMonitorNamespaceSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"env": "prod"}},
		PodMonitorNamespaceSelector:     &metav1.LabelSelector{MatchLabels: map[string]string{"env": "prod"}},
		AllowNamespaces:                 []string{"payments"},
		DenyNamespaces:                  []string{"kube-system"},
		ScrapeInterval:                  "60s",
	}
	cfg.Profiling.Enabled = true
	cfg.NodeSelector = map[string]string{"pool": "observability"}
	cfg.Tolerations = []corev1.Toleration{{Key: "dedicated", Operator: corev1.TolerationOpExists, Effect: corev1.TaintEffectNoSchedule}}
	cfg.Affinity = &corev1.Affinity{PodAntiAffinity: &corev1.PodAntiAffinity{}}
	cfg.Agent.ExtraEnvs = []corev1.EnvVar{{Name: "GOMEMLIMIT", Value: "400MiB"}}
	cfg.Agent.CollectLogs = ptr.To(false)
	cfg.Agent.AddLogsVolumes = ptr.To(true)
	cfg.Gateway.Resources = &corev1.ResourceRequirements{Limits: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("1Gi")}}
	return cfg
}
