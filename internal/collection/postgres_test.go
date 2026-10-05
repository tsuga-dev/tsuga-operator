package collection

import (
	"slices"
	"strings"
	"testing"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/tsuga-dev/tsuga-operator/api/v1alpha1"
	"github.com/tsuga-dev/tsuga-operator/internal/collection/assets"
)

const testAgentEndpoint = "tsuga-agent-collector.tsuga-operator-system.svc.cluster.local:4317"

func sqlqueryDatasources(t *testing.T, obj *unstructured.Unstructured) []string {
	t.Helper()
	receivers, _, _ := unstructured.NestedMap(obj.Object, "spec", "config", "receivers")
	var out []string
	for name, raw := range receivers {
		if !strings.HasPrefix(name, "sqlquery/") {
			continue
		}
		ds, _ := raw.(map[string]interface{})["datasource"].(string)
		out = append(out, ds)
	}
	return out
}

func pgSpec(sslMode string) v1alpha1.TsugaPostgresMonitoringSpec {
	return ResolvePostgres(v1alpha1.TsugaPostgresMonitoringSpec{
		Host:     "orders-pg.rds.amazonaws.com",
		Port:     6543,
		Database: "appdb",
		SSLMode:  sslMode,
		Provisioning: v1alpha1.PostgresProvisioningSpec{
			AdminSecretRef: &corev1.LocalObjectReference{Name: "orders-pg-admin"},
		},
	})
}

func renderPG(t *testing.T, sslMode string) *unstructured.Unstructured {
	t.Helper()
	obj, err := RenderPostgresCollector("orders-db", "orders", pgSpec(sslMode), "", testAgentEndpoint)
	if err != nil {
		t.Fatal(err)
	}
	return obj
}

func TestResolvePostgresFillsDefaults(t *testing.T) {
	got := ResolvePostgres(v1alpha1.TsugaPostgresMonitoringSpec{Host: "h"})
	if got.Port != 5432 || got.Database != "postgres" || got.SSLMode != "require" || got.Provisioning.Mode != PostgresModeManaged {
		t.Fatalf("defaults not applied: %+v", got)
	}
}

func TestRenderPostgresCollectorSubstitutesTarget(t *testing.T) {
	obj := renderPG(t, "disable")
	out, err := yamlRoundtrip(obj)
	if err != nil {
		t.Fatal(err)
	}
	if contains(out, "__PG_") || contains(out, "__AGENT_") {
		t.Fatalf("unsubstituted placeholder left in:\n%s", out)
	}
	datasources := sqlqueryDatasources(t, obj)
	if len(datasources) != 8 {
		t.Fatalf("want 8 sqlquery datasources, got %d", len(datasources))
	}
	expected := "host=orders-pg.rds.amazonaws.com port=6543 dbname=appdb user=otel_monitor password=${env:OTEL_MONITOR_PASSWORD} sslmode=disable"
	for i, ds := range datasources {
		if ds != expected {
			t.Fatalf("datasource %d = %q, want %q", i, ds, expected)
		}
	}
	endpoint, _, _ := unstructuredString(obj, "spec", "config", "receivers", "postgresql", "endpoint")
	if endpoint != "orders-pg.rds.amazonaws.com:6543" {
		t.Fatalf("postgresql receiver endpoint = %q", endpoint)
	}
}

func TestRenderPostgresCollectorTLS(t *testing.T) {
	disabled := renderPG(t, "disable")
	if v, _, _ := unstructured.NestedBool(disabled.Object, "spec", "config", "receivers", "postgresql", "tls", "insecure"); !v {
		t.Fatal("sslMode disable: want tls.insecure true on the postgresql receiver")
	}

	required := renderPG(t, "require")
	insecure, _, _ := unstructured.NestedBool(required.Object, "spec", "config", "receivers", "postgresql", "tls", "insecure")
	skip, _, _ := unstructured.NestedBool(required.Object, "spec", "config", "receivers", "postgresql", "tls", "insecure_skip_verify")
	if insecure || !skip {
		t.Fatalf("sslMode require: want insecure=false insecure_skip_verify=true, got %v %v", insecure, skip)
	}
	datasources := sqlqueryDatasources(t, required)
	for i, ds := range datasources {
		if !strings.HasSuffix(ds, "sslmode=require") {
			t.Fatalf("datasource %d doesn't end with sslmode=require: %q", i, ds)
		}
	}
}

