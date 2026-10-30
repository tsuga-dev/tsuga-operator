package e2efull

import (
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/tsuga-dev/tsuga-operator/test/e2efull/tsugacli"
)

const (
	// kitchenSinkWindow is the window every kitchen-sink query reads.
	kitchenSinkWindow = "-15m"
	// baselineControlWindow is wide because Tier A's baseline collectors were
	// torn down long before the kitchen sink runs, see assertSignalsInTsuga.
	baselineControlWindow = "-6h"

	// kitchenSinkOTLPPort is agent.otlp.httpEndpoint in renderKitchenSinkConfig.
	// The emitter can only reach the agent there, so every span check below
	// also depends on the custom endpoint working.
	kitchenSinkOTLPPort    = 4319
	kitchenSinkEmitterName = "e2e-custom-port"
	// kitchenSinkProfilerImage is profiling.image in renderKitchenSinkConfig.
	// It is one release behind the operator's defaultProfilerImage
	// (internal/collection/collector.go), so the DaemonSet only carries it if
	// the field took effect.
	kitchenSinkProfilerImage = "ghcr.io/open-telemetry/opentelemetry-collector-releases/" +
		"opentelemetry-collector-ebpf-profiler:0.160.0"
	warningEventPodName = "e2e-warning-event"
)

type signalKind int

const (
	logSignal signalKind = iota
	spanSignal
	metricSignal
)

type expectation int

const (
	present expectation = iota
	// absentUnlikeBaseline: nothing on the kitchen-sink cluster, while Tier
	// A's baseline cluster, which runs the operator defaults apart from
	// kubernetesEvents: false, has some. The baseline is the live control that
	// makes the empty result mean anything.
	absentUnlikeBaseline
	// absent: nothing on the kitchen-sink cluster, and no other cluster
	// produces it either. An earlier present query in the same check, on the
	// same field, is what proves the query shape.
	absent
	// absentOnBaseline inverts absentUnlikeBaseline for a field whose
	// operator default is the feature itself: nothing on Tier A's baseline
	// cluster, which turns the feature off, while the kitchen-sink cluster has
	// some. Its query is judged on the baseline cluster, the only check that
	// is.
	absentOnBaseline
)

// signalQuery is one Tsuga query a kitchen-sink check makes: a log or span
// search, or a datapoint count for metric. query is ANDed with the cluster
// filter.
type signalQuery struct {
	kind   signalKind
	metric string
	query  string
	expect expectation
}

func (q signalQuery) String() string {
	switch q.kind {
	case logSignal:
		return fmt.Sprintf("logs [%s]", q.query)
	case spanSignal:
		return fmt.Sprintf("spans [%s]", q.query)
	default:
		return fmt.Sprintf("metric %s [%s]", q.metric, q.query)
	}
}

func (q signalQuery) count(c *tsugacli.Client, cluster, window string) (int, error) {
	switch q.kind {
	case logSignal:
		return c.CountLogs(cluster, q.query, window)
	case spanSignal:
		return c.CountSpans(cluster, q.query, window)
	default:
		return c.CountMetricSeriesWhere(q.metric, cluster, q.query, window)
	}
}

// kitchenSinkCheck is one feature of renderKitchenSinkConfig verified in
// Tsuga. Its queries run in order and presence comes first: the kitchen-sink
// cluster name holds no data until ingestion for it is live, so an empty
// result only becomes evidence once a positive has landed.
type kitchenSinkCheck struct {
	feature string
	queries []signalQuery
}

func logs(query string, expect expectation) signalQuery {
	return signalQuery{kind: logSignal, query: query, expect: expect}
}

func spans(query string, expect expectation) signalQuery {
	return signalQuery{kind: spanSignal, query: query, expect: expect}
}

func metric(name, query string, expect expectation) signalQuery {
	return signalQuery{kind: metricSignal, metric: name, query: query, expect: expect}
}

