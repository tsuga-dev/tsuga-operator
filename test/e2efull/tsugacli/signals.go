package tsugacli

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// unwrapData returns the contents of a `data` envelope when the payload has
// one, and the payload untouched otherwise.
//
// The Tsuga API wraps every response as {"requestId": ..., "data": ...}, but
// the CLI may unwrap that before printing. Rather than bet the suite on which
// one arrives — a wrong guess decodes to zero items silently, which fails every
// positive assertion and makes every negative one vacuous — accept both.
// Response shapes verified against public-open-api.json: /v1/logs/search
// returns data.logs, /v1/traces/search returns data.spans, and the
// kubernetes-explorer endpoints return data as a bare array whose cluster field
// is named `cluster`.
func unwrapData(raw []byte) []byte {
	var envelope struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(raw, &envelope); err == nil && len(envelope.Data) > 0 {
		return envelope.Data
	}
	return raw
}

// decodeLogs accepts both shapes `logs search -o json` has printed: a bare
// array (CLI v1.53+) and the older `{"logs": [...]}` object.
func decodeLogs(out []byte) ([]json.RawMessage, error) {
	raw := unwrapData(out)
	var logs []json.RawMessage
	if err := json.Unmarshal(raw, &logs); err == nil {
		return logs, nil
	}
	var body struct {
		Logs []json.RawMessage `json:"logs"`
	}
	err := json.Unmarshal(raw, &body)
	return body.Logs, err
}

// SignalCounter returns how many items of one telemetry kind exist for a
// cluster name. Used by AssertAbsentWithControl.
type SignalCounter func(clusterName string) (int, error)

// clusterFilter is the query predicate selecting one cluster's telemetry.
//
// Tsuga indexes OTel resource attributes under a `context.` namespace, so the
// bare attribute name a collector config writes is NOT what a query matches on:
// `k8s.cluster.name:"x"` parses fine and returns nothing at all, for every
// cluster, forever. Confirmed against the live API for logs, traces and
// metrics, and visible in `tsuga metrics list`, which reports each metric's
// queryable attributes already prefixed.
//
// This is the failure mode every negative assertion in this file is built to
// survive: an empty result that looks exactly like a correctly pruned
// pipeline. Keep the prefix in one place so a positive and its control can
// never drift apart.
func clusterFilter(clusterName string) string {
	return fmt.Sprintf("context.k8s.cluster.name:%q", clusterName)
}

// CountContainerLogs returns how many container log records carry this cluster
// name - the output of the agent's file_log receiver, which is what the
// agent.logs toggle controls.
//
// The log.iostream term is what makes this specific to that toggle. A cluster's
// log stream also carries the gateway's k8s_objects output, both its object
// records and the entity events it emits with an empty scope, so counting every
// log record for the cluster reports the agent's logs pipeline as alive
// whenever kubernetesObjects is on. Measured: a scenario with agent.logs off
// and kubernetesObjects on returned 100 records unfiltered and 0 with this
// term.
//
// Note on `-o json`: `logs search` is the ONLY tsuga subcommand that accepts an
// output flag. Every other one — traces search, aggregation, kubernetes, teams,
// dashboards, monitors — rejects it with "unknown option '-o'" and fails the
// call outright. JSON is their default output regardless, so the flag buys
// nothing. Do not add it elsewhere for consistency's sake.
func (c *Client) CountContainerLogs(clusterName, from string) (int, error) {
	out, err := c.Run("logs", "search",
		"--query", clusterFilter(clusterName)+` AND log.iostream:"stdout"`,
		"--from="+from, "--max-results", "100", "-o", "json")
	if err != nil {
		return 0, err
	}
	logs, err := decodeLogs(out)
	if err != nil {
		return 0, fmt.Errorf("decode logs response: %w: %s", err, c.redact(string(out)))
	}
	return len(logs), nil
}

// k8sObjectsScope is the instrumentation scope the k8s_objects receiver stamps
// on everything it emits. It is what tells that receiver's output apart from
// the agent's ordinary container logs, which share the cluster name.
const k8sObjectsScope = "github.com/open-telemetry/opentelemetry-collector-contrib/receiver/k8sobjectsreceiver"

