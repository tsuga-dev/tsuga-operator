package collection

import (
	"maps"
	"os"
	"slices"
	"testing"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/utils/ptr"
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
		case *rbacv1.Role, *rbacv1.RoleBinding:
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
	cfg.Profiling.Enabled = true
	roles, bindings := renderedRBAC(t, cfg)

	for collector, sa := range map[string]string{
		agentName:    "tsuga-agent",
		gatewayName:  "tsuga-gateway",
		scraperName:  "tsuga-scraper",
		profilerName: "tsuga-profiler",
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
	if !grants(gateway, "", "events", "watch") {
		t.Fatal("gateway must read core/v1 events for k8s_objects/events")
	}
	if grants(agent, "", "events", "watch") || grants(scraper, "", "events", "watch") {
		t.Fatal("only the gateway reads events")
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

// grantSet flattens rules into "group/resource:verb" entries.
func grantSet(rules []rbacv1.PolicyRule) map[string]bool {
	set := map[string]bool{}
	for _, r := range rules {
		for _, g := range r.APIGroups {
			for _, res := range r.Resources {
				for _, v := range r.Verbs {
					set[g+"/"+res+":"+v] = true
				}
			}
		}
	}
	return set
}

func TestGatewayRoleFollowsToggles(t *testing.T) {
	cfg := effective()
	cfg.Gateway.ClusterMetrics = false
	cfg.Gateway.KubernetesObjects = false
	cfg.Gateway.KubernetesEvents = true
	roles, _ := renderedRBAC(t, cfg)
	got := grantSet(roles[collectorRBACName(gatewayName)].Rules)
	want := grantSet(append(k8sAttributesRules(),
		rbacv1.PolicyRule{APIGroups: []string{""}, Resources: []string{"events"}, Verbs: readVerbs}))
	if !maps.Equal(got, want) {
		t.Fatalf("events-only gateway: want %v got %v", want, got)
	}

	cfg.Gateway.ClusterMetrics = true
	cfg.Gateway.KubernetesObjects = true
	roles, _ = renderedRBAC(t, cfg)
	got = grantSet(roles[collectorRBACName(gatewayName)].Rules)
	want = grantSet(append(k8sAttributesRules(),
		rbacv1.PolicyRule{
			APIGroups: []string{""},
			Resources: []string{
				"nodes", "services", "events",
				"replicationcontrollers", "resourcequotas",
				"persistentvolumes", "persistentvolumeclaims",
			},
			Verbs: readVerbs,
		},
		rbacv1.PolicyRule{APIGroups: []string{"apps"}, Resources: []string{"deployments", "daemonsets", "statefulsets"}, Verbs: readVerbs},
		rbacv1.PolicyRule{APIGroups: []string{"extensions"}, Resources: []string{"replicasets"}, Verbs: readVerbs},
		rbacv1.PolicyRule{APIGroups: []string{"batch"}, Resources: []string{"cronjobs"}, Verbs: readVerbs},
		rbacv1.PolicyRule{APIGroups: []string{"autoscaling"}, Resources: []string{"horizontalpodautoscalers"}, Verbs: readVerbs},
	))
	if !maps.Equal(got, want) {
		t.Fatalf("all-toggles gateway: want %v got %v", want, got)
	}
}

func TestRenderCollectorRBACIsReadOnly(t *testing.T) {
	cfg := withPrometheusCR(effectiveWithTargetAllocator())
	cfg.Profiling.Enabled = true
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
	cfg.Gateway.KubernetesEvents = false
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
	allowed := func(group, resource, verb, name string) bool {
		for _, r := range manager.Rules {
			if slices.Contains(r.APIGroups, group) && slices.Contains(r.Resources, resource) &&
				slices.Contains(r.Verbs, verb) &&
				((verb == "create" && len(r.ResourceNames) == 0) ||
					(verb != "create" && slices.Contains(r.ResourceNames, name))) {
				return true
			}
		}
		return false
	}

	cfg := withPrometheusCR(effectiveWithTargetAllocator())
	cfg.Profiling.Enabled = true
	roles, bindings := renderedRBAC(t, cfg)
	sas := RenderCollectorServiceAccounts(cfg)
	accounts := make([]string, 0, len(sas))
	for _, sa := range sas {
		accounts = append(accounts, sa.GetName())
	}
	if len(accounts) == 0 {
		t.Fatal("no ServiceAccounts rendered")
	}
	for resource, r := range map[string]struct {
		group string
		names []string
	}{
		"clusterroles":        {rbacv1.GroupName, slices.Collect(maps.Keys(roles))},
		"clusterrolebindings": {rbacv1.GroupName, slices.Collect(maps.Keys(bindings))},
		"serviceaccounts":     {"", accounts},
		"roles":               {rbacv1.GroupName, []string{collectorRBACName(scraperName)}},
		"rolebindings":        {rbacv1.GroupName, []string{collectorRBACName(scraperName)}},
	} {
		for _, name := range r.names {
			for _, verb := range []string{"create", "patch", "delete"} {
				if !allowed(r.group, resource, verb, name) {
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

	cfg := withPrometheusCR(effectiveWithTargetAllocator())
	cfg.Profiling.Enabled = true
	roles, _ := renderedRBAC(t, cfg)
	rulesByRole := map[string][]rbacv1.PolicyRule{}
	for name, role := range roles {
		rulesByRole[name] = role.Rules
	}
	for _, o := range RenderCollectorRBAC(cfg) {
		if role, ok := o.(*rbacv1.Role); ok {
			rulesByRole["Role "+role.Name] = role.Rules
		}
	}
	for name, rules := range rulesByRole {
		for _, r := range rules {
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

func TestRenderCollectorRBACProfilerOnlyResolvesPods(t *testing.T) {
	cfg := effective()
	cfg.Profiling.Enabled = true
	roles, _ := renderedRBAC(t, cfg)
	role := roles[collectorRBACName(profilerName)]
	if role == nil {
		t.Fatal("profiler role not rendered")
	}
	if !slices.EqualFunc(role.Rules, k8sAttributesRules(), func(a, b rbacv1.PolicyRule) bool {
		return slices.Equal(a.APIGroups, b.APIGroups) && slices.Equal(a.Resources, b.Resources) && slices.Equal(a.Verbs, b.Verbs)
	}) {
		t.Fatalf("profiler needs only k8s_attributes access, got %v", role.Rules)
	}
}

func TestRenderCollectorServiceAccounts(t *testing.T) {
	cfg := effective()
	cfg.Prometheus.Enabled = true
	cfg.Profiling.Enabled = true
	cfg.ServiceAccount.Annotations = map[string]string{"eks.amazonaws.com/role-arn": "arn:aws:iam::1:role/x"}
	got := map[string]*corev1.ServiceAccount{}
	for _, o := range RenderCollectorServiceAccounts(cfg) {
		sa := o.(*corev1.ServiceAccount)
		got[sa.Name] = sa
	}
	for _, name := range []string{"tsuga-agent", "tsuga-gateway", "tsuga-scraper", "tsuga-profiler"} {
		sa := got[name]
		if sa == nil {
			t.Fatalf("missing ServiceAccount %q", name)
		}
		if sa.Namespace != cfg.CollectorNamespace || sa.Annotations["eks.amazonaws.com/role-arn"] != "arn:aws:iam::1:role/x" {
			t.Fatalf("%s: wrong namespace or annotations: %+v", name, sa.ObjectMeta)
		}
	}
	if len(got) != 4 {
		t.Fatalf("one ServiceAccount per rendered collector, got %d", len(got))
	}
}

func TestRenderBringYourOwnServiceAccount(t *testing.T) {
	cfg := effectiveWithTargetAllocator()
	cfg.Profiling.Enabled = true
	cfg.ServiceAccount = v1alpha1.ServiceAccountSpec{Create: ptr.To(false), Name: "my-sa"}
	if sas := RenderCollectorServiceAccounts(cfg); len(sas) != 0 {
		t.Fatalf("create: false renders no ServiceAccounts, got %d", len(sas))
	}
	objs, err := RenderCollectors(cfg)
	if err != nil {
		t.Fatal(err)
	}
	for _, o := range objs {
		if sa, _, _ := unstructured.NestedString(o.Object, "spec", "serviceAccount"); sa != "my-sa" {
			t.Fatalf("%s %s must run as my-sa, got %q", o.GetKind(), o.GetName(), sa)
		}
	}
	_, bindings := renderedRBAC(t, cfg)
	for name, b := range bindings {
		if b.Subjects[0].Name != "my-sa" {
			t.Fatalf("binding %s must bind my-sa, got %s", name, b.Subjects[0].Name)
		}
	}
}

func TestRenderNoRBACWhenRBACCreateOff(t *testing.T) {
	cfg := effective()
	cfg.RBAC.Create = ptr.To(false)
	if objs := RenderCollectorRBAC(cfg); len(objs) != 0 {
		t.Fatalf("rbac.create: false renders no RBAC, got %d objects", len(objs))
	}
}

func TestRenderCollectorsRunAsTheirServiceAccount(t *testing.T) {
	cfg := effective()
	cfg.Profiling.Enabled = true
	objs, err := RenderCollectors(cfg)
	if err != nil {
		t.Fatal(err)
	}
	for _, o := range objs {
		if sa, _, _ := unstructured.NestedString(o.Object, "spec", "serviceAccount"); sa != o.GetName() {
			t.Fatalf("%s must run as %s, got %q", o.GetName(), o.GetName(), sa)
		}
	}
}

func TestScraperRoleReadsMonitorsOnlyWhenPromCREnabled(t *testing.T) {
	for _, enabled := range []bool{true, false} {
		cfg := effectiveWithTargetAllocator()
		cfg.Profiling.Enabled = true
		cfg.Prometheus.PrometheusCR.Enabled = enabled
		roles, _ := renderedRBAC(t, cfg)
		for _, name := range []string{agentName, gatewayName, scraperName, profilerName} {
			role := roles[collectorRBACName(name)]
			if role == nil {
				t.Fatalf("enabled=%v: no role for %s", enabled, name)
			}
			for _, res := range []string{"servicemonitors", "podmonitors", "probes", "scrapeconfigs"} {
				want := enabled && name == scraperName
				for _, verb := range []string{"get", "list", "watch"} {
					if got := grants(role.Rules, "monitoring.coreos.com", res, verb); got != want {
						t.Errorf("enabled=%v %s %s %s: got %v want %v", enabled, name, verb, res, got, want)
					}
				}
			}
		}
	}
}

func TestScraperSecretsRoleOnlyWithPromCRAndRBACCreate(t *testing.T) {
	for _, tc := range []struct {
		name       string
		promCR     bool
		rbacCreate bool
		want       bool
	}{
		{"promCR and rbac.create", true, true, true},
		{"promCR off", false, true, false},
		{"rbac.create off", true, false, false},
	} {
		cfg := effectiveWithTargetAllocator()
		cfg.ServiceAccount = v1alpha1.ServiceAccountSpec{Create: ptr.To(false), Name: "my-sa"}
		cfg.Prometheus.PrometheusCR.Enabled = tc.promCR
		cfg.RBAC.Create = ptr.To(tc.rbacCreate)
		var role *rbacv1.Role
		var binding *rbacv1.RoleBinding
		for _, o := range RenderCollectorRBAC(cfg) {
			switch obj := o.(type) {
			case *rbacv1.Role:
				role = obj
			case *rbacv1.RoleBinding:
				binding = obj
			}
		}
		if got := role != nil && binding != nil; got != tc.want {
			t.Fatalf("%s: Role/RoleBinding rendered=%v want %v", tc.name, got, tc.want)
		}
		if !tc.want {
			continue
		}
		name := collectorRBACName(scraperName)
		if role.Name != name || role.Namespace != cfg.CollectorNamespace ||
			binding.Name != name || binding.Namespace != cfg.CollectorNamespace {
			t.Fatalf("want %s/%s, got Role %s/%s RoleBinding %s/%s", cfg.CollectorNamespace, name,
				role.Namespace, role.Name, binding.Namespace, binding.Name)
		}
		want := grantSet([]rbacv1.PolicyRule{{APIGroups: []string{""}, Resources: []string{"secrets"}, Verbs: readVerbs}})
		if got := grantSet(role.Rules); !maps.Equal(got, want) {
			t.Fatalf("Role rules: want %v got %v", want, got)
		}
		if binding.RoleRef.Kind != "Role" || binding.RoleRef.Name != name {
			t.Fatalf("roleRef want Role/%s got %s/%s", name, binding.RoleRef.Kind, binding.RoleRef.Name)
		}
		subject := rbacv1.Subject{Kind: rbacv1.ServiceAccountKind, Name: "my-sa", Namespace: cfg.CollectorNamespace}
		if len(binding.Subjects) != 1 || binding.Subjects[0] != subject {
			t.Fatalf("subjects want [%v] got %v", subject, binding.Subjects)
		}
	}
}

func TestScraperRoleIsNeverClusterWideForSecrets(t *testing.T) {
	cfg := withPrometheusCR(effectiveWithTargetAllocator())
	roles, _ := renderedRBAC(t, cfg)
	for name, role := range roles {
		if grants(role.Rules, "", "secrets", "get") {
			t.Fatalf("ClusterRole %q grants cluster-wide secret read", name)
		}
	}
}

func TestK8sAttributesRulesReadNodes(t *testing.T) {
	got := grantSet(k8sAttributesRules())
	want := grantSet([]rbacv1.PolicyRule{{APIGroups: []string{""}, Resources: []string{"nodes"}, Verbs: readVerbs}})
	for g := range want {
		if !got[g] {
			t.Fatalf("k8s_attributes needs %v for node metadata, got %v", g, got)
		}
	}
}