// Every metric name and attribute key below was confirmed live against the
// test org with `tsuga metrics list`, `tsuga logs search`, `tsuga traces
// search` or `tsuga aggregation scalar`.
const (
	promFixtureMetric = "e2e_fixture_requests_total"
	// The prometheus receiver turns each target's job label into
	// service.name. prometheus-operator relabels job, so it is not the
	// "serviceMonitor/<ns>/<name>/0" scrape-config name: a ServiceMonitor's
	// targets get the Service's name and a PodMonitor's get "<ns>/<name>".
	annotationScrapeJob = `context.service.name:"tsuga-pod-annotations"`
	serviceMonitorJob   = `context.service.name:"prom-app"`
	podMonitorJob       = `context.service.name:"e2e-tier-a/prom-app-pm"`

	// k8s_objects/events records carry the watched resource as
	// k8s.resource.name and the Event itself under object.
	eventsQuery       = `k8s.resource.name:"events"`
	warningEventQuery = eventsQuery + ` AND object.involvedObject.name:"` + warningEventPodName + `"`

	spanMetricsCalls = "traces.span.metrics.calls"
	// Span attributes are queried under span_attributes., resource
	// attributes under context.; metric and log attributes take no prefix.
	cardNumber = "4111111111111111"

	// tsugaExporterName is the operator's Tsuga exporter, tsugaExporter in
	// internal/collection/tuning.go.
	tsugaExporterName = "otlp_http/tsuga"
)

var kitchenSinkChecks = []kitchenSinkCheck{
	{feature: "ships Kubernetes events", queries: []signalQuery{
		logs(warningEventQuery, present),
	}},
	// kubernetesEvents defaults to true, so the kitchen sink shipping events
	// proves nothing about the field. Tier A's baseline renders
	// kubernetesEvents: false.
	{feature: "ships no Kubernetes events when kubernetesEvents is false", queries: []signalQuery{
		logs(eventsQuery, absentOnBaseline),
	}},
	{feature: "reports only the configured node conditions and allocatable types", queries: []signalQuery{
		metric("k8s.node.condition_memory_pressure", "", present),
		metric("k8s.node.allocatable_cpu", "", present),
		metric("k8s.node.condition_disk_pressure", "", absentUnlikeBaseline),
		metric("k8s.node.allocatable_pods", "", absentUnlikeBaseline),
	}},
	{feature: "reports container status reasons", queries: []signalQuery{
		metric("k8s.container.status.reason", "", present),
	}},
	{feature: "scrapes annotated pods", queries: []signalQuery{
		metric(promFixtureMetric, annotationScrapeJob, present),
	}},
	{feature: "scrapes ServiceMonitor and PodMonitor targets", queries: []signalQuery{
		metric(promFixtureMetric, serviceMonitorJob, present),
		metric(promFixtureMetric, podMonitorJob, present),
	}},
	{feature: "detects resource attributes", queries: []signalQuery{
		spans(`context.e2e.detected:"yes"`, present),
	}},
	{feature: "redacts credentials", queries: []signalQuery{
		spans("span_attributes.e2e.card:*", present),
		spans(`span_attributes.e2e.card:"`+cardNumber+`"`, absentUnlikeBaseline),
	}},
	{feature: "applies the k8sAttributes metadata override", queries: []signalQuery{
		spans("context.k8s.replicaset.name:*", present),
		spans("context.container.image.tags:*", absentUnlikeBaseline),
	}},
	{feature: "emits span metrics with the configured dimension", queries: []signalQuery{
		metric(spanMetricsCalls, `e2e.dimension:"d1"`, present),
	}},
	// filesystem is also left out of the custom scrapers, but its absence is
	// asserted in the cluster only: on kind every filesystem is one the
	// default scraper excludes, so the baseline emits no filesystem metric
	// and cannot serve as a control.
	{feature: "collects exactly the custom host-metrics scrapers", queries: []signalQuery{
		metric("system.paging.usage", "", present),
		metric("system.network.io", "", present),
		metric("process.cpu.time", "", present),
		metric("system.disk.io", "", absentUnlikeBaseline),
	}},
	{feature: "collects only the configured kubelet metric groups", queries: []signalQuery{
		metric("k8s.pod.memory.usage", "", present),
		metric("k8s.volume.available", "", absentUnlikeBaseline),
	}},
	{feature: "honours fileLog include and exclude", queries: []signalQuery{
		logs(`context.k8s.namespace.name:"e2e-tier-a" AND log.iostream:*`, present),
		logs(`message:"e2e-included-line"`, present),
		logs(`context.k8s.namespace.name:"kube-system" AND log.iostream:*`, absentUnlikeBaseline),
		logs(`message:"e2e-excluded-line"`, absent),
	}},
	{feature: "applies extra processors and mappings", queries: []signalQuery{
		spans(`span_attributes.e2e.extra:"added"`, present),
		spans(`context.e2e.label:"lbl"`, present),
		spans(`context.e2e.annotation:"ann"`, present),
		logs(`e2e.extra.gw:"added"`, present),
		metric(promFixtureMetric, `e2e.extra.scraper:"added"`, present),
	}},
	{feature: "receives OTLP on the custom endpoint", queries: []signalQuery{
		spans(`context.service.name:"`+kitchenSinkEmitterName+`"`, present),
	}},
}

