package collection

import (
	"cmp"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"strconv"
	"strings"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/yaml"

	"github.com/tsuga-dev/tsuga-operator/api/v1alpha1"
	"github.com/tsuga-dev/tsuga-operator/internal/collection/assets"
)

const (
	PostgresModeManaged = "managed"
	PostgresModeManual  = "manual"

	PostgresCollectorSidecar = "sidecar"

	// PostgresMonitoringLabel carries the owning CR's name on the Secret,
	// ConfigMap and setup Jobs, so the controller can list its Jobs.
	PostgresMonitoringLabel = "observability.tsuga.com/postgres-monitoring"
	// PostgresAdminSecretLabel must be "true" on a Secret before a CR may
	// name it as adminSecretRef. Otherwise anyone able to create the CR could
	// send any Secret in the namespace to a host they choose.
	PostgresAdminSecretLabel = "observability.tsuga.com/postgres-admin"
	// PostgresCredentialsSecretLabel must be "true" on a Secret before a CR
	// may name it as passwordSecretRef or mount it through tls.volume, for the
	// same reason.
	PostgresCredentialsSecretLabel = "observability.tsuga.com/postgres-credentials"

	PostgresDefaultRole = "otel_monitor"
	// PostgresRoleMarker is the comment the setup SQL puts on a role it
	// created. Only roles carrying it get their password changed.
	PostgresRoleMarker = "managed-by: tsuga-operator"
	// PostgresTLSMountPath is where the TLS volume is mounted, read-only, in
	// the collector and the setup Job.
	PostgresTLSMountPath = "/etc/tsuga/pg-tls"

	postgresTLSVolume = "pg-tls"
	// postgresCollectorGID is the group the contrib collector image runs as.
	postgresCollectorGID = int64(10001)
	// postgresKeyFileMode lets the group read a mounted private key.
	postgresKeyFileMode = int32(0o440)

	postgresSSLDisable    = "disable"
	postgresSSLRequire    = "require"
	postgresSSLVerifyFull = "verify-full"
	// postgresPasswordKey is the key holding a password in both the monitor
	// Secret and the admin Secret.
	postgresPasswordKey = "password"
)

// ResolvePostgres fills the defaults the CRD also declares, for callers that
// never went through API server defaulting (tests, and the zero value).
func ResolvePostgres(spec v1alpha1.TsugaPostgresMonitoringSpec) v1alpha1.TsugaPostgresMonitoringSpec {
	if spec.Port == 0 {
		spec.Port = 5432
	}
	if spec.Database == "" {
		spec.Database = "postgres"
	}
	if spec.SSLMode == "" {
		spec.SSLMode = postgresSSLRequire
	}
	if spec.Role == "" {
		spec.Role = PostgresDefaultRole
	}
	if spec.Mode == "" {
		spec.Mode = modeDeployment
	}
	if spec.Provisioning.Mode == "" {
		spec.Provisioning.Mode = PostgresModeManaged
	}
	return spec
}

// PostgresMonitorSecretName is the Secret holding the generated otel_monitor
// password.
func PostgresMonitorSecretName(name string) string {
	return name + "-pg-monitor"
}

// PostgresPasswordSecretName is the Secret holding the monitor role's
// password: the user's passwordSecretRef, else the generated one.
func PostgresPasswordSecretName(spec v1alpha1.TsugaPostgresMonitoringSpec, name string) string {
	if spec.PasswordSecretRef != nil {
		return spec.PasswordSecretRef.Name
	}
	return PostgresMonitorSecretName(name)
}

func postgresTLSPath(file string) string {
	return PostgresTLSMountPath + "/" + file
}

// PostgresTLSSecretNames returns the Secrets spec.tls.volume mounts, directly
// or through a projected volume.
func PostgresTLSSecretNames(spec v1alpha1.TsugaPostgresMonitoringSpec) []string {
	if spec.TLS == nil {
		return nil
	}
	var names []string
	if s := spec.TLS.Volume.Secret; s != nil {
		names = append(names, s.SecretName)
	}
	if p := spec.TLS.Volume.Projected; p != nil {
		for _, src := range p.Sources {
			if src.Secret != nil {
				names = append(names, src.Secret.Name)
			}
		}
	}
	return names
}

