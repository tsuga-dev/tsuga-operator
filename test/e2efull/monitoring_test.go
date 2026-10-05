package e2efull

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// operatorFieldManager must match internal/controller's fieldManager, the
// owner the controllers apply Instrumentations under.
const operatorFieldManager = "tsuga-operator"

// instrumentedLanguages are the language keys an Instrumentation spec can
// carry. internal/collection/instrumentation.go sets spec.<language> to an
// empty map for each language the TsugaMonitoring enables.
var instrumentedLanguages = []string{
	"java", "nodejs", "python", "dotnet", "go", "apacheHttpd", "nginx",
}

// instrumentationLanguages returns the language blocks the namespace's
// Instrumentation carries *because this operator asked for them*.
//
// Two weaker checks do not work here, and both look like they do:
//
//   - `kubectl get instrumentation -n e2e-inject-java -o yaml` piped through
//     ContainSubstring("java") matches on "namespace: e2e-inject-java". It
//     passes for any Instrumentation, whatever language block was rendered,
//     and equally in all four language namespaces.
//   - Reading spec.<language> or spec.<language>.image does not distinguish
//     either. The OpenTelemetry Operator's defaulting webhook
//     (internal/webhook/instrumentation_webhook.go) sets Image and Resources
//     for Java, NodeJS, Python, DotNet and the rest unconditionally, with no
//     regard for which blocks the applied object contained - so after admission
//     every language block is populated on every Instrumentation.
//
// What survives that defaulting is ownership. The controllers apply with
// server-side apply under the "tsuga-operator" field manager, and
// metadata.managedFields records exactly which top-level spec fields that
// manager set. A language this operator did not request is owned by the
// webhook's manager, not ours.
func instrumentationLanguages(namespace string) ([]string, error) {
	// --show-managed-fields is required: kubectl strips metadata.managedFields
	// from its output by default (since 1.21), so without it every ownership
	// lookup here reads an empty list and reports that the operator requested
	// no languages at all - for every namespace, whatever the operator did.
	raw, err := kubectlStdout("get", "instrumentation", "-n", namespace,
		"-o", "json", "--show-managed-fields")
	if err != nil {
		return nil, err
	}
	var list struct {
		Items []struct {
			Metadata struct {
				ManagedFields []struct {
					Manager  string         `json:"manager"`
					FieldsV1 map[string]any `json:"fieldsV1"`
				} `json:"managedFields"`
			} `json:"metadata"`
		} `json:"items"`
	}
	if err := json.Unmarshal([]byte(raw), &list); err != nil {
		return nil, fmt.Errorf("decoding instrumentations in %s: %w", namespace, err)
	}
	if len(list.Items) == 0 {
		return nil, fmt.Errorf("no Instrumentation in namespace %s", namespace)
	}

	owned := map[string]bool{}
	for _, entry := range list.Items[0].Metadata.ManagedFields {
		if entry.Manager != operatorFieldManager {
			continue
		}
		spec, ok := entry.FieldsV1["f:spec"].(map[string]any)
		if !ok {
			continue
		}
		for field := range spec {
			owned[strings.TrimPrefix(field, "f:")] = true
		}
	}

	var present []string
	for _, language := range instrumentedLanguages {
		if owned[language] {
			present = append(present, language)
		}
	}
	return present, nil
}

