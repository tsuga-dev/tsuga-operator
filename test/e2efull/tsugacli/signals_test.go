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

func TestCountLogsAndsTheExtraQueryOntoTheClusterFilter(t *testing.T) {
	bin := stubCLI(t, `
case "$*" in
  *'--query context.k8s.cluster.name:"c1" AND context.k8s.event.reason:* '*)
    echo '{"logs":[{"message":"a"},{"message":"b"}]}' ;;
  *) echo '{"logs":[]}' ;;
esac`)
	c := New(testConfig(), &bytes.Buffer{})
	c.Bin = bin

	n, err := c.CountLogs("c1", "context.k8s.event.reason:*", "-15m")
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("want 2 logs for the combined query, got %d", n)
	}
}

func TestCountLogsWithNoExtraQueryUsesTheClusterFilterAlone(t *testing.T) {
	bin := stubCLI(t, `
case "$*" in
  *'--query context.k8s.cluster.name:"c1" --from'*)
    echo '{"logs":[{"message":"a"}]}' ;;
  *) echo '{"logs":[]}' ;;
esac`)
	c := New(testConfig(), &bytes.Buffer{})
	c.Bin = bin

	n, err := c.CountLogs("c1", "", "-15m")
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("want 1 log for the cluster filter alone, got %d", n)
	}
}

func TestCountSpansAndsTheExtraQueryOntoTheClusterFilter(t *testing.T) {
	bin := stubCLI(t, `
case "$*" in
  *'--query context.k8s.cluster.name:"c1" AND span.kind:server '*)
    echo '{"spans":[{"spanId":"1"},{"spanId":"2"}]}' ;;
  *) echo '{"spans":[]}' ;;
esac`)
	c := New(testConfig(), &bytes.Buffer{})
	c.Bin = bin

	n, err := c.CountSpans("c1", "span.kind:server", "-15m")
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("want 2 spans for the combined query, got %d", n)
	}
}

func TestCountMetricSeriesWhereAndsTheExtraFilter(t *testing.T) {
	bin := stubCLI(t, `
case "$*" in
  *'"filter": "context.k8s.cluster.name:\"c1\" AND context.k8s.namespace.name:\"ns\""'*)
    echo '{"results":[{"id":"q1","group":{},"value":4}]}' ;;
  *) echo '{"results":[]}' ;;
esac`)
	c := New(testConfig(), &bytes.Buffer{})
	c.Bin = bin

	n, err := c.CountMetricSeriesWhere("cpu.usage", "c1", `context.k8s.namespace.name:"ns"`, "-15m")
	if err != nil {
		t.Fatal(err)
	}
	if n != 4 {
		t.Fatalf("want 4 series when the extra filter is ANDed in, got %d", n)
	}
}

// pagedStub serves a 1000-item first page (one match, prefixed with the
// "Showing" line) and a short second page, keyed on --offset.
func pagedStub(t *testing.T) string {
	t.Helper()
	return stubCLI(t, `
case "$*" in
  *"--offset 0"*)
    echo "Showing 1000 of 1001 (offset 0). Use --limit (max 1000) and --offset 1000 for the next page."
    printf '['
    printf '{"id":"p1","name":"e2e-abc12345-first"}'
    i=0
    while [ $i -lt 999 ]; do printf ',{"id":"f%s","name":"filler"}' $i; i=$((i+1)); done
    echo ']' ;;
  *"--offset 1000"*) echo '[{"id":"p2","name":"e2e-abc12345-second"}]' ;;
  *) echo "unexpected offset: $*" >&2; exit 1 ;;
esac`)
}

func TestListTaggedWithRunReadsEveryPageAndStopsOnShortPage(t *testing.T) {
	c := New(testConfig(), &bytes.Buffer{})
	c.Bin = pagedStub(t)

	got, err := c.ListTaggedWithRun("dashboards", "e2e-abc12345")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].ID != "p1" || got[1].ID != "p2" {
		t.Fatalf("want p1 and p2 from both pages, got %+v", got)
	}
}

func TestListTaggedWithRunStopsWhenTheCLIIgnoresOffset(t *testing.T) {
	c := New(testConfig(), &bytes.Buffer{})
	c.Bin = stubCLI(t, `
printf '[{"id":"p1","name":"e2e-abc12345"}'
i=0
while [ $i -lt 999 ]; do printf ',{"id":"f%s","name":"filler"}' $i; i=$((i+1)); done
echo ']'`)

	got, err := c.ListTaggedWithRun("monitors", "e2e-abc12345")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != "p1" {
		t.Fatalf("want p1 once, got %+v", got)
	}
}

func TestListTaggedWithRunGivesUpAfterThePageCap(t *testing.T) {
	c := New(testConfig(), &bytes.Buffer{})
	c.Bin = stubCLI(t, `
offset=$(echo "$*" | sed 's/.*--offset \([0-9]*\).*/\1/')
printf '[{"id":"first-%s","name":"filler"}' "$offset"
i=0
while [ $i -lt 999 ]; do printf ',{"id":"f%s","name":"filler"}' $i; i=$((i+1)); done
echo ']'`)

	if _, err := c.ListTaggedWithRun("monitors", "e2e-abc12345"); err == nil {
		t.Fatal("want an error once the page cap is reached")
	}
}