// postgresTLSVolumeAndMount returns the volume carrying the TLS files and its
// read-only mount, or nil when spec.TLS is unset. A mounted key gets mode 0440
// unless the user chose one, so the collector group can read it.
func postgresTLSVolumeAndMount(spec v1alpha1.TsugaPostgresMonitoringSpec) (*corev1.Volume, *corev1.VolumeMount) {
	if spec.TLS == nil {
		return nil, nil
	}
	src := spec.TLS.Volume
	vs := corev1.VolumeSource{
		Secret:    src.Secret.DeepCopy(),
		ConfigMap: src.ConfigMap.DeepCopy(),
		Projected: src.Projected.DeepCopy(),
		CSI:       src.CSI.DeepCopy(),
	}
	if spec.TLS.KeyFile != "" {
		mode := postgresKeyFileMode
		switch {
		case vs.Secret != nil && vs.Secret.DefaultMode == nil:
			vs.Secret.DefaultMode = &mode
		case vs.ConfigMap != nil && vs.ConfigMap.DefaultMode == nil:
			vs.ConfigMap.DefaultMode = &mode
		case vs.Projected != nil && vs.Projected.DefaultMode == nil:
			vs.Projected.DefaultMode = &mode
		}
	}
	return &corev1.Volume{Name: postgresTLSVolume, VolumeSource: vs},
		&corev1.VolumeMount{Name: postgresTLSVolume, MountPath: PostgresTLSMountPath, ReadOnly: true}
}

// postgresTLSSettings returns the receiver tls map and the DSN sslmode tail
// (the sslmode value plus any sslrootcert, sslcert and sslkey) for spec.
func postgresTLSSettings(spec v1alpha1.TsugaPostgresMonitoringSpec) (map[string]interface{}, string) {
	if spec.SSLMode == postgresSSLDisable {
		return map[string]interface{}{"insecure": true}, spec.SSLMode
	}
	// Matches libpq: require encrypts without verifying the certificate.
	// verify-full must say false: the receiver defaults it to true.
	tls := map[string]interface{}{
		"insecure":             false,
		"insecure_skip_verify": spec.SSLMode == postgresSSLRequire,
	}
	dsn := spec.SSLMode
	if spec.TLS == nil {
		return tls, dsn
	}
	if spec.SSLMode == postgresSSLVerifyFull && spec.TLS.CAFile != "" {
		tls["ca_file"] = postgresTLSPath(spec.TLS.CAFile)
		dsn += " sslrootcert=" + postgresTLSPath(spec.TLS.CAFile)
	}
	if spec.TLS.CertFile != "" {
		tls["cert_file"] = postgresTLSPath(spec.TLS.CertFile)
		dsn += " sslcert=" + postgresTLSPath(spec.TLS.CertFile)
	}
	if spec.TLS.KeyFile != "" {
		tls["key_file"] = postgresTLSPath(spec.TLS.KeyFile)
		dsn += " sslkey=" + postgresTLSPath(spec.TLS.KeyFile)
	}
	return tls, dsn
}

