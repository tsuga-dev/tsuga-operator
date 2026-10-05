package tsugacli

import (
	"os"
	"strings"
	"testing"
)

// TestLiveSignalQueries runs the real signal queries against the real API.
// It is skipped unless TSUGA_LIVE_CLUSTER names a scenario cluster that is
// known to have telemetry, because it asserts on data it did not produce.
func TestLiveSignalQueries(t *testing.T) {
	cluster := os.Getenv("TSUGA_LIVE_CLUSTER")
	if cluster == "" {
		t.Skip("set TSUGA_LIVE_CLUSTER to a cluster with known telemetry")
	}
	cfg, err := ConfigFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	c := New(cfg, os.Stderr)

	logs, err := c.CountContainerLogs(cluster, "-60m")
	if err != nil {
		t.Fatalf("CountContainerLogs: %v", err)
	}
	t.Logf("logs=%d", logs)

	spans, err := c.CountTraces(cluster, "-60m")
	if err != nil {
		t.Fatalf("CountTraces: %v", err)
	}
	t.Logf("spans=%d", spans)

	series, err := c.CountMetricSeries("e2e.emitter.ticks", cluster, "-60m")
	if err != nil {
		t.Fatalf("CountMetricSeries: %v", err)
	}
	t.Logf("metricDatapoints=%d", series)

	if logs == 0 || spans == 0 || series == 0 {
		t.Fatalf("every signal must be non-zero for a cluster with telemetry: logs=%d spans=%d metrics=%d",
			logs, spans, series)
	}
}

// TestLiveForeignControls checks the two explorer controls return data for a
// prefix no cluster of ours uses. They are the controls for Tier A's
// kubernetes-pods and cluster-metrics absence assertions, and a control that
// reads zero fails those assertions as a suite error, so this is the check
// that the control is sound before a two-hour run depends on it.
func TestLiveForeignControls(t *testing.T) {
	if os.Getenv("TSUGA_LIVE_CLUSTER") == "" {
		t.Skip("set TSUGA_LIVE_CLUSTER to run the live probes")
	}
	cfg, err := ConfigFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	c := New(cfg, os.Stderr)

	clusters, err := c.CountForeignK8sClusters("e2e-")
	if err != nil {
		t.Fatalf("CountForeignK8sClusters: %v", err)
	}
	pods, err := c.CountForeignK8sPods("e2e-")
	if err != nil {
		t.Fatalf("CountForeignK8sPods: %v", err)
	}
	t.Logf("foreignClusters=%d foreignPods=%d", clusters, pods)

	if clusters == 0 || pods == 0 {
		t.Fatalf("the explorer must report clusters outside this run for the control to mean anything: clusters=%d pods=%d",
			clusters, pods)
	}
}

// TestLiveToggleDiscrimination pins each signal to the toggle it actually
// measures, using clusters from a real run. Every pairing here was wrong at
// some point, each time in a way that returned a confident zero rather than an
// error, so this is the check that a green Tier A means something.
//
// Only the time-windowed signals are covered. The Kubernetes explorer reports
// what is reporting now, so a finished scenario's pods and cluster metrics
// have already decayed out of it and cannot be checked after the fact - they
// have to be read while the scenario runs.
//
// Set TSUGA_LIVE_MATRIX to a comma-separated list of
// cluster=logs|traces|objects assignments, naming the toggles that scenario
// ran with. Anything unnamed is asserted absent.
func TestLiveToggleDiscrimination(t *testing.T) {
	spec := os.Getenv("TSUGA_LIVE_MATRIX")
	if spec == "" {
		t.Skip("set TSUGA_LIVE_MATRIX to cluster=toggle|toggle,... from a real run")
	}
	cfg, err := ConfigFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	c := New(cfg, os.Stderr)

	for _, entry := range strings.Split(spec, ",") {
		cluster, toggles, ok := strings.Cut(strings.TrimSpace(entry), "=")
		if !ok {
			t.Fatalf("malformed entry %q, want cluster=toggle|toggle", entry)
		}
		on := map[string]bool{}
		for _, name := range strings.Split(toggles, "|") {
			on[strings.TrimSpace(name)] = true
		}

		counts := map[string]func() (int, error){
			"logs":    func() (int, error) { return c.CountContainerLogs(cluster, "-6h") },
			"traces":  func() (int, error) { return c.CountTraces(cluster, "-6h") },
			"objects": func() (int, error) { return c.CountK8sObjectLogs(cluster, "-6h") },
		}
		for _, signal := range []string{"logs", "traces", "objects"} {
			got, err := counts[signal]()
			if err != nil {
				t.Fatalf("%s %s: %v", cluster, signal, err)
			}
			t.Logf("%s %s=%d (expected %v)", cluster, signal, got, on[signal])
			if on[signal] && got == 0 {
				t.Errorf("%s ran with %s on but counted 0", cluster, signal)
			}
			if !on[signal] && got != 0 {
				t.Errorf("%s ran with %s off but counted %d", cluster, signal, got)
			}
		}
	}
}