// renderWarningEventPod produces a pod that can never start, so the kubelet
// keeps emitting Warning events about it for the k8s_objects/events receiver.
func renderWarningEventPod(namespace string) string {
	return fmt.Sprintf(`apiVersion: v1
kind: Pod
metadata:
  name: %s
  namespace: %s
spec:
  containers:
    - name: missing
      image: tsuga-e2e/does-not-exist:never
      imagePullPolicy: Never
`, warningEventPodName, namespace)
}

// tierAKitchenSinkSpecs registers the kitchen sink: one TsugaCollectorConfig
// with every collector feature set to a non-default value, checked in the
// cluster and in Tsuga. It runs after tierASpecs, whose baseline scenario is
// the live control for its absence checks: those need Tier A's s00 in the same
// run, so focus with `s00|kitchen sink`, not `kitchen sink` alone.
func tierAKitchenSinkSpecs() {
	Describe("Tier A: kitchen sink", Ordered, Label("tier-a", "kitchen-sink"), func() {
		cluster := runID + "-" + kitchenSinkSuffix
		baseline := tierAScenarios()[0].ClusterName
		var config string
		workloads := []string{
			renderPromApp(tierANamespace),
			renderPlainLogger(tierANamespace, "log-excluded", "e2e-excluded-line"),
			renderPlainLogger(tierANamespace, "log-included", "e2e-included-line"),
			renderEmitterWorkload(tierANamespace, kitchenSinkEmitterName, operatorNS, kitchenSinkOTLPPort),
			renderWarningEventPod(tierANamespace),
		}

		BeforeAll(func() {
			Expect(applyNamespace(tierANamespace)).To(Succeed())
			// The profiler runs privileged with hostPID, which a restricted
			// Pod Security level would reject.
			_, err := kubectl("label", "namespace", operatorNS,
				"pod-security.kubernetes.io/enforce=privileged", "--overwrite")
			Expect(err).NotTo(HaveOccurred())

			config = renderKitchenSinkConfig(cluster, cli.Config().OTLPEndpoint, operatorNS)
			Expect(applyYAML(config)).To(Succeed())
			for _, workload := range workloads {
				Expect(applyYAML(workload)).To(Succeed())
			}

			By("waiting for the CR and its collectors to be ready")
			Expect(waitForCRPhase("tsugacollectorconfig", "", collectorConfigName,
				"Ready", collectorTimeout)).To(Succeed())
			// Not the profiler: it crash-loops on Docker Desktop arm64.
			for _, name := range []string{"tsuga-agent", "tsuga-gateway", "tsuga-scraper"} {
				Expect(waitForCollectorReady(operatorNS, name, collectorTimeout)).To(Succeed())
			}
			for _, deployment := range []string{"otlp-emitter", "prom-app", "log-excluded", "log-included"} {
				_, err = kubectl("wait", "--for=condition=Available", "-n", tierANamespace,
					"deploy/"+deployment, "--timeout=120s")
				Expect(err).NotTo(HaveOccurred())
			}

			By("waiting for the first kitchen-sink container logs in Tsuga")
			Eventually(func() (int, error) { return cli.CountContainerLogs(cluster, kitchenSinkWindow) },
				ingestTimeout(), signalPollInterval).Should(BeNumerically(">", 0),
				"no container logs reached Tsuga for %s", cluster)
		})

		AfterAll(func() {
			for _, workload := range workloads {
				_ = deleteYAML(workload)
			}
			if config != "" {
				_ = deleteYAML(config)
			}
			Expect(waitForCollectorsGone(operatorNS, 90*time.Second)).To(Succeed())
		})

		It("renders the in-cluster-only settings", func() {
			By("checking the agent runs off the host network by default")
			for _, object := range []string{"opentelemetrycollector/tsuga-agent", "daemonset/tsuga-agent-collector"} {
				path := "{.spec.hostNetwork}"
				if strings.HasPrefix(object, "daemonset/") {
					path = "{.spec.template.spec.hostNetwork}"
				}
				out, err := kubectl("get", object, "-n", operatorNS, "-o", "jsonpath="+path)
				Expect(err).NotTo(HaveOccurred())
				Expect(out).NotTo(Equal("true"), object)
			}

			By("checking the operator made its collector ClusterRoles, the control for the service-account scenario")
			roles, err := kubectlStdout("get", "clusterroles", "-o", "name")
			Expect(err).NotTo(HaveOccurred())
			Expect(strings.Fields(roles)).To(ContainElement(
				"clusterrole.rbac.authorization.k8s.io/" + operatorRBACPrefix + "tsuga-agent"))

			By("waiting for the Target Allocator pod to be Ready")
			_, err = kubectl("wait", "--for=condition=Ready", "pod", "-n", operatorNS,
				"-l", "app.kubernetes.io/component=opentelemetry-targetallocator", "--timeout=120s")
			Expect(err).NotTo(HaveOccurred())
		})

		// In the cluster only, and without waiting for the profiler to be
		// ready: it crash-loops on Docker Desktop arm64.
		It("renders the profiler", func() {
			const daemonSet = "tsuga-profiler-collector"
			Eventually(func() error {
				_, err := kubectl("get", "daemonset", daemonSet, "-n", operatorNS)
				return err
			}, collectorTimeout, 2*time.Second).Should(Succeed())
			podSpec := func(jsonpath string) string {
				out, err := kubectl("get", "daemonset", daemonSet, "-n", operatorNS,
					"-o", "jsonpath={.spec.template.spec"+jsonpath+"}")
				Expect(err).NotTo(HaveOccurred())
				return out
			}

			Expect(podSpec(`.containers[?(@.name=="otc-container")].securityContext.privileged`)).To(Equal("true"))
			Expect(podSpec(".hostPID")).To(Equal("true"))
			Expect(podSpec(`.containers[?(@.name=="otc-container")].image`)).To(Equal(kitchenSinkProfilerImage))
			var tolerations []map[string]any
			Expect(json.Unmarshal([]byte(podSpec(".tolerations")), &tolerations)).To(Succeed())
			Expect(tolerations).To(ContainElement(map[string]any{"operator": "Exists"}))
		})

		for _, check := range kitchenSinkChecks {
			It(check.feature, func() {
				for _, q := range check.queries {
					assertKitchenSinkQuery(q, cluster, baseline)
				}
			})
		}
	})
}