// RenderPostgresCollector builds the OpenTelemetryCollector that scrapes one
// Postgres server and forwards to the node agent. A sidecar collector connects
// to its own pod, by the pod name the OpenTelemetry Operator sets. The
// collector holds no Tsuga credentials: the agent adds cluster enrichment and
// the API key. spec must be resolved with ResolvePostgres.
func RenderPostgresCollector(name, namespace string, spec v1alpha1.TsugaPostgresMonitoringSpec, image, agentEndpoint string) (*unstructured.Unstructured, error) {
	tlsConfig, dsnTLS := postgresTLSSettings(spec)
	connectHost := spec.Host
	if spec.Mode == PostgresCollectorSidecar {
		connectHost = "${env:OTEL_RESOURCE_ATTRIBUTES_POD_NAME}"
	}
	// The values spliced into the config are restricted by CRD patterns (host,
	// database, role, TLS file paths), so plain string substitution cannot
	// inject extra DSN or YAML tokens.
	r := strings.NewReplacer(
		"__PG_USER__", spec.Role,
		"__PG_CONNECT_HOST__", connectHost,
		"__PG_HOST__", spec.Host,
		"__PG_PORT__", strconv.Itoa(int(spec.Port)),
		"__PG_DATABASE__", spec.Database,
		"__PG_SSLMODE__", dsnTLS,
		"__AGENT_ENDPOINT__", agentEndpoint,
	)
	var config map[string]interface{}
	if err := yaml.Unmarshal([]byte(r.Replace(assets.PostgresConfig)), &config); err != nil {
		return nil, fmt.Errorf("parse postgres collector config: %w", err)
	}
	_ = unstructured.SetNestedMap(config, tlsConfig, "receivers", "postgresql", "tls")
	extensions, err := decodeExtras("extraExtensions", spec.ExtraExtensions)
	if err != nil {
		return nil, err
	}
	if err := addExtras(config, "extensions", "extension", extensions); err != nil {
		return nil, err
	}
	if len(spec.ServiceExtensions) > 0 {
		service := nestedMap(config, "service")
		service["extensions"] = appendMissing(sliceOf(service["extensions"]), spec.ServiceExtensions)
	}
	image = cmp.Or(spec.Image, image, defaultAgentImage)
	passwordEnv := func(env string) map[string]interface{} {
		return map[string]interface{}{
			"name": env,
			"valueFrom": map[string]interface{}{
				"secretKeyRef": map[string]interface{}{
					"name": PostgresPasswordSecretName(spec, name),
					"key":  postgresPasswordKey,
				},
			},
		}
	}
	collectorSpec := defaultCollectorPodFields()
	maps.Copy(collectorSpec, map[string]interface{}{
		"mode":     spec.Mode,
		"replicas": int64(1),
		"image":    image,
		"config":   config,
		"env": []interface{}{
			passwordEnv("OTEL_MONITOR_PASSWORD"),
			// lib/pq reads PGPASSWORD for the sql_query receivers, whose
			// unquoted key=value DSNs cannot hold an arbitrary password.
			passwordEnv("PGPASSWORD"),
			map[string]interface{}{
				"name": "K8S_NODE_NAME",
				"valueFrom": map[string]interface{}{
					"fieldRef": map[string]interface{}{"fieldPath": "spec.nodeName"},
				},
			},
		},
	})
	if spec.Resources != nil {
		resources, err := runtime.DefaultUnstructuredConverter.ToUnstructured(spec.Resources)
		if err != nil {
			return nil, fmt.Errorf("convert resources: %w", err)
		}
		collectorSpec["resources"] = resources
	}
	if spec.TLS != nil && spec.TLS.KeyFile != "" {
		// libpq and lib/pq accept a root-owned key up to 0640; fsGroup makes
		// the 0440 key readable to the non-root UID.
		collectorSpec["podSecurityContext"].(map[string]interface{})["fsGroup"] = postgresCollectorGID
	}
	placement, err := podPlacement(v1alpha1.CollectorPodSpec{NodeSelector: spec.NodeSelector, Tolerations: spec.Tolerations}, v1alpha1.TsugaCollectorConfigSpec{})
	if err != nil {
		return nil, err
	}
	collector := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": collectorAPIVersion,
		"kind":       collectorKind,
		"metadata": map[string]interface{}{
			"name":      name + "-pg",
			"namespace": namespace,
		},
		"spec": collectorSpec,
	}}
	if volume, mount := postgresTLSVolumeAndMount(spec); volume != nil {
		volumeObj, err := runtime.DefaultUnstructuredConverter.ToUnstructured(volume)
		if err != nil {
			return nil, fmt.Errorf("convert tls volume: %w", err)
		}
		mountObj, err := runtime.DefaultUnstructuredConverter.ToUnstructured(mount)
		if err != nil {
			return nil, fmt.Errorf("convert tls volume mount: %w", err)
		}
		_ = unstructured.SetNestedSlice(collector.Object, []interface{}{volumeObj}, "spec", "volumes")
		_ = unstructured.SetNestedSlice(collector.Object, []interface{}{mountObj}, "spec", "volumeMounts")
	}
	if spec.Mode == PostgresCollectorSidecar {
		hardenSidecar(collector.Object)
	}
	for key, value := range placement {
		_ = unstructured.SetNestedField(collector.Object, value, "spec", key)
	}
	return collector, nil
}

