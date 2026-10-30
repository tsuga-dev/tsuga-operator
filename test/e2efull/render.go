package e2efull

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"sigs.k8s.io/yaml"
)

// inlineJSON renders a generated body as a YAML flow mapping. JSON is valid
// YAML, so a generated map can be dropped into a manifest template without
// this package growing a YAML emitter for arbitrarily nested values.
func inlineJSON(body map[string]any) (string, error) {
	encoded, err := json.Marshal(body)
	if err != nil {
		return "", fmt.Errorf("encoding generated body: %w", err)
	}
	return string(encoded), nil
}

// dashboardTimePreset is a value from the API's timePreset enum. The CRD's
// own example ("last_1h") is not one of them, and the API rejects it.
const dashboardTimePreset = "past-1-hour"

// Each graph is 6x4 on Tsuga's 12-column grid, so two sit side by side per
// row. The API's minimum widget size is 2x2. The layout keys are quoted in
// the manifest because YAML 1.1 reads a bare y as the boolean true.
const (
	dashboardGraphWidth  = 6
	dashboardGraphHeight = 4
)

// renderDashboardWithGraphs produces a Dashboard CR with one graph per
// visualization, ids <name>-g<i>, plus a timePreset, a filter and a layout
// per graph so those fields round-trip through the operator too.
//
// The graph fields are load-bearing against the CRD
// (config/crd/bases/observability.tsuga.com_dashboards.yaml): a graph item
// declares the properties [description, id, layout, name, visualization] and
// requires id (minLength 1) and visualization. "title" is not among them, so
// the API server silently prunes it and then rejects the object for a
// missing spec.graphs[0].id. The widget's label goes in "name".
//
// Each visualization body carries exactly the fields the variant's schema
// marks required, generated from public-open-api.json - a bare {type} is
// rejected for most of the 26 variants, which require source/queries,
// columns, query, connectionId or sloId on top of it.
func renderDashboardWithGraphs(name, namespace, owner string, visualizations []string) (string, error) {
	var graphs strings.Builder
	for i, visualization := range visualizations {
		body, err := MinimalVisualization(openAPISpecPath, visualization)
		if err != nil {
			return "", err
		}
		encoded, err := inlineJSON(body)
		if err != nil {
			return "", err
		}
		fmt.Fprintf(&graphs, `    - id: %s-g%d
      name: %s
      layout: {"x": %d, "y": %d, "w": %d, "h": %d}
      visualization: %s
`, name, i, visualization,
			(i%2)*dashboardGraphWidth, (i/2)*dashboardGraphHeight,
			dashboardGraphWidth, dashboardGraphHeight, encoded)
	}
	return fmt.Sprintf(`apiVersion: observability.tsuga.com/v1alpha1
kind: Dashboard
metadata:
  name: %s
  namespace: %s
spec:
  name: %s
  owner: %q
  timePreset: %s
  filters:
    - key: service
      values: [e2e]
  tags:
    - key: e2e-run
      value: %s
  graphs:
%s`, name, namespace, name, owner, dashboardTimePreset, runID, graphs.String()), nil
}

const sloNamespace = "e2e-slo"

// renderSLO produces an event SLO CR modelled on
// config/samples/observability_v1alpha1_slo.yaml, with one priority-2
// burn-rate alert. The name carries the run id, which is what the sweeps match.
func renderSLO(name, owner string, target float64) string {
	return fmt.Sprintf(`apiVersion: observability.tsuga.com/v1alpha1
kind: SLO
metadata:
  name: %s
  namespace: %s
spec:
  name: %s
  owner: %q
  permissions: all
  target: %v
  timeframeDays: 7
  tags:
    - key: e2e-run
      value: %s
  configuration:
    type: event
    dataSource: traces
    noDataBehavior: good
    goodQuery:
      formula: "q1"
      queries:
        - aggregate:
            type: count
          filter: "service:e2e -status:error"
    totalQuery:
      formula: "q1"
      queries:
        - aggregate:
            type: count
          filter: "service:e2e"
  alerts:
    - priority: 2
      configuration:
        type: burn-rate
        burnRate: 14.4
`, name, sloNamespace, name, owner, target, runID)
}

