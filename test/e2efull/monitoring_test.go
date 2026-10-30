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

// tierBLanguages are the languages Tier B injects, each with a fixture under
// test/fixtures.
var tierBLanguages = []string{"java", "nodejs", "python", "dotnet", "go", "nginx"}

// injectedContainer names the container the OTel Operator adds to a pod it
// instruments: an init container that copies the agent in for most
// languages, a privileged eBPF sidecar for go, and a module-attaching init
// container for nginx.
func injectedContainer(language string) string {
	switch language {
	case languageGo:
		return "opentelemetry-auto-instrumentation"
	case "nginx":
		return "otel-agent-attach-nginx"
	default:
		return "opentelemetry-auto-instrumentation-"
	}
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

		for _, language := range tierBLanguages {
			It("injects "+language+" instrumentation", func() {
				namespace := "e2e-inject-" + language
				Expect(applyNamespace(namespace)).To(Succeed())
				DeferCleanup(func() { _, _ = kubectl("delete", "namespace", namespace, "--wait=false") })

				// Except for go (below), the workload is deployed BEFORE the
				// TsugaMonitoring, so this covers Workloads mode adopting an
				// existing Deployment: the controller annotates its pod
				// template, which rolls it.
				deployWorkload := func(optIn bool) {
					Expect(applyYAML(renderLanguageWorkload(namespace, language, optIn))).To(Succeed())
					_, err := kubectl("wait", "--for=condition=Available",
						"-n", namespace, "deploy/app-"+language, "--timeout=300s")
					Expect(err).NotTo(HaveOccurred())
				}
				applyMonitoring := func() {
					Expect(applyYAML(renderMonitoring(namespace, true, []string{language}, "Workloads", ""))).To(Succeed())
					Expect(waitForCRPhase("tsugamonitoring", namespace, "e2e-monitoring",
						"Ready", crTimeout)).To(Succeed())
				}

				// The operator never bulk-injects go: injection needs the
				// per-workload otel-go-auto-target-exe annotation, so a go
				// workload opts in by hand. The webhook only injects at pod
				// creation, so it is deployed after the Instrumentation exists.
				if language == languageGo {
					applyMonitoring()
					By("deploying the hand-annotated go workload")
					deployWorkload(true)
				} else {
					By("running the uninstrumented workload first, so it is an existing workload")
					deployWorkload(false)
					applyMonitoring()
				}

				By("asserting the Instrumentation carries the language block")
				Expect(instrumentationLanguages(namespace)).To(ConsistOf(language),
					"the rendered Instrumentation must carry exactly the requested language block")

				By("asserting the pod received the injected agent container")
				Eventually(func() (string, error) {
					// Every pod, not items[0]: the pre-injection pod lingers
					// through the roll, and for nginx never leaves.
					return kubectl("get", "pods", "-n", namespace, "-l", "app="+language,
						"-o", "jsonpath={.items[*].spec.initContainers[*].name} {.items[*].spec.containers[*].name}")
				}, crTimeout, 5*time.Second).Should(ContainSubstring(injectedContainer(language)),
					"the OTel Operator should have injected its agent container")

				// ponytail: the spike on kind/arm64 (Docker Desktop) injected
				// nginx but the pod never starts: the OTel Operator's
				// autoinstrumentation-apache-httpd:1.0.4 image, which carries the
				// nginx module, is published for amd64 only ("no match for
				// platform in manifest"). nginx is therefore checked in-cluster
				// only, like profiling. Go's eBPF agent did produce spans there.
				if language == "nginx" {
					return
				}

				By("asserting this workload's spans reached Tsuga, which only an attached agent can produce")
				// Scoped to this workload: counting spans for the whole cluster
				// would let this scenario pass on the spans an earlier language
				// produced.
				//
				// Scoped by Deployment name rather than service.name - see
				// CountTracesForWorkload. app-<language> is the Deployment this
				// spec just waited on, so the scope is exact.
				Eventually(func() (int, error) {
					return cli.CountTracesForWorkload("app-"+language, baselineCluster, "-15m")
				}, ingestTimeout(), 10*time.Second).Should(BeNumerically(">", 0))
			})
		}

		It("injects all six languages at once", func() {
			namespace := "e2e-inject-all"
			Expect(applyNamespace(namespace)).To(Succeed())
			DeferCleanup(func() { _, _ = kubectl("delete", "namespace", namespace, "--wait=false") })

			Expect(applyYAML(renderMonitoring(namespace, true, tierBLanguages, "Workloads", ""))).To(Succeed())
			Expect(waitForCRPhase("tsugamonitoring", namespace, "e2e-monitoring",
				"Ready", crTimeout)).To(Succeed())

			Expect(instrumentationLanguages(namespace)).To(ConsistOf(
				"java", "nodejs", "python", "dotnet", "go", "nginx"))
		})

		It("merges instrumentation.spec into the Instrumentation", func() {
			namespace := "e2e-inject-spec"
			Expect(applyNamespace(namespace)).To(Succeed())
			DeferCleanup(func() { _, _ = kubectl("delete", "namespace", namespace, "--wait=false") })

			Expect(applyYAML(renderLanguageWorkload(namespace, "nodejs", false))).To(Succeed())
			_, err := kubectl("wait", "--for=condition=Available",
				"-n", namespace, "deploy/app-nodejs", "--timeout=300s")
			Expect(err).NotTo(HaveOccurred())

			// resourceAttributes is the Instrumentation CRD's key; the schema
			// rejects resource.attributes.
			Expect(applyYAML(renderMonitoring(namespace, true, []string{"nodejs"}, "Workloads",
				"resource:\n  resourceAttributes:\n    e2e.merged: \"yes\""))).To(Succeed())
			Expect(waitForCRPhase("tsugamonitoring", namespace, "e2e-monitoring",
				"Ready", crTimeout)).To(Succeed())

			By("asserting the user's resource attribute landed on the Instrumentation")
			merged, err := kubectlStdout("get", "instrumentation", "-n", namespace,
				"-o", `jsonpath={.items[0].spec.resource.resourceAttributes.e2e\.merged}`)
			Expect(err).NotTo(HaveOccurred())
			Expect(merged).To(Equal("yes"))

			By("asserting the operator-owned exporter endpoint survived, so spec was merged, not substituted")
			endpoint, err := kubectlStdout("get", "instrumentation", "-n", namespace,
				"-o", "jsonpath={.items[0].spec.exporter.endpoint}")
			Expect(err).NotTo(HaveOccurred())
			Expect(endpoint).To(Equal(fmt.Sprintf(
				"http://tsuga-agent-collector.%s.svc.cluster.local:4318", operatorNS)))

			By("asserting spans carrying the merged attribute reached Tsuga")
			Eventually(func() (int, error) {
				return cli.CountSpans(baselineCluster,
					"context.e2e.merged:yes AND context.k8s.deployment.name:app-nodejs", "-15m")
			}, ingestTimeout(), signalPollInterval).Should(BeNumerically(">", 0))
		})

		It("injects new pods through the namespace annotation without rolling existing ones", func() {
			namespace := "e2e-inject-namespace"
			Expect(applyNamespace(namespace)).To(Succeed())
			DeferCleanup(func() { _, _ = kubectl("delete", "namespace", namespace, "--wait=false") })

			Expect(applyYAML(renderLanguageWorkload(namespace, "nodejs", false))).To(Succeed())
			_, err := kubectl("wait", "--for=condition=Available",
				"-n", namespace, "deploy/app-nodejs", "--timeout=300s")
			Expect(err).NotTo(HaveOccurred())
			generation := func() (string, error) {
				return kubectlStdout("get", "deploy", "app-nodejs", "-n", namespace, "-o", "jsonpath={.metadata.generation}")
			}
			before, err := generation()
			Expect(err).NotTo(HaveOccurred())

			Expect(applyYAML(renderMonitoring(namespace, true, []string{"nodejs"}, "Namespace", ""))).To(Succeed())
			Expect(waitForCRPhase("tsugamonitoring", namespace, "e2e-monitoring",
				"Ready", crTimeout)).To(Succeed())
			namespaceAnnotation := func() (string, error) {
				return kubectlStdout("get", "namespace", namespace,
					"-o", `jsonpath={.metadata.annotations.instrumentation\.opentelemetry\.io/inject-nodejs}`)
			}
			Eventually(namespaceAnnotation, crTimeout, 5*time.Second).Should(Equal("e2e-monitoring"))

			By("asserting the running Deployment was not rolled")
			podContainers := func() (string, error) {
				return kubectl("get", "pods", "-n", namespace, "-l", "app=nodejs",
					"-o", "jsonpath={.items[*].spec.initContainers[*].name} {.items[*].spec.containers[*].name}")
			}
			Consistently(podContainers, 20*time.Second, 5*time.Second).ShouldNot(ContainSubstring(injectedContainer("nodejs")))
			Expect(generation()).To(Equal(before))

			By("restarting the Deployment, whose new pod is injected at admission")
			_, err = kubectl("rollout", "restart", "deploy/app-nodejs", "-n", namespace)
			Expect(err).NotTo(HaveOccurred())
			Eventually(podContainers, crTimeout, 5*time.Second).Should(ContainSubstring(injectedContainer("nodejs")))
			Eventually(func() (int, error) {
				return cli.CountTracesForWorkload("app-nodejs", baselineCluster, "-15m")
			}, ingestTimeout(), 10*time.Second).Should(BeNumerically(">", 0))

			By("deleting the TsugaMonitoring, which strips the namespace annotation")
			_, err = kubectl("delete", "tsugamonitoring", "e2e-monitoring", "-n", namespace, "--timeout=60s")
			Expect(err).NotTo(HaveOccurred())
			Eventually(namespaceAnnotation, crTimeout, 5*time.Second).Should(BeEmpty())
		})

		It("creates no Instrumentation when disabled", func() {
			namespace := "e2e-inject-off"
			Expect(applyNamespace(namespace)).To(Succeed())
			DeferCleanup(func() { _, _ = kubectl("delete", "namespace", namespace, "--wait=false") })

			Expect(applyYAML(renderMonitoring(namespace, false, nil, "Workloads", ""))).To(Succeed())
			Expect(waitForCRPhase("tsugamonitoring", namespace, "e2e-monitoring",
				"Ready", crTimeout)).To(Succeed())

			Consistently(func() string {
				out, _ := kubectlStdout("get", "instrumentation", "-n", namespace,
					"--ignore-not-found", "-o", "name")
				return strings.TrimSpace(out)
			}, 20*time.Second, 5*time.Second).Should(BeEmpty())
		})

		It("annotates only annotated workloads in Manual mode", func() {
			namespace := "e2e-inject-optin"
			Expect(applyNamespace(namespace)).To(Succeed())
			DeferCleanup(func() { _, _ = kubectl("delete", "namespace", namespace, "--wait=false") })

			Expect(applyYAML(renderMonitoring(
				namespace, true, []string{"nodejs"}, "Manual", ""))).To(Succeed())
			Expect(waitForCRPhase("tsugamonitoring", namespace, "e2e-monitoring",
				"Ready", crTimeout)).To(Succeed())

			// injection is Manual here, so the controller annotates nothing
			// in this namespace: it neither adds nor removes
			// annotations. A workload without the opt-in annotation not being
			// injected proves nothing by itself - that holds even if the OTel
			// webhook is down or injection is impossible in this cluster. The
			// positive control below (a workload deployed already carrying the
			// opt-in annotation, injected via the webhook directly rather than
			// via the controller) establishes that injection is live
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
				"a workload carrying the opt-in annotation must be injected even in "+
					"Manual mode")

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
	})
}