// hardenSidecar adapts a rendered collector to sidecar injection, which
// copies only container-level fields into the database pod. The pod security
// context moves onto the container, and the password Secret is optional, so a
// deleted resource cannot keep the database pod from restarting.
func hardenSidecar(collector map[string]interface{}) {
	unstructured.RemoveNestedField(collector, "spec", "replicas")
	moveSecurityContextToContainer(collector["spec"].(map[string]interface{}))
	env, _, _ := unstructured.NestedSlice(collector, "spec", "env")
	for _, e := range env {
		if ref, ok, _ := unstructured.NestedMap(e.(map[string]interface{}), "valueFrom", "secretKeyRef"); ok {
			ref["optional"] = true
			_ = unstructured.SetNestedMap(e.(map[string]interface{}), ref, "valueFrom", "secretKeyRef")
		}
	}
	_ = unstructured.SetNestedSlice(collector, env, "spec", "env")
}

// moveSecurityContextToContainer replaces the default pod security context,
// which sidecar injection drops, with the same settings on the container.
func moveSecurityContextToContainer(spec map[string]interface{}) {
	delete(spec, "podSecurityContext")
	_ = unstructured.SetNestedField(spec, true, "securityContext", "runAsNonRoot")
	_ = unstructured.SetNestedField(spec, "RuntimeDefault", "securityContext", "seccompProfile", "type")
}

const (
	postgresSetupImage  = "postgres:17"
	postgresSetupSQLKey = "monitoring-setup.sql"
	postgresSetupVolume = "setup"
	postgresUID         = int64(999)
)

// postgresSetupScript runs the setup SQL driven by libpq env vars, so no
// credential appears in the pod spec. The password reaches psql's \password on
// stdin, which sends the server a hash only.
//
// proven is on once a login with the Secret's password succeeds and a login
// with a random one fails: under pg_hba trust or cert auth any password logs
// in, which proves nothing. Only a proven role is adopted (adopt=on marks a
// pre-marker otel_monitor) or, when unmarked, given pg_monitor.
//
// pg_isready reports a client-side TLS error as "no response", so the wait is
// bounded: after two minutes psql runs anyway and prints the real error.
const postgresSetupScript = `set -e
i=0
until pg_isready || [ "$i" -ge 60 ]; do i=$((i + 1)); sleep 2; done
proven=off
if PGUSER="$MONITOR_ROLE" PGPASSWORD="$OTEL_MONITOR_PASSWORD" psql -c "SELECT 1" > /dev/null 2>&1 &&
  ! PGUSER="$MONITOR_ROLE" PGPASSWORD="$(cat /proc/sys/kernel/random/uuid)" psql -c "SELECT 1" > /dev/null 2>&1; then
  proven=on
fi
adopt=off
if [ "$ADOPT_LEGACY_ROLE" = on ]; then adopt=$proven; fi
printf '%s\n%s\n' "$OTEL_MONITOR_PASSWORD" "$OTEL_MONITOR_PASSWORD" |
  psql -v ON_ERROR_STOP=1 -v role="$MONITOR_ROLE" -v set_password=on -v proven="$proven" -v adopt="$adopt" -f /scripts/` + postgresSetupSQLKey + `
if ! PGUSER="$MONITOR_ROLE" PGPASSWORD="$OTEL_MONITOR_PASSWORD" psql -c "SELECT 1" > /dev/null; then
  echo "role $MONITOR_ROLE exists, is not managed by tsuga-operator, and the password in the Secret does not match" >&2
  exit 1
fi
usable=$(PGUSER="$MONITOR_ROLE" PGPASSWORD="$OTEL_MONITOR_PASSWORD" psql -Atc "SELECT pg_has_role('pg_monitor', 'MEMBER') AND has_schema_privilege('otel', 'USAGE') AND has_function_privilege('otel.pg_top_queries()', 'EXECUTE')" || true)
if [ "$usable" != t ]; then
  echo "role $MONITOR_ROLE is not in pg_monitor or cannot use the otel schema; when another admin created otel, run setup as that admin" >&2
  exit 1
fi
`

