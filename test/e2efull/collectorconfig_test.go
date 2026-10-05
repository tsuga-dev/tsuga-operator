package e2efull

import (
	"fmt"
	"strings"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/tsuga-dev/tsuga-operator/test/e2efull/tsugacli"
)

const (
	// signalPollInterval and absenceWindow keep Tier A's assertions under the
	// API's measured ceiling of roughly 6 requests per minute. A positive poll
	// costs one request and a negative poll costs two, one for the scenario and
	// one for its control, so a 10s interval demanded about 12 per minute and
	// sat in permanent deficit.
	signalPollInterval = 30 * time.Second
	absenceWindow      = 90 * time.Second

	tierANamespace   = "e2e-tier-a"
	collectorTimeout = 180 * time.Second
	// hostMetric is produced by the agent's host_metrics scraper whenever the
	// metrics pipeline is enabled, so it is the metric liveness probe.
	hostMetric = "system.cpu.utilization"
)

// tierAScenarios builds the Tier A scenario slice. Both the spec tree below
// and TestTierAScenariosUseRealRunIDNotPlaceholder call this same function,
// so a regression that reintroduces a placeholder run id here is caught by
// the unit test without needing to run Ginkgo.
func tierAScenarios() []CollectorScenario {
	return CollectorScenarios(runID)
}

// tierASpecs registers Tier A. It is a function rather than a top-level
// `var _ = Describe(...)` because Ginkgo randomizes the order of top-level
// containers; suite_test.go nests every tier under one Ordered container so
// the documented Tier C -> Tier A -> Tier B order is the order that runs.
func tierASpecs() {
	Describe("Tier A: TsugaCollectorConfig", Ordered, Label("tier-a"), func() {
		var baselineCluster string
		// Ginkgo builds this entire spec tree - including the `for` loop below -
		// before BeforeAll ever runs. So the scenario slice must be built here,
		// at tree-construction time, from the real runID (already resolved
		// eagerly in cluster.go). A BeforeAll reassignment of an outer variable
		// would be too late: each It below closes over the scenario value
		// captured when this loop executes, not whatever a later reassignment
		// sets the outer slice to. Do not reintroduce a placeholder seed here.
		scenarios := tierAScenarios()

		BeforeAll(func() {
			Expect(applyYAML(fmt.Sprintf(
				"apiVersion: v1\nkind: Namespace\nmetadata:\n  name: %s\n", tierANamespace))).To(Succeed())
		})

		for i := range scenarios {
			scenario := scenarios[i]

			It("handles "+scenario.Name, func() {
				collectorNS := operatorNS
				if scenario.Variant == "customNamespace" {
					collectorNS = "e2e-collectors"
					Expect(applyYAML(fmt.Sprintf(
						"apiVersion: v1\nkind: Namespace\nmetadata:\n  name: %s\n", collectorNS))).To(Succeed())
					Expect(copyIngestionSecret(collectorNS)).To(Succeed())
				}
				if scenario.Variant == "endpointSecretRef" {
					Expect(createEndpointSecret(collectorNS)).To(Succeed())
				}

				manifest := renderCollectorConfig(scenario, cli.Config().OTLPEndpoint, collectorNS)
				Expect(applyYAML(manifest)).To(Succeed())
				DeferCleanup(func() {
					_ = deleteYAML(manifest)
					// Deleting the CR does not block on its OpenTelemetryCollector
					// children, which the Kubernetes garbage collector removes in the
					// background. Every scenario reuses the same collector names, so
					// the next scenario's config must not be applied until this
					// scenario's collectors have actually drained.
					Expect(waitForCollectorsGone(collectorNS, 90*time.Second)).To(Succeed())
				})

				if scenario.ExpectError {
					By("expecting the CR to report an error and create no collectors")
					Expect(waitForCRPhase("tsugacollectorconfig", "", collectorConfigName,
						"Error", collectorTimeout)).To(Succeed())
					out, err := kubectlStdout("get", "opentelemetrycollectors", "-n", collectorNS,
						"--ignore-not-found", "-o", "name")
					Expect(err).NotTo(HaveOccurred())
					Expect(out).To(BeEmpty(), "no collector may be created when every agent signal is off")
					return
				}

				By("waiting for the CR and its collectors to be ready")
				Expect(waitForCRPhase("tsugacollectorconfig", "", collectorConfigName,
					"Ready", collectorTimeout)).To(Succeed())
				Expect(waitForCollectorReady(collectorNS, "tsuga-agent", collectorTimeout)).To(Succeed())

				if scenario.ExpectGateway {
					Expect(waitForCollectorReady(collectorNS, "tsuga-gateway", collectorTimeout)).To(Succeed())
				} else {
					out, _ := kubectlStdout("get", "opentelemetrycollector", "tsuga-gateway",
						"-n", collectorNS, "--ignore-not-found", "-o", "name")
					Expect(out).To(BeEmpty(), "gateway must be absent when both gateway toggles are off")
				}

				By("asserting the rendered pipelines match the toggles")
				assertPipelines(collectorNS, scenario)

				if scenario.Variant == "imageOverride" {
					out, err := kubectl("get", "opentelemetrycollector", "tsuga-agent",
						"-n", collectorNS, "-o", "jsonpath={.spec.image}")
					Expect(err).NotTo(HaveOccurred())
					Expect(out).To(ContainSubstring("0.157.0"))
				}
				if scenario.Variant == "resourcesOverride" {
					out, err := kubectl("get", "opentelemetrycollector", "tsuga-agent",
						"-n", collectorNS, "-o", "jsonpath={.spec.resources.limits.memory}")
					Expect(err).NotTo(HaveOccurred())
					Expect(out).To(Equal("1Gi"))
				}

				By("running the emitter so the pipelines have something to carry")
				emitter := renderEmitterWorkload(tierANamespace, scenario.ClusterName, collectorNS)
				Expect(applyYAML(emitter)).To(Succeed())
				DeferCleanup(func() { _ = deleteYAML(emitter) })
				_, err := kubectl("wait", "--for=condition=Available",
					"-n", tierANamespace, "deploy/otlp-emitter", "--timeout=120s")
				Expect(err).NotTo(HaveOccurred())

				assertSignalsInTsuga(scenario, baselineCluster)

				if scenario.IsBaseline {
					baselineCluster = scenario.ClusterName
				}
			})
		}
	})
}