// Metric names the service-account scenario's flags add to the default
// host_metrics scrapers (internal/collection/tuning.go): the default set in
// assets/daemonset-config.yaml has no network or process scraper.
const (
	// serviceAccountWindow is the window every service-account query reads.
	serviceAccountWindow = "-15m"

	collectNetworkMetric   = "system.network.io"
	collectProcessesMetric = "process.uptime"
)

// operatorRBACPrefix prefixes every ClusterRole and ClusterRoleBinding the
// operator creates, collectorRBACPrefix in internal/collection/rbac.go. They
// carry no labels, so the name is what identifies them.
const operatorRBACPrefix = "tsuga-operator-collector-"

// tierAServiceAccountSpecs registers the scenario that brings its own
// ServiceAccount and RBAC. It runs after the kitchen sink, whose operator-made
// accounts and roles the garbage collector removes in the background.
func tierAServiceAccountSpecs() {
	Describe("Tier A: service account", Ordered, Label("tier-a", "service-account"), func() {
		cluster := runID + "-" + serviceAccountSuffix
		shared := renderSharedServiceAccount(operatorNS)
		var config string

		BeforeAll(func() {
			Expect(applyYAML(shared)).To(Succeed())
			config = renderServiceAccountConfig(cluster, cli.Config().OTLPEndpoint, operatorNS)
			Expect(applyYAML(config)).To(Succeed())

			By("waiting for the CR and the agent to be ready")
			Expect(waitForCRPhase("tsugacollectorconfig", "", collectorConfigName,
				"Ready", collectorTimeout)).To(Succeed())
			Expect(waitForCollectorReady(operatorNS, "tsuga-agent", collectorTimeout)).To(Succeed())
		})

		AfterAll(func() {
			if config != "" {
				_ = deleteYAML(config)
			}
			Expect(waitForCollectorsGone(operatorNS, 90*time.Second)).To(Succeed())
			_ = deleteYAML(shared)
		})

		It("ships container logs", func() {
			Eventually(func() (int, error) { return cli.CountContainerLogs(cluster, serviceAccountWindow) },
				ingestTimeout(), signalPollInterval).Should(BeNumerically(">", 0),
				"no container logs reached Tsuga for %s", cluster)
		})

		It("runs the agent as the shared ServiceAccount", func() {
			out, err := kubectl("get", "pods", "-n", operatorNS,
				"-l", "app.kubernetes.io/name=tsuga-agent-collector",
				"-o", "jsonpath={.items[*].spec.serviceAccountName}")
			Expect(err).NotTo(HaveOccurred())
			Expect(strings.Fields(out)).NotTo(BeEmpty())
			Expect(strings.Fields(out)).To(HaveEach(sharedServiceAccount))
		})

		It("creates no ServiceAccount or cluster RBAC of its own", func() {
			Eventually(func() (string, error) {
				return kubectlStdout("get", "serviceaccount", "tsuga-agent", "-n", operatorNS,
					"--ignore-not-found", "-o", "name")
			}, collectorTimeout, 2*time.Second).Should(BeEmpty())
			Eventually(func() ([]string, error) {
				out, err := kubectlStdout("get", "clusterroles,clusterrolebindings", "-o", "name")
				var operatorMade []string
				for _, name := range strings.Fields(out) {
					if strings.Contains(name, "/"+operatorRBACPrefix) {
						operatorMade = append(operatorMade, name)
					}
				}
				return operatorMade, err
			}, collectorTimeout, 2*time.Second).Should(BeEmpty())
		})

		It("collects network metrics when collectNetwork is true", func() {
			Eventually(func() (int, error) {
				return cli.CountMetricSeries(collectNetworkMetric, cluster, serviceAccountWindow)
			}, ingestTimeout(), signalPollInterval).Should(BeNumerically(">", 0),
				"%s should have reached Tsuga for %s", collectNetworkMetric, cluster)
		})

		It("collects process metrics when collectProcesses is true", func() {
			Eventually(func() (int, error) {
				return cli.CountMetricSeries(collectProcessesMetric, cluster, serviceAccountWindow)
			}, ingestTimeout(), signalPollInterval).Should(BeNumerically(">", 0),
				"%s should have reached Tsuga for %s", collectProcessesMetric, cluster)
		})
	})
}

