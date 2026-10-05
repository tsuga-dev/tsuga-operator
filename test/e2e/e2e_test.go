/*
Copyright 2026 Tsuga.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package e2e

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/tsuga-dev/tsuga-operator/test/utils"
)

// namespace where the project is deployed in
const namespace = "tsuga-operator-system"

// serviceAccountName created for the project
const serviceAccountName = "tsuga-operator-controller-manager"

// metricsServiceName is the name of the metrics service of the project
const metricsServiceName = "tsuga-operator-controller-manager-metrics-service"

// metricsRoleBindingName is the name of the RBAC that will be created to allow get the metrics data
const metricsRoleBindingName = "tsuga-operator-metrics-binding"

var _ = Describe("Manager", Ordered, func() {
	var controllerPodName string

	// Before running the tests, set up the environment by creating the namespace,
	// enforce the restricted security policy to the namespace, installing CRDs,
	// and deploying the controller.
	BeforeAll(func() {
		By("creating manager namespace")
		cmd := exec.Command("kubectl", "create", "ns", namespace, "--dry-run=client", "-o", "yaml")
		nsManifest, err := utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to render namespace")
		cmd = exec.Command("kubectl", "apply", "-f", "-")
		cmd.Stdin = strings.NewReader(nsManifest)
		_, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to create namespace")

		By("labeling the namespace to enforce the restricted security policy")
		cmd = exec.Command("kubectl", "label", "--overwrite", "ns", namespace,
			"pod-security.kubernetes.io/enforce=restricted")
		_, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to label namespace with restricted policy")

		By("creating the placeholder Tsuga credentials secret the manager deployment requires")
		// make deploy does not create this secret — config/manager/kustomization.yaml omits
		// credentials_secret.yaml on purpose, so a real cluster never receives its placeholder
		// token. The token here is deliberately invalid: no spec may depend on reaching the
		// Tsuga API.
		cmd = exec.Command("kubectl", "create", "secret", "generic", "tsuga-credentials",
			"-n", namespace, "--from-literal=api-token=e2e-placeholder-token", "--dry-run=client", "-o", "yaml")
		secretManifest, err := utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to render the Tsuga credentials secret")
		cmd = exec.Command("kubectl", "apply", "-f", "-")
		cmd.Stdin = strings.NewReader(secretManifest)
		_, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to create the Tsuga credentials secret")

		By("installing the OpenTelemetry Operator the collection controllers need")
		// The collection controllers are wired only when the OpenTelemetryCollector
		// CRD is served at manager startup, so this must precede the deploy.
		// CERT_MANAGER_VERSION matches the version BeforeSuite installs, so the target's cert-manager apply is a no-op.
		cmd = exec.Command("make", "install-otel-prereqs", "CERT_MANAGER_VERSION=v1.16.3")
		_, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to install the OpenTelemetry Operator")

		By("installing CRDs")
		cmd = exec.Command("make", "install")
		_, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to install CRDs")

		By("deploying the controller-manager")
		cmd = exec.Command("make", "deploy", fmt.Sprintf("IMG=%s", projectImage))
		_, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to deploy the controller-manager")
	})

	// After all tests have been executed, clean up by undeploying the controller, uninstalling CRDs,
	// and deleting the namespace.
	AfterAll(func() {
		By("cleaning up the curl pod for metrics")
		cmd := exec.Command("kubectl", "delete", "pod", "curl-metrics", "-n", namespace)
		_, _ = utils.Run(cmd)

		By("undeploying the controller-manager")
		cmd = exec.Command("make", "undeploy")
		_, _ = utils.Run(cmd)

		By("uninstalling CRDs")
		cmd = exec.Command("make", "uninstall")
		_, _ = utils.Run(cmd)

		By("removing manager namespace")
		cmd = exec.Command("kubectl", "delete", "ns", namespace)
		_, _ = utils.Run(cmd)
	})

	// After each test, check for failures and collect logs, events,
	// and pod descriptions for debugging.
	AfterEach(func() {
		specReport := CurrentSpecReport()
		if specReport.Failed() {
			By("Fetching controller manager pod logs")
			cmd := exec.Command("kubectl", "logs", controllerPodName, "-n", namespace)
			controllerLogs, err := utils.Run(cmd)
			if err == nil {
				_, _ = fmt.Fprintf(GinkgoWriter, "Controller logs:\n %s", controllerLogs)
			} else {
				_, _ = fmt.Fprintf(GinkgoWriter, "Failed to get Controller logs: %s", err)
			}

			By("Fetching Kubernetes events")
			cmd = exec.Command("kubectl", "get", "events", "-n", namespace, "--sort-by=.lastTimestamp")
			eventsOutput, err := utils.Run(cmd)
			if err == nil {
				_, _ = fmt.Fprintf(GinkgoWriter, "Kubernetes events:\n%s", eventsOutput)
			} else {
				_, _ = fmt.Fprintf(GinkgoWriter, "Failed to get Kubernetes events: %s", err)
			}

			By("Fetching curl-metrics logs")
			cmd = exec.Command("kubectl", "logs", "curl-metrics", "-n", namespace)
			metricsOutput, err := utils.Run(cmd)
			if err == nil {
				_, _ = fmt.Fprintf(GinkgoWriter, "Metrics logs:\n %s", metricsOutput)
			} else {
				_, _ = fmt.Fprintf(GinkgoWriter, "Failed to get curl-metrics logs: %s", err)
			}

			By("Fetching controller manager pod description")
			cmd = exec.Command("kubectl", "describe", "pod", controllerPodName, "-n", namespace)
			podDescription, err := utils.Run(cmd)
			if err == nil {
				fmt.Println("Pod description:\n", podDescription)
			} else {
				fmt.Println("Failed to describe controller pod")
			}
		}
	})

	SetDefaultEventuallyTimeout(2 * time.Minute)
	SetDefaultEventuallyPollingInterval(time.Second)

	Context("Manager", func() {
		It("should run successfully", func() {
			By("validating that the controller-manager pod is running as expected")
			verifyControllerUp := func(g Gomega) {
				// Get the name of the controller-manager pod
				cmd := exec.Command("kubectl", "get",
					"pods", "-l", "control-plane=controller-manager",
					"-o", "go-template={{ range .items }}"+
						"{{ if not .metadata.deletionTimestamp }}"+
						"{{ .metadata.name }}"+
						"{{ \"\\n\" }}{{ end }}{{ end }}",
					"-n", namespace,
				)

				podOutput, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred(), "Failed to retrieve controller-manager pod information")
				podNames := utils.GetNonEmptyLines(podOutput)
				g.Expect(podNames).To(HaveLen(1), "expected 1 controller pod running")
				controllerPodName = podNames[0]
				g.Expect(controllerPodName).To(ContainSubstring("controller-manager"))

				// Validate the pod's status
				cmd = exec.Command("kubectl", "get",
					"pods", controllerPodName, "-o", "jsonpath={.status.phase}",
					"-n", namespace,
				)
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Equal("Running"), "Incorrect controller-manager pod status")
			}
			Eventually(verifyControllerUp).Should(Succeed())
		})

		It("should ensure the metrics endpoint is serving metrics", func() {
			By("creating a ClusterRoleBinding for the service account to allow access to metrics")
			// Clean up any pre-existing binding (e.g. from a previous failed run) and defer final cleanup.
			_, _ = utils.Run(exec.Command(
				"kubectl",
				"delete",
				"clusterrolebinding",
				metricsRoleBindingName,
				"--ignore-not-found"))
			DeferCleanup(func() {
				_, _ = utils.Run(exec.Command(
					"kubectl",
					"delete",
					"clusterrolebinding",
					metricsRoleBindingName,
					"--ignore-not-found"))
			})
			cmd := exec.Command("kubectl", "create", "clusterrolebinding", metricsRoleBindingName,
				"--clusterrole=tsuga-operator-metrics-reader",
				fmt.Sprintf("--serviceaccount=%s:%s", namespace, serviceAccountName),
			)
			_, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to create ClusterRoleBinding")

			By("validating that the metrics service is available")
			cmd = exec.Command("kubectl", "get", "service", metricsServiceName, "-n", namespace)
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Metrics service should exist")

			By("getting the service account token")
			token, err := serviceAccountToken()
			Expect(err).NotTo(HaveOccurred())
			Expect(token).NotTo(BeEmpty())

			By("waiting for the metrics endpoint to be ready")
			verifyMetricsEndpointReady := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "endpoints", metricsServiceName, "-n", namespace)
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(ContainSubstring("8443"), "Metrics endpoint is not ready")
			}
			Eventually(verifyMetricsEndpointReady).Should(Succeed())

			By("verifying that the controller manager is serving the metrics server")
			verifyMetricsServerStarted := func(g Gomega) {
				cmd := exec.Command("kubectl", "logs", controllerPodName, "-n", namespace)
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(ContainSubstring("Serving metrics server"),
					"Metrics server not yet started")
			}
			Eventually(verifyMetricsServerStarted).Should(Succeed())

			By("creating the curl-metrics pod to access the metrics endpoint")
			cmd = exec.Command("kubectl", "run", "curl-metrics", "--restart=Never",
				"--namespace", namespace,
				"--image=curlimages/curl:latest",
				"--overrides",
				fmt.Sprintf(`{
					"spec": {
						"containers": [{
							"name": "curl",
							"image": "curlimages/curl:latest",
							"command": ["/bin/sh", "-c"],
							"args": ["curl -v -k -H 'Authorization: Bearer %s' https://%s.%s.svc.cluster.local:8443/metrics"],
							"securityContext": {
								"allowPrivilegeEscalation": false,
								"capabilities": {
									"drop": ["ALL"]
								},
								"runAsNonRoot": true,
								"runAsUser": 1000,
								"seccompProfile": {
									"type": "RuntimeDefault"
								}
							}
						}],
						"serviceAccount": "%s"
					}
				}`, token, metricsServiceName, namespace, serviceAccountName))
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to create curl-metrics pod")

			By("waiting for the curl-metrics pod to complete.")
			verifyCurlUp := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "pods", "curl-metrics",
					"-o", "jsonpath={.status.phase}",
					"-n", namespace)
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Equal("Succeeded"), "curl pod in wrong status")
			}
			Eventually(verifyCurlUp, 5*time.Minute).Should(Succeed())

			By("getting the metrics by checking curl-metrics logs")
			metricsOutput := getMetricsOutput()
			Expect(metricsOutput).To(ContainSubstring(
				"controller_runtime_reconcile_total",
			))
		})

		// +kubebuilder:scaffold:e2e-webhooks-checks

		// TODO: Customize the e2e test suite with scenarios specific to your project.
		// Consider applying sample/CR(s) and check their status and/or verifying
		// the reconciliation by using the metrics, i.e.:
		// metricsOutput := getMetricsOutput()
		// Expect(metricsOutput).To(ContainSubstring(
		//    fmt.Sprintf(`controller_runtime_reconcile_total{controller="%s",result="success"} 1`,
		//    strings.ToLower(<Kind>),
		// ))

		It("should serve the SLO CRD and finalize an SLO resource", func() {
			By("verifying the SLO CRD is served by the API server")
			verifyCRD := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "crd", "slos.observability.tsuga.com")
				_, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
			}
			Eventually(verifyCRD).Should(Succeed())

			By("creating an SLO resource")
			manifest := `apiVersion: observability.tsuga.com/v1alpha1
kind: SLO
metadata:
  name: e2e-smoke-slo
  namespace: default
spec:
  name: "E2E Smoke SLO"
  owner: "e2e-team"
  permissions: "all"
  target: 99.9
  timeframeDays: 30
  configuration:
    type: event
    dataSource: traces
    noDataBehavior: good
    goodQuery:
      formula: "q1"
      queries:
        - aggregate: {type: count}
          filter: "service:api -status:error"
    totalQuery:
      formula: "q1"
      queries:
        - aggregate: {type: count}
          filter: "service:api"
`
			cmd := exec.Command("kubectl", "apply", "-f", "-")
			cmd.Stdin = strings.NewReader(manifest)
			_, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to create the SLO resource")
			// Runs at spec end while the controller is still up, so the finalizer
			// clears normally. Tolerates the happy path, where the spec's own delete
			// below already removed the resource, and times out rather than risking
			// AfterAll's unbounded CRD uninstall hanging on a leftover finalizer.
			DeferCleanup(func() {
				_, _ = utils.Run(exec.Command("kubectl", "delete", "slo", "e2e-smoke-slo",
					"-n", "default", "--ignore-not-found", "--timeout=60s"))
			})

			By("verifying the controller writes its finalizer")
			// The assertions stop here on purpose. The deployed manager holds the
			// invalid placeholder API token this suite's BeforeAll creates above
			// (e2e-placeholder-token), so the remote create cannot succeed and
			// status.phase settles at Error. Asserting on the finalizer keeps this
			// test independent of network reachability from CI.
			verifyFinalizer := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "slo", "e2e-smoke-slo", "-n", "default",
					"-o", "jsonpath={.metadata.finalizers[0]}")
				out, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(out).To(Equal("observability.tsuga.com/slo-finalizer"))
			}
			Eventually(verifyFinalizer).Should(Succeed())

			By("deleting the SLO resource")
			// status.id is empty because the remote create never succeeded, and the
			// placeholder token gets the tag lookup a 401, so handleDelete drops the
			// finalizer without a remote call. The delete must return, not block.
			cmd = exec.Command("kubectl", "delete", "slo", "e2e-smoke-slo", "-n", "default", "--timeout=60s")
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to delete the SLO resource")
		})

		It("should provision and monitor a Postgres server", func() {
			const pgNamespace = "e2e-pg"
			manifest := `apiVersion: v1
kind: Namespace
metadata:
  name: e2e-pg
---
apiVersion: v1
kind: Secret
metadata:
  name: pg-admin
  namespace: e2e-pg
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
  namespace: e2e-pg
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
  namespace: e2e-pg
spec:
  selector: {app: postgres}
  ports:
    - port: 5432
---
apiVersion: observability.tsuga.com/v1alpha1
kind: TsugaCollectorConfig
metadata:
  name: cluster
spec:
  clusterName: e2e
  export:
    endpoint: https://otlp.e2e.invalid
    tokenSecretRef:
      name: tsuga-credentials
      key: api-token
  agent: {}
  gateway: {}
---
apiVersion: observability.tsuga.com/v1alpha1
kind: TsugaPostgresMonitoring
metadata:
  name: e2e-pg
  namespace: e2e-pg
spec:
  host: postgres.e2e-pg.svc
  sslMode: disable
  provisioning:
    mode: managed
    adminSecretRef:
      name: pg-admin
`
			cmd := exec.Command("kubectl", "apply", "-f", "-")
			cmd.Stdin = strings.NewReader(manifest)
			_, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to apply the Postgres fixtures")
			DeferCleanup(func() {
				_, _ = utils.Run(exec.Command("kubectl", "delete", "ns", pgNamespace, "--ignore-not-found", "--timeout=120s"))
				_, _ = utils.Run(exec.Command("kubectl", "delete", "tsugacollectorconfig", "cluster",
					"--ignore-not-found", "--timeout=60s"))
			})

			By("waiting for the setup job to provision otel_monitor")
			verifyReady := func(g Gomega) {
				out, err := utils.Run(exec.Command("kubectl", "get", "tspg", "e2e-pg", "-n", pgNamespace,
					"-o", `jsonpath={.status.conditions[?(@.type=="Ready")].status}/{.status.conditions[?(@.type=="Ready")].reason}`))
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(out).To(Equal("True/Reconciled"))
			}
			Eventually(verifyReady, 5*time.Minute).Should(Succeed())

			By("waiting for the Postgres collector to become available")
			// Polled rather than `kubectl wait`, which errors at once while the
			// OTel Operator has not created the Deployment yet.
			verifyCollector := func(g Gomega) {
				out, err := utils.Run(exec.Command("kubectl", "get", "deploy", "e2e-pg-pg-collector", "-n", pgNamespace,
					"-o", "jsonpath={.status.availableReplicas}"))
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(out).To(Equal("1"))
			}
			Eventually(verifyCollector, 3*time.Minute).Should(Succeed())
		})
	})
})

// serviceAccountToken returns a token for the specified service account in the given namespace.
// It uses the Kubernetes TokenRequest API to generate a token by directly sending a request
// and parsing the resulting token from the API response.
func serviceAccountToken() (string, error) {
	const tokenRequestRawString = `{
		"apiVersion": "authentication.k8s.io/v1",
		"kind": "TokenRequest"
	}`

	// Temporary file to store the token request
	secretName := fmt.Sprintf("%s-token-request", serviceAccountName)
	tokenRequestFile := filepath.Join("/tmp", secretName)
	err := os.WriteFile(tokenRequestFile, []byte(tokenRequestRawString), os.FileMode(0o644))
	if err != nil {
		return "", err
	}

	var out string
	verifyTokenCreation := func(g Gomega) {
		// Execute kubectl command to create the token
		cmd := exec.Command("kubectl", "create", "--raw", fmt.Sprintf(
			"/api/v1/namespaces/%s/serviceaccounts/%s/token",
			namespace,
			serviceAccountName,
		), "-f", tokenRequestFile)

		output, err := cmd.CombinedOutput()
		g.Expect(err).NotTo(HaveOccurred())

		// Parse the JSON output to extract the token
		var token tokenRequest
		err = json.Unmarshal(output, &token)
		g.Expect(err).NotTo(HaveOccurred())

		out = token.Status.Token
	}
	Eventually(verifyTokenCreation).Should(Succeed())

	return out, err
}

// getMetricsOutput retrieves and returns the logs from the curl pod used to access the metrics endpoint.
func getMetricsOutput() string {
	By("getting the curl-metrics logs")
	cmd := exec.Command("kubectl", "logs", "curl-metrics", "-n", namespace)
	metricsOutput, err := utils.Run(cmd)
	Expect(err).NotTo(HaveOccurred(), "Failed to retrieve logs from curl pod")
	Expect(metricsOutput).To(ContainSubstring("< HTTP/1.1 200 OK"))
	return metricsOutput
}

// tokenRequest is a simplified representation of the Kubernetes TokenRequest API response,
// containing only the token field that we need to extract.
type tokenRequest struct {
	Status struct {
		Token string `json:"token"`
	} `json:"status"`
}
