package e2efull

import (
	"encoding/json"
	"fmt"
	"strconv"
	"time"
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

// renderDashboard produces a Dashboard CR with a single graph of the given
// visualization type.
//
// Both graph fields are load-bearing against the CRD
// (config/crd/bases/observability.tsuga.com_dashboards.yaml): a graph item
// declares the properties [description, id, layout, name, visualization] and
// requires id (minLength 1) and visualization. "title" is not among them, so
// the API server silently prunes it and then rejects the object for a
// missing spec.graphs[0].id. The widget's label goes in "name".
//
// The visualization body carries exactly the fields the variant's schema
// marks required, generated from public-open-api.json - a bare {type} is
// rejected for most of the 26 variants, which require source/queries,
// columns, query, connectionId or sloId on top of it.
func renderDashboard(name, namespace, owner, visualization string) (string, error) {
	body, err := MinimalVisualization(openAPISpecPath, visualization)
	if err != nil {
		return "", err
	}
	encoded, err := inlineJSON(body)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf(`apiVersion: observability.tsuga.com/v1alpha1
kind: Dashboard
metadata:
  name: %s
  namespace: %s
spec:
  name: %s
  owner: %q
  tags:
    - key: e2e-run
      value: %s
  graphs:
    - id: %s-g0
      name: %s
      visualization: %s
`, name, namespace, name, owner, runID, name, visualization, encoded), nil
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

// NOTE: crArgs already exists in cluster.go (Task 8), which also carries its
// unit tests. Do NOT redeclare it here — a second definition in the same
// package is a duplicate-symbol build failure. The functions below call the
// existing one.

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
	extra := ""
	switch s.Variant {
	case "imageOverride":
		extra = "  image: ghcr.io/open-telemetry/opentelemetry-collector-releases/" +
			"opentelemetry-collector-contrib:0.157.0\n"
	case "resourcesOverride":
		extra = "  resources:\n    limits:\n      cpu: \"1\"\n      memory: 1Gi\n" +
			"    requests:\n      cpu: 200m\n      memory: 256Mi\n"
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
%s`, collectorConfigName, s.ClusterName, namespace, export,
		s.Agent.Traces, s.Agent.Metrics, s.Agent.Logs,
		s.Gateway.ClusterMetrics, s.Gateway.KubernetesObjects, extra)
}

// renderMonitoring produces a TsugaMonitoring for one namespace.
func renderMonitoring(namespace string, enabled bool, languages []string,
	injectExisting bool) string {

	languageBlock := ""
	for _, language := range languages {
		languageBlock += fmt.Sprintf("      - %s\n", language)
	}
	if languageBlock != "" {
		languageBlock = "    languages:\n" + languageBlock
	}

	return fmt.Sprintf(`apiVersion: observability.tsuga.com/v1alpha1
kind: TsugaMonitoring
metadata:
  name: e2e-monitoring
  namespace: %s
spec:
  injectExistingWorkloads: %t
  instrumentation:
    enabled: %t
%s`, namespace, injectExisting, enabled, languageBlock)
}

// renderLanguageWorkload produces a Deployment running one language fixture.
// It carries no OpenTelemetry configuration: any spans it produces are the
// injected agent's doing.
//
// The opt-in mechanism is NOT a label. The OTel Operator (and this
// controller's annotateExistingWorkloads, see
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
func renderLanguageWorkload(namespace, language string, optInLabel bool) string {
	annotations := ""
	if optInLabel {
		annotations = fmt.Sprintf(`      annotations:
        instrumentation.opentelemetry.io/inject-%s: e2e-monitoring
`, language)
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
// Tier A's telemetry, pointed at the agent collector's in-cluster Service.
func renderEmitterWorkload(namespace, serviceName, collectorNamespace string) string {
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
    spec:
      containers:
        - name: emitter
          image: tsuga-e2e/otlp-emitter:latest
          imagePullPolicy: Never
          env:
            - name: OTEL_EXPORTER_OTLP_ENDPOINT
              value: http://tsuga-agent-collector.%s.svc.cluster.local:4318
            - name: OTEL_SERVICE_NAME
              value: %s
`, namespace, collectorNamespace, serviceName)
}
