package e2efull

import (
	"encoding/hex"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestNewRunIDHasExpectedShape(t *testing.T) {
	id := newRunID()
	if !strings.HasPrefix(id, "e2e-") {
		t.Fatalf("want e2e- prefix, got %q", id)
	}
	suffix := strings.TrimPrefix(id, "e2e-")
	if len(suffix) != 8 {
		t.Fatalf("want 8 hex characters after prefix, got %q (len %d)", suffix, len(suffix))
	}
	if _, err := hex.DecodeString(suffix); err != nil {
		t.Fatalf("want hex suffix, got %q: %v", suffix, err)
	}
}

func TestNewRunIDDiffersAcrossCalls(t *testing.T) {
	first := newRunID()
	second := newRunID()
	if first == second {
		t.Fatalf("want two successive calls to differ, both returned %q", first)
	}
}

func TestIngestTimeoutDefaultsWhenUnset(t *testing.T) {
	got := ingestTimeout()
	if got != 180*time.Second {
		t.Fatalf("want 180s default, got %s", got)
	}
}

func TestIngestTimeoutParsesValidDuration(t *testing.T) {
	t.Setenv("E2E_INGEST_TIMEOUT", "45s")
	got := ingestTimeout()
	if got != 45*time.Second {
		t.Fatalf("want 45s, got %s", got)
	}
}

func TestIngestTimeoutFallsBackOnUnparsableValue(t *testing.T) {
	t.Setenv("E2E_INGEST_TIMEOUT", "not-a-duration")
	got := ingestTimeout()
	if got != 180*time.Second {
		t.Fatalf("want fallback to 180s default, got %s", got)
	}
}

func TestKeepClusterFalseWhenUnset(t *testing.T) {
	if keepCluster() {
		t.Fatal("want false when E2E_KEEP_CLUSTER is unset")
	}
}

func TestKeepClusterTrueWhenSet(t *testing.T) {
	t.Setenv("E2E_KEEP_CLUSTER", "1")
	if !keepCluster() {
		t.Fatal("want true when E2E_KEEP_CLUSTER is set")
	}
}

func TestCrArgsIncludesNamespaceFlagWhenNamespaced(t *testing.T) {
	got := crArgs("Dashboard", "tsuga-operator-system", "my-dash", "-o", "jsonpath={.status.phase}")
	want := []string{
		"get", "Dashboard", "my-dash",
		"-n", "tsuga-operator-system",
		"-o", "jsonpath={.status.phase}",
	}
	assertStringSliceEqual(t, got, want)
}

func TestCrArgsOmitsNamespaceFlagWhenClusterScoped(t *testing.T) {
	got := crArgs("TsugaCollectorConfig", "", "my-config", "-o", "jsonpath={.status.id}")
	want := []string{
		"get", "TsugaCollectorConfig", "my-config",
		"-o", "jsonpath={.status.id}",
	}
	assertStringSliceEqual(t, got, want)
	for _, arg := range got {
		if arg == "-n" {
			t.Fatalf("want no -n flag for a cluster-scoped resource, got %v", got)
		}
	}
}

func assertStringSliceEqual(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("want %v, got %v", want, got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("want %v, got %v", want, got)
		}
	}
}

func TestIdsTaggedWithRunFiltersByRunID(t *testing.T) {
	// `tsuga <kind> list` returns a bare array. Verified against the live CLI.
	payload := []byte(`[
		{"id": "d-1", "name": "checkout-e2e-abc12345"},
		{"id": "d-2", "name": "checkout-e2e-other000"},
		{"id": "d-3", "name": "unrelated-dashboard"}
	]`)
	got := idsTaggedWithRun(payload, "dashboards", "e2e-abc12345")
	want := []string{"d-1"}
	assertStringSliceEqual(t, got, want)
}

// TestIdsTaggedWithRunReturnsNilOnEnvelopedPayload pins the failure mode to
// watch for if the CLI's response shape ever changes. Kind isolation is
// structural now — the suite makes one `list` call per kind, so a payload can
// only ever contain that kind's resources. But if the CLI started wrapping the
// array in an envelope, this decoder would stop matching anything and the sweep
// would silently delete nothing, leaking every resource the run created. That
// is the quiet failure, so it gets an explicit test.
func TestIdsTaggedWithRunReturnsNilOnEnvelopedPayload(t *testing.T) {
	payload := []byte(`{"dashboards": [{"id": "d-1", "name": "checkout-e2e-abc12345"}]}`)
	got := idsTaggedWithRun(payload, "dashboards", "e2e-abc12345")
	if got != nil {
		t.Fatalf("want nil when the payload is enveloped rather than a bare array, got %v", got)
	}
}

