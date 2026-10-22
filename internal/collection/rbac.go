package collection

import (
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/tsuga-dev/tsuga-operator/api/v1alpha1"
)

const (
	// collectorRBACPrefix prefixes each collector's ClusterRole and
	// ClusterRoleBinding name ("tsuga-operator-collector-tsuga-agent", ...).
	collectorRBACPrefix = "tsuga-operator-collector-"
)

// ServiceAccountName is the ServiceAccount a collector runs as: one named
// after the collector when the operator creates them, else the user's.
// The OTel Operator's own <collector>-collector account is avoided on
// purpose: the OpenTelemetryCollector owns it and the OTel Operator prunes
// owned objects it no longer wants.
func ServiceAccountName(cfg v1alpha1.TsugaCollectorConfigSpec, collector string) string {
	if !ptr.Deref(cfg.ServiceAccount.Create, true) {
		return cfg.ServiceAccount.Name
	}
	return collector
}

// RenderCollectorServiceAccounts builds one ServiceAccount per rendered
// collector, or none when the user brings their own.
func RenderCollectorServiceAccounts(cfg v1alpha1.TsugaCollectorConfigSpec) []client.Object {
	if !ptr.Deref(cfg.ServiceAccount.Create, true) {
		return nil
	}
	specs := collectorSpecs(cfg)
	objs := make([]client.Object, 0, len(specs))
	for _, spec := range specs {
		objs = append(objs, &corev1.ServiceAccount{
			TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "ServiceAccount"},
			ObjectMeta: metav1.ObjectMeta{
				Name:        spec.name,
				Namespace:   cfg.CollectorNamespace,
				Annotations: cfg.ServiceAccount.Annotations,
			},
		})
	}
	return objs
}

var readVerbs = []string{"get", "list", "watch"}