// renderMonitor produces a Monitor CR. What is under test is the envelope,
// the discriminator and the validated enums (priority, permissions).
//
// Each discriminator variant requires fields beyond "type" per
// InputMonitorConfiguration<Variant> in public-open-api.json - metric needs
// conditions, noDataBehavior, timeframe, groupByFields and queries;
// certificate-expiry needs warnBeforeInDays and aggregationAlertLogic - so
// the body is generated from that variant's required list. Coverage for a
// variant is not something this renderer drops or weakens.
func renderMonitor(name, namespace, owner, configType string, priority int, permissions string) (string, error) {
	encoded, err := monitorConfigurationJSON(configType)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf(`apiVersion: observability.tsuga.com/v1alpha1
kind: Monitor
metadata:
  name: %s
  namespace: %s
spec:
  name: %s
  owner: %q
  priority: %d
  permissions: %s
  tags:
    - key: e2e-run
      value: %s
  configuration: %s
`, name, namespace, name, owner, priority, permissions, runID, encoded), nil
}

// renderMonitorWithDashboardRef produces a Monitor that points at a Dashboard
// CR by name rather than by Tsuga id, which the operator must resolve.
func renderMonitorWithDashboardRef(name, namespace, owner, dashboardName string) (string, error) {
	encoded, err := monitorConfigurationJSON("metric")
	if err != nil {
		return "", err
	}
	return fmt.Sprintf(`apiVersion: observability.tsuga.com/v1alpha1
kind: Monitor
metadata:
  name: %s
  namespace: %s
spec:
  name: %s
  owner: %q
  priority: 3
  permissions: all
  dashboardRef:
    name: %s
  tags:
    - key: e2e-run
      value: %s
  configuration: %s
`, name, namespace, name, owner, dashboardName, runID, encoded), nil
}

func monitorConfigurationJSON(configType string) (string, error) {
	body, err := MinimalMonitorConfiguration(openAPISpecPath, configType)
	if err != nil {
		return "", err
	}
	return inlineJSON(body)
}

// waitForCRPhase polls a custom resource's status.phase until it matches.
func waitForCRPhase(kind, namespace, name, phase string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	var last string
	for time.Now().Before(deadline) {
		out, err := kubectl(crArgs(kind, namespace, name,
			"-o", "jsonpath={.status.phase}")...)
		if err == nil {
			last = out
			if out == phase {
				return nil
			}
		}
		time.Sleep(2 * time.Second)
	}
	message, _ := kubectl(crArgs(kind, namespace, name,
		"-o", "jsonpath={.status.message}")...)
	return fmt.Errorf("%s/%s did not reach phase %q within %s (last phase %q, message %q)",
		kind, name, phase, timeout, last, message)
}

// crStatusID reads status.id, the Tsuga resource id the operator recorded.
//
//nolint:unparam // only Tier C reads ids today; the namespace keeps it a generic CR helper
func crStatusID(kind, namespace, name string) (string, error) {
	return kubectl(crArgs(kind, namespace, name, "-o", "jsonpath={.status.id}")...)
}

// jsonField pulls a top-level string field out of a CLI response envelope.
func jsonField(payload []byte, path ...string) (string, error) {
	var node any
	if err := json.Unmarshal(payload, &node); err != nil {
		return "", fmt.Errorf("decode response: %w: %s", err, string(payload))
	}
	for _, key := range path {
		switch parent := node.(type) {
		case map[string]any:
			child, ok := parent[key]
			if !ok {
				return "", fmt.Errorf("field %q not found in %s", key, string(payload))
			}
			node = child
		case []any:
			index, err := strconv.Atoi(key)
			if err != nil {
				return "", fmt.Errorf("field %q: parent is an array, need a numeric index", key)
			}
			if index < 0 || index >= len(parent) {
				return "", fmt.Errorf("index %d out of range (len %d)", index, len(parent))
			}
			node = parent[index]
		default:
			return "", fmt.Errorf("field %q: parent is neither object nor array", key)
		}
	}
	text, ok := node.(string)
	if !ok {
		return "", fmt.Errorf("field %v is not a string", path)
	}
	return text, nil
}