// CountK8sObjectLogs returns how many records the gateway's k8s_objects
// receiver produced for this cluster.
//
// This, not the Kubernetes explorer, is what the kubernetesObjects toggle
// controls. k8s_objects watches pods and ships them as logs; the explorer's
// pod and cluster views are built from the k8s_cluster receiver, which the
// clusterMetrics toggle controls. Measured: a scenario with kubernetesObjects
// on and clusterMetrics off puts zero rows in the explorer, and a scenario
// with the toggles the other way round puts eight pods there while emitting
// no k8s_objects logs at all.
func (c *Client) CountK8sObjectLogs(clusterName, from string) (int, error) {
	out, err := c.Run("logs", "search",
		"--query", fmt.Sprintf("%s AND context.scope.name:%q", clusterFilter(clusterName), k8sObjectsScope),
		"--from="+from, "--max-results", "100", "-o", "json")
	if err != nil {
		return 0, err
	}
	logs, err := decodeLogs(out)
	if err != nil {
		return 0, fmt.Errorf("decode k8s object logs response: %w: %s", err, c.redact(string(out)))
	}
	return len(logs), nil
}

// CountTraces returns how many spans carry this cluster name.
func (c *Client) CountTraces(clusterName, from string) (int, error) {
	out, err := c.Run("traces", "search",
		"--query", clusterFilter(clusterName),
		"--from="+from, "--max-results", "100")
	if err != nil {
		return 0, err
	}
	var body struct {
		Spans []json.RawMessage `json:"spans"`
	}
	if err := json.Unmarshal(unwrapData(out), &body); err != nil {
		return 0, fmt.Errorf("decode traces response: %w: %s", err, c.redact(string(out)))
	}
	return len(body.Spans), nil
}

// CountTracesForWorkload counts spans for one workload on one cluster. Tier B
// needs this: asserting spans exist for the whole cluster lets one language's
// scenario pass on the spans another language's scenario produced.
//
// The discriminator is the Deployment name, not service.name. Spans are not
// indexed on `context.service.name` — the query parses and matches nothing,
// while logs from the very same pod do match it. The k8s_attributes processor's
// output is indexed for spans, so `context.k8s.deployment.name` is the handle
// that works, and Tier B already names each language's Deployment
// app-<language>, which is exactly the per-scenario scope the assertion wants.
func (c *Client) CountTracesForWorkload(workloadName, clusterName, from string) (int, error) {
	out, err := c.Run("traces", "search",
		"--query", fmt.Sprintf("%s AND context.k8s.deployment.name:%q",
			clusterFilter(clusterName), workloadName),
		"--from="+from, "--max-results", "100")
	if err != nil {
		return 0, err
	}
	var body struct {
		Spans []json.RawMessage `json:"spans"`
	}
	if err := json.Unmarshal(unwrapData(out), &body); err != nil {
		return 0, fmt.Errorf("decode traces response: %w: %s", err, c.redact(string(out)))
	}
	return len(body.Spans), nil
}

// AssertAbsentWithControl checks that scenarioCluster has no data of this
// kind, and that controlCluster does. Without the control an empty result
// proves nothing: a wrong field name, a malformed query or indexing lag all
// look identical to a genuinely pruned pipeline.
//
// The two counters are separate because they need different time windows. The
// control cluster's collectors were torn down when its scenario ended, so its
// telemetry leaves a short window long before the tier finishes; the control
// counter queries a wide window, the scenario counter a narrow one.
func AssertAbsentWithControl(count, controlCount SignalCounter, scenarioCluster, controlCluster string) error {
	scenarioCount, err := count(scenarioCluster)
	if err != nil {
		return fmt.Errorf("querying %s: %w", scenarioCluster, err)
	}
	if scenarioCount > 0 {
		return fmt.Errorf("expected no data for %s, found %d items",
			scenarioCluster, scenarioCount)
	}
	control, err := controlCount(controlCluster)
	if err != nil {
		return fmt.Errorf("querying control %s: %w", controlCluster, err)
	}
	if control == 0 {
		return fmt.Errorf(
			"control cluster %s returned no data either, so the absence of data for %s proves nothing "+
				"(suite error: check the query shape, not the operator)",
			controlCluster, scenarioCluster)
	}
	return nil
}