// tierBSpecs registers Tier B; see tierASpecs for why the tiers are
// functions rather than top-level containers.
func tierBSpecs() {
	Describe("Tier B: TsugaMonitoring", Ordered, Label("tier-b"), func() {
		var baselineCluster string

		BeforeAll(func() {
			By("applying an all-on collector config for the tier to export through")
			baseline := CollectorScenarios(runID)[0]
			manifest := renderCollectorConfig(baseline, cli.Config().OTLPEndpoint, operatorNS)
			Expect(applyYAML(manifest)).To(Succeed())
			Expect(waitForCRPhase("tsugacollectorconfig", "", collectorConfigName,
				"Ready", collectorTimeout)).To(Succeed())
			Expect(waitForCollectorReady(operatorNS, "tsuga-agent", collectorTimeout)).To(Succeed())
			baselineCluster = baseline.ClusterName
		})

		for _, language := range []string{"java", "nodejs", "python", "dotnet"} {
			It("injects "+language+" instrumentation", func() {
				namespace := "e2e-inject-" + language
				Expect(applyYAML(fmt.Sprintf(
					"apiVersion: v1\nkind: Namespace\nmetadata:\n  name: %s\n", namespace))).To(Succeed())
				DeferCleanup(func() { _, _ = kubectl("delete", "namespace", namespace, "--wait=false") })

				// The workload is deployed BEFORE the TsugaMonitoring, because
				// injectExistingWorkloads means exactly that. The controller
				// annotates the Deployments it finds while reconciling the
				// TsugaMonitoring, and it watches only TsugaMonitoring and the
				// Instrumentation it owns - not Deployments. A workload created
				// afterwards is therefore never annotated until something else
				// touches the TsugaMonitoring, and the injection assertion below
				// waits out its full timeout on a workload the operator was never
				// asked to adopt. Workloads created later are the opt-in
				// annotation's job, which "honours an opt-in annotation" covers.
				By("running the uninstrumented workload first, so it is an existing workload")
				workload := renderLanguageWorkload(namespace, language, false)
				Expect(applyYAML(workload)).To(Succeed())
				_, err := kubectl("wait", "--for=condition=Available",
					"-n", namespace, "deploy/app-"+language, "--timeout=300s")
				Expect(err).NotTo(HaveOccurred())

				monitoring := renderMonitoring(namespace, true, []string{language}, true)
				Expect(applyYAML(monitoring)).To(Succeed())
				Expect(waitForCRPhase("tsugamonitoring", namespace, "e2e-monitoring",
					"Ready", crTimeout)).To(Succeed())

				By("asserting the Instrumentation carries the language block")
				Expect(instrumentationLanguages(namespace)).To(ConsistOf(language),
					"the rendered Instrumentation must carry exactly the requested language block")

				By("asserting the pod received an injected init container")
				Eventually(func() (string, error) {
					return kubectl("get", "pods", "-n", namespace,
						"-l", "app="+language, "-o", "jsonpath={.items[0].spec.initContainers[*].name}")
				}, crTimeout, 5*time.Second).Should(ContainSubstring("opentelemetry"),
					"the OTel Operator should have injected an init container")

				By("asserting this workload's spans reached Tsuga, which only an attached agent can produce")
				// Scoped to this workload: counting spans for the whole cluster
				// would let this scenario pass on the spans an earlier language
				// produced.
				//
				// Scoped by Deployment name rather than service.name because spans
				// are not indexed on context.service.name - see
				// CountTracesForWorkload. app-<language> is the Deployment this
				// spec just waited on, so the scope is exact.
				Eventually(func() (int, error) {
					return cli.CountTracesForWorkload("app-"+language, baselineCluster, "-15m")
				}, ingestTimeout(), 10*time.Second).Should(BeNumerically(">", 0))
			})
		}

		It("injects all four languages at once", func() {
			namespace := "e2e-inject-all"
			Expect(applyYAML(fmt.Sprintf(
				"apiVersion: v1\nkind: Namespace\nmetadata:\n  name: %s\n", namespace))).To(Succeed())
			DeferCleanup(func() { _, _ = kubectl("delete", "namespace", namespace, "--wait=false") })

			languages := []string{"java", "nodejs", "python", "dotnet"}
			Expect(applyYAML(renderMonitoring(namespace, true, languages, true))).To(Succeed())
			Expect(waitForCRPhase("tsugamonitoring", namespace, "e2e-monitoring",
				"Ready", crTimeout)).To(Succeed())

			Expect(instrumentationLanguages(namespace)).To(ConsistOf(
				"java", "nodejs", "python", "dotnet"))
		})

		It("creates no Instrumentation when disabled", func() {
			namespace := "e2e-inject-off"
			Expect(applyYAML(fmt.Sprintf(
				"apiVersion: v1\nkind: Namespace\nmetadata:\n  name: %s\n", namespace))).To(Succeed())
			DeferCleanup(func() { _, _ = kubectl("delete", "namespace", namespace, "--wait=false") })

			Expect(applyYAML(renderMonitoring(namespace, false, nil, true))).To(Succeed())
			Expect(waitForCRPhase("tsugamonitoring", namespace, "e2e-monitoring",
				"Ready", crTimeout)).To(Succeed())

			Consistently(func() string {
				out, _ := kubectlStdout("get", "instrumentation", "-n", namespace,
					"--ignore-not-found", "-o", "name")
				return strings.TrimSpace(out)
			}, 20*time.Second, 5*time.Second).Should(BeEmpty())
		})

		It("annotates only annotated workloads when injectExistingWorkloads is false", func() {
			namespace := "e2e-inject-optin"
			Expect(applyYAML(fmt.Sprintf(
				"apiVersion: v1\nkind: Namespace\nmetadata:\n  name: %s\n", namespace))).To(Succeed())
			DeferCleanup(func() { _, _ = kubectl("delete", "namespace", namespace, "--wait=false") })

			Expect(applyYAML(renderMonitoring(
				namespace, true, []string{"nodejs"}, false))).To(Succeed())
			Expect(waitForCRPhase("tsugamonitoring", namespace, "e2e-monitoring",
				"Ready", crTimeout)).To(Succeed())

			// injectExistingWorkloads is false here, so annotateExistingWorkloads
			// never runs in this namespace: it neither adds nor removes
			// annotations. A workload without the opt-in annotation not being
			// injected proves nothing by itself - that holds even if the OTel
			// webhook is down or injection is impossible in this cluster. The
			// positive control below (a workload deployed already carrying the
			// opt-in annotation, injected via the webhook directly rather than
			// via annotateExistingWorkloads) establishes that injection is live
			// in this environment, so a dead webhook surfaces as a positive-
			// control failure rather than a silently vacuous negative.
			By("deploying a workload that already carries the opt-in annotation")
			Expect(applyYAML(renderLanguageWorkload(namespace, "nodejs", true))).To(Succeed())
			_, err := kubectl("wait", "--for=condition=Available",
				"-n", namespace, "deploy/app-nodejs", "--timeout=180s")
			Expect(err).NotTo(HaveOccurred())

			By("asserting the pre-annotated workload IS injected")
			Eventually(func() (string, error) {
				return kubectl("get", "pods", "-n", namespace, "-l", "app=nodejs",
					"-o", "jsonpath={.items[0].spec.initContainers[*].name}")
			}, crTimeout, 5*time.Second).Should(ContainSubstring("opentelemetry"),
				"a workload carrying the opt-in annotation must be injected even when "+
					"injectExistingWorkloads is false")

			By("deploying a second workload without the opt-in annotation")
			unannotated := fmt.Sprintf(`apiVersion: apps/v1
kind: Deployment
metadata:
  name: app-nodejs-noopt
  namespace: %s
spec:
  replicas: 1
  selector:
    matchLabels:
      app: nodejs-noopt
  template:
    metadata:
      labels:
        app: nodejs-noopt
    spec:
      containers:
        - name: app
          image: tsuga-e2e/app-nodejs:latest
          imagePullPolicy: Never
          ports:
            - containerPort: 8080
`, namespace)
			Expect(applyYAML(unannotated)).To(Succeed())
			_, err = kubectl("wait", "--for=condition=Available",
				"-n", namespace, "deploy/app-nodejs-noopt", "--timeout=180s")
			Expect(err).NotTo(HaveOccurred())

			By("asserting the un-annotated workload is NOT injected")
			Consistently(func() string {
				out, _ := kubectl("get", "pods", "-n", namespace, "-l", "app=nodejs-noopt",
					"-o", "jsonpath={.items[0].spec.initContainers[*].name}")
				return out
			}, 20*time.Second, 5*time.Second).ShouldNot(ContainSubstring("opentelemetry"),
				"an un-annotated workload must not be injected when opt-in is required")
		})

		// Instrumented workloads carry no credential of their own: the
		// Instrumentation exports to the in-cluster agent, which holds the
		// cluster export token.
		It("renders an Instrumentation exporting through the in-cluster agent", func() {
			namespace := "e2e-inject-token"
			Expect(applyYAML(fmt.Sprintf(
				"apiVersion: v1\nkind: Namespace\nmetadata:\n  name: %s\n", namespace))).To(Succeed())
			DeferCleanup(func() { _, _ = kubectl("delete", "namespace", namespace, "--wait=false") })

			Expect(applyYAML(renderMonitoring(
				namespace, true, []string{"nodejs"}, true))).To(Succeed())
			Expect(waitForCRPhase("tsugamonitoring", namespace, "e2e-monitoring",
				"Ready", crTimeout)).To(Succeed())

			endpoint, err := kubectlStdout("get", "instrumentation", "-n", namespace,
				"-o", "jsonpath={.items[0].spec.exporter.endpoint}")
			Expect(err).NotTo(HaveOccurred())
			Expect(endpoint).To(Equal(fmt.Sprintf(
				"http://tsuga-agent-collector.%s.svc.cluster.local:4318", operatorNS)),
				"the Instrumentation must export to the agent Service, which holds the token")
		})
	})
}