// assertSignalsInTsuga checks each enabled toggle produced data and each
// disabled toggle produced none, the latter always against a live control.
//
// Positives run before negatives, deliberately. A scenario's cluster name is
// brand new, so "no traces for this cluster" is trivially true the instant the
// scenario starts and would pass without meaning. Once a positive has landed we
// know ingestion is live for this cluster name, and absence becomes evidence.
//
// The control counters use a wide window because the control scenario's
// collectors were torn down when it ended: its telemetry leaves a 15 minute
// window long before this ~90 minute tier finishes.
func assertSignalsInTsuga(s CollectorScenario, controlCluster string) {
	const scenarioWindow = "-15m"
	const controlWindow = "-6h"
	timeout := ingestTimeout()

	type signal struct {
		label   string
		enabled bool
		count   tsugacli.SignalCounter
		control tsugacli.SignalCounter
		// controlArg is what control is called with. It defaults to the
		// control cluster's name; the explorer signals override it because
		// their control counts rows that are not this run's at all.
		controlArg string
	}
	signals := []signal{
		{label: "logs", enabled: s.Agent.Logs,
			count:      func(c string) (int, error) { return cli.CountContainerLogs(c, scenarioWindow) },
			control:    func(c string) (int, error) { return cli.CountContainerLogs(c, controlWindow) },
			controlArg: controlCluster},
		{label: "traces", enabled: s.Agent.Traces,
			count:      func(c string) (int, error) { return cli.CountTraces(c, scenarioWindow) },
			control:    func(c string) (int, error) { return cli.CountTraces(c, controlWindow) },
			controlArg: controlCluster},
		{label: "metrics", enabled: s.Agent.Metrics,
			count:      func(c string) (int, error) { return cli.CountMetricSeries(hostMetric, c, scenarioWindow) },
			control:    func(c string) (int, error) { return cli.CountMetricSeries(hostMetric, c, controlWindow) },
			controlArg: controlCluster},
		// kubernetesObjects is verified through logs, not the explorer. The
		// k8s_objects receiver watches pods and ships them as log records
		// carrying its own instrumentation scope; the explorer's pod and
		// cluster views come from k8s_cluster, which is the clusterMetrics
		// toggle. Measured on a live run: kubernetesObjects on with
		// clusterMetrics off put zero rows in the explorer, and the reverse
		// combination put eight pods there while producing no k8s_objects
		// logs. Keying this signal on the explorer failed 16 of the 36
		// scenarios while the operator was doing exactly the right thing.
		{label: "kubernetes objects", enabled: s.Gateway.KubernetesObjects,
			count:      func(c string) (int, error) { return cli.CountK8sObjectLogs(c, scenarioWindow) },
			control:    func(c string) (int, error) { return cli.CountK8sObjectLogs(c, controlWindow) },
			controlArg: controlCluster},
		// The two explorer signals both belong to clusterMetrics, because
		// both views are built from the same k8s_cluster receiver. They stay
		// separate rows because a cluster can register in the explorer
		// without its pods appearing, and that gap is worth catching.
		//
		// They take a foreign-cluster control rather than the previous
		// scenario. The explorer reports what is reporting now and accepts no
		// time window, so a finished scenario drops out of it within minutes
		// and its control reads zero - failing the absence assertion as a
		// suite error while the operator is behaving. The control argument is
		// therefore this run's cluster-name prefix, and the counter counts
		// every row that is not ours.
		// Explorer pods come from either side: the agent's kubelet_stats
		// receiver scrapes the pod metric group, and the gateway's k8s_cluster
		// receiver reports pods too. A scenario with agent metrics on and the
		// whole gateway off still puts its pods in the explorer - measured at
		// seven - so keying this on clusterMetrics alone failed the absence
		// assertion for every metrics-only scenario.
		{label: "kubernetes pods", enabled: s.Agent.Metrics || s.Gateway.ClusterMetrics,
			count: cli.CountK8sPods, control: cli.CountForeignK8sPods,
			controlArg: runID},
		{label: "cluster metrics", enabled: s.Gateway.ClusterMetrics,
			count: cli.CountK8sClusters, control: cli.CountForeignK8sClusters,
			controlArg: runID},
	}

	for _, sig := range signals {
		if !sig.enabled {
			continue
		}
		By("asserting " + sig.label + " reached Tsuga")
		Eventually(func() (int, error) { return sig.count(s.ClusterName) },
			timeout, signalPollInterval).Should(BeNumerically(">", 0),
			"%s should have reached Tsuga for %s", sig.label, s.ClusterName)
	}

	for _, sig := range signals {
		if sig.enabled {
			continue
		}
		if controlCluster == "" {
			Skip("no baseline control yet; absence cannot be asserted soundly")
		}
		By("asserting " + sig.label + " is absent, against a live control")
		Consistently(func() error {
			return tsugacli.AssertAbsentWithControl(
				sig.count, sig.control, s.ClusterName, sig.controlArg)
		}, absenceWindow, signalPollInterval).Should(Succeed())
	}
}

