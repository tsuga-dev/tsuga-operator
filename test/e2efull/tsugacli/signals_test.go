package tsugacli

import (
	"bytes"
	"strings"
	"testing"
)

// The iostream term is in the stub pattern deliberately: a query without it
// also matches the gateway's k8s_objects records, which would report the
// agent's logs pipeline as alive when only kubernetesObjects is on.
func TestCountContainerLogsFiltersByClusterAndStream(t *testing.T) {
	bin := stubCLI(t, `
case "$*" in
  *"context.k8s.cluster.name:\"run-s01\" AND log.iostream:\"stdout\""*)
    echo '{"logs":[{"message":"a"},{"message":"b"}]}' ;;
  *) echo '{"logs":[]}' ;;
esac`)
	c := New(testConfig(), &bytes.Buffer{})
	c.Bin = bin

	n, err := c.CountContainerLogs("run-s01", "-15m")
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("want 2 logs for the matching cluster, got %d", n)
	}

	n, err = c.CountContainerLogs("run-s02", "-15m")
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("want 0 logs for a different cluster, got %d", n)
	}
}

func TestCountContainerLogsAcceptsEnvelopeAndBareShapes(t *testing.T) {
	// The API wraps responses as {"requestId":..,"data":{..}}; the CLI may or
	// may not unwrap that before printing. Both must decode, because a silent
	// zero here fails every positive assertion in the suite.
	for name, payload := range map[string]string{
		"enveloped": `{"requestId":"r1","data":{"logs":[{"message":"a"},{"message":"b"}]}}`,
		"bare":      `{"logs":[{"message":"a"},{"message":"b"}]}`,
	} {
		bin := stubCLI(t, "cat <<'JSON'\n"+payload+"\nJSON")
		c := New(testConfig(), &bytes.Buffer{})
		c.Bin = bin
		got, err := c.CountContainerLogs("run-s01", "-15m")
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if got != 2 {
			t.Errorf("%s: want 2 logs, got %d", name, got)
		}
	}
}

// A row with readyNodes null is a cluster that is sending something but no
// cluster metrics - which is what every gateway-off scenario looks like. It
// must not count, or the clusterMetrics assertion passes for all of them.
func TestCountK8sClustersRequiresClusterMetricFields(t *testing.T) {
	bin := stubCLI(t, `cat <<'JSON'
{"requestId":"r1","data":[
  {"cluster":"run-s00","readyNodes":1},{"cluster":"run-s01"},{"cluster":"other","readyNodes":3}]}
JSON`)
	c := New(testConfig(), &bytes.Buffer{})
	c.Bin = bin

	found, err := c.CountK8sClusters("run-s00")
	if err != nil {
		t.Fatal(err)
	}
	if found != 1 {
		t.Fatalf("want the cluster with readyNodes to count, got %d", found)
	}
	listedWithoutMetrics, err := c.CountK8sClusters("run-s01")
	if err != nil {
		t.Fatal(err)
	}
	if listedWithoutMetrics != 0 {
		t.Fatalf("a row without readyNodes carries no cluster metrics and must not count, got %d",
			listedWithoutMetrics)
	}
	missing, err := c.CountK8sClusters("absent")
	if err != nil {
		t.Fatal(err)
	}
	if missing != 0 {
		t.Fatalf("want no match for a cluster that is not listed, got %d", missing)
	}
}

// The control has to clear the same bar as the scenario: a foreign cluster
// with no cluster metrics cannot vouch for a query that demands them.
func TestCountForeignK8sClustersRequiresClusterMetricFields(t *testing.T) {
	bin := stubCLI(t, `cat <<'JSON'
{"requestId":"r1","data":[
  {"cluster":"run-s00","readyNodes":1},{"cluster":"foreign-no-metrics"},{"cluster":"foreign-live","readyNodes":3}]}
JSON`)
	c := New(testConfig(), &bytes.Buffer{})
	c.Bin = bin

	n, err := c.CountForeignK8sClusters("run-")
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("only the foreign cluster carrying cluster metrics may count, got %d", n)
	}
}

func TestCountK8sPodsFiltersByClusterField(t *testing.T) {
	bin := stubCLI(t, `cat <<'JSON'
{"requestId":"r1","data":[{"cluster":"run-s00"},{"cluster":"run-s00"},{"cluster":"other"}]}
JSON`)
	c := New(testConfig(), &bytes.Buffer{})
	c.Bin = bin

	got, err := c.CountK8sPods("run-s00")
	if err != nil {
		t.Fatal(err)
	}
	if got != 2 {
		t.Fatalf("want 2 pods for run-s00, got %d", got)
	}
}

func TestAssertAbsentWithControlPassesWhenControlHasData(t *testing.T) {
	none := func(string) (int, error) { return 0, nil }
	live := func(string) (int, error) { return 5, nil }
	if err := AssertAbsentWithControl(none, live, "scenario", "control"); err != nil {
		t.Fatalf("absent signal with a live control should pass, got: %v", err)
	}
}

func TestAssertAbsentWithControlFailsWhenSignalPresent(t *testing.T) {
	present := func(string) (int, error) { return 3, nil }
	live := func(string) (int, error) { return 5, nil }
	err := AssertAbsentWithControl(present, live, "scenario", "control")
	if err == nil || !strings.Contains(err.Error(), "expected no data") {
		t.Fatalf("present signal should fail the absence assertion, got: %v", err)
	}
}