func postgresLabels(name string) map[string]string {
	return map[string]string{PostgresMonitoringLabel: name}
}

func postgresSetupName(name string) string {
	return name + "-pg-setup"
}

// RenderPostgresMonitorSecret returns the otel_monitor password Secret with a
// fresh random password. The controller only ever creates it, so the password
// is generated once per Secret.
func RenderPostgresMonitorSecret(name, namespace string) *corev1.Secret {
	return &corev1.Secret{
		TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Secret"},
		ObjectMeta: metav1.ObjectMeta{
			Name:      PostgresMonitorSecretName(name),
			Namespace: namespace,
			Labels:    postgresLabels(name),
		},
		Type:       corev1.SecretTypeOpaque,
		StringData: map[string]string{postgresPasswordKey: rand.Text()},
	}
}

// RenderPostgresSetupConfigMap returns the ConfigMap the setup Job mounts the
// provisioning SQL from.
func RenderPostgresSetupConfigMap(name, namespace string) *corev1.ConfigMap {
	return &corev1.ConfigMap{
		TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "ConfigMap"},
		ObjectMeta: metav1.ObjectMeta{
			Name:      postgresSetupName(name),
			Namespace: namespace,
			Labels:    postgresLabels(name),
		},
		Data: map[string]string{postgresSetupSQLKey: assets.PostgresSetupSQL},
	}
}

// postgresSetupHash names the setup Job after everything that changes what it
// does. Jobs are immutable, so a new name is how a changed input re-runs.
// Callers must resolve spec.Provisioning.AdminSecretRef to non-nil first.
func postgresSetupHash(spec v1alpha1.TsugaPostgresMonitoringSpec) string {
	h := sha256.New()
	passwordSecret := ""
	if spec.PasswordSecretRef != nil {
		passwordSecret = spec.PasswordSecretRef.Name
	}
	tls, _ := json.Marshal(spec.TLS)
	_, _ = fmt.Fprintf(h, "%s\x00%d\x00%s\x00%s\x00%s\x00%s\x00%s\x00%s\x00",
		spec.Host, spec.Port, spec.Database, spec.SSLMode, spec.Provisioning.AdminSecretRef.Name,
		spec.Role, passwordSecret, tls)
	_, _ = h.Write([]byte(assets.PostgresSetupSQL))
	return hex.EncodeToString(h.Sum(nil))[:10]
}

func secretEnv(name, secret, key string) corev1.EnvVar {
	return corev1.EnvVar{Name: name, ValueFrom: &corev1.EnvVarSource{
		SecretKeyRef: &corev1.SecretKeySelector{
			LocalObjectReference: corev1.LocalObjectReference{Name: secret},
			Key:                  key,
		},
	}}
}