func TestIdsTaggedWithRunReturnsNilForMalformedJSON(t *testing.T) {
	got := idsTaggedWithRun([]byte("not json at all"), "dashboards", "e2e-abc12345")
	if got != nil {
		t.Fatalf("want nil for malformed JSON, got %v", got)
	}
}

func TestIdsTaggedWithRunReturnsNilWhenNoneMatch(t *testing.T) {
	payload := []byte(`[{"id": "d-1", "name": "unrelated"}]`)
	got := idsTaggedWithRun(payload, "dashboards", "e2e-abc12345")
	if got != nil {
		t.Fatalf("want nil when nothing matches, got %v", got)
	}
}

// selfMonitoringTelemetryBlock reproduces the collector's own
// service.telemetry.metrics self-monitoring config, which is present
// unconditionally regardless of which agent pipelines are enabled. A
// substring match against the whole rendered config document would see the
// literal text "metrics" here and wrongly report the metrics pipeline as
// present even when it was correctly pruned.
func selfMonitoringTelemetryBlock() map[string]interface{} {
	return map[string]interface{}{
		"metrics": map[string]interface{}{
			"readers": []interface{}{
				map[string]interface{}{
					"pull": map[string]interface{}{
						"exporter": map[string]interface{}{
							"prometheus": map[string]interface{}{
								"host": "0.0.0.0",
								"port": float64(8888),
							},
						},
					},
				},
			},
		},
	}
}

func TestCheckAgentPipelinesIgnoresSelfMonitoringTelemetryBlock(t *testing.T) {
	config := map[string]interface{}{
		"service": map[string]interface{}{
			"telemetry": selfMonitoringTelemetryBlock(),
			"pipelines": map[string]interface{}{
				"traces": map[string]interface{}{
					"receivers": []interface{}{"otlp"},
					"exporters": []interface{}{"otlp"},
				},
				"logs": map[string]interface{}{
					"receivers": []interface{}{"file_log"},
					"exporters": []interface{}{"otlp"},
				},
			},
		},
		"receivers":  map[string]interface{}{"otlp": map[string]interface{}{}, "file_log": map[string]interface{}{}},
		"connectors": map[string]interface{}{},
	}
	scenario := CollectorScenario{Agent: AgentToggles{Traces: true, Metrics: false, Logs: true}}

	got := checkAgentPipelines(config, scenario)
	if len(got) != 0 {
		t.Fatalf("want no problems for a metrics-disabled scenario despite the "+
			"service.telemetry.metrics self-monitoring block, got %v", got)
	}
}

func TestCheckAgentPipelinesReportsGenuineMismatch(t *testing.T) {
	config := map[string]interface{}{
		"service": map[string]interface{}{
			"telemetry": selfMonitoringTelemetryBlock(),
			"pipelines": map[string]interface{}{
				"metrics": map[string]interface{}{
					"receivers": []interface{}{"hostmetrics"},
					"exporters": []interface{}{"otlp"},
				},
			},
		},
	}
	scenario := CollectorScenario{Agent: AgentToggles{Traces: false, Metrics: false, Logs: false}}

	got := checkAgentPipelines(config, scenario)
	if len(got) == 0 {
		t.Fatal("want a problem reported for a metrics pipeline present with the toggle off, got none")
	}
	var sawMetrics bool
	for _, problem := range got {
		if strings.Contains(problem, "metrics") {
			sawMetrics = true
		}
	}
	if !sawMetrics {
		t.Fatalf("want a problem naming the metrics pipeline, got %v", got)
	}
}

func TestCheckAgentPipelinesDetectsUnprunedSpanMetricsConnector(t *testing.T) {
	config := map[string]interface{}{
		"service": map[string]interface{}{
			"pipelines": map[string]interface{}{
				"logs": map[string]interface{}{
					"receivers": []interface{}{"file_log"},
					"exporters": []interface{}{"otlp"},
				},
			},
		},
		"connectors": map[string]interface{}{"span_metrics": map[string]interface{}{}},
	}
	scenario := CollectorScenario{Agent: AgentToggles{Traces: false, Metrics: false, Logs: true}}

	got := checkAgentPipelines(config, scenario)
	var sawSpanMetrics bool
	for _, problem := range got {
		if strings.Contains(problem, "span_metrics") {
			sawSpanMetrics = true
		}
	}
	if !sawSpanMetrics {
		t.Fatalf("want a problem naming the unpruned span_metrics connector, got %v", got)
	}
}