// tierAConflictingTuningSpecs registers the scenario whose settings override
// the generated config: an agent customConfig, and a gateway that keeps
// cluster metrics in the cluster.
func tierAConflictingTuningSpecs() {
	Describe("Tier A: conflicting tuning", Ordered, Label("tier-a", "conflicting-tuning"), func() {
		cluster := runID + "-" + conflictingTuningSuffix
		var config string

		BeforeAll(func() {
			config = renderConflictingTuningConfig(cluster, cli.Config().OTLPEndpoint, operatorNS)
			Expect(applyYAML(config)).To(Succeed())

			By("waiting for the CR and its collectors to be ready")
			Expect(waitForCRPhase("tsugacollectorconfig", "", collectorConfigName,
				"Ready", collectorTimeout)).To(Succeed())
			for _, name := range []string{"tsuga-agent", "tsuga-gateway"} {
				Expect(waitForCollectorReady(operatorNS, name, collectorTimeout)).To(Succeed())
			}
		})

		AfterAll(func() {
			if config != "" {
				_ = deleteYAML(config)
			}
			Expect(waitForCollectorsGone(operatorNS, 90*time.Second)).To(Succeed())
		})

		It("runs the agent's customConfig as written", func() {
			agent, err := collectorConfig(operatorNS, "tsuga-agent")
			Expect(err).NotTo(HaveOccurred())
			// The OpenTelemetry Operator's webhook fills in receiver endpoints
			// and service.telemetry, so the components and pipelines are
			// compared rather than the whole config.
			custom := conflictingAgentCustomConfig()
			for _, section := range []string{"processors", "exporters"} {
				Expect(agent[section]).To(Equal(custom[section]), section)
			}
			Expect(slices.Collect(maps.Keys(nestedMap(agent, "receivers")))).To(ConsistOf("otlp"))
			Expect(nestedMap(agent, "service")["pipelines"]).To(Equal(nestedMap(custom, "service")["pipelines"]))
		})

		It("drops the Tsuga exporter from the gateway", func() {
			gateway, err := collectorConfig(operatorNS, "tsuga-gateway")
			Expect(err).NotTo(HaveOccurred())
			Expect(nestedMap(gateway, "exporters")).NotTo(HaveKey(tsugaExporterName))
			Expect(nestedMap(gateway, "service", "pipelines", "metrics")["exporters"]).
				To(Equal([]any{"debug/e2e"}))
		})

		It("keeps cluster metrics out of Tsuga", func() {
			By("waiting for the gateway to hand cluster metrics to its debug exporter")
			Eventually(func() (string, error) {
				return kubectl("logs", "-n", operatorNS, "deploy/tsuga-gateway-collector", "--since=5m")
			}, collectorTimeout, 10*time.Second).Should(MatchRegexp(gatewayDebugMetricsLine))

			// Nothing on this cluster reaches Tsuga, so no positive can show
			// ingestion is live for it: absence has to outlast the time a
			// positive would take to land. The kitchen sink cannot be the
			// control: the explorer forgets a cluster minutes after its
			// collectors stop, so other clusters in the org stand in, as in
			// Tier A.
			By("asserting the cluster is absent from the explorer, against a live control")
			Consistently(func() error {
				return tsugacli.AssertAbsentWithControl(
					cli.CountK8sClusters, cli.CountForeignK8sClusters, cluster, runID)
			}, ingestTimeout(), signalPollInterval).Should(Succeed())
		})
	})
}