// collectorConfigName is the name every TsugaCollectorConfig this suite
// applies must carry.
//
// It is fixed, not per-run, and it is not a cosmetic choice.
// TsugaMonitoringReconciler resolves the cluster-wide collector config by
// this exact name:
//
//	r.Get(ctx, types.NamespacedName{Name: "cluster"}, cluster)
//
// so a TsugaCollectorConfig under any other name leaves every
// TsugaMonitoring reconciling to Error with a NotFound, and every Tier B
// spec failing while it waits for Ready. That name is the documented
// contract, not an accident: config/samples/observability_v1alpha1_tsugacollectorconfig.yaml
// and the README's install instructions both use `metadata.name: cluster`.
//
// Do not "improve" this into a per-run name to isolate concurrent runs. Tier
// A would keep passing - it reads its config back by whatever name it wrote
// - and Tier B would break silently. The resource is cluster-scoped, so
// concurrent runs against one cluster are already unsupported;
// KindClusterName gives each run its own cluster instead.
const collectorConfigName = "cluster"

// renderCollectorConfig produces a TsugaCollectorConfig for one scenario,
// applying whichever orthogonal variant the scenario carries.
func renderCollectorConfig(s CollectorScenario, endpoint, namespace string) string {
	export := fmt.Sprintf("    endpoint: %s\n", endpoint)
	if s.Variant == "endpointSecretRef" {
		export = "    endpointSecretRef:\n      name: tsuga-endpoint\n      key: endpoint\n"
	}
	// resourcesOverride leaves kubernetesEvents unset so the CRD default is
	// applied from YAML; the other scenarios turn events off.
	events := "    kubernetesEvents: false\n"
	extra := ""
	switch s.Variant {
	case "imageOverride":
		extra = "  image: ghcr.io/open-telemetry/opentelemetry-collector-releases/" +
			"opentelemetry-collector-contrib:0.161.0\n"
	case "resourcesOverride":
		extra = "  resources:\n    limits:\n      cpu: \"1\"\n      memory: 1Gi\n" +
			"    requests:\n      cpu: 200m\n      memory: 256Mi\n"
		events = ""
	}

	return fmt.Sprintf(`apiVersion: observability.tsuga.com/v1alpha1
kind: TsugaCollectorConfig
metadata:
  name: %s
spec:
  clusterName: %s
  collectorNamespace: %s
  export:
    tokenSecretRef:
      name: tsuga-ingestion
      key: api-token
%s  agent:
    traces: %t
    metrics: %t
    logs: %t
  gateway:
    clusterMetrics: %t
    kubernetesObjects: %t
%s%s`, collectorConfigName, s.ClusterName, namespace, export,
		s.Agent.Traces, s.Agent.Metrics, s.Agent.Logs,
		s.Gateway.ClusterMetrics, s.Gateway.KubernetesObjects, events, extra)
}

// renderMonitoring produces a TsugaMonitoring for one namespace. injection is
// spec.injection (Namespace, Workloads or Manual). spec is raw YAML placed
// under instrumentation.spec, omitted when empty.
func renderMonitoring(namespace string, enabled bool, languages []string,
	injection string, spec string) string {

	languageBlock := ""
	for _, language := range languages {
		languageBlock += fmt.Sprintf("      - %s\n", language)
	}
	if languageBlock != "" {
		languageBlock = "    languages:\n" + languageBlock
	}
	if spec != "" {
		languageBlock += "    spec:\n      " + strings.ReplaceAll(spec, "\n", "\n      ") + "\n"
	}

	return fmt.Sprintf(`apiVersion: observability.tsuga.com/v1alpha1
kind: TsugaMonitoring
metadata:
  name: e2e-monitoring
  namespace: %s
spec:
  injection: %s
  instrumentation:
    enabled: %t
%s`, namespace, injection, enabled, languageBlock)
}

// languageGo is the one language the operator never bulk-injects.
const languageGo = "go"

