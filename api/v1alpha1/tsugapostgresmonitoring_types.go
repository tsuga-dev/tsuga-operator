package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// PostgresProvisioningSpec controls who creates the monitoring role, the
// pg_stat_statements extension and the otel schema functions.
//
// A CR that omits this block, or gives mode: managed without a non-empty
// adminSecretRef, is rejected: it must either name an admin Secret or opt out
// with mode: manual. That rule is enforced via CEL rule on
// TsugaPostgresMonitoringSpec, since structural defaulting does not descend
// into an absent object.
type PostgresProvisioningSpec struct {
	// managed runs a setup Job with the admin credentials; manual leaves the
	// setup SQL to a DBA, who reads the generated monitor password Secret.
	// +kubebuilder:default=managed
	// +kubebuilder:validation:Enum=managed;manual
	// +optional
	Mode string `json:"mode,omitempty"`
	// Secret in this namespace with `username` and `password` keys for a role
	// able to create roles and extensions (superuser, rds_superuser,
	// cloudsqlsuperuser, azure_pg_admin). Only the setup Job pod reads it. The Secret must
	// carry the label observability.tsuga.com/postgres-admin=true, so creating
	// this resource cannot hand another Secret in the namespace to spec.host.
	// +optional
	AdminSecretRef *corev1.LocalObjectReference `json:"adminSecretRef,omitempty"`
}

// PostgresTLSVolumeSource is the volume that carries the TLS files. Exactly
// one source is set. Every Secret it mounts, directly or projected, must carry
// the label observability.tsuga.com/postgres-credentials=true, so creating
// this resource cannot hand another Secret in the namespace to the collector.
// +kubebuilder:validation:XValidation:rule="[has(self.secret), has(self.configMap), has(self.projected), has(self.csi)].filter(x, x).size() == 1",message="exactly one of secret, configMap, projected or csi must be set"
type PostgresTLSVolumeSource struct {
	// +optional
	Secret *corev1.SecretVolumeSource `json:"secret,omitempty"`
	// +optional
	ConfigMap *corev1.ConfigMapVolumeSource `json:"configMap,omitempty"`
	// +optional
	Projected *corev1.ProjectedVolumeSource `json:"projected,omitempty"`
	// +optional
	CSI *corev1.CSIVolumeSource `json:"csi,omitempty"`
}

// PostgresTLSSpec mounts a volume into the collector and the setup Job and
// names the CA bundle and client certificate files inside it. File paths are
// relative to the volume root.
type PostgresTLSSpec struct {
	Volume PostgresTLSVolumeSource `json:"volume"`
	// CA bundle used to verify the server certificate. Requires sslMode verify-full.
	// +kubebuilder:validation:Pattern=`^[A-Za-z0-9_][A-Za-z0-9_.-]*(/[A-Za-z0-9_][A-Za-z0-9_.-]*)*$`
	// +optional
	CAFile string `json:"caFile,omitempty"`
	// Client certificate for mutual TLS. Requires keyFile.
	// +kubebuilder:validation:Pattern=`^[A-Za-z0-9_][A-Za-z0-9_.-]*(/[A-Za-z0-9_][A-Za-z0-9_.-]*)*$`
	// +optional
	CertFile string `json:"certFile,omitempty"`
	// Client private key for mutual TLS. Requires certFile.
	// +kubebuilder:validation:Pattern=`^[A-Za-z0-9_][A-Za-z0-9_.-]*(/[A-Za-z0-9_][A-Za-z0-9_.-]*)*$`
	// +optional
	KeyFile string `json:"keyFile,omitempty"`
}