// TestTierAScenariosUseRealRunIDNotPlaceholder guards against the closure-
// capture bug where Tier A's spec tree built its scenario slice from a
// placeholder run id at tree-construction time and only reassigned the
// (unused, already-captured-by-value) outer variable inside BeforeAll, which
// Ginkgo runs after the tree - including every It's closure - is already
// built. It calls tierAScenarios directly: the exact function the Describe
// block above calls, so a reintroduced placeholder is caught here without
// needing to run Ginkgo.
func TestTierAScenariosUseRealRunIDNotPlaceholder(t *testing.T) {
	if runID == "" {
		t.Fatal("want package-level runID to be initialized eagerly, got empty")
	}
	if runID == "placeholder" {
		t.Fatalf("want the real package-level runID, not the literal %q", "placeholder")
	}

	scenarios := tierAScenarios()
	if len(scenarios) == 0 {
		t.Fatal("want a non-empty scenario slice")
	}
	for _, s := range scenarios {
		if strings.Contains(s.ClusterName, "placeholder") {
			t.Fatalf("scenario %q has a placeholder cluster name %q; Tier A's spec tree must "+
				"build its scenario slice from the real runID at tree-construction time, not "+
				"reassign it later in BeforeAll (Ginkgo builds the tree before BeforeAll runs)",
				s.Name, s.ClusterName)
		}
		if !strings.HasPrefix(s.ClusterName, runID) {
			t.Errorf("want cluster name %q to carry the real run id %q", s.ClusterName, runID)
		}
	}
}