//go:embed testdata/aggregation-scalar.json
var aggregationScalarTemplate string

// CountMetricSeries returns how many datapoints metricName produced on this
// cluster in the window. Zero means the metric is not emitting, which is what
// the toggle assertions need: `tsuga metrics list` is a static catalog that
// still lists a metric long after it stopped arriving.
//
// Two things about the metrics aggregation are not guessable from the other
// signal queries, and both returned a confident, silent zero when guessed:
//
//   - The metric is selected by aggregate.field, not by a metric.name term in
//     the filter. `filter: "metric.name:\"x\""` matches nothing; the filter is
//     for attributes only.
//   - A filter that matches no data returns `results: []`, while a filter that
//     matches with an unknown aggregate field returns one row with value 0.
//     Neither is an error, so a wrong field name reads as "the pipeline is
//     off".
//
// The aggregation body requires an absolute Unix-second timeRange (unlike
// logs/traces search, which take a relative --from string), so `from` is
// parsed as a duration relative to now rather than substituted verbatim.
func (c *Client) CountMetricSeries(metricName, clusterName, from string) (int, error) {
	relative, err := time.ParseDuration(from)
	if err != nil {
		return 0, fmt.Errorf("parsing relative time %q: %w", from, err)
	}
	to := time.Now()
	body := strings.NewReplacer(
		"__METRIC__", metricName,
		"__CLUSTER__", clusterName,
		"__FROM__", strconv.FormatInt(to.Add(relative).Unix(), 10),
		"__TO__", strconv.FormatInt(to.Unix(), 10),
	).Replace(aggregationScalarTemplate)

	out, err := c.Run("aggregation", "scalar", "-d", body)
	if err != nil {
		return 0, err
	}
	var result struct {
		Results []struct {
			Value float64 `json:"value"`
		} `json:"results"`
	}
	if err := json.Unmarshal(unwrapData(out), &result); err != nil {
		return 0, fmt.Errorf("decode aggregation response: %w: %s", err, c.redact(string(out)))
	}
	total := 0
	for _, r := range result.Results {
		total += int(r.Value)
	}
	return total, nil
}

// explorerRow is one row of a Kubernetes-explorer listing. ReadyNodes is a
// pointer because its absence is the signal: see CountK8sClusters.
type explorerRow struct {
	Cluster    string `json:"cluster"`
	ReadyNodes *int   `json:"readyNodes"`
}

// explorerRows returns every row one of the Kubernetes-explorer endpoints
// reports. Both endpoints key their rows the same way, so clusters and pods
// share this.
func (c *Client) explorerRows(subcommand string) ([]explorerRow, error) {
	out, err := c.Run("kubernetes", subcommand)
	if err != nil {
		return nil, err
	}
	var rows []explorerRow
	if err := json.Unmarshal(unwrapData(out), &rows); err != nil {
		return nil, fmt.Errorf("decode %s response: %w: %s", subcommand, err, c.redact(string(out)))
	}
	return rows, nil
}

func names(rows []explorerRow) []string {
	out := make([]string, 0, len(rows))
	for _, row := range rows {
		out = append(out, row.Cluster)
	}
	return out
}

func countMatching(names []string, clusterName string) int {
	matching := 0
	for _, name := range names {
		if name == clusterName {
			matching++
		}
	}
	return matching
}

func countOtherThan(names []string, prefix string) int {
	other := 0
	for _, name := range names {
		if !strings.HasPrefix(name, prefix) {
			other++
		}
	}
	return other
}

// CountK8sClusters returns how many entries Tsuga's Kubernetes explorer
// holds for this cluster name, which is what the gateway's cluster-metrics
// pipeline produces.
//
// A counter rather than a boolean so cluster metrics can sit in Tier A's
// signals table alongside logs, traces and pods, and get the same treatment:
// a positive when the toggle is on, and a negative against a live control
// when it is off. Sixteen of the 32 toggle scenarios have clusterMetrics off
// and previously asserted nothing at all about it.
func (c *Client) CountK8sClusters(clusterName string) (int, error) {
	rows, err := c.explorerRows("clusters")
	if err != nil {
		return 0, err
	}
	return countClusterMetricRows(rows, clusterName), nil
}