// renderLanguageWorkload produces a Deployment running one language fixture.
// It carries no OpenTelemetry configuration: any spans it produces are the
// injected agent's doing.
//
// The opt-in mechanism is NOT a label. The OTel Operator (and this
// controller's Workloads injection mode, see
// internal/controller/tsugamonitoring_controller.go) opts an existing pod
// template into instrumentation via a per-language annotation on the pod
// template:
//
//	instrumentation.opentelemetry.io/inject-<language>: <Instrumentation CR name>
//
// renderMonitoring always names the TsugaMonitoring (and therefore the
// Instrumentation CR it renders) "e2e-monitoring", so that is the annotation
// value used here. The brief's assumed key, "tsuga.com/inject", does not
// exist in the operator; using it would make the opt-in scenario pass
// vacuously (no injection would ever happen, opted-in or not).
//
// A go workload also names its binary in otel-go-auto-target-exe: the eBPF
// agent attaches to that executable and the webhook skips go injection
// without it.
func renderLanguageWorkload(namespace, language string, optInLabel bool) string {
	annotations := ""
	if optInLabel {
		annotations = fmt.Sprintf(`      annotations:
        instrumentation.opentelemetry.io/inject-%s: e2e-monitoring
`, language)
		if language == languageGo {
			annotations += "        instrumentation.opentelemetry.io/otel-go-auto-target-exe: /app\n"
		}
	}
	return fmt.Sprintf(`apiVersion: apps/v1
kind: Deployment
metadata:
  name: app-%s
  namespace: %s
spec:
  replicas: 1
  selector:
    matchLabels:
      app: %s
  template:
    metadata:
      labels:
        app: %s
%s    spec:
      containers:
        - name: app
          image: tsuga-e2e/app-%s:latest
          imagePullPolicy: Never
          ports:
            - containerPort: 8080
`, language, namespace, language, language, annotations, language)
}

// renderEmitterWorkload produces the OTLP emitter Deployment that supplies
// Tier A's telemetry, pointed at the agent collector's in-cluster Service on
// otlpHTTPPort: 4318 by default, the kitchen sink's custom port for it.
// The e2e-label label and e2e-annotation annotation feed the kitchen sink's
// extraLabelMapping and extraAnnotationsMapping; the resource.opentelemetry.io
// annotations feed k8s_attributes' otel_annotations.
//
//nolint:unparam // every caller runs it in Tier A's namespace today; the namespace keeps the manifest self-describing
func renderEmitterWorkload(namespace, serviceName, collectorNamespace string, otlpHTTPPort int) string {
	return fmt.Sprintf(`apiVersion: apps/v1
kind: Deployment
metadata:
  name: otlp-emitter
  namespace: %s
spec:
  replicas: 1
  selector:
    matchLabels:
      app: otlp-emitter
  template:
    metadata:
      labels:
        app: otlp-emitter
        e2e-label: lbl
      annotations:
        e2e-annotation: ann
        resource.opentelemetry.io/deployment.environment.name: e2e-env
        resource.opentelemetry.io/e2e.otel: "on"
    spec:
      containers:
        - name: emitter
          image: tsuga-e2e/otlp-emitter:latest
          imagePullPolicy: Never
          env:
            - name: OTEL_EXPORTER_OTLP_ENDPOINT
              value: http://tsuga-agent-collector.%s.svc.cluster.local:%d
            - name: OTEL_SERVICE_NAME
              value: %s
`, namespace, collectorNamespace, otlpHTTPPort, serviceName)
}

// kitchenSinkSuffix names the kitchen-sink scenario: its cluster is
// runID+"-"+kitchenSinkSuffix.
const kitchenSinkSuffix = "ks"