// gatewayDebugMetricsLine is the debug exporter's summary of a metrics batch
// it received.
const gatewayDebugMetricsLine = `debug/e2e.*data points`

func assertKitchenSinkQuery(q signalQuery, cluster, baseline string) {
	count := func(c string) (int, error) { return q.count(cli, c, kitchenSinkWindow) }
	switch q.expect {
	case present:
		By("asserting " + q.String() + " reached Tsuga")
		Eventually(func() (int, error) { return count(cluster) },
			ingestTimeout(), signalPollInterval).Should(BeNumerically(">", 0),
			"%s should have reached Tsuga for %s", q, cluster)
	case absentUnlikeBaseline:
		By("asserting " + q.String() + " is absent, against the baseline control")
		control := func(c string) (int, error) { return q.count(cli, c, baselineControlWindow) }
		Consistently(func() error {
			return tsugacli.AssertAbsentWithControl(count, control, cluster, baseline)
		}, absenceWindow, signalPollInterval).Should(Succeed())
	case absentOnBaseline:
		By("asserting " + q.String() + " is absent on the baseline, against the kitchen sink as control")
		onBaseline := func(c string) (int, error) { return q.count(cli, c, baselineControlWindow) }
		Consistently(func() error {
			return tsugacli.AssertAbsentWithControl(onBaseline, count, baseline, cluster)
		}, absenceWindow, signalPollInterval).Should(Succeed())
	case absent:
		By("asserting " + q.String() + " is absent")
		Consistently(func() (int, error) { return count(cluster) },
			absenceWindow, signalPollInterval).Should(BeZero(), "%s should not reach Tsuga for %s", q, cluster)
	}
}