// countClusterMetricRows counts rows for clusterName that actually carry
// cluster metrics.
//
// Mere presence in the clusters listing proves nothing about the k8s_cluster
// receiver: a cluster shows up there as soon as it sends anything at all, so a
// scenario with the whole gateway switched off still gets a row - with
// readyNodes and podPhaseCounts null. Those fields are what k8s_cluster
// populates, so readyNodes is the discriminator.
func countClusterMetricRows(rows []explorerRow, clusterName string) int {
	matching := 0
	for _, row := range rows {
		if row.Cluster == clusterName && row.ReadyNodes != nil {
			matching++
		}
	}
	return matching
}

// CountK8sPods returns how many pods Tsuga has observed for this cluster,
// which is what the gateway's k8s_objects pipeline produces.
func (c *Client) CountK8sPods(clusterName string) (int, error) {
	rows, err := c.explorerRows("pods")
	if err != nil {
		return 0, err
	}
	return countMatching(names(rows), clusterName), nil
}

// CountForeignK8sClusters and CountForeignK8sPods count explorer rows that do
// NOT belong to this run, given the run's cluster-name prefix.
//
// They exist because the explorer is a view of what is reporting now, not a
// time-windowed query: it takes no --from, and a cluster drops out of it once
// its collectors stop. Every other signal can use a finished scenario as its
// control by widening the window, but these two cannot - the control scenario
// disappears from the explorer minutes after its collectors are torn down,
// and the control then reads as zero, which fails the absence assertion as a
// suite error even though the operator did exactly the right thing.
//
// Any cluster outside this run serves the purpose a control is actually for:
// proving that an empty result means "absent" rather than "the query is
// broken or the endpoint is empty". It says nothing about this run, which is
// precisely why it stays valid for the whole tier.
func (c *Client) CountForeignK8sClusters(runPrefix string) (int, error) {
	rows, err := c.explorerRows("clusters")
	if err != nil {
		return 0, err
	}
	// The control has to clear the same bar the scenario is judged against,
	// so it counts only foreign clusters that carry cluster metrics.
	foreign := 0
	for _, row := range rows {
		if !strings.HasPrefix(row.Cluster, runPrefix) && row.ReadyNodes != nil {
			foreign++
		}
	}
	return foreign, nil
}

func (c *Client) CountForeignK8sPods(runPrefix string) (int, error) {
	rows, err := c.explorerRows("pods")
	if err != nil {
		return 0, err
	}
	return countOtherThan(names(rows), runPrefix), nil
}

// TaggedResource is one Tsuga resource a run created, as a `list` call
// reports it.
type TaggedResource struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// ResourcesTaggedWithRun extracts the resources in a `tsuga <kind> list`
// payload whose name carries runID.
//
// `tsuga <kind> list` returns a BARE JSON array of resources, not an object
// keyed by kind. Verified against the live CLI for both dashboards and
// monitors. An earlier version decoded a kind-keyed map, which failed to
// unmarshal entirely and made the sweep a silent no-op: every run leaked its
// dashboards and monitors into the org.
//
// Matching is narrow by construction: only resources whose NAME carries the
// run id are returned, and every name the suite creates is built from runID.
// Do not widen this to match on anything else - the sweep deletes what it
// returns, against an org that holds resources this suite did not create.
//
// kind is used only to give a decode failure useful context; the payload
// holds one kind's resources, because the caller lists each kind separately.
func ResourcesTaggedWithRun(payload []byte, kind, runID string) ([]TaggedResource, error) {
	var items []TaggedResource
	if err := json.Unmarshal(payload, &items); err != nil {
		return nil, fmt.Errorf("decoding %s list: %w", kind, err)
	}
	var tagged []TaggedResource
	for _, item := range items {
		if strings.Contains(item.Name, runID) {
			tagged = append(tagged, item)
		}
	}
	return tagged, nil
}