// renderKitchenSinkConfig produces the one TsugaCollectorConfig that turns on
// every collector feature at once, each with a value the kitchen-sink specs
// can recognise in the rendered config or in Tsuga.
//
// Every pinned value differs from the operator's default, so a check can tell
// the field took effect.
//
// hostMetrics.scrapers replaces the built-in scraper map wholesale, which also
// makes the operator ignore collectNetwork and collectProcesses, so those are
// not set. The map leaves out the built-in disk and filesystem scrapers (their
// absence is the proof) and adds network, process and processes.
//
// k8sAttributes.metadata is the agent's built-in list from
// internal/collection/assets/daemonset-config.yaml without
// container.image.tags, plus k8s.replicaset.name, which the k8s_attributes
// processor supports but does not extract by default and which every
// Deployment pod, the otlp-emitter included, has.
//
// The agent's extra components form one loop the collector must accept at
// startup: otlp/e2e feeds traces/e2e-in, which hands them through the
// forward/e2e connector to traces/e2e-out and on to Tsuga.
func renderKitchenSinkConfig(clusterName, endpoint, namespace string) string {
	return fmt.Sprintf(`apiVersion: observability.tsuga.com/v1alpha1
kind: TsugaCollectorConfig
metadata:
  name: %s
spec:
  clusterName: %s
  collectorNamespace: %s
  export:
    tokenSecretRef:
      name: tsuga-ingestion
      key: api-token
    endpoint: %s
    encoding: proto
    compression: zstd
  agent:
    traces: true
    metrics: true
    logs: true
    otlp:
      httpEndpoint: 0.0.0.0:4319
    collectOtelLogs: true
    fileLog:
      include: ["/var/log/pods/e2e-tier-a_*/*/*.log"]
      exclude: ["/var/log/pods/e2e-tier-a_log-excluded*/*/*.log"]
    hostMetrics:
      collectionInterval: 15s
      scrapers:
        cpu: {}
        memory: {}
        load: {}
        paging: {}
        network: {}
        process:
          mute_process_all_errors: true
        processes: {}
    kubeletStats:
      metricGroups: [node, pod, container]
    spanMetrics:
      aggregationTemporality: AGGREGATION_TEMPORALITY_CUMULATIVE
      dimensions:
        - name: e2e.dimension
    nodeSelector:
      kubernetes.io/os: linux
    tolerations:
      - key: e2e
        operator: Exists
    extraEnvs:
      - name: E2E_MARKER
        value: agent
      - name: OTEL_RESOURCE_ATTRIBUTES
        value: e2e.detected=yes
    healthCheckEndpoint: 0.0.0.0:13134
    extraLabelMapping:
      - tagName: e2e.label
        key: e2e-label
        from: pod
    extraAnnotationsMapping:
      - tagName: e2e.annotation
        key: e2e-annotation
        from: pod
    extraProcessors:
      attributes/e2e:
        actions:
          - key: e2e.extra
            value: added
            action: upsert
    pipelineProcessors:
      traces: [attributes/e2e]
      metrics: [attributes/e2e]
      logs: [attributes/e2e]
    extraReceivers:
      otlp/e2e:
        protocols:
          grpc:
            endpoint: 127.0.0.1:4327
    extraConnectors:
      forward/e2e: {}
    extraExtensions:
      zpages/e2e:
        endpoint: 127.0.0.1:55679
    serviceExtensions: [zpages/e2e]
    extraPipelines:
      traces/e2e-in:
        receivers: [otlp/e2e]
        exporters: [forward/e2e]
      traces/e2e-out:
        receivers: [forward/e2e]
        exporters: [otlp_http/tsuga]
  gateway:
    clusterMetrics: true
    kubernetesObjects: true
    kubernetesEvents: true
    collectionInterval: 20s
    nodeConditionsToReport: [Ready, MemoryPressure]
    allocatableTypesToReport: [cpu, memory]
    nodeSelector:
      kubernetes.io/os: linux
    affinity:
      nodeAffinity:
        preferredDuringSchedulingIgnoredDuringExecution:
          - weight: 1
            preference:
              matchExpressions:
                - key: kubernetes.io/os
                  operator: In
                  values: [linux]
    extraEnvs:
      - name: E2E_MARKER
        value: gateway
    extraProcessors:
      attributes/e2e-gw:
        actions:
          - key: e2e.extra.gw
            value: added
            action: upsert
    pipelineProcessors:
      metrics: [attributes/e2e-gw]
      logs: [attributes/e2e-gw]
  prometheus:
    enabled: true
    annotationDiscovery: true
    scrapeInterval: 15s
    replicas: 2
    prometheusCR:
      enabled: true
    targetAllocator:
      enabled: true
      allocationStrategy: least-weighted
    resources:
      limits:
        memory: 512Mi
    nodeSelector:
      kubernetes.io/os: linux
    extraEnvs:
      - name: E2E_MARKER
        value: scraper
    extraProcessors:
      attributes/e2e-scraper:
        actions:
          - key: e2e.extra.scraper
            value: added
            action: upsert
    pipelineProcessors:
      metrics: [attributes/e2e-scraper]
  profiling:
    enabled: true
    image: ghcr.io/open-telemetry/opentelemetry-collector-releases/opentelemetry-collector-ebpf-profiler:0.160.0
    serviceNameEnvVar: OTEL_SERVICE_NAME
    k8sAttributesMetadata: [k8s.namespace.name, k8s.pod.name]
    nodeSelector:
      kubernetes.io/os: linux
    tolerations:
      - operator: Exists
    extraEnvs:
      - name: E2E_MARKER
        value: profiler
  k8sAttributes:
    metadata:
      - k8s.namespace.name
      - k8s.deployment.name
      - k8s.statefulset.name
      - k8s.daemonset.name
      - k8s.cronjob.name
      - k8s.job.name
      - k8s.node.name
      - k8s.pod.name
      - k8s.pod.uid
      - k8s.pod.start_time
      - service.name
      - service.version
      - k8s.container.name
      - container.id
      - container.image.name
      - k8s.replicaset.name
  resourceDetection:
    enabled: true
    detectors: [env]
    timeout: 5s
  redaction:
    enabled: true
    config:
      allow_all_keys: true
      blocked_values: ['4[0-9]{12}(?:[0-9]{3})?']
  batch:
    sendBatchSize: 512
    sendBatchMaxSize: 1024
    timeout: 2s
  serviceAccount:
    annotations:
      e2e: kitchen-sink
`, collectorConfigName, clusterName, namespace, endpoint)
}