// tierANodeLocalSpecs registers the scenario where the agent scrapes
// annotated pods on its own node and no tsuga-scraper is rendered.
func tierANodeLocalSpecs() {
	Describe("Tier A: node-local scraping", Ordered, Label("tier-a", "node-local"), func() {
		cluster := runID + "-" + nodeLocalSuffix
		promApp := renderPromApp(tierANamespace)
		var config string

		BeforeAll(func() {
			Expect(applyNamespace(tierANamespace)).To(Succeed())
			config = renderNodeLocalConfig(cluster, cli.Config().OTLPEndpoint, operatorNS)
			Expect(applyYAML(config)).To(Succeed())
			Expect(applyYAML(promApp)).To(Succeed())

			By("waiting for the CR and the agent to be ready")
			Expect(waitForCRPhase("tsugacollectorconfig", "", collectorConfigName,
				"Ready", collectorTimeout)).To(Succeed())
			Expect(waitForCollectorReady(operatorNS, "tsuga-agent", collectorTimeout)).To(Succeed())
			_, err := kubectl("wait", "--for=condition=Available", "-n", tierANamespace,
				"deploy/prom-app", "--timeout=120s")
			Expect(err).NotTo(HaveOccurred())
		})

		AfterAll(func() {
			_ = deleteYAML(promApp)
			if config != "" {
				_ = deleteYAML(config)
			}
			Expect(waitForCollectorsGone(operatorNS, 90*time.Second)).To(Succeed())
		})

		It("renders no tsuga-scraper", func() {
			Eventually(func() (string, error) {
				return kubectlStdout("get", "opentelemetrycollector", "tsuga-scraper", "-n", operatorNS,
					"--ignore-not-found", "-o", "name")
			}, collectorTimeout, 2*time.Second).Should(BeEmpty())
		})

		It("scrapes the annotated pod from the agent on its node, enriched", func() {
			node, err := kubectlStdout("get", "pods", "-n", tierANamespace, "-l", "app=prom-app",
				"-o", "jsonpath={.items[0].spec.nodeName}")
			Expect(err).NotTo(HaveOccurred())
			Expect(node).NotTo(BeEmpty())
			filter := annotationScrapeJob + ` AND context.k8s.deployment.name:"prom-app"` +
				` AND context.k8s.node.name:"` + node + `"`
			Eventually(func() (int, error) {
				return cli.CountMetricSeriesWhere(promFixtureMetric, cluster, filter, nodeLocalWindow)
			}, ingestTimeout(), signalPollInterval).Should(BeNumerically(">", 0),
				"%s with %s should have reached Tsuga for %s", promFixtureMetric, filter, cluster)
		})
	})
}

// nodeLocalWindow is the window every node-local query reads.
const nodeLocalWindow = "-15m"
