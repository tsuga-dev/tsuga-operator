// Package e2efull holds the tsuga-operator end-to-end suite.
package e2efull

import "fmt"

// AgentToggles mirrors TsugaCollectorConfigSpec.Agent.
type AgentToggles struct{ Traces, Metrics, Logs bool }

// GatewayToggles mirrors TsugaCollectorConfigSpec.Gateway.
type GatewayToggles struct{ ClusterMetrics, KubernetesObjects bool }

// CollectorScenario is one TsugaCollectorConfig the suite applies and asserts
// against. Each carries its own ClusterName so the telemetry it produces can
// be told apart from every other scenario's, which is what lets a negative
// assertion mean anything.
type CollectorScenario struct {
	Name          string
	ClusterName   string
	Agent         AgentToggles
	Gateway       GatewayToggles
	Variant       string
	ExpectError   bool
	ExpectGateway bool
	IsBaseline    bool
}

// CollectorScenarios returns the 12 scenarios Tier A applies: the all-on
// baseline, each of the five toggles turned off once with the rest on, both
// gateway receivers off, every agent signal off (an expected error), and the
// 4 orthogonal variants. The baseline is first: Tier A's negative assertions
// use its cluster name as their positive control, so it must have run and
// produced telemetry before any other scenario is judged.
func CollectorScenarios(runID string) []CollectorScenario {
	var out []CollectorScenario

	add := func(agent AgentToggles, gateway GatewayToggles, variant string) {
		index := len(out)
		agentAllOff := !agent.Traces && !agent.Metrics && !agent.Logs
		out = append(out, CollectorScenario{
			Name:          scenarioName(index, agent, gateway, variant),
			ClusterName:   fmt.Sprintf("%s-s%02d", runID, index),
			Agent:         agent,
			Gateway:       gateway,
			Variant:       variant,
			ExpectError:   agentAllOff && variant == "",
			ExpectGateway: gateway.ClusterMetrics || gateway.KubernetesObjects,
			IsBaseline:    index == 0,
		})
	}

	allOn := AgentToggles{true, true, true}
	fullGateway := GatewayToggles{true, true}
	add(allOn, fullGateway, "")
	add(AgentToggles{false, true, true}, fullGateway, "")
	add(AgentToggles{true, false, true}, fullGateway, "")
	add(AgentToggles{true, true, false}, fullGateway, "")
	add(allOn, GatewayToggles{false, true}, "")
	add(allOn, GatewayToggles{true, false}, "")
	add(allOn, GatewayToggles{false, false}, "")
	add(AgentToggles{false, false, false}, fullGateway, "")

	for _, variant := range []string{
		"endpointSecretRef", "customNamespace", "imageOverride", "resourcesOverride",
	} {
		add(allOn, fullGateway, variant)
	}
	return out
}

func scenarioName(index int, agent AgentToggles, gateway GatewayToggles, variant string) string {
	if variant != "" {
		return fmt.Sprintf("s%02d-%s", index, variant)
	}
	return fmt.Sprintf("s%02d-agent[%s%s%s]-gateway[%s%s]", index,
		flag("t", agent.Traces), flag("m", agent.Metrics), flag("l", agent.Logs),
		flag("c", gateway.ClusterMetrics), flag("o", gateway.KubernetesObjects))
}

func flag(letter string, on bool) string {
	if on {
		return letter
	}
	return "-"
}
