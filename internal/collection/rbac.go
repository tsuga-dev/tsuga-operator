package collection

import (
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/tsuga-dev/tsuga-operator/api/v1alpha1"
)

const (
	// collectorServiceAccountSuffix is the suffix the OTel Operator appends to
	// a collector's name when it auto-creates that collector's ServiceAccount
	// (i.e. "<collector-name>-collector").
	collectorServiceAccountSuffix = "-collector"

	// collectorRBACPrefix prefixes each collector's ClusterRole and
	// ClusterRoleBinding name ("tsuga-operator-collector-tsuga-agent", ...).
	collectorRBACPrefix = "tsuga-operator-collector-"
)

// AgentServiceAccountName, GatewayServiceAccountName and
// ScraperServiceAccountName are the ServiceAccount names the OTel Operator
// creates for the agent, gateway and scraper collectors.
const (
	AgentServiceAccountName   = agentName + collectorServiceAccountSuffix
	GatewayServiceAccountName = gatewayName + collectorServiceAccountSuffix
	ScraperServiceAccountName = scraperName + collectorServiceAccountSuffix
)

var readVerbs = []string{"get", "list", "watch"}

// k8sAttributesRules is what the k8s_attributes processor, present in every
// collector, reads to resolve pod metadata: pods and namespaces, plus
// ReplicaSets and Jobs to walk up to the owning Deployment and CronJob.
func k8sAttributesRules() []rbacv1.PolicyRule {
	return []rbacv1.PolicyRule{
		{APIGroups: []string{""}, Resources: []string{"pods", "namespaces"}, Verbs: readVerbs},
		{APIGroups: []string{"apps"}, Resources: []string{"replicasets"}, Verbs: readVerbs},
		{APIGroups: []string{"batch"}, Resources: []string{"jobs"}, Verbs: readVerbs},
	}
}

// collectorRules maps each collector to the cluster access its receivers
// need, on top of k8sAttributesRules. The operator's manager ClusterRole must
// hold the union of these, or the Kubernetes RBAC escalation check rejects
// creating the roles; the //+kubebuilder:rbac markers on the
// TsugaCollectorConfig controller keep the two in sync.
var collectorRules = map[string][]rbacv1.PolicyRule{
	// kubelet_stats authenticates to the kubelet with its ServiceAccount
	// token. nodes/proxy is needed on top of nodes/stats because the agent
	// enables extra_metadata_labels and the request/limit utilization metrics,
	// which read the kubelet's /pods endpoint.
	agentName: {
		{APIGroups: []string{""}, Resources: []string{"nodes/stats", "nodes/proxy"}, Verbs: []string{"get"}},
	},
	// k8s_cluster watches workload and node state across the cluster;
	// k8s_objects only watches pods, already covered by k8sAttributesRules.
	gatewayName: {
		{
			APIGroups: []string{""},
			Resources: []string{
				"nodes", "services",
				"replicationcontrollers", "resourcequotas",
				"persistentvolumes", "persistentvolumeclaims",
			},
			Verbs: readVerbs,
		},
		{APIGroups: []string{"apps"}, Resources: []string{"deployments", "daemonsets", "statefulsets"}, Verbs: readVerbs},
		{APIGroups: []string{"extensions"}, Resources: []string{"replicasets"}, Verbs: readVerbs},
		{APIGroups: []string{"batch"}, Resources: []string{"cronjobs"}, Verbs: readVerbs},
		{APIGroups: []string{"autoscaling"}, Resources: []string{"horizontalpodautoscalers"}, Verbs: readVerbs},
	},
	// The scraper's ServiceAccount is shared with its Target Allocator (see
	// renderTargetAllocator), which runs Prometheus service discovery.
	scraperName: {
		{APIGroups: []string{""}, Resources: []string{"services", "endpoints"}, Verbs: readVerbs},
		{
			// The Target Allocator resolves scrape targets through
			// EndpointSlices; without this it starts and allocates nothing.
			APIGroups: []string{"discovery.k8s.io"},
			Resources: []string{"endpointslices"},
			Verbs:     readVerbs,
		},
	},
}

// collectorRBACName is the name of a collector's ClusterRole and
// ClusterRoleBinding.
func collectorRBACName(collector string) string {
	return collectorRBACPrefix + collector
}

// RenderCollectorRBAC builds one ClusterRole and ClusterRoleBinding per
// rendered collector, each granting only that collector's ServiceAccount the
// access its receivers need. A collector left out by the configuration gets
// no RBAC, so the caller prunes any it created before. The binding references
// the ServiceAccount in cfg.CollectorNamespace even before the OTel Operator
// has created it, which Kubernetes permits.
func RenderCollectorRBAC(cfg v1alpha1.TsugaCollectorConfigSpec) []client.Object {
	specs := collectorSpecs(cfg)
	objs := make([]client.Object, 0, 2*len(specs))
	for _, spec := range specs {
		name := collectorRBACName(spec.name)
		objs = append(objs,
			&rbacv1.ClusterRole{
				TypeMeta:   metav1.TypeMeta{APIVersion: "rbac.authorization.k8s.io/v1", Kind: "ClusterRole"},
				ObjectMeta: metav1.ObjectMeta{Name: name},
				Rules:      append(k8sAttributesRules(), collectorRules[spec.name]...),
			},
			&rbacv1.ClusterRoleBinding{
				TypeMeta:   metav1.TypeMeta{APIVersion: "rbac.authorization.k8s.io/v1", Kind: "ClusterRoleBinding"},
				ObjectMeta: metav1.ObjectMeta{Name: name},
				RoleRef: rbacv1.RoleRef{
					APIGroup: "rbac.authorization.k8s.io",
					Kind:     "ClusterRole",
					Name:     name,
				},
				Subjects: []rbacv1.Subject{{
					Kind:      rbacv1.ServiceAccountKind,
					Name:      spec.name + collectorServiceAccountSuffix,
					Namespace: cfg.CollectorNamespace,
				}},
			},
		)
	}
	return objs
}