func TestAssertAbsentWithControlFailsWhenControlIsEmpty(t *testing.T) {
	none := func(string) (int, error) { return 0, nil }
	err := AssertAbsentWithControl(none, none, "scenario", "control")
	if err == nil || !strings.Contains(err.Error(), "control") {
		t.Fatalf("an empty control makes the assertion vacuous and must fail loudly, got: %v", err)
	}
}

// The stub matches the fully-prefixed query on purpose. Tsuga indexes resource
// attributes under `context.`, and an unprefixed name returns an empty result
// rather than an error, so a stub that accepted both spellings would keep
// passing against the one that can never match live data.
func TestCountTracesForWorkloadFiltersByWorkloadAndCluster(t *testing.T) {
	bin := stubCLI(t, `
case "$*" in
  *"context.k8s.cluster.name:\"run-s01\" AND context.k8s.deployment.name:\"checkout\""*)
    echo '{"spans":[{"spanId":"1"}]}' ;;
  *) echo '{"spans":[]}' ;;
esac`)
	c := New(testConfig(), &bytes.Buffer{})
	c.Bin = bin

	n, err := c.CountTracesForWorkload("checkout", "run-s01", "-15m")
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("want 1 span for the matching service and cluster, got %d", n)
	}

	n, err = c.CountTracesForWorkload("checkout", "run-s02", "-15m")
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("want 0 spans for a different cluster, got %d", n)
	}
}

func TestCountMetricSeriesSumsResults(t *testing.T) {
	// Two shapes are pinned here. The scalar endpoint returns
	// {"results":[{"id","group","value"}]} per public-open-api.json's
	// ScalarAggregationResponse, not a bare "values" list. And the metric is
	// named in aggregate.field, not in the filter — a filter-side metric.name
	// term matches nothing live, so the stub only answers the request that
	// carries the metric as the field.
	bin := stubCLI(t, `
case "$*" in
  *'"field": "cpu.usage"'*'context.k8s.cluster.name:\"run-s01\"'*) cat <<'JSON'
{"requestId":"r1","data":{"results":[{"id":"q1","group":{},"value":3}]}}
JSON
;;
  *) echo '{"results":[]}' ;;
esac`)
	c := New(testConfig(), &bytes.Buffer{})
	c.Bin = bin

	n, err := c.CountMetricSeries("cpu.usage", "run-s01", "-15m")
	if err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Fatalf("want 3 series, got %d", n)
	}
}

func TestCountMetricSeriesRejectsUnparsableFrom(t *testing.T) {
	c := New(testConfig(), &bytes.Buffer{})
	c.Bin = stubCLI(t, `echo '{"results":[]}'`)

	if _, err := c.CountMetricSeries("cpu.usage", "run-s01", "not-a-duration"); err == nil {
		t.Fatal("want an error when from cannot be parsed as a duration")
	}
}

// The foreign control must exclude everything this run created, not just the
// one cluster it is compared against. Counting a sibling scenario's cluster
// would let a run act as its own control, which is the thing the control
// exists to rule out.
func TestCountOtherThanExcludesEveryClusterOfThisRun(t *testing.T) {
	names := []string{
		"e2e-abc123-s00", "e2e-abc123-s01", "e2e-abc123-s01",
		"otel-demo-cluster", "desktop-k8s-someone",
	}
	if got := countOtherThan(names, "e2e-abc123"); got != 2 {
		t.Fatalf("want 2 foreign clusters, got %d", got)
	}
	if got := countOtherThan(names, "e2e-abc123-s00"); got != 4 {
		t.Fatalf("a narrower prefix must leave siblings foreign, want 4, got %d", got)
	}
}

func TestCountMatchingCountsDuplicateRows(t *testing.T) {
	names := []string{"a", "b", "a"}
	if got := countMatching(names, "a"); got != 2 {
		t.Fatalf("want 2, got %d", got)
	}
	if got := countMatching(names, "zzz"); got != 0 {
		t.Fatalf("want 0 for an absent cluster, got %d", got)
	}
}

// The scope term is the whole point of this query: without it the count picks
// up the agent's ordinary container logs, which carry the same cluster name,
// and the kubernetesObjects toggle would appear to work whenever the agent's
// logs pipeline happened to be on.
func TestCountK8sObjectLogsRequiresTheReceiverScope(t *testing.T) {
	bin := stubCLI(t, `
scope=github.com/open-telemetry/opentelemetry-collector-contrib/receiver/k8sobjectsreceiver
case "$*" in
  *"context.k8s.cluster.name:\"run-s01\" AND context.scope.name:\"$scope\""*)
    echo '{"logs":[{"message":"a"},{"message":"b"}]}' ;;
  *) echo '{"logs":[]}' ;;
esac`)
	c := New(testConfig(), &bytes.Buffer{})
	c.Bin = bin

	n, err := c.CountK8sObjectLogs("run-s01", "-15m")
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("want 2 k8s_objects records, got %d", n)
	}

	n, err = c.CountK8sObjectLogs("run-s02", "-15m")
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("want 0 for a different cluster, got %d", n)
	}
}
