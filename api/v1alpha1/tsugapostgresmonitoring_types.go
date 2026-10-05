package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// PostgresProvisioningSpec controls who creates the otel_monitor role, the
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

// TsugaPostgresMonitoringSpec points a dedicated collector at one Postgres
// server, in-cluster or managed.
// +kubebuilder:validation:XValidation:rule="has(self.provisioning) && (self.provisioning.mode != 'managed' || (has(self.provisioning.adminSecretRef) && size(self.provisioning.adminSecretRef.name) > 0))",message="provisioning.adminSecretRef is required when provisioning.mode is managed"
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
	// require encrypts without verifying the server certificate.
	// +kubebuilder:default=require
	// +kubebuilder:validation:Enum=disable;require
	// +optional
	SSLMode string `json:"sslMode,omitempty"`
	// +optional
	Provisioning PostgresProvisioningSpec `json:"provisioning,omitempty"`
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
