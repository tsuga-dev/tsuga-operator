package collection

import (
	"maps"
	"os"
	"slices"
	"testing"

	rbacv1 "k8s.io/api/rbac/v1"
	"sigs.k8s.io/yaml"

	"github.com/tsuga-dev/tsuga-operator/api/v1alpha1"
)

// renderedRBAC indexes RenderCollectorRBAC output by object name.
func renderedRBAC(t *testing.T, cfg v1alpha1.TsugaCollectorConfigSpec) (map[string]*rbacv1.ClusterRole, map[string]*rbacv1.ClusterRoleBinding) {
	t.Helper()
	roles := map[string]*rbacv1.ClusterRole{}
	bindings := map[string]*rbacv1.ClusterRoleBinding{}
	for _, o := range RenderCollectorRBAC(cfg) {
		switch obj := o.(type) {
		case *rbacv1.ClusterRole:
			roles[obj.Name] = obj
		case *rbacv1.ClusterRoleBinding:
			bindings[obj.Name] = obj
		default:
			t.Fatalf("unexpected object %T", o)
		}
	}
	return roles, bindings
}

func grants(rules []rbacv1.PolicyRule, group, resource, verb string) bool {
	for _, r := range rules {
		if slices.Contains(r.APIGroups, group) && slices.Contains(r.Resources, resource) && slices.Contains(r.Verbs, verb) {
			return true
		}
	}
	return false
}

func TestRenderCollectorRBACBindsEachServiceAccountToItsOwnRole(t *testing.T) {
	cfg := effective()
	cfg.Prometheus.Enabled = true
	roles, bindings := renderedRBAC(t, cfg)

	for collector, sa := range map[string]string{
		agentName:   AgentServiceAccountName,
		gatewayName: GatewayServiceAccountName,
		scraperName: ScraperServiceAccountName,
	} {
		name := collectorRBACName(collector)
		if roles[name] == nil {
			t.Fatalf("missing ClusterRole %q", name)
		}
		b := bindings[name]
		if b == nil {
			t.Fatalf("missing ClusterRoleBinding %q", name)
		}
		if b.RoleRef.Kind != "ClusterRole" || b.RoleRef.Name != name {
			t.Fatalf("binding %q roleRef want ClusterRole/%s got %s/%s", name, name, b.RoleRef.Kind, b.RoleRef.Name)
		}
		want := rbacv1.Subject{Kind: rbacv1.ServiceAccountKind, Name: sa, Namespace: cfg.CollectorNamespace}
		if len(b.Subjects) != 1 || b.Subjects[0] != want {
			t.Fatalf("binding %q subjects want [%v] got %v", name, want, b.Subjects)
		}
	}
}

func TestRenderCollectorRBACScopesPermissionsPerCollector(t *testing.T) {
	cfg := effective()
	cfg.Prometheus.Enabled = true
	roles, _ := renderedRBAC(t, cfg)
	agent := roles[collectorRBACName(agentName)].Rules
	gateway := roles[collectorRBACName(gatewayName)].Rules
	scraper := roles[collectorRBACName(scraperName)].Rules

	// Every collector runs k8s_attributes.
	for _, rules := range [][]rbacv1.PolicyRule{agent, gateway, scraper} {
		if !grants(rules, "", "pods", "watch") || !grants(rules, "apps", "replicasets", "watch") {
			t.Fatalf("k8s_attributes access missing from %v", rules)
		}
	}

	// Kubelet access is the agent's alone.
	if !grants(agent, "", "nodes/proxy", "get") || !grants(agent, "", "nodes/stats", "get") {
		t.Fatal("agent must reach the kubelet through nodes/stats and nodes/proxy")
	}
	for _, rules := range [][]rbacv1.PolicyRule{gateway, scraper} {
		if grants(rules, "", "nodes/proxy", "get") || grants(rules, "", "nodes/stats", "get") {
			t.Fatal("only the agent may reach the kubelet")
		}
	}

	// Cluster state is the gateway's alone.
	if !grants(gateway, "apps", "deployments", "watch") || !grants(gateway, "", "nodes", "watch") {
		t.Fatal("gateway must read cluster state for k8s_cluster")
	}
	// k8s_cluster waits for every informer to sync; a forbidden one stalls it
	// until initial_sync_timeout, then the collector exits without emitting.
	if !grants(gateway, "", "persistentvolumes", "watch") || !grants(gateway, "", "persistentvolumeclaims", "watch") {
		t.Fatal("gateway must read persistentvolumes and persistentvolumeclaims for k8s_cluster")
	}
	if grants(agent, "apps", "deployments", "watch") || grants(scraper, "apps", "deployments", "watch") {
		t.Fatal("only the gateway reads cluster workload state")
	}

	// The Target Allocator watches EndpointSlices to resolve scrape targets;
	// without this rule it starts and silently allocates nothing.
	if !grants(scraper, "discovery.k8s.io", "endpointslices", "watch") {
		t.Fatal("scraper must read endpointslices for the Target Allocator")
	}
}