// TsugaPostgresMonitoringSpec points a dedicated collector at one Postgres
// server, in-cluster or managed.
// +kubebuilder:validation:XValidation:rule="has(self.provisioning) && (self.provisioning.mode != 'managed' || (has(self.provisioning.adminSecretRef) && size(self.provisioning.adminSecretRef.name) > 0))",message="provisioning.adminSecretRef is required when provisioning.mode is managed"
// +kubebuilder:validation:XValidation:rule="!has(self.tls) || self.sslMode != 'disable'",message="tls requires sslMode require or verify-full"
// +kubebuilder:validation:XValidation:rule="!has(self.tls) || !has(self.tls.caFile) || self.sslMode == 'verify-full'",message="tls.caFile requires sslMode verify-full"
// +kubebuilder:validation:XValidation:rule="!has(self.tls) || has(self.tls.caFile) || has(self.tls.certFile)",message="tls requires caFile or certFile"
// +kubebuilder:validation:XValidation:rule="!has(self.tls) || has(self.tls.certFile) == has(self.tls.keyFile)",message="tls.certFile and tls.keyFile must be set together"
// +kubebuilder:validation:XValidation:rule="self.sslMode != 'verify-full' || !has(self.provisioning) || self.provisioning.mode != 'managed' || (has(self.tls) && has(self.tls.caFile))",message="tls.caFile is required for verify-full when provisioning.mode is managed: the setup Job image has no system CA store"
// +kubebuilder:validation:XValidation:rule="!has(self.mode) || self.mode != 'sidecar' || (!has(self.tls) && self.sslMode != 'verify-full')",message="sidecar mode supports sslMode disable or require without tls: the sidecar connects by pod name, which the server certificate does not cover"
// +kubebuilder:validation:XValidation:rule="!has(self.mode) || self.mode != 'sidecar' || (!has(self.nodeSelector) && !has(self.tolerations))",message="nodeSelector and tolerations do not apply in sidecar mode: the collector runs in the database pod"
// +kubebuilder:validation:XValidation:rule="!has(self.mode) || self.mode != 'sidecar' || (!has(self.extraExtensions) && !has(self.serviceExtensions))",message="extraExtensions and serviceExtensions are not supported in sidecar mode: an extension the collector cannot start would keep the database pod NotReady"
type TsugaPostgresMonitoringSpec struct {
	// DNS name or IPv4 address of the server: a Service in-cluster, or the
	// managed endpoint. It is spliced into libpq DSNs, so the pattern keeps
	// out spaces, '=' and newlines, and requires dot-separated RFC 1123
	// labels (a dotted-decimal IPv4 address is a valid label sequence).
	// +kubebuilder:validation:MaxLength=253
	// +kubebuilder:validation:Pattern=`^[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?(\.[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?)*$`
	Host string `json:"host"`
	// +kubebuilder:default=5432
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=65535
	// +optional
	Port int32 `json:"port,omitempty"`
	// Database that holds the otel schema. pg_stat_statements is server-wide,
	// so this only picks where the functions live.
	// +kubebuilder:default=postgres
	// +kubebuilder:validation:Pattern=`^[A-Za-z_][A-Za-z0-9_$-]*$`
	// +optional
	Database string `json:"database,omitempty"`
	// TLS mode used by the collector and the setup Job. disable sends
	// plaintext; require encrypts without verifying the server certificate;
	// verify-full verifies the certificate chain and host name against
	// tls.caFile. Without tls.caFile, verify-full uses the collector's system
	// roots and is allowed only with provisioning.mode manual, since the setup
	// Job image has no system CA store.
	// +kubebuilder:default=require
	// +kubebuilder:validation:Enum=disable;require;verify-full
	// +optional
	SSLMode string `json:"sslMode,omitempty"`
	// Mounts the CA bundle and/or client certificate used for the connection.
	// +optional
	TLS *PostgresTLSSpec `json:"tls,omitempty"`
	// Name of the monitoring role. The operator manages only roles it created
	// itself, recognised by a marker comment on the role, and refuses to take
	// over a pre-existing role without it.
	// +kubebuilder:default=otel_monitor
	// +kubebuilder:validation:Pattern=`^[a-z_][a-z0-9_]{0,62}$`
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="role is immutable"
	// +optional
	Role string `json:"role,omitempty"`
	// Secret in this namespace with a `password` key for the monitoring role,
	// for servers that require a password the operator did not generate. The
	// Secret must carry the label observability.tsuga.com/postgres-credentials=true,
	// so creating this resource cannot hand another Secret in the namespace to
	// the collector.
	// +optional
	PasswordSecretRef *corev1.LocalObjectReference `json:"passwordSecretRef,omitempty"`
	// deployment runs the collector as its own pod, connecting to host:port.
	// sidecar renders an OpenTelemetry Operator sidecar collector that
	// connects to its own pod on port; add the annotation
	// sidecar.opentelemetry.io/inject: <name>-pg to the database pod
	// template. The resource must then live in the database's namespace,
	// since the sidecar reads the monitor Secret from there. host still
	// sets server.address and is where the setup Job connects. Sidecar mode
	// takes no tls, verify-full, nodeSelector or tolerations. Changing mode
	// recreates the collector.
	// +kubebuilder:default=deployment
	// +kubebuilder:validation:Enum=deployment;sidecar
	// +optional
	Mode string `json:"mode,omitempty"`
	// +optional
	Provisioning PostgresProvisioningSpec `json:"provisioning,omitempty"`
	// Collector image. Defaults to the cluster TsugaCollectorConfig
	// spec.image, then the operator's agent image.
	// +optional
	Image string `json:"image,omitempty"`
	// Collector CPU/memory. Defaults to requests 50m/64Mi and limits
	// 200m/256Mi; cluster spec.resources does not apply. Memory grows with
	// the number of statements in pg_stat_statements.
	// +optional
	Resources *corev1.ResourceRequirements `json:"resources,omitempty"`
	// Collector node selector. Cluster spec.nodeSelector does not apply.
	// +optional
	NodeSelector map[string]string `json:"nodeSelector,omitempty"`
	// Collector tolerations. Cluster spec.tolerations does not apply.
	// +optional
	Tolerations []corev1.Toleration `json:"tolerations,omitempty"`
	// Extensions added to this collector's config, keyed by component name.
	// A name the collector already defines is a render error.
	// +kubebuilder:pruning:PreserveUnknownFields
	// +kubebuilder:validation:Schemaless
	// +kubebuilder:validation:Type=object
	// +optional
	ExtraExtensions *apiextensionsv1.JSON `json:"extraExtensions,omitempty"`
	// Extension names appended to service.extensions.
	// +optional
	ServiceExtensions []string `json:"serviceExtensions,omitempty"`
}

// The 43 character cap keeps the setup Job name, <name>-pg-setup-<10 hex>,
// within the 63 characters of the job-name label Kubernetes puts on its pods.
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:path=tsugapostgresmonitorings,scope=Namespaced,singular=tsugapostgresmonitoring,shortName=tspg
// +kubebuilder:validation:XValidation:rule="size(self.metadata.name) <= 43",message="metadata.name must be at most 43 characters"
// +kubebuilder:printcolumn:name="Host",type=string,JSONPath=".spec.host"
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=".status.phase"
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=".metadata.creationTimestamp"
type TsugaPostgresMonitoring struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              TsugaPostgresMonitoringSpec `json:"spec,omitempty"`
	Status            CollectionStatus            `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type TsugaPostgresMonitoringList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []TsugaPostgresMonitoring `json:"items"`
}

func init() {
	SchemeBuilder.Register(&TsugaPostgresMonitoring{}, &TsugaPostgresMonitoringList{})
}