func TestCheckAgentPipelinesDetectsUnprunedFileLogReceiver(t *testing.T) {
	config := map[string]interface{}{
		"service":   map[string]interface{}{"pipelines": map[string]interface{}{}},
		"receivers": map[string]interface{}{"file_log": map[string]interface{}{}},
	}
	scenario := CollectorScenario{Agent: AgentToggles{Traces: false, Metrics: false, Logs: false}}

	got := checkAgentPipelines(config, scenario)
	var sawFileLog bool
	for _, problem := range got {
		if strings.Contains(problem, "file_log") {
			sawFileLog = true
		}
	}
	if !sawFileLog {
		t.Fatalf("want a problem naming the unpruned file_log receiver, got %v", got)
	}
}

func TestCheckAgentPipelinesPassesWhenFullyPruned(t *testing.T) {
	config := map[string]interface{}{
		"service": map[string]interface{}{
			"telemetry": selfMonitoringTelemetryBlock(),
			"pipelines": map[string]interface{}{
				"metrics": map[string]interface{}{
					"receivers": []interface{}{"hostmetrics"},
					"exporters": []interface{}{"otlp"},
				},
			},
		},
		"receivers":  map[string]interface{}{"hostmetrics": map[string]interface{}{}},
		"connectors": map[string]interface{}{},
	}
	scenario := CollectorScenario{Agent: AgentToggles{Traces: false, Metrics: true, Logs: false}}

	got := checkAgentPipelines(config, scenario)
	if len(got) != 0 {
		t.Fatalf("want no problems for a correctly pruned config, got %v", got)
	}
}

func TestRepoRootResolvesToTheDirectoryHoldingGoMod(t *testing.T) {
	root, err := repoRoot()
	if err != nil {
		t.Fatalf("resolving the repo root: %v", err)
	}
	if !filepath.IsAbs(root) {
		t.Fatalf("want an absolute path, got %q", root)
	}
	// Walking up to go.mod exists because utils.GetProjectDir deletes the
	// substring "/test/e2e" from the working directory, which from
	// test/e2efull cuts characters out of the middle of the path and names a
	// directory that does not exist. Assert both properties that broken
	// version fails: the directory exists, and it is the module root.
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		t.Fatalf("want %q to be an existing directory: %v", root, err)
	}
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("want %q to contain go.mod: %v", root, err)
	}
}

func TestKubeContextPinsTheSuitesOwnCluster(t *testing.T) {
	if got, want := kubeContext(), "kind-"+KindClusterName; got != want {
		t.Fatalf("want kube context %q, got %q", want, got)
	}
}

func TestKubectlCommandPinsTheContextAheadOfTheCallersArguments(t *testing.T) {
	cmd := kubectlCommand("get", "pods")
	want := []string{"kubectl", "--context", "kind-" + KindClusterName, "get", "pods"}
	if !reflect.DeepEqual(cmd.Args, want) {
		t.Fatalf("want args %v, got %v", want, cmd.Args)
	}
}

// The terminating-pod case is the one that matters: it is what made
// `kubectl wait` fail on a collector that was in fact ready.
func TestReadyPodCountsIgnoresTerminatingPods(t *testing.T) {
	raw := `{"items":[
	  {"metadata":{"name":"old","deletionTimestamp":"2026-09-21T10:00:00Z"},
	   "status":{"conditions":[{"type":"Ready","status":"False"}]}},
	  {"metadata":{"name":"new"},
	   "status":{"conditions":[{"type":"Ready","status":"True"}]}}
	]}`
	ready, total, _ := readyPodCounts(raw)
	if ready != 1 || total != 1 {
		t.Fatalf("a terminating pod must not count, want 1/1, got %d/%d", ready, total)
	}
}

func TestReadyPodCountsReportsNotReadyPods(t *testing.T) {
	raw := `{"items":[
	  {"metadata":{"name":"a"},"status":{"conditions":[{"type":"Ready","status":"True"}]}},
	  {"metadata":{"name":"b"},"status":{"conditions":[{"type":"Ready","status":"False"}]}}
	]}`
	ready, total, describe := readyPodCounts(raw)
	if ready != 1 || total != 2 {
		t.Fatalf("want 1/2, got %d/%d", ready, total)
	}
	if !strings.Contains(describe, "b=NotReady") {
		t.Fatalf("the description must name the unready pod, got %q", describe)
	}
}

// An empty list must not read as success: a collector that was never created
// has no pods, and returning 0/0 as "ready" would pass the assertion.
func TestReadyPodCountsTreatsEmptyListAsNotReady(t *testing.T) {
	for _, raw := range []string{"", `{"items":[]}`} {
		ready, total, _ := readyPodCounts(raw)
		if ready != 0 || total != 0 {
			t.Fatalf("empty input %q must yield 0/0, got %d/%d", raw, ready, total)
		}
	}
}