// k8sAttributesRules is what the k8s_attributes processor, present in every
// collector, reads to resolve pod metadata: pods and namespaces, nodes for
// k8s.node.uid and node labels and annotations, plus ReplicaSets and Jobs to
// walk up to the owning Deployment and CronJob.
func k8sAttributesRules() []rbacv1.PolicyRule {
	return []rbacv1.PolicyRule{
		{APIGroups: []string{""}, Resources: []string{"pods", "namespaces", "nodes"}, Verbs: readVerbs},
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
}

// gatewayRules is separate from collectorRules because each gateway toggle
// enables its own receiver. k8s_cluster watches workload and node state
// across the cluster; k8s_objects watches pods, already covered by
// k8sAttributesRules; k8s_objects/events watches core/v1 Warning events.
func gatewayRules(cfg v1alpha1.TsugaCollectorConfigSpec) []rbacv1.PolicyRule {
	var rules []rbacv1.PolicyRule
	if cfg.Gateway.ClusterMetrics {
		rules = append(rules,
			rbacv1.PolicyRule{
				APIGroups: []string{""},
				Resources: []string{
					"nodes", "services",
					"replicationcontrollers", "resourcequotas",
					"persistentvolumes", "persistentvolumeclaims",
				},
				Verbs: readVerbs,
			},
			rbacv1.PolicyRule{APIGroups: []string{"apps"}, Resources: []string{"deployments", "daemonsets", "statefulsets"}, Verbs: readVerbs},
			rbacv1.PolicyRule{APIGroups: []string{"extensions"}, Resources: []string{"replicasets"}, Verbs: readVerbs},
			rbacv1.PolicyRule{APIGroups: []string{"batch"}, Resources: []string{"cronjobs"}, Verbs: readVerbs},
			rbacv1.PolicyRule{APIGroups: []string{"autoscaling"}, Resources: []string{"horizontalpodautoscalers"}, Verbs: readVerbs},
		)
	}
	if cfg.Gateway.KubernetesEvents {
		rules = append(rules, rbacv1.PolicyRule{APIGroups: []string{""}, Resources: []string{"events"}, Verbs: readVerbs})
	}
	return rules
}

// scraperRules is separate from collectorRules because it depends on the
// config. The scraper's ServiceAccount is shared with its Target Allocator
// (see renderTargetAllocator), which runs Prometheus service discovery and,
// with prometheusCR, watches ServiceMonitors and PodMonitors, plus Probes and
// ScrapeConfigs when their CRDs are installed. Each forbidden informer stalls
// the Target Allocator's start on its cache-sync timeout.
func scraperRules(cfg v1alpha1.TsugaCollectorConfigSpec) []rbacv1.PolicyRule {
	rules := []rbacv1.PolicyRule{
		{APIGroups: []string{""}, Resources: []string{"services", "endpoints"}, Verbs: readVerbs},
		{
			// The Target Allocator resolves scrape targets through
			// EndpointSlices; without this it starts and allocates nothing.
			APIGroups: []string{"discovery.k8s.io"},
			Resources: []string{"endpointslices"},
			Verbs:     readVerbs,
		},
	}
	if cfg.Prometheus.PrometheusCR.Enabled {
		rules = append(rules, rbacv1.PolicyRule{
			APIGroups: []string{"monitoring.coreos.com"},
			Resources: []string{"servicemonitors", "podmonitors", "probes", "scrapeconfigs"},
			Verbs:     readVerbs,
		})
	}
	return rules
}

// collectorRBACName is the name of a collector's ClusterRole and
// ClusterRoleBinding.
func collectorRBACName(collector string) string {
	return collectorRBACPrefix + collector
}

// RenderCollectorRBAC builds one ClusterRole and ClusterRoleBinding per
// rendered collector, each granting only that collector's ServiceAccount the
// access its receivers need. A collector left out by the configuration gets
// no RBAC, so the caller prunes any it created before. rbac.create: false
// renders none.
func RenderCollectorRBAC(cfg v1alpha1.TsugaCollectorConfigSpec) []client.Object {
	if !ptr.Deref(cfg.RBAC.Create, true) {
		return nil
	}
	specs := collectorSpecs(cfg)
	objs := make([]client.Object, 0, 2*len(specs))
	for _, spec := range specs {
		name := collectorRBACName(spec.name)
		rules := collectorRules[spec.name]
		switch spec.name {
		case gatewayName:
			rules = gatewayRules(cfg)
		case scraperName:
			rules = scraperRules(cfg)
		}
		objs = append(objs,
			&rbacv1.ClusterRole{
				TypeMeta:   metav1.TypeMeta{APIVersion: "rbac.authorization.k8s.io/v1", Kind: "ClusterRole"},
				ObjectMeta: metav1.ObjectMeta{Name: name},
				Rules:      append(k8sAttributesRules(), rules...),
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
					Name:      ServiceAccountName(cfg, spec.name),
					Namespace: cfg.CollectorNamespace,
				}},
			},
		)
	}
	objs = append(objs, scraperSecretsRole(cfg)...)
	return objs
}

// scraperSecretsRole builds a Role and RoleBinding letting the scraper's
// Target Allocator read Secrets in the collector namespace. With
// prometheusCR, the Target Allocator always runs a Secrets informer scoped to
// its own namespace, and reads the Secrets that monitors reference for
// authentication. A namespaced Role keeps Secret access out of the
// cluster-wide ClusterRole.
func scraperSecretsRole(cfg v1alpha1.TsugaCollectorConfigSpec) []client.Object {
	if !cfg.Prometheus.Enabled || !cfg.Prometheus.PrometheusCR.Enabled {
		return nil
	}
	name := collectorRBACName(scraperName)
	return []client.Object{
		&rbacv1.Role{
			TypeMeta:   metav1.TypeMeta{APIVersion: rbacv1.SchemeGroupVersion.String(), Kind: "Role"},
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: cfg.CollectorNamespace},
			Rules: []rbacv1.PolicyRule{
				{APIGroups: []string{""}, Resources: []string{"secrets"}, Verbs: readVerbs},
			},
		},
		&rbacv1.RoleBinding{
			TypeMeta:   metav1.TypeMeta{APIVersion: rbacv1.SchemeGroupVersion.String(), Kind: "RoleBinding"},
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: cfg.CollectorNamespace},
			RoleRef: rbacv1.RoleRef{
				APIGroup: rbacv1.GroupName,
				Kind:     "Role",
				Name:     name,
			},
			Subjects: []rbacv1.Subject{{
				Kind:      rbacv1.ServiceAccountKind,
				Name:      ServiceAccountName(cfg, scraperName),
				Namespace: cfg.CollectorNamespace,
			}},
		},
	}
}
