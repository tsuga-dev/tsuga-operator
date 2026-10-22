package e2efull

import (
	"strings"
	"testing"
)

func TestCollectorScenariosBaselineIsFirstAndAllOn(t *testing.T) {
	first := CollectorScenarios("run")[0]
	if !first.IsBaseline {
		t.Fatal("the first scenario must be the baseline, it is the positive control for every negative assertion")
	}
	if !first.Agent.Traces || !first.Agent.Metrics || !first.Agent.Logs {
		t.Fatalf("baseline must enable every agent signal, got %+v", first.Agent)
	}
	if !first.Gateway.ClusterMetrics || !first.Gateway.KubernetesObjects {
		t.Fatalf("baseline must enable every gateway receiver, got %+v", first.Gateway)
	}
	if first.ExpectError {
		t.Fatal("baseline must be a succeeding scenario")
	}
}

func TestCollectorScenariosClusterNamesAreUniqueAndStamped(t *testing.T) {
	seen := map[string]bool{}
	for _, s := range CollectorScenarios("run") {
		if !strings.HasPrefix(s.ClusterName, "run-") {
			t.Errorf("%s: cluster name %q must carry the run id", s.Name, s.ClusterName)
		}
		if seen[s.ClusterName] {
			t.Fatalf("duplicate cluster name %q: per-scenario uniqueness is what makes negative assertions sound", s.ClusterName)
		}
		seen[s.ClusterName] = true
	}
}
