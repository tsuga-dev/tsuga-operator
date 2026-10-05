package e2efull

import (
	"github.com/onsi/ginkgo/v2"
)

// dumpDiagnostics prints everything needed to diagnose a failed scenario
// without re-running it: the operator's logs, the rendered collectors, the
// collector pods' own logs, and recent events.
func dumpDiagnostics(scenarioName string) {
	w := ginkgo.GinkgoWriter
	w.Printf("\n===== diagnostics for %s =====\n", scenarioName)

	for _, probe := range []struct {
		label string
		args  []string
	}{
		//nolint:goconst // kubectl vocabulary
		{"TsugaCollectorConfig", []string{"get", "tsugacollectorconfigs", "-o", "yaml"}},
		{"TsugaMonitorings", []string{"get", "tsugamonitorings", "-A", "-o", "yaml"}},
		{"OpenTelemetryCollectors", []string{"get", "opentelemetrycollectors", "-A", "-o", "yaml"}},
		{"Instrumentations", []string{"get", "instrumentations", "-A", "-o", "yaml"}},
		{"Dashboards", []string{"get", "dashboards", "-A", "-o", "yaml"}},
		{"Monitors", []string{"get", "monitors", "-A", "-o", "yaml"}},
		{"pods", []string{"get", "pods", "-A", "-o", "wide"}},
		{"events", []string{"get", "events", "-A", "--field-selector", "type!=Normal", "--sort-by=.lastTimestamp"}},
		{"operator logs", []string{"logs", "-n", operatorNS,
			managerDeployment, "--tail=200"}},
	} {
		out, err := kubectl(probe.args...)
		w.Printf("\n--- %s ---\n%s\n", probe.label, out)
		if err != nil {
			w.Printf("(command failed: %v)\n", err)
		}
	}

	for _, collector := range []string{"tsuga-agent", "tsuga-gateway"} {
		out, err := kubectl("logs", "-n", operatorNS,
			"-l", "app.kubernetes.io/name="+collector+"-collector", "--tail=100")
		w.Printf("\n--- %s logs ---\n%s\n", collector, out)
		if err != nil {
			w.Printf("(command failed: %v)\n", err)
		}
	}
	w.Printf("===== end diagnostics =====\n\n")
}