// renderPromApp produces the Prometheus fixture three ways at once: pod
// annotations for annotation discovery, plus a ServiceMonitor and a PodMonitor
// for the Target Allocator's prometheusCR discovery. The kitchen sink scrapes
// it all three ways on purpose, to cover each discovery path; that is the
// double scrape docs/site/kubernetes-monitoring/prometheus.md warns against.
func renderPromApp(namespace string) string {
	return fmt.Sprintf(`apiVersion: apps/v1
kind: Deployment
metadata:
  name: prom-app
  namespace: %[1]s
spec:
  replicas: 1
  selector:
    matchLabels:
      app: prom-app
  template:
    metadata:
      labels:
        app: prom-app
      annotations:
        prometheus.io/scrape: "true"
        prometheus.io/port: "8080"
    spec:
      containers:
        - name: prom-app
          image: tsuga-e2e/app-go:latest
          imagePullPolicy: Never
          ports:
            - name: metrics
              containerPort: 8080
---
apiVersion: v1
kind: Service
metadata:
  name: prom-app
  namespace: %[1]s
  labels:
    app: prom-app
spec:
  selector:
    app: prom-app
  ports:
    - name: metrics
      port: 8080
      targetPort: metrics
---
apiVersion: monitoring.coreos.com/v1
kind: ServiceMonitor
metadata:
  name: prom-app-sm
  namespace: %[1]s
spec:
  selector:
    matchLabels:
      app: prom-app
  endpoints:
    - port: metrics
---
apiVersion: monitoring.coreos.com/v1
kind: PodMonitor
metadata:
  name: prom-app-pm
  namespace: %[1]s
spec:
  selector:
    matchLabels:
      app: prom-app
  podMetricsEndpoints:
    - port: metrics
`, namespace)
}

// renderPlainLogger produces a busybox Deployment echoing one plain-text line.
func renderPlainLogger(namespace, name, line string) string {
	return fmt.Sprintf(`apiVersion: apps/v1
kind: Deployment
metadata:
  name: %[2]s
  namespace: %[1]s
spec:
  replicas: 1
  selector:
    matchLabels:
      app: %[2]s
  template:
    metadata:
      labels:
        app: %[2]s
    spec:
      containers:
        - name: logger
          image: busybox:1.36
          command: ["sh", "-c", "while true; do echo %[3]s; sleep 2; done"]
`, namespace, name, line)
}

const (
	// serviceAccountSuffix, conflictingTuningSuffix and nodeLocalSuffix name
	// the scenarios after the kitchen sink: their clusters are
	// runID+"-"+suffix.
	serviceAccountSuffix    = "sa"
	conflictingTuningSuffix = "ct"
	nodeLocalSuffix         = "nl"

	// sharedServiceAccount is the user-managed account every collector runs
	// as in the service-account scenario.
	sharedServiceAccount = "e2e-shared-sa"
)