func TestRenderPostgresCollectorExportsToAgent(t *testing.T) {
	obj := renderPG(t, "require")
	endpoint, _, _ := unstructuredString(obj, "spec", "config", "exporters", "otlp", "endpoint")
	if endpoint != testAgentEndpoint {
		t.Fatalf("otlp exporter endpoint = %q", endpoint)
	}
	processors, _, _ := unstructured.NestedStringSlice(obj.Object, "spec", "config", "service", "pipelines", "metrics", "processors")
	if !slices.Equal(processors, []string{"memory_limiter", "resource/postgres", "batch"}) {
		t.Fatalf("metrics processors = %v, want memory_limiter, resource/postgres, batch", processors)
	}
	attrs, _, _ := unstructured.NestedSlice(obj.Object, "spec", "config", "processors", "resource/postgres", "attributes")
	got := map[string]interface{}{}
	for _, a := range attrs {
		m := a.(map[string]interface{})
		got[m["key"].(string)] = m["value"]
	}
	if got["server.address"] != "orders-pg.rds.amazonaws.com" {
		t.Fatalf("server.address = %v", got["server.address"])
	}
	if port, ok := got["server.port"].(float64); !ok || port != 6543 {
		t.Fatalf("server.port = %#v, want the number 6543", got["server.port"])
	}
}

func TestRenderPostgresCollectorNumericHostStaysAString(t *testing.T) {
	spec := pgSpec("require")
	spec.Host = "1234"
	obj, err := RenderPostgresCollector("orders-db", "orders", spec, "", testAgentEndpoint)
	if err != nil {
		t.Fatal(err)
	}
	attrs, _, _ := unstructured.NestedSlice(obj.Object, "spec", "config", "processors", "resource/postgres", "attributes")
	got := map[string]interface{}{}
	for _, a := range attrs {
		m := a.(map[string]interface{})
		got[m["key"].(string)] = m["value"]
	}
	if address, ok := got["server.address"].(string); !ok || address != "1234" {
		t.Fatalf("server.address = %#v, want the string \"1234\"", got["server.address"])
	}
}

func TestRenderPostgresCollectorObjectShape(t *testing.T) {
	obj := renderPG(t, "require")
	if obj.GetName() != "orders-db-pg" || obj.GetNamespace() != "orders" {
		t.Fatalf("got %s/%s", obj.GetNamespace(), obj.GetName())
	}
	if mode, _, _ := unstructuredString(obj, "spec", "mode"); mode != modeDeployment {
		t.Fatalf("mode = %q", mode)
	}
	if replicas, _, _ := unstructured.NestedInt64(obj.Object, "spec", "replicas"); replicas != 1 {
		t.Fatalf("replicas = %d", replicas)
	}
	if image, _, _ := unstructuredString(obj, "spec", "image"); image != defaultAgentImage {
		t.Fatalf("image = %q", image)
	}
	env, _, _ := unstructured.NestedSlice(obj.Object, "spec", "env")
	if len(env) != 2 {
		t.Fatalf("env = %v", env)
	}
	ref, _, _ := unstructured.NestedStringMap(env[0].(map[string]interface{}), "valueFrom", "secretKeyRef")
	if ref["name"] != "orders-db-pg-monitor" || ref["key"] != "password" {
		t.Fatalf("OTEL_MONITOR_PASSWORD secretKeyRef = %v", ref)
	}
	node, _, _ := unstructured.NestedString(env[1].(map[string]interface{}), "valueFrom", "fieldRef", "fieldPath")
	if node != "spec.nodeName" {
		t.Fatalf("K8S_NODE_NAME fieldPath = %q, want spec.nodeName", node)
	}

	overridden, err := RenderPostgresCollector("orders-db", "orders", pgSpec("require"), "example.com/otelcol:1", testAgentEndpoint)
	if err != nil {
		t.Fatal(err)
	}
	if image, _, _ := unstructuredString(overridden, "spec", "image"); image != "example.com/otelcol:1" {
		t.Fatalf("image override ignored: %q", image)
	}
}

