package collection

import (
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/api/resource"
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
		if !strings.HasPrefix(name, "sql_query/") {
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
	if got.Port != 5432 || got.Database != "postgres" || got.SSLMode != "require" || got.Provisioning.Mode != PostgresModeManaged || got.Role != "otel_monitor" {
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
		t.Fatalf("want 8 sql_query datasources, got %d", len(datasources))
	}
	expected := "host=orders-pg.rds.amazonaws.com port=6543 dbname=appdb user=otel_monitor sslmode=disable"
	for i, ds := range datasources {
		if ds != expected {
			t.Fatalf("datasource %d = %q, want %q", i, ds, expected)
		}
	}
	endpoint, _, _ := unstructuredString(obj, "spec", "config", "receivers", "postgresql", "endpoint")
	if endpoint != "orders-pg.rds.amazonaws.com:6543" {
		t.Fatalf("postgresql receiver endpoint = %q", endpoint)
	}
	env, _, _ := unstructured.NestedSlice(obj.Object, "spec", "env")
	if !slices.ContainsFunc(env, func(e interface{}) bool {
		secret, _, _ := unstructured.NestedString(e.(map[string]interface{}), "valueFrom", "secretKeyRef", "name")
		return e.(map[string]interface{})["name"] == "PGPASSWORD" && secret == "orders-db-pg-monitor"
	}) {
		t.Fatalf("the sql_query receivers need PGPASSWORD from the monitor Secret: %#v", env)
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
	if len(env) != 3 {
		t.Fatalf("env = %v", env)
	}
	ref, _, _ := unstructured.NestedStringMap(env[0].(map[string]interface{}), "valueFrom", "secretKeyRef")
	if ref["name"] != "orders-db-pg-monitor" || ref["key"] != "password" {
		t.Fatalf("OTEL_MONITOR_PASSWORD secretKeyRef = %v", ref)
	}
	node, _, _ := unstructured.NestedString(env[2].(map[string]interface{}), "valueFrom", "fieldRef", "fieldPath")
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

func TestRenderPostgresCollectorPodOverrides(t *testing.T) {
	obj := renderPG(t, "require")
	if mem, _, _ := unstructuredString(obj, "spec", "resources", "limits", "memory"); mem != "256Mi" {
		t.Fatalf("default memory limit = %q", mem)
	}
	for _, key := range []string{"nodeSelector", "tolerations"} {
		if _, found, _ := unstructured.NestedFieldNoCopy(obj.Object, "spec", key); found {
			t.Fatalf("spec.%s set without an override", key)
		}
	}

	spec := pgSpec("require")
	spec.Image = "example.com/pg-otelcol:2"
	spec.Resources = &corev1.ResourceRequirements{
		Limits: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("1Gi")},
	}
	spec.NodeSelector = map[string]string{"pool": "db"}
	spec.Tolerations = []corev1.Toleration{{Key: "dedicated", Operator: corev1.TolerationOpExists}}
	obj, err := RenderPostgresCollector("orders-db", "orders", spec, "example.com/otelcol:1", testAgentEndpoint)
	if err != nil {
		t.Fatal(err)
	}
	if image, _, _ := unstructuredString(obj, "spec", "image"); image != "example.com/pg-otelcol:2" {
		t.Fatalf("image = %q, want the resource's own image over the cluster's", image)
	}
	if mem, _, _ := unstructuredString(obj, "spec", "resources", "limits", "memory"); mem != "1Gi" {
		t.Fatalf("memory limit = %q", mem)
	}
	if _, found, _ := unstructured.NestedFieldNoCopy(obj.Object, "spec", "resources", "requests"); found {
		t.Fatal("default requests merged into an explicit resources block")
	}
	if pool, _, _ := unstructuredString(obj, "spec", "nodeSelector", "pool"); pool != "db" {
		t.Fatalf("nodeSelector pool = %q", pool)
	}
	tolerations, _, _ := unstructured.NestedSlice(obj.Object, "spec", "tolerations")
	if len(tolerations) != 1 || tolerations[0].(map[string]interface{})["key"] != "dedicated" {
		t.Fatalf("tolerations = %v", tolerations)
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
	cmd := strings.Join(c.Command, " ")
	if !contains(cmd, `-v set_password=on`) || !contains(assets.PostgresSetupSQL, `\password :"role"`) ||
		contains(assets.PostgresSetupSQL, "PASSWORD %L") {
		t.Fatal("the password must reach the server only as the hash \\password sends")
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
		"role":           func(s *v1alpha1.TsugaPostgresMonitoringSpec) { s.Role = "dba_mon" },
		"passwordSecretRef": func(s *v1alpha1.TsugaPostgresMonitoringSpec) {
			s.PasswordSecretRef = &corev1.LocalObjectReference{Name: "dba-pw"}
		},
	} {
		spec := pgSpec("require")
		mutate(&spec)
		if renderJob(t, spec).Name == base {
			t.Fatalf("changing %s did not change the job name", field)
		}
	}
	tlsSpec := func() v1alpha1.TsugaPostgresMonitoringSpec {
		spec := pgSpec("verify-full")
		spec.TLS = &v1alpha1.PostgresTLSSpec{Volume: tlsSecretVolume(), CAFile: "ca.crt"}
		return spec
	}
	tlsBase := renderJob(t, tlsSpec()).Name
	for field, mutate := range map[string]func(*v1alpha1.TsugaPostgresMonitoringSpec){
		"tls.caFile":            func(s *v1alpha1.TsugaPostgresMonitoringSpec) { s.TLS.CAFile = "other.crt" },
		"tls.volume.secretName": func(s *v1alpha1.TsugaPostgresMonitoringSpec) { s.TLS.Volume.Secret.SecretName = "other-certs" },
	} {
		spec := tlsSpec()
		mutate(&spec)
		if renderJob(t, spec).Name == tlsBase {
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

func renderPGSpec(t *testing.T, spec v1alpha1.TsugaPostgresMonitoringSpec) *unstructured.Unstructured {
	t.Helper()
	obj, err := RenderPostgresCollector("orders-db", "orders", ResolvePostgres(spec), "", testAgentEndpoint)
	if err != nil {
		t.Fatal(err)
	}
	return obj
}

func tlsSecretVolume() v1alpha1.PostgresTLSVolumeSource {
	return v1alpha1.PostgresTLSVolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: "pg-certs"}}
}

func receiverTLS(t *testing.T, obj *unstructured.Unstructured) map[string]interface{} {
	t.Helper()
	m, found, _ := unstructured.NestedMap(obj.Object, "spec", "config", "receivers", "postgresql", "tls")
	if !found {
		t.Fatal("receiver tls missing")
	}
	return m
}

func requireDatasourceSuffix(t *testing.T, obj *unstructured.Unstructured, suffix string) {
	t.Helper()
	ds := sqlqueryDatasources(t, obj)
	if len(ds) != 8 {
		t.Fatalf("want 8 datasources, got %d", len(ds))
	}
	for _, d := range ds {
		if !strings.HasSuffix(d, suffix) {
			t.Fatalf("datasource %q does not end with %q", d, suffix)
		}
	}
}

func TestRenderPostgresCollectorDefaultsUnchanged(t *testing.T) {
	obj := renderPG(t, "require")
	username, _, _ := unstructured.NestedString(obj.Object, "spec", "config", "receivers", "postgresql", "username")
	if username != "otel_monitor" {
		t.Fatalf("username = %q", username)
	}
	want := map[string]interface{}{"insecure": false, "insecure_skip_verify": true}
	if got := receiverTLS(t, obj); !reflect.DeepEqual(got, want) {
		t.Fatalf("tls = %#v", got)
	}
	requireDatasourceSuffix(t, obj, " user=otel_monitor sslmode=require")
	for _, k := range []string{"volumes", "volumeMounts"} {
		if _, found, _ := unstructured.NestedFieldNoCopy(obj.Object, "spec", k); found {
			t.Fatalf("unexpected %s", k)
		}
	}
	psc, _, _ := unstructured.NestedMap(obj.Object, "spec", "podSecurityContext")
	if _, ok := psc["runAsGroup"]; ok {
		t.Fatal("unexpected runAsGroup")
	}
	if _, ok := psc["fsGroup"]; ok {
		t.Fatal("unexpected fsGroup")
	}
}

func TestRenderPostgresCollectorRole(t *testing.T) {
	spec := pgSpec("require")
	spec.Role = "dba_mon"
	obj := renderPGSpec(t, spec)
	if u, _, _ := unstructured.NestedString(obj.Object, "spec", "config", "receivers", "postgresql", "username"); u != "dba_mon" {
		t.Fatalf("username = %q", u)
	}
	for _, d := range sqlqueryDatasources(t, obj) {
		if !strings.Contains(d, " user=dba_mon ") {
			t.Fatalf("datasource %q", d)
		}
	}
}

func TestRenderPostgresCollectorRoleStaysAString(t *testing.T) {
	for _, role := range []string{"null", "true", "no", "off"} {
		spec := pgSpec("require")
		spec.Role = role
		obj := renderPGSpec(t, spec)
		if u, ok, _ := unstructured.NestedString(obj.Object, "spec", "config", "receivers", "postgresql", "username"); !ok || u != role {
			t.Fatalf("role %q: username = %q", role, u)
		}
	}
}

func TestRenderPostgresCollectorVerifyFullWithCA(t *testing.T) {
	spec := pgSpec("verify-full")
	spec.TLS = &v1alpha1.PostgresTLSSpec{Volume: tlsSecretVolume(), CAFile: "ca.crt"}
	obj := renderPGSpec(t, spec)
	want := map[string]interface{}{"insecure": false, "insecure_skip_verify": false, "ca_file": "/etc/tsuga/pg-tls/ca.crt"}
	if got := receiverTLS(t, obj); !reflect.DeepEqual(got, want) {
		t.Fatalf("tls = %#v", got)
	}
	requireDatasourceSuffix(t, obj, " sslmode=verify-full sslrootcert=/etc/tsuga/pg-tls/ca.crt")
	vols, _, _ := unstructured.NestedSlice(obj.Object, "spec", "volumes")
	if len(vols) != 1 || vols[0].(map[string]interface{})["name"] != "pg-tls" {
		t.Fatalf("volumes = %#v", vols)
	}
	mounts, _, _ := unstructured.NestedSlice(obj.Object, "spec", "volumeMounts")
	wantMount := map[string]interface{}{"name": "pg-tls", "mountPath": "/etc/tsuga/pg-tls", "readOnly": true}
	if len(mounts) != 1 || !reflect.DeepEqual(mounts[0], wantMount) {
		t.Fatalf("mounts = %#v", mounts)
	}
}

func TestRenderPostgresCollectorVerifyFullSystemRoots(t *testing.T) {
	obj := renderPGSpec(t, pgSpec("verify-full"))
	if got := receiverTLS(t, obj); !reflect.DeepEqual(got, map[string]interface{}{"insecure": false, "insecure_skip_verify": false}) {
		t.Fatalf("tls = %#v", got)
	}
	requireDatasourceSuffix(t, obj, " sslmode=verify-full")
}

func TestRenderPostgresCollectorClientCert(t *testing.T) {
	spec := pgSpec("require")
	spec.TLS = &v1alpha1.PostgresTLSSpec{Volume: tlsSecretVolume(), CertFile: "tls.crt", KeyFile: "tls.key"}
	obj := renderPGSpec(t, spec)
	want := map[string]interface{}{
		"insecure": false, "insecure_skip_verify": true,
		"cert_file": "/etc/tsuga/pg-tls/tls.crt", "key_file": "/etc/tsuga/pg-tls/tls.key",
	}
	if got := receiverTLS(t, obj); !reflect.DeepEqual(got, want) {
		t.Fatalf("tls = %#v", got)
	}
	requireDatasourceSuffix(t, obj, " sslmode=require sslcert=/etc/tsuga/pg-tls/tls.crt sslkey=/etc/tsuga/pg-tls/tls.key")
	vols, _, _ := unstructured.NestedSlice(obj.Object, "spec", "volumes")
	if mode, _, _ := unstructured.NestedInt64(vols[0].(map[string]interface{}), "secret", "defaultMode"); mode != 0o440 {
		t.Fatalf("defaultMode = %o", mode)
	}
	if g, _, _ := unstructured.NestedInt64(obj.Object, "spec", "podSecurityContext", "fsGroup"); g != 10001 {
		t.Fatalf("fsGroup = %d", g)
	}
	if _, found, _ := unstructured.NestedFieldNoCopy(obj.Object, "spec", "podSecurityContext", "runAsGroup"); found {
		t.Fatal("unexpected runAsGroup")
	}
}

func TestRenderPostgresCollectorCertWithoutKeyFileHasNoGID(t *testing.T) {
	spec := pgSpec("verify-full")
	spec.TLS = &v1alpha1.PostgresTLSSpec{Volume: tlsSecretVolume(), CAFile: "ca.crt"}
	obj := renderPGSpec(t, spec)
	psc, _, _ := unstructured.NestedMap(obj.Object, "spec", "podSecurityContext")
	if _, ok := psc["fsGroup"]; ok {
		t.Fatal("unexpected fsGroup")
	}
}

func TestRenderPostgresCollectorKeepsUserDefaultMode(t *testing.T) {
	mode := int32(0o400)
	spec := pgSpec("require")
	spec.TLS = &v1alpha1.PostgresTLSSpec{
		Volume:   v1alpha1.PostgresTLSVolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: "pg-certs", DefaultMode: &mode}},
		CertFile: "tls.crt", KeyFile: "tls.key",
	}
	obj := renderPGSpec(t, spec)
	vols, _, _ := unstructured.NestedSlice(obj.Object, "spec", "volumes")
	if got, _, _ := unstructured.NestedInt64(vols[0].(map[string]interface{}), "secret", "defaultMode"); got != 0o400 {
		t.Fatalf("defaultMode = %o", got)
	}
}

func TestRenderPostgresCollectorPasswordSecretRef(t *testing.T) {
	spec := pgSpec("require")
	spec.PasswordSecretRef = &corev1.LocalObjectReference{Name: "dba-pw"}
	obj := renderPGSpec(t, spec)
	env, _, _ := unstructured.NestedSlice(obj.Object, "spec", "env")
	ref := env[0].(map[string]interface{})["valueFrom"].(map[string]interface{})["secretKeyRef"].(map[string]interface{})
	if env[0].(map[string]interface{})["name"] != "OTEL_MONITOR_PASSWORD" || ref["name"] != "dba-pw" || ref["key"] != "password" {
		t.Fatalf("env = %#v", env[0])
	}
}

func TestRenderPostgresSetupJobRoleAndPassword(t *testing.T) {
	spec := pgSpec("require")
	spec.Role = "dba_mon"
	spec.PasswordSecretRef = &corev1.LocalObjectReference{Name: "dba-pw"}
	env := envByName(renderJob(t, spec).Spec.Template.Spec.Containers[0])
	if env["MONITOR_ROLE"].Value != "dba_mon" || env["ADOPT_LEGACY_ROLE"].Value != "off" {
		t.Fatalf("env = %+v", env)
	}
	ref := env["OTEL_MONITOR_PASSWORD"].ValueFrom
	if ref == nil || ref.SecretKeyRef.Name != "dba-pw" || ref.SecretKeyRef.Key != "password" {
		t.Fatalf("OTEL_MONITOR_PASSWORD = %+v", env["OTEL_MONITOR_PASSWORD"])
	}

	env = envByName(renderJob(t, pgSpec("require")).Spec.Template.Spec.Containers[0])
	if env["MONITOR_ROLE"].Value != "otel_monitor" || env["ADOPT_LEGACY_ROLE"].Value != "on" {
		t.Fatalf("default env = %+v", env)
	}

	spec = pgSpec("require")
	spec.PasswordSecretRef = &corev1.LocalObjectReference{Name: "dba-pw"}
	if v := envByName(renderJob(t, spec).Spec.Template.Spec.Containers[0])["ADOPT_LEGACY_ROLE"].Value; v != "off" {
		t.Fatalf("ADOPT_LEGACY_ROLE with passwordSecretRef = %q", v)
	}
}

func TestRenderPostgresSetupJobTLS(t *testing.T) {
	spec := pgSpec("verify-full")
	spec.TLS = &v1alpha1.PostgresTLSSpec{Volume: tlsSecretVolume(), CAFile: "ca.crt", CertFile: "tls.crt", KeyFile: "tls.key"}
	job := renderJob(t, spec)
	pod := job.Spec.Template.Spec
	c := pod.Containers[0]
	env := envByName(c)
	for k, want := range map[string]string{
		"PGSSLMODE":     "verify-full",
		"PGSSLROOTCERT": "/etc/tsuga/pg-tls/ca.crt",
		"PGSSLCERT":     "/etc/tsuga/pg-tls/tls.crt",
		"PGSSLKEY":      "/etc/tsuga/pg-tls/tls.key",
	} {
		if env[k].Value != want {
			t.Fatalf("%s = %q, want %q", k, env[k].Value, want)
		}
	}
	var tlsVol *corev1.Volume
	for i := range pod.Volumes {
		if pod.Volumes[i].Name == "pg-tls" {
			tlsVol = &pod.Volumes[i]
		}
	}
	if tlsVol == nil || tlsVol.Secret == nil || tlsVol.Secret.SecretName != "pg-certs" ||
		tlsVol.Secret.DefaultMode == nil || *tlsVol.Secret.DefaultMode != 0o440 {
		t.Fatalf("tls volume = %+v", tlsVol)
	}
	wantMount := corev1.VolumeMount{Name: "pg-tls", MountPath: "/etc/tsuga/pg-tls", ReadOnly: true}
	if !slices.ContainsFunc(c.VolumeMounts, func(m corev1.VolumeMount) bool { return reflect.DeepEqual(m, wantMount) }) {
		t.Fatalf("mounts = %+v", c.VolumeMounts)
	}
	psc := pod.SecurityContext
	if psc == nil || psc.RunAsGroup != nil || psc.FSGroup == nil || *psc.FSGroup != 999 {
		t.Fatalf("pod security context = %+v", psc)
	}

	spec = pgSpec("verify-full")
	spec.TLS = &v1alpha1.PostgresTLSSpec{Volume: tlsSecretVolume(), CAFile: "ca.crt"}
	pod = renderJob(t, spec).Spec.Template.Spec
	if pod.SecurityContext != nil && (pod.SecurityContext.FSGroup != nil || pod.SecurityContext.RunAsGroup != nil) {
		t.Fatalf("no key file, want no group override: %+v", pod.SecurityContext)
	}
	if _, ok := envByName(pod.Containers[0])["PGSSLKEY"]; ok {
		t.Fatal("unexpected PGSSLKEY")
	}

	job = renderJob(t, pgSpec("require"))
	if _, ok := envByName(job.Spec.Template.Spec.Containers[0])["PGSSLROOTCERT"]; ok {
		t.Fatal("require must not set PGSSLROOTCERT")
	}
	if len(job.Spec.Template.Spec.Volumes) != 1 || len(job.Spec.Template.Spec.Containers[0].VolumeMounts) != 1 {
		t.Fatal("require without tls must not add a volume")
	}
}

func TestPostgresSetupSQLHasNoHardcodedRole(t *testing.T) {
	for i, line := range strings.Split(assets.PostgresSetupSQL, "\n") {
		if strings.Contains(line, "otel_monitor") && strings.TrimSpace(line) != `\set role otel_monitor` {
			t.Fatalf("line %d hardcodes otel_monitor: %q", i+1, line)
		}
	}
	if !strings.Contains(assets.PostgresSetupSQL, "'"+PostgresRoleMarker+"'") {
		t.Fatal("setup SQL must use PostgresRoleMarker")
	}
}

// TestDumpPostgresSetupScript writes the setup Job script to
// $PG_SETUP_SCRIPT_OUT for hack/test-postgres-setup.sh.
func TestDumpPostgresSetupScript(t *testing.T) {
	out := os.Getenv("PG_SETUP_SCRIPT_OUT")
	if out == "" {
		t.Skip("PG_SETUP_SCRIPT_OUT not set")
	}
	if err := os.WriteFile(out, []byte(postgresSetupScript), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestRenderPostgresCollectorSidecar(t *testing.T) {
	spec := pgSpec("disable")
	spec.Mode = PostgresCollectorSidecar
	obj, err := RenderPostgresCollector("orders-db", "orders", spec, "", testAgentEndpoint)
	if err != nil {
		t.Fatal(err)
	}
	if mode, _, _ := unstructuredString(obj, "spec", "mode"); mode != PostgresCollectorSidecar {
		t.Fatalf("mode = %q", mode)
	}
	if _, found, _ := unstructured.NestedFieldNoCopy(obj.Object, "spec", "replicas"); found {
		t.Fatal("sidecar collectors take no replicas")
	}
	if _, found, _ := unstructured.NestedFieldNoCopy(obj.Object, "spec", "podSecurityContext"); found {
		t.Fatal("sidecar injection drops podSecurityContext; it must not be rendered")
	}
	sc, _, _ := unstructured.NestedMap(obj.Object, "spec", "securityContext")
	if sc["runAsNonRoot"] != true || !reflect.DeepEqual(sc["seccompProfile"], map[string]interface{}{"type": "RuntimeDefault"}) {
		t.Fatalf("container securityContext = %#v", sc)
	}
	env, _, _ := unstructured.NestedSlice(obj.Object, "spec", "env")
	if optional, _, _ := unstructured.NestedBool(env[0].(map[string]interface{}), "valueFrom", "secretKeyRef", "optional"); !optional {
		t.Fatalf("password secretKeyRef must be optional in sidecar mode: %#v", env[0])
	}
	// An unset password fails the receiver's config validation and crashloops
	// the sidecar, which takes the database pod out of Ready.
	if pw, _, _ := unstructuredString(obj, "spec", "config", "receivers", "postgresql", "password"); !strings.HasPrefix(pw, "${env:OTEL_MONITOR_PASSWORD:-") {
		t.Fatalf("postgresql receiver password needs a default for a missing Secret: %q", pw)
	}
	endpoint, _, _ := unstructuredString(obj, "spec", "config", "receivers", "postgresql", "endpoint")
	if endpoint != "${env:OTEL_RESOURCE_ATTRIBUTES_POD_NAME}:6543" {
		t.Fatalf("postgresql receiver endpoint = %q", endpoint)
	}
	for i, ds := range sqlqueryDatasources(t, obj) {
		if !strings.HasPrefix(ds, "host=${env:OTEL_RESOURCE_ATTRIBUTES_POD_NAME} port=6543 ") {
			t.Fatalf("datasource %d = %q", i, ds)
		}
	}
	attrs, _, _ := unstructured.NestedSlice(obj.Object, "spec", "config", "processors", "resource/postgres", "attributes")
	for _, a := range attrs {
		if m := a.(map[string]interface{}); m["key"] == "server.address" && m["value"] != "orders-pg.rds.amazonaws.com" {
			t.Fatalf("server.address = %v, want spec.host", m["value"])
		}
	}
}

func TestRenderPostgresCollectorAddsExtensions(t *testing.T) {
	spec := pgSpec("require")
	spec.ExtraExtensions = &apiextensionsv1.JSON{Raw: []byte(`{"opamp":{"server":{"ws":{"endpoint":"ws://x:4320"}}}}`)}
	spec.ServiceExtensions = []string{"opamp"}
	obj, err := RenderPostgresCollector("orders-db", "orders", spec, "", testAgentEndpoint)
	if err != nil {
		t.Fatal(err)
	}
	endpoint, _, _ := unstructured.NestedString(obj.Object, "spec", "config", "extensions", "opamp", "server", "ws", "endpoint")
	if endpoint != "ws://x:4320" {
		t.Fatalf("opamp extension not merged: %q", endpoint)
	}
	exts, _, _ := unstructured.NestedSlice(obj.Object, "spec", "config", "service", "extensions")
	if !slices.Equal(exts, []interface{}{"opamp"}) {
		t.Fatalf("service.extensions = %v", exts)
	}
}

func TestRenderPostgresCollectorWithoutExtensionsUnchanged(t *testing.T) {
	obj := renderPG(t, "require")
	if _, found, _ := unstructured.NestedFieldNoCopy(obj.Object, "spec", "config", "extensions"); found {
		t.Fatal("extensions rendered without extraExtensions")
	}
	if _, found, _ := unstructured.NestedFieldNoCopy(obj.Object, "spec", "config", "service", "extensions"); found {
		t.Fatal("service.extensions rendered without serviceExtensions")
	}
}

func TestRenderPostgresCollectorExtensionsMalformed(t *testing.T) {
	spec := pgSpec("require")
	spec.ExtraExtensions = &apiextensionsv1.JSON{Raw: []byte(`[1]`)}
	if _, err := RenderPostgresCollector("orders-db", "orders", spec, "", testAgentEndpoint); err == nil {
		t.Fatal("want an error for a non-object extraExtensions")
	}
}