// renderServiceAccountConfig produces a TsugaCollectorConfig that brings its
// own ServiceAccount and RBAC: the operator must create neither. Only the
// agent runs, with the network and process scrapers its flags add; no custom
// scrapers map is set, since one makes the operator ignore both flags.
func renderServiceAccountConfig(clusterName, endpoint, namespace string) string {
	return fmt.Sprintf(`apiVersion: observability.tsuga.com/v1alpha1
kind: TsugaCollectorConfig
metadata:
  name: %s
spec:
  clusterName: %s
  collectorNamespace: %s
  export:
    tokenSecretRef:
      name: tsuga-ingestion
      key: api-token
    endpoint: %s
  agent:
    traces: true
    metrics: true
    logs: true
    collectNetwork: true
    collectProcesses: true
  gateway:
    clusterMetrics: false
    kubernetesObjects: false
    kubernetesEvents: false
  serviceAccount:
    create: false
    name: %s
  rbac:
    create: false
`, collectorConfigName, clusterName, namespace, endpoint, sharedServiceAccount)
}

// renderSharedServiceAccount produces sharedServiceAccount and the cluster
// access the agent needs, as docs/site/operations/production.md lists it:
// pod metadata for k8s_attributes, and the kubelet for kubelet_stats.
func renderSharedServiceAccount(namespace string) string {
	return fmt.Sprintf(`apiVersion: v1
kind: ServiceAccount
metadata:
  name: %[1]s
  namespace: %[2]s
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: %[1]s
rules:
  - apiGroups: [""]
    resources: [pods, namespaces]
    verbs: [get, list, watch]
  - apiGroups: [apps]
    resources: [replicasets]
    verbs: [get, list, watch]
  - apiGroups: [batch]
    resources: [jobs]
    verbs: [get, list, watch]
  - apiGroups: [""]
    resources: [nodes/stats, nodes/proxy]
    verbs: [get]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: %[1]s
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: %[1]s
subjects:
  - kind: ServiceAccount
    name: %[1]s
    namespace: %[2]s
`, sharedServiceAccount, namespace)
}

// conflictingAgentCustomConfig is the agent's entire config in the
// conflicting-tuning scenario: OTLP in, debug out, nothing sent to Tsuga. The
// batch/e2e-custom processor name marks it as ours in the rendered config.
func conflictingAgentCustomConfig() map[string]any {
	return map[string]any{
		"receivers":  map[string]any{"otlp": map[string]any{"protocols": map[string]any{"http": map[string]any{}}}},
		"processors": map[string]any{"batch/e2e-custom": map[string]any{}},
		"exporters":  map[string]any{"debug": map[string]any{}},
		"service": map[string]any{"pipelines": map[string]any{"traces": map[string]any{
			"receivers":  []any{"otlp"},
			"processors": []any{"batch/e2e-custom"},
			"exporters":  []any{"debug"},
		}}},
	}
}

// renderConflictingTuningConfig produces a TsugaCollectorConfig whose
// settings fight the generated config: the agent's customConfig replaces
// every agent toggle and tuning field, and the gateway runs cluster metrics
// with exportToTsuga off. The operator refuses a pipeline left with no
// exporter, so the gateway's metrics go to a debug exporter instead, and its
// other pipelines are off.
func renderConflictingTuningConfig(clusterName, endpoint, namespace string) string {
	custom, err := inlineJSON(conflictingAgentCustomConfig())
	if err != nil {
		panic(err)
	}
	return fmt.Sprintf(`apiVersion: observability.tsuga.com/v1alpha1
kind: TsugaCollectorConfig
metadata:
  name: %s
spec:
  clusterName: %s
  collectorNamespace: %s
  export:
    tokenSecretRef:
      name: tsuga-ingestion
      key: api-token
    endpoint: %s
  agent:
    traces: true
    metrics: true
    logs: true
    customConfig: %s
  gateway:
    clusterMetrics: true
    kubernetesObjects: false
    kubernetesEvents: false
    exportToTsuga: false
    extraExporters:
      debug/e2e: {}
    pipelineExporters:
      metrics: [debug/e2e]
`, collectorConfigName, clusterName, namespace, endpoint, custom)
}