func TestRenderPostgresCollectorSecurityContext(t *testing.T) {
	obj := renderPG(t, "require")

	podSC, _, _ := unstructured.NestedMap(obj.Object, "spec", "podSecurityContext")
	if runAsNonRoot, _ := podSC["runAsNonRoot"].(bool); !runAsNonRoot {
		t.Fatalf("podSecurityContext.runAsNonRoot = %v, want true", podSC["runAsNonRoot"])
	}
	seccompType, _, _ := unstructured.NestedString(podSC, "seccompProfile", "type")
	if seccompType != "RuntimeDefault" {
		t.Fatalf("podSecurityContext.seccompProfile.type = %q, want RuntimeDefault", seccompType)
	}

	sc, _, _ := unstructured.NestedMap(obj.Object, "spec", "securityContext")
	if allow, _ := sc["allowPrivilegeEscalation"].(bool); allow {
		t.Fatalf("securityContext.allowPrivilegeEscalation = %v, want false", sc["allowPrivilegeEscalation"])
	}
	drop, _, _ := unstructured.NestedStringSlice(sc, "capabilities", "drop")
	if len(drop) != 1 || drop[0] != "ALL" {
		t.Fatalf("securityContext.capabilities.drop = %v, want [ALL]", drop)
	}
}

func renderJob(t *testing.T, spec v1alpha1.TsugaPostgresMonitoringSpec) *batchv1.Job {
	t.Helper()
	job, err := RenderPostgresSetupJob("orders-db", "orders", spec)
	if err != nil {
		t.Fatal(err)
	}
	return job
}

func envByName(c corev1.Container) map[string]corev1.EnvVar {
	out := map[string]corev1.EnvVar{}
	for _, e := range c.Env {
		out[e.Name] = e
	}
	return out
}

func TestRenderPostgresMonitorSecret(t *testing.T) {
	a := RenderPostgresMonitorSecret("orders-db", "orders")
	b := RenderPostgresMonitorSecret("orders-db", "orders")
	if a.Name != "orders-db-pg-monitor" || a.Namespace != "orders" {
		t.Fatalf("got %s/%s", a.Namespace, a.Name)
	}
	if len(a.StringData["password"]) < 20 {
		t.Fatalf("password too short: %q", a.StringData["password"])
	}
	if a.StringData["password"] == b.StringData["password"] {
		t.Fatal("two renders produced the same password")
	}
	if a.Labels[PostgresMonitoringLabel] != "orders-db" {
		t.Fatalf("labels = %v", a.Labels)
	}
}

func TestRenderPostgresSetupConfigMap(t *testing.T) {
	cm := RenderPostgresSetupConfigMap("orders-db", "orders")
	if cm.Name != "orders-db-pg-setup" || cm.Data["monitoring-setup.sql"] != assets.PostgresSetupSQL {
		t.Fatalf("configmap %s missing the setup SQL", cm.Name)
	}
	if cm.APIVersion != "v1" || cm.Kind != "ConfigMap" {
		t.Fatal("TypeMeta must be set for server-side apply")
	}
}

