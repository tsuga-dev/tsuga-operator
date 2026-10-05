package e2efull

import (
	"strings"
	"testing"
)

func TestCollectorScenariosCount(t *testing.T) {
	got := CollectorScenarios("run")
	if len(got) != 36 {
		t.Fatalf("want 36 scenarios (32 toggle combinations + 4 variants), got %d", len(got))
	}
}

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

func TestCollectorScenariosErrorCases(t *testing.T) {
	errorCount := 0
	for _, s := range CollectorScenarios("run") {
		agentAllOff := !s.Agent.Traces && !s.Agent.Metrics && !s.Agent.Logs
		if s.ExpectError != (agentAllOff && s.Variant == "") {
			t.Errorf("%s: ExpectError=%v but agent all-off=%v", s.Name, s.ExpectError, agentAllOff)
		}
		if s.ExpectError {
			errorCount++
		}
	}
	if errorCount != 4 {
		t.Fatalf("want 4 error scenarios (agent all-off across 4 gateway combinations), got %d", errorCount)
	}
}

func TestCollectorScenariosGatewayExpectation(t *testing.T) {
	noGateway := 0
	for _, s := range CollectorScenarios("run") {
		wantGateway := s.Gateway.ClusterMetrics || s.Gateway.KubernetesObjects
		if s.ExpectGateway != wantGateway {
			t.Errorf("%s: ExpectGateway=%v but toggles are %+v", s.Name, s.ExpectGateway, s.Gateway)
		}
		if !s.ExpectError && !s.ExpectGateway {
			noGateway++
		}
	}
	if noGateway != 7 {
		t.Fatalf("want 7 succeeding scenarios with no gateway, got %d", noGateway)
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

func TestCollectorScenariosCoverEveryVariant(t *testing.T) {
	want := map[string]bool{
		"endpointSecretRef": false, "customNamespace": false,
		"imageOverride": false, "resourcesOverride": false,
	}
	for _, s := range CollectorScenarios("run") {
		if _, ok := want[s.Variant]; ok {
			want[s.Variant] = true
		}
	}
	for variant, covered := range want {
		if !covered {
			t.Errorf("variant %q has no scenario", variant)
		}
	}
}

func TestCollectorScenariosToggleCombinations(t *testing.T) {
	// Collect all non-variant scenario toggle tuples.
	type toggleTuple struct {
		traces     bool
		metrics    bool
		logs       bool
		clusterMet bool
		kubeObj    bool
	}

	got := make(map[toggleTuple]bool)
	for _, s := range CollectorScenarios("run") {
		if s.Variant != "" {
			continue // skip variants
		}
		tt := toggleTuple{
			traces:     s.Agent.Traces,
			metrics:    s.Agent.Metrics,
			logs:       s.Agent.Logs,
			clusterMet: s.Gateway.ClusterMetrics,
			kubeObj:    s.Gateway.KubernetesObjects,
		}
		got[tt] = true
	}

	// Verify exactly 32 non-variant scenarios with distinct tuples.
	if len(got) != 32 {
		t.Fatalf("want 32 distinct toggle tuples in non-variant scenarios, got %d", len(got))
	}

	// Verify the set equals all 32 combinations of the five booleans.
	bits := []bool{true, false}
	want := make(map[toggleTuple]bool)
	for _, traces := range bits {
		for _, metrics := range bits {
			for _, logs := range bits {
				for _, clusterMet := range bits {
					for _, kubeObj := range bits {
						tt := toggleTuple{traces, metrics, logs, clusterMet, kubeObj}
						want[tt] = true
					}
				}
			}
		}
	}

	if len(want) != 32 {
		t.Fatalf("expected 32 combinations, generated %d", len(want))
	}

	for tt := range want {
		if !got[tt] {
			t.Errorf("missing combination: %+v", tt)
		}
	}
}