// renderPostgresFixture produces the namespace, the labelled admin Secret and
// a Postgres server (Deployment and Service) for the Postgres tier. Same
// fixture as the smoke suite's Postgres spec.
func renderPostgresFixture(namespace string) string {
	return fmt.Sprintf(`apiVersion: v1
kind: Namespace
metadata:
  name: %[1]s
---
apiVersion: v1
kind: Secret
metadata:
  name: pg-admin
  namespace: %[1]s
  labels:
    observability.tsuga.com/postgres-admin: "true"
stringData:
  username: postgres
  password: e2e-admin
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: postgres
  namespace: %[1]s
spec:
  selector:
    matchLabels: {app: postgres}
  template:
    metadata:
      labels: {app: postgres}
    spec:
      containers:
        - name: postgres
          image: postgres:17
          args: ["-c", "shared_preload_libraries=pg_stat_statements"]
          env:
            - name: POSTGRES_PASSWORD
              value: e2e-admin
          ports:
            - containerPort: 5432
---
apiVersion: v1
kind: Service
metadata:
  name: postgres
  namespace: %[1]s
spec:
  selector: {app: postgres}
  ports:
    - port: 5432
`, namespace)
}

// renderPostgresMonitoring produces a managed-mode TsugaPostgresMonitoring
// for the server renderPostgresFixture deploys in the same namespace.
func renderPostgresMonitoring(namespace string) string {
	return fmt.Sprintf(`apiVersion: observability.tsuga.com/v1alpha1
kind: TsugaPostgresMonitoring
metadata:
  name: e2e-pg
  namespace: %[1]s
spec:
  host: postgres.%[1]s.svc
  sslMode: disable
  provisioning:
    mode: managed
    adminSecretRef:
      name: pg-admin
`, namespace)
}

// collectorMonitor is one Monitor from the shipped collector-monitors.yaml
// example, renamed for this run.
type collectorMonitor struct {
	name     string
	priority int
	manifest string
}

// renderCollectorMonitors loads docs/site/examples/collector-monitors.yaml as
// users get it and rewrites only what a run needs: namespace, owner, and the
// run id in metadata.name, spec.name (what the sweeps match) and an e2e-run tag.
func renderCollectorMonitors(namespace, owner string) ([]collectorMonitor, error) {
	root, err := repoRoot()
	if err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(filepath.Join(root, "docs", "site", "examples", "collector-monitors.yaml"))
	if err != nil {
		return nil, err
	}
	var out []collectorMonitor
	for _, doc := range strings.Split(string(raw), "\n---\n") {
		var m map[string]any
		if err := yaml.Unmarshal([]byte(doc), &m); err != nil {
			return nil, fmt.Errorf("parsing collector-monitors.yaml: %w", err)
		}
		metadata := m["metadata"].(map[string]any)
		spec := m["spec"].(map[string]any)
		name := runID + "-" + strings.TrimPrefix(metadata["name"].(string), "tsuga-")
		metadata["name"] = name
		metadata["namespace"] = namespace
		spec["owner"] = owner
		spec["name"] = runID + " " + spec["name"].(string)
		spec["tags"] = append(spec["tags"].([]any), map[string]any{"key": "e2e-run", "value": runID})
		manifest, err := yaml.Marshal(m)
		if err != nil {
			return nil, err
		}
		out = append(out, collectorMonitor{name: name, priority: int(spec["priority"].(float64)), manifest: string(manifest)})
	}
	return out, nil
}

// renderNodeLocalConfig produces a TsugaCollectorConfig whose agent scrapes
// annotated pods on its own node, with no tsuga-scraper.
func renderNodeLocalConfig(clusterName, endpoint, namespace string) string {
	return fmt.Sprintf(`apiVersion: observability.tsuga.com/v1alpha1
kind: TsugaCollectorConfig
metadata:
  name: %s
spec:
  clusterName: %s
  collectorNamespace: %s
  export:
    tokenSecretRef:
      name: tsuga-ingestion
      key: api-token
    endpoint: %s
  agent:
    traces: true
    metrics: true
    logs: true
  gateway:
    clusterMetrics: false
    kubernetesObjects: false
    kubernetesEvents: false
  prometheus:
    enabled: true
    nodeLocal: true
    scrapeInterval: 15s
`, collectorConfigName, clusterName, namespace, endpoint)
}