func TestRenderPostgresSetupJobCredentialsComeFromSecrets(t *testing.T) {
	job := renderJob(t, pgSpec("require"))
	c := job.Spec.Template.Spec.Containers[0]
	env := envByName(c)
	for name, want := range map[string][2]string{
		"PGUSER":                {"orders-pg-admin", "username"},
		"PGPASSWORD":            {"orders-pg-admin", "password"},
		"OTEL_MONITOR_PASSWORD": {"orders-db-pg-monitor", "password"},
	} {
		ref := env[name].ValueFrom
		if ref == nil || ref.SecretKeyRef == nil || ref.SecretKeyRef.Name != want[0] || ref.SecretKeyRef.Key != want[1] {
			t.Fatalf("%s must come from secret %s/%s, got %+v", name, want[0], want[1], env[name])
		}
	}
	if env["PGHOST"].Value != "orders-pg.rds.amazonaws.com" || env["PGPORT"].Value != "6543" ||
		env["PGDATABASE"].Value != "appdb" || env["PGSSLMODE"].Value != "require" {
		t.Fatalf("connection env wrong: %+v", env)
	}
	for _, e := range c.Env {
		if strings.Contains(e.Name, "PASSWORD") && e.Value != "" {
			t.Fatalf("%s must not carry a literal value", e.Name)
		}
	}
	if !contains(strings.Join(c.Command, " "), `:'pw'`) {
		t.Fatal("ALTER USER must bind the password through a psql variable")
	}
}

func TestRenderPostgresSetupJobShape(t *testing.T) {
	job := renderJob(t, pgSpec("require"))
	if !strings.HasPrefix(job.Name, "orders-db-pg-setup-") || len(job.Name) != len("orders-db-pg-setup-")+10 {
		t.Fatalf("job name = %q", job.Name)
	}
	if job.Labels[PostgresMonitoringLabel] != "orders-db" || job.Spec.Template.Labels[PostgresMonitoringLabel] != "orders-db" {
		t.Fatal("job and pod template must carry the owner label")
	}
	if *job.Spec.BackoffLimit != 3 || *job.Spec.ActiveDeadlineSeconds != 600 || job.Spec.TTLSecondsAfterFinished != nil {
		t.Fatalf("job limits wrong: %+v", job.Spec)
	}
	pod := job.Spec.Template.Spec
	if pod.RestartPolicy != corev1.RestartPolicyNever || pod.Volumes[0].ConfigMap.Name != "orders-db-pg-setup" {
		t.Fatalf("pod spec wrong: %+v", pod)
	}
	sc := pod.Containers[0].SecurityContext
	if sc == nil || !*sc.RunAsNonRoot || *sc.RunAsUser != 999 || *sc.AllowPrivilegeEscalation ||
		len(sc.Capabilities.Drop) != 1 || sc.Capabilities.Drop[0] != "ALL" ||
		sc.SeccompProfile.Type != corev1.SeccompProfileTypeRuntimeDefault {
		t.Fatalf("security context does not meet the restricted Pod Security level: %+v", sc)
	}
}

func TestRenderPostgresSetupJobNameTracksInputs(t *testing.T) {
	base := renderJob(t, pgSpec("require")).Name
	if again := renderJob(t, pgSpec("require")).Name; again != base {
		t.Fatalf("same inputs gave %s and %s", base, again)
	}
	for field, mutate := range map[string]func(*v1alpha1.TsugaPostgresMonitoringSpec){
		"host":           func(s *v1alpha1.TsugaPostgresMonitoringSpec) { s.Host = "other" },
		"port":           func(s *v1alpha1.TsugaPostgresMonitoringSpec) { s.Port = 5433 },
		"database":       func(s *v1alpha1.TsugaPostgresMonitoringSpec) { s.Database = "other" },
		"sslMode":        func(s *v1alpha1.TsugaPostgresMonitoringSpec) { s.SSLMode = "disable" },
		"adminSecretRef": func(s *v1alpha1.TsugaPostgresMonitoringSpec) { s.Provisioning.AdminSecretRef.Name = "other-admin" },
	} {
		spec := pgSpec("require")
		mutate(&spec)
		if renderJob(t, spec).Name == base {
			t.Fatalf("changing %s did not change the job name", field)
		}
	}
}

func TestRenderPostgresSetupJobRequiresAdminSecret(t *testing.T) {
	spec := pgSpec("require")
	spec.Provisioning.AdminSecretRef = nil
	if _, err := RenderPostgresSetupJob("orders-db", "orders", spec); err == nil {
		t.Fatal("want an error without adminSecretRef")
	}
}
