package collection

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/yaml"

	"github.com/tsuga-dev/tsuga-operator/api/v1alpha1"
	"github.com/tsuga-dev/tsuga-operator/internal/collection/assets"
)

const (
	PostgresModeManaged = "managed"
	PostgresModeManual  = "manual"

	// PostgresMonitoringLabel carries the owning CR's name on the Secret,
	// ConfigMap and setup Jobs, so the controller can list its Jobs.
	PostgresMonitoringLabel = "observability.tsuga.com/postgres-monitoring"
	// PostgresAdminSecretLabel must be "true" on a Secret before a CR may
	// name it as adminSecretRef. Otherwise anyone able to create the CR could
	// send any Secret in the namespace to a host they choose.
	PostgresAdminSecretLabel = "observability.tsuga.com/postgres-admin"

	postgresSSLRequire = "require"
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

// RenderPostgresCollector builds the deployment-mode OpenTelemetryCollector
// that scrapes one Postgres server and forwards to the node agent. The
// collector holds no Tsuga credentials: the agent adds cluster enrichment and
// the API key. spec must be resolved with ResolvePostgres.
func RenderPostgresCollector(name, namespace string, spec v1alpha1.TsugaPostgresMonitoringSpec, image, agentEndpoint string) (*unstructured.Unstructured, error) {
	r := strings.NewReplacer(
		"__PG_HOST__", spec.Host,
		"__PG_PORT__", strconv.Itoa(int(spec.Port)),
		"__PG_DATABASE__", spec.Database,
		"__PG_SSLMODE__", spec.SSLMode,
		"__AGENT_ENDPOINT__", agentEndpoint,
	)
	var config map[string]interface{}
	if err := yaml.Unmarshal([]byte(r.Replace(assets.PostgresConfig)), &config); err != nil {
		return nil, fmt.Errorf("parse postgres collector config: %w", err)
	}
	if spec.SSLMode == postgresSSLRequire {
		// Matches libpq sslmode=require: encrypt, don't verify the certificate.
		_ = unstructured.SetNestedMap(config, map[string]interface{}{
			"insecure":             false,
			"insecure_skip_verify": true,
		}, "receivers", "postgresql", "tls")
	}
	if image == "" {
		image = defaultAgentImage
	}
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": collectorAPIVersion,
		"kind":       collectorKind,
		"metadata": map[string]interface{}{
			"name":      name + "-pg",
			"namespace": namespace,
		},
		"spec": map[string]interface{}{
			"mode":     modeDeployment,
			"replicas": int64(1),
			"image":    image,
			"config":   config,
			"env": []interface{}{
				map[string]interface{}{
					"name": "OTEL_MONITOR_PASSWORD",
					"valueFrom": map[string]interface{}{
						"secretKeyRef": map[string]interface{}{
							"name": PostgresMonitorSecretName(name),
							"key":  postgresPasswordKey,
						},
					},
				},
				map[string]interface{}{
					"name": "K8S_NODE_NAME",
					"valueFrom": map[string]interface{}{
						"fieldRef": map[string]interface{}{"fieldPath": "spec.nodeName"},
					},
				},
			},
			"resources": map[string]interface{}{
				"requests": map[string]interface{}{"cpu": "50m", "memory": "64Mi"},
				"limits":   map[string]interface{}{"cpu": "200m", "memory": "256Mi"},
			},
			"podSecurityContext": map[string]interface{}{
				"runAsNonRoot":   true,
				"seccompProfile": map[string]interface{}{"type": "RuntimeDefault"},
			},
			"securityContext": map[string]interface{}{
				"allowPrivilegeEscalation": false,
				"capabilities":             map[string]interface{}{"drop": []interface{}{"ALL"}},
			},
		},
	}}, nil
}

const (
	postgresSetupImage  = "postgres:17"
	postgresSetupSQLKey = "monitoring-setup.sql"
	postgresSetupVolume = "setup"
	postgresUID         = int64(999)
)

// postgresSetupScript is the chart's setup sequence driven by libpq env vars,
// so no credential appears in the pod spec. psql only interpolates :'pw' in
// script or stdin input, not in -c, hence the echo.
const postgresSetupScript = `set -e
until pg_isready; do sleep 2; done
psql -v ON_ERROR_STOP=1 -f /scripts/` + postgresSetupSQLKey + `
echo "ALTER USER otel_monitor WITH PASSWORD :'pw'" | psql -v ON_ERROR_STOP=1 -v pw="$OTEL_MONITOR_PASSWORD"
PGUSER=otel_monitor PGPASSWORD="$OTEL_MONITOR_PASSWORD" psql -v ON_ERROR_STOP=1 -c "SELECT 1" > /dev/null
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
	_, _ = fmt.Fprintf(h, "%s\x00%d\x00%s\x00%s\x00%s\x00",
		spec.Host, spec.Port, spec.Database, spec.SSLMode, spec.Provisioning.AdminSecretRef.Name)
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

// RenderPostgresSetupJob returns the Job that creates otel_monitor, the
// pg_stat_statements extension and the otel functions, then sets the role's
// password from the monitor Secret. spec must be resolved with
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
					Containers: []corev1.Container{{
						Name:    "setup",
						Image:   postgresSetupImage,
						Command: []string{"/bin/sh", "-c", postgresSetupScript},
						Resources: corev1.ResourceRequirements{
							Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("50m"), corev1.ResourceMemory: resource.MustParse("64Mi")},
							Limits:   corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("500m"), corev1.ResourceMemory: resource.MustParse("256Mi")},
						},
						Env: []corev1.EnvVar{
							{Name: "PGHOST", Value: spec.Host},
							{Name: "PGPORT", Value: strconv.Itoa(int(spec.Port))},
							{Name: "PGDATABASE", Value: spec.Database},
							{Name: "PGSSLMODE", Value: spec.SSLMode},
							secretEnv("PGUSER", admin, "username"),
							secretEnv("PGPASSWORD", admin, postgresPasswordKey),
							secretEnv("OTEL_MONITOR_PASSWORD", PostgresMonitorSecretName(name), postgresPasswordKey),
						},
						VolumeMounts: []corev1.VolumeMount{{Name: postgresSetupVolume, MountPath: "/scripts", ReadOnly: true}},
						SecurityContext: &corev1.SecurityContext{
							RunAsNonRoot:             &nonRoot,
							RunAsUser:                &uid,
							AllowPrivilegeEscalation: &noEscalation,
							Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
							SeccompProfile:           &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
						},
					}},
					Volumes: []corev1.Volume{{
						Name: postgresSetupVolume,
						VolumeSource: corev1.VolumeSource{
							ConfigMap: &corev1.ConfigMapVolumeSource{
								LocalObjectReference: corev1.LocalObjectReference{Name: postgresSetupName(name)},
							},
						},
					}},
				},
			},
		},
	}, nil
}
