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

// CollectorScenarios returns the 32 toggle combinations plus the 4 orthogonal
// variants. The all-on baseline is first: Tier A's negative assertions use its
// cluster name as their positive control, so it must have run and produced
// telemetry before any other scenario is judged.
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

	// Enumerate all 32 toggle combinations (2^5) using a bitmask.
	// Bit assignments: [4:traces, 3:metrics, 2:logs, 1:clusterMetrics, 0:kubernetesObjects]
	const (
		traceMask      = 0b10000
		metricMask     = 0b01000
		logMask        = 0b00100
		clusterMask    = 0b00010
		kubeObjectMask = 0b00001
	)
	for combo := 0; combo < 32; combo++ {
		agent := AgentToggles{
			Traces:  combo&traceMask != 0,
			Metrics: combo&metricMask != 0,
			Logs:    combo&logMask != 0,
		}
		gateway := GatewayToggles{
			ClusterMetrics:    combo&clusterMask != 0,
			KubernetesObjects: combo&kubeObjectMask != 0,
		}
		if agent == allOn && gateway == fullGateway {
			continue // already added as the baseline
		}
		add(agent, gateway, "")
	}

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