// RenderPostgresSetupJob returns the Job that creates the monitoring role, the
// pg_stat_statements extension and the otel functions, and sets the role's
// password when the operator created the role. spec must be resolved with
// ResolvePostgres.
func RenderPostgresSetupJob(name, namespace string, spec v1alpha1.TsugaPostgresMonitoringSpec) (*batchv1.Job, error) {
	if spec.Provisioning.AdminSecretRef == nil {
		return nil, errors.New("provisioning.adminSecretRef is required when provisioning.mode is managed")
	}
	admin := spec.Provisioning.AdminSecretRef.Name
	backoffLimit := int32(3)
	deadline := int64(600)
	nonRoot, noEscalation, noToken, uid := true, false, false, postgresUID
	labels := postgresLabels(name)
	adopt := "off"
	if spec.Role == PostgresDefaultRole && spec.PasswordSecretRef == nil {
		adopt = "on"
	}
	env := []corev1.EnvVar{
		{Name: "PGHOST", Value: spec.Host},
		{Name: "PGPORT", Value: strconv.Itoa(int(spec.Port))},
		{Name: "PGDATABASE", Value: spec.Database},
		{Name: "PGSSLMODE", Value: spec.SSLMode},
		secretEnv("PGUSER", admin, "username"),
		secretEnv("PGPASSWORD", admin, postgresPasswordKey),
		{Name: "MONITOR_ROLE", Value: spec.Role},
		secretEnv("OTEL_MONITOR_PASSWORD", PostgresPasswordSecretName(spec, name), postgresPasswordKey),
		{Name: "ADOPT_LEGACY_ROLE", Value: adopt},
	}
	// The CRD requires caFile for managed verify-full: the image has no
	// system CA store for PGSSLROOTCERT=system to use.
	if spec.TLS != nil && spec.TLS.CAFile != "" {
		env = append(env, corev1.EnvVar{Name: "PGSSLROOTCERT", Value: postgresTLSPath(spec.TLS.CAFile)})
	}
	if spec.TLS != nil && spec.TLS.CertFile != "" {
		env = append(env, corev1.EnvVar{Name: "PGSSLCERT", Value: postgresTLSPath(spec.TLS.CertFile)})
	}
	if spec.TLS != nil && spec.TLS.KeyFile != "" {
		env = append(env, corev1.EnvVar{Name: "PGSSLKEY", Value: postgresTLSPath(spec.TLS.KeyFile)})
	}
	mounts := []corev1.VolumeMount{{Name: postgresSetupVolume, MountPath: "/scripts", ReadOnly: true}}
	volumes := []corev1.Volume{{
		Name: postgresSetupVolume,
		VolumeSource: corev1.VolumeSource{
			ConfigMap: &corev1.ConfigMapVolumeSource{
				LocalObjectReference: corev1.LocalObjectReference{Name: postgresSetupName(name)},
			},
		},
	}}
	if volume, mount := postgresTLSVolumeAndMount(spec); volume != nil {
		volumes = append(volumes, *volume)
		mounts = append(mounts, *mount)
	}
	var podSecurityContext *corev1.PodSecurityContext
	if spec.TLS != nil && spec.TLS.KeyFile != "" {
		// libpq accepts a root-owned key up to 0640; fsGroup makes the 0440
		// key readable to UID 999.
		gid := postgresUID
		podSecurityContext = &corev1.PodSecurityContext{FSGroup: &gid}
	}
	return &batchv1.Job{
		TypeMeta: metav1.TypeMeta{APIVersion: "batch/v1", Kind: "Job"},
		ObjectMeta: metav1.ObjectMeta{
			Name:      postgresSetupName(name) + "-" + postgresSetupHash(spec),
			Namespace: namespace,
			Labels:    labels,
		},
		Spec: batchv1.JobSpec{
			BackoffLimit:          &backoffLimit,
			ActiveDeadlineSeconds: &deadline,
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec: corev1.PodSpec{
					RestartPolicy:                corev1.RestartPolicyNever,
					AutomountServiceAccountToken: &noToken,
					SecurityContext:              podSecurityContext,
					Containers: []corev1.Container{{
						Name:    "setup",
						Image:   postgresSetupImage,
						Command: []string{"/bin/sh", "-c", postgresSetupScript},
						Resources: corev1.ResourceRequirements{
							Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("50m"), corev1.ResourceMemory: resource.MustParse("64Mi")},
							Limits:   corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("500m"), corev1.ResourceMemory: resource.MustParse("256Mi")},
						},
						Env:          env,
						VolumeMounts: mounts,
						SecurityContext: &corev1.SecurityContext{
							RunAsNonRoot:             &nonRoot,
							RunAsUser:                &uid,
							AllowPrivilegeEscalation: &noEscalation,
							Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
							SeccompProfile:           &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
						},
					}},
					Volumes: volumes,
				},
			},
		},
	}, nil
}