func TestRenderCollectorRBACIsReadOnly(t *testing.T) {
	cfg := effective()
	cfg.Prometheus.Enabled = true
	roles, _ := renderedRBAC(t, cfg)
	for name, role := range roles {
		for _, r := range role.Rules {
			for _, v := range r.Verbs {
				if v != "get" && v != "list" && v != "watch" {
					t.Fatalf("role %q grants write verb %q on %v", name, v, r.Resources)
				}
			}
		}
	}
}

func TestRenderCollectorRBACSkipsDisabledCollectors(t *testing.T) {
	cfg := effective()
	cfg.Gateway.ClusterMetrics = false
	cfg.Gateway.KubernetesObjects = false
	cfg.Prometheus.Enabled = false
	roles, bindings := renderedRBAC(t, cfg)

	agent := collectorRBACName(agentName)
	if len(roles) != 1 || roles[agent] == nil || len(bindings) != 1 || bindings[agent] == nil {
		t.Fatalf("want only %q RBAC, got roles %v bindings %v", agent, roles, bindings)
	}
}

// Create must be unscoped; patch and delete are restricted to rendered names.
func TestRenderCollectorRBACNamesAreGrantedToManager(t *testing.T) {
	raw, err := os.ReadFile("../../config/rbac/role.yaml")
	if err != nil {
		t.Fatal(err)
	}
	manager := &rbacv1.ClusterRole{}
	if err := yaml.Unmarshal(raw, manager); err != nil {
		t.Fatal(err)
	}
	allowed := func(resource, verb, name string) bool {
		for _, r := range manager.Rules {
			if slices.Contains(r.APIGroups, rbacv1.GroupName) && slices.Contains(r.Resources, resource) &&
				slices.Contains(r.Verbs, verb) &&
				((verb == "create" && len(r.ResourceNames) == 0) ||
					(verb != "create" && slices.Contains(r.ResourceNames, name))) {
				return true
			}
		}
		return false
	}

	cfg := effective()
	cfg.Prometheus.Enabled = true
	roles, bindings := renderedRBAC(t, cfg)
	for resource, names := range map[string][]string{
		"clusterroles":        slices.Collect(maps.Keys(roles)),
		"clusterrolebindings": slices.Collect(maps.Keys(bindings)),
	} {
		for _, name := range names {
			for _, verb := range []string{"create", "patch", "delete"} {
				if !allowed(resource, verb, name) {
					t.Errorf("manager role cannot %s %s %q; add it to the resourceNames marker and run make manifests", verb, resource, name)
				}
			}
		}
	}
}

// Kubernetes rejects a ClusterRole granting anything its creator lacks, so
// trimming the manager role must never drop a collector rule.
func TestManagerRoleCoversCollectorRules(t *testing.T) {
	raw, err := os.ReadFile("../../config/rbac/role.yaml")
	if err != nil {
		t.Fatal(err)
	}
	manager := &rbacv1.ClusterRole{}
	if err := yaml.Unmarshal(raw, manager); err != nil {
		t.Fatal(err)
	}

	cfg := effective()
	cfg.Prometheus.Enabled = true
	roles, _ := renderedRBAC(t, cfg)
	for name, role := range roles {
		for _, r := range role.Rules {
			for _, g := range r.APIGroups {
				for _, res := range r.Resources {
					for _, v := range r.Verbs {
						if !grants(manager.Rules, g, res, v) {
							t.Errorf("%s grants %s %s/%s the manager lacks; add a //+kubebuilder:rbac marker and run make manifests", name, v, g, res)
						}
					}
				}
			}
		}
	}
}
