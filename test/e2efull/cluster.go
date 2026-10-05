package e2efull

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/onsi/ginkgo/v2"

	"github.com/tsuga-dev/tsuga-operator/test/e2efull/tsugacli"
)

// KindClusterName is deliberately distinct from the dev and smoke-suite
// clusters so a full run can never clobber a cluster someone is using.
const KindClusterName = "tsuga-operator-e2e-full"

const (
	operatorNS = "tsuga-operator-system"
	// tsugaSecret must match config/manager/manager.yaml, which mounts
	// secret "tsuga-credentials" key "api-token" into the manager. A different
	// name leaves the operator CrashLooping on a missing secret.
	tsugaSecret = "tsuga-credentials"
)

// cli and runID are declared here rather than in suite_test.go because this
// file uses them, and a non-test file cannot reference identifiers declared in
// a _test.go file. BeforeSuite assigns cli.
//
// runID is initialized eagerly, at package-variable-initialization time,
// rather than inside BeforeSuite. Ginkgo builds the entire spec tree —
// including every top-level `for` loop that constructs `It`s from a scenario
// slice — before any BeforeSuite/BeforeAll runs. A tier that builds its
// scenario slice from runID inside the Describe body (as Tier A does) needs
// the real value already resolved at that point; assigning runID in
// BeforeSuite would be too late; the tree is already built by then.
var (
	cli   *tsugacli.Client
	runID = newRunID()
)

// newRunID returns the identifier stamped into every cluster name and Tsuga
// resource this run creates, so a sweep can find them later.
func newRunID() string {
	buf := make([]byte, 4)
	if _, err := rand.Read(buf); err != nil {
		return fmt.Sprintf("e2e-%d", time.Now().UnixNano())
	}
	return "e2e-" + hex.EncodeToString(buf)
}

// repoRoot returns the module root: the nearest ancestor of the working
// directory that holds go.mod.
//
// utils.GetProjectDir is deliberately not used here. It derives the root by
// stripping the literal substring "/test/e2e" out of the working directory,
// which from test/e2efull cuts those characters from the middle of the path
// and yields a directory that does not exist. Every command configured with
// that directory fails before it starts. Walking up to go.mod does not care
// where the package sits in the tree. os.Chdir is avoided too: it mutates
// process-global state shared by every spec in the process.
func repoRoot() (string, error) {
	wd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("working directory: %w", err)
	}
	for dir := wd; ; {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("no go.mod found in %q or any parent", wd)
		}
		dir = parent
	}
}

// runAtRepoRoot runs a command with its working directory pinned to the
// module root, so the relative paths inside the Makefile resolve.
func runAtRepoRoot(name string, args ...string) (string, error) {
	root, err := repoRoot()
	if err != nil {
		return "", err
	}
	cmd := exec.Command(name, args...)
	cmd.Dir = root
	ginkgo.GinkgoWriter.Printf("running %s %s (in %s)\n",
		name, strings.Join(args, " "), root)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("%s %s failed: %w: %s",
			name, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

// runMake runs a make target from the module root.
func runMake(args ...string) (string, error) { return runAtRepoRoot("make", args...) }

// kubeContext is the kubeconfig context every kubectl invocation is pinned
// to.
//
// Nothing the suite runs reads the kubeconfig's current-context, and nothing
// it runs changes it. A run takes 90 to 120 minutes; someone switching
// context in another terminal must not be able to redirect an apply - or
// worse, a namespace delete - at their own cluster. (kind switches the
// current-context itself, once, when it creates the cluster; that is kind's
// behaviour and cannot be suppressed without a separate kubeconfig file.)
func kubeContext() string { return "kind-" + KindClusterName }

// kubectlCommand builds a kubectl invocation pinned to the suite's context.
// Every kubectl the suite runs goes through here.
func kubectlCommand(args ...string) *exec.Cmd {
	return exec.Command("kubectl", append([]string{"--context", kubeContext()}, args...)...)
}

// kubectl runs kubectl and returns its combined output, which makes a
// failure self-describing. Callers that parse the result, or test it for
// emptiness, must use kubectlStdout instead.
func kubectl(args ...string) (string, error) {
	out, err := kubectlCommand(args...).CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("kubectl %s failed: %w: %s",
			strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

// kubectlStdout runs kubectl and returns stdout only.
//
// Since kubectl 1.18 an empty result set prints "No resources found in <ns>
// namespace." on stderr. A caller that reads combined output therefore
// never sees an empty string for an empty list and concludes the objects
// are still there - which is a guaranteed timeout, not a flake. Pair this
// with --ignore-not-found when the query names a specific object.
func kubectlStdout(args ...string) (string, error) {
	cmd := kubectlCommand(args...)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return string(out), fmt.Errorf("kubectl %s failed: %w: %s",
			strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return string(out), nil
}

// applyYAML pipes a manifest to kubectl apply.
func applyYAML(manifest string) error {
	return pipeToKubectl(manifest, "apply", "-f", "-")
}

// deleteYAML pipes a manifest to kubectl delete, tolerating absent objects.
func deleteYAML(manifest string) error {
	return pipeToKubectl(manifest, "delete", "--ignore-not-found", "-f", "-")
}

func pipeToKubectl(manifest string, args ...string) error {
	cmd := kubectlCommand(args...)
	cmd.Stdin = strings.NewReader(manifest)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("kubectl %s failed: %w: %s",
			strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

// crArgs builds kubectl arguments for a custom resource, omitting -n for
// cluster-scoped kinds. TsugaCollectorConfig is cluster-scoped, so callers
// pass an empty namespace for it and "-n \"\"" would fail.
func crArgs(kind, namespace, name string, tail ...string) []string {
	args := []string{"get", kind, name}
	if namespace != "" {
		args = append(args, "-n", namespace)
	}
	return append(args, tail...)
}

// ingestTimeout is the ceiling on waiting for telemetry to become queryable.
// Poll helpers return as soon as data appears, so this is not a per-scenario
// cost. New metric names can take minutes to index, hence the generous default.
func ingestTimeout() time.Duration {
	if raw := os.Getenv("E2E_INGEST_TIMEOUT"); raw != "" {
		if parsed, err := time.ParseDuration(raw); err == nil {
			return parsed
		}
	}
	return 180 * time.Second
}

// keepCluster reports whether the kind cluster should survive the run, which
// is what makes a failure debuggable.
func keepCluster() bool { return os.Getenv("E2E_KEEP_CLUSTER") != "" }

// ensureKindCluster creates the suite's cluster unless it already exists.
func ensureKindCluster() error {
	out, err := exec.Command("kind", "get", "clusters").CombinedOutput()
	if err != nil {
		return fmt.Errorf("kind get clusters: %w: %s", err, string(out))
	}
	for _, line := range strings.Split(string(out), "\n") {
		if strings.TrimSpace(line) == KindClusterName {
			ginkgo.GinkgoWriter.Printf("reusing kind cluster %s\n", KindClusterName)
			return nil
		}
	}
	create := exec.Command("kind", "create", "cluster", "--name", KindClusterName)
	if out, err := create.CombinedOutput(); err != nil {
		return fmt.Errorf("kind create cluster: %w: %s", err, string(out))
	}
	// No `kubectl config use-context` here on purpose: kubeContext pins
	// every kubectl the suite runs, so switching the user's current-context
	// would buy nothing and change state that is not ours.
	return nil
}

// createCredentialSecrets creates the operator's API token secret and the
// collectors' ingestion key secret in the operator namespace.
func createCredentialSecrets(cfg tsugacli.Config) error {
	if _, err := kubectl("create", "namespace", operatorNS,
		"--dry-run=client", "-o", "yaml"); err != nil {
		return err
	}
	apply := kubectlCommand("apply", "-f", "-")
	apply.Stdin = strings.NewReader(fmt.Sprintf(
		"apiVersion: v1\nkind: Namespace\nmetadata:\n  name: %s\n", operatorNS))
	if out, err := apply.CombinedOutput(); err != nil {
		return fmt.Errorf("create namespace: %w: %s", err, string(out))
	}

	for _, secret := range []struct{ name, key, value string }{
		{tsugaSecret, "api-token", cfg.APIToken},
		{"tsuga-ingestion", "api-token", cfg.IngestionKey},
	} {
		cmd := kubectlCommand("-n", operatorNS, "create", "secret", "generic",
			secret.name, "--from-literal="+secret.key+"="+secret.value,
			"--dry-run=client", "-o", "yaml")
		manifest, err := cmd.Output()
		if err != nil {
			return fmt.Errorf("render secret %s: %w", secret.name, err)
		}
		if err := applyYAML(string(manifest)); err != nil {
			return fmt.Errorf("apply secret %s: %w", secret.name, err)
		}
	}
	return nil
}

// sweepTsugaResources deletes every dashboard and monitor tagged with this
// run id. Ingested telemetry cannot be deleted and ages out with retention.
//
// The list call is unpaged, which is an unverified assumption: a run creates
// roughly 150 Tsuga resources, so if the CLI caps a page below that, this
// sweep deletes only the first page and `make e2e-sweep` cannot recover the
// rest either - re-running it re-reads the same page. Check `tsuga <kind>
// list` for a paging flag before concluding the sweep is broken.
func sweepTsugaResources(id string) {
	for _, kind := range []string{"dashboards", "monitors"} {
		out, err := cli.Run(kind, "list")
		if err != nil {
			ginkgo.GinkgoWriter.Printf("sweep: listing %s failed: %v\n", kind, err)
			continue
		}
		for _, resourceID := range idsTaggedWithRun(out, kind, id) {
			if _, err := cli.Run(kind, "delete", resourceID); err != nil {
				ginkgo.GinkgoWriter.Printf(
					"sweep: deleting %s %s failed: %v\n", kind, resourceID, err)
			}
		}
	}
}

// requireTeamID returns a team id from the test org to own the resources this
// suite creates. Dashboards and Monitors both require a real owner.
func requireTeamID() string {
	out, err := cli.Run("teams", "list")
	if err != nil {
		ginkgo.Fail(fmt.Sprintf("listing teams: %v", err))
	}
	// `tsuga teams list` returns a bare JSON array, not an object with a
	// "teams" key. Verified against the live CLI.
	var teams []struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(out, &teams); err != nil {
		ginkgo.Fail(fmt.Sprintf("decoding teams: %v: %s", err, string(out)))
	}
	if len(teams) == 0 {
		ginkgo.Fail("the test org has no teams; create one before running the suite")
	}
	return teams[0].ID
}

// idsTaggedWithRun extracts resource ids whose name carries the run id. The
// matching itself lives in tsugacli so the sweep command, which cannot import
// this Ginkgo-dependent package, shares one implementation with it.
func idsTaggedWithRun(payload []byte, kind, id string) []string {
	tagged, err := tsugacli.ResourcesTaggedWithRun(payload, kind, id)
	if err != nil {
		ginkgo.GinkgoWriter.Printf("sweep: %v\n", err)
		return nil
	}
	var ids []string
	for _, item := range tagged {
		ids = append(ids, item.ID)
	}
	return ids
}

// waitForCollectorReady waits for an OpenTelemetryCollector's pods to be ready.
//
// This polls rather than calling `kubectl wait`. Every scenario in a tier
// reuses the same collector names, so applying the next scenario's config
// rolls the agent's pods: one pod is terminating while its replacement comes
// up. `kubectl wait` resolves the label selector once, then fails outright
// when a pod in that snapshot disappears - "pod/...-z7nv5 condition met,
// Error from server (NotFound): pods ...-w7f2b not found" - reporting a
// failure for a collector that is in fact ready.
//
// Pods carrying a deletionTimestamp are ignored for the same reason: a
// terminating pod never becomes Ready, so counting it would make the wait
// depend on the previous scenario's teardown finishing first.
func waitForCollectorReady(namespace, name string, timeout time.Duration) error {
	selector := "app.kubernetes.io/name=" + name + "-collector"
	deadline := time.Now().Add(timeout)
	var last string
	for {
		out, err := kubectlStdout("get", "pods", "-n", namespace,
			"-l", selector, "--ignore-not-found", "-o", "json")
		if err == nil {
			ready, total, describe := readyPodCounts(out)
			last = describe
			if total > 0 && ready == total {
				return nil
			}
		}
		if !time.Now().Before(deadline) {
			return fmt.Errorf("pods matching %s in %s were not ready within %s (last: %s)",
				selector, namespace, timeout, last)
		}
		time.Sleep(2 * time.Second)
	}
}

// readyPodCounts reports how many live pods in a `kubectl get pods -o json`
// payload are Ready, out of how many are not terminating.
func readyPodCounts(raw string) (ready, total int, describe string) {
	var list struct {
		Items []struct {
			Metadata struct {
				Name              string `json:"name"`
				DeletionTimestamp string `json:"deletionTimestamp"`
			} `json:"metadata"`
			Status struct {
				Conditions []struct {
					Type   string `json:"type"`
					Status string `json:"status"`
				} `json:"conditions"`
			} `json:"status"`
		} `json:"items"`
	}
	if strings.TrimSpace(raw) == "" {
		return 0, 0, "no pods matched"
	}
	if err := json.Unmarshal([]byte(raw), &list); err != nil {
		return 0, 0, fmt.Sprintf("undecodable pod list: %v", err)
	}
	var states []string
	for _, pod := range list.Items {
		if pod.Metadata.DeletionTimestamp != "" {
			continue
		}
		total++
		state := "NotReady"
		for _, condition := range pod.Status.Conditions {
			if condition.Type == "Ready" && condition.Status == "True" {
				ready++
				state = "Ready"
				break
			}
		}
		states = append(states, pod.Metadata.Name+"="+state)
	}
	if len(states) == 0 {
		return 0, 0, "no live pods matched"
	}
	return ready, total, strings.Join(states, " ")
}

// waitForCollectorsGone waits for every OpenTelemetryCollector in namespace to
// be deleted.
//
// Deleting a TsugaCollectorConfig removes that object immediately, but its
// OpenTelemetryCollector children are removed by the Kubernetes garbage
// collector in the background, not by the delete call blocking on them.
// Every scenario in a tier reuses the same CR name and the same collector
// names, so without this wait a still-terminating collector from the
// previous scenario can still be running while the next scenario's config is
// applied on top of it.
//
// --ignore-not-found and kubectlStdout are both load-bearing. Without them
// an empty list still yields kubectl's "No resources found in <ns>
// namespace." on stderr, the emptiness check never fires, and this burns
// the whole timeout and then fails - on every one of Tier A's 36 scenarios,
// from DeferCleanup, where the failure is attributed to the scenario that
// just passed.
func waitForCollectorsGone(namespace string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	var last string
	for time.Now().Before(deadline) {
		out, err := kubectlStdout("get", "opentelemetrycollectors", "-n", namespace,
			"--ignore-not-found", "-o", "name")
		if err == nil {
			last = out
			if strings.TrimSpace(out) == "" {
				return nil
			}
		}
		time.Sleep(2 * time.Second)
	}
	return fmt.Errorf("collectors in %s did not drain within %s (last: %q)",
		namespace, timeout, strings.TrimSpace(last))
}

// assertPipelines checks the rendered collector config against the scenario's
// toggles, including the orphan pruning the operator performs.
//
// It fetches spec.config via `-o jsonpath-as-json`, which serializes the
// matched value as real JSON, and hands the decoded structure to
// checkAgentPipelines rather than substring-matching the raw text: the
// agent's own service.telemetry.metrics self-monitoring block contains the
// literal text "metrics" unconditionally, so a substring check reports every
// metrics-disabled scenario as broken even when pruning worked correctly.
func assertPipelines(namespace string, s CollectorScenario) {
	raw, err := kubectl("get", "opentelemetrycollector", "tsuga-agent",
		"-n", namespace, "-o", "jsonpath-as-json={.spec.config}")
	if err != nil {
		ginkgo.Fail(fmt.Sprintf("reading agent config: %v", err))
		return
	}
	var wrapped []map[string]interface{}
	if err := json.Unmarshal([]byte(raw), &wrapped); err != nil || len(wrapped) == 0 {
		ginkgo.Fail(fmt.Sprintf("decoding agent config: %v: %s", err, raw))
		return
	}
	for _, problem := range checkAgentPipelines(wrapped[0], s) {
		ginkgo.Fail(problem)
	}
}

// checkAgentPipelines inspects a decoded agent OpenTelemetryCollector config
// (the parsed .spec.config) against the scenario's toggles and returns a
// description of every mismatch, including orphan pruning. A nil/empty
// result means the config matches the toggles exactly.
func checkAgentPipelines(config map[string]interface{}, s CollectorScenario) []string {
	var problems []string
	pipelines := nestedMap(config, "service", "pipelines")

	for _, toggle := range []struct {
		pipeline string
		enabled  bool
	}{
		{"traces", s.Agent.Traces},
		{"metrics", s.Agent.Metrics},
		{"logs", s.Agent.Logs},
	} {
		_, present := pipelines[toggle.pipeline]
		if present != toggle.enabled {
			problems = append(problems, fmt.Sprintf(
				"agent %s pipeline present=%v but toggle=%v", toggle.pipeline, present, toggle.enabled))
		}
	}

	if !s.Agent.Traces {
		_, inConnectors := nestedMap(config, "connectors")["span_metrics"]
		metricsPipeline, _ := pipelines["metrics"].(map[string]interface{})
		inMetricsReceivers := sliceContainsString(metricsPipeline["receivers"], "span_metrics")
		if inConnectors || inMetricsReceivers {
			problems = append(problems,
				"span_metrics connector must be pruned when traces are disabled")
		}
	}

	if !s.Agent.Logs {
		if _, ok := nestedMap(config, "receivers")["file_log"]; ok {
			problems = append(problems, "file_log receiver must be pruned when logs are disabled")
		}
	}

	return problems
}

// nestedMap walks a chain of string keys through decoded JSON/YAML maps,
// returning nil the moment any segment is missing or not itself a map.
func nestedMap(v interface{}, path ...string) map[string]interface{} {
	m, ok := v.(map[string]interface{})
	if !ok {
		return nil
	}
	if len(path) == 0 {
		return m
	}
	return nestedMap(m[path[0]], path[1:]...)
}

// sliceContainsString reports whether a decoded JSON/YAML array contains target.
func sliceContainsString(v interface{}, target string) bool {
	list, ok := v.([]interface{})
	if !ok {
		return false
	}
	for _, item := range list {
		if s, ok := item.(string); ok && s == target {
			return true
		}
	}
	return false
}

// copyIngestionSecret duplicates the ingestion secret into another namespace,
// which the customNamespace variant needs.
func copyIngestionSecret(namespace string) error {
	if err := applyYAML(fmt.Sprintf(
		"apiVersion: v1\nkind: Namespace\nmetadata:\n  name: %s\n", namespace)); err != nil {
		return err
	}
	cmd := kubectlCommand("-n", namespace, "create", "secret", "generic",
		"tsuga-ingestion", "--from-literal=api-token="+cli.Config().IngestionKey,
		"--dry-run=client", "-o", "yaml")
	manifest, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("render ingestion secret: %w", err)
	}
	return applyYAML(string(manifest))
}

// createEndpointSecret backs the endpointSecretRef variant.
func createEndpointSecret(namespace string) error {
	cmd := kubectlCommand("-n", namespace, "create", "secret", "generic",
		"tsuga-endpoint", "--from-literal=endpoint="+cli.Config().OTLPEndpoint,
		"--dry-run=client", "-o", "yaml")
	manifest, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("render endpoint secret: %w", err)
	}
	return applyYAML(string(manifest))
}

// kustomizationPath is the manifest `make deploy` rewrites through
// `kustomize edit set image`.
const kustomizationPath = "config/manager/kustomization.yaml"

var kustomizationSnapshot []byte

// snapshotKustomization records the current contents of the manager
// kustomization so restoreKustomization can put them back.
func snapshotKustomization() error {
	root, err := repoRoot()
	if err != nil {
		return err
	}
	data, err := os.ReadFile(filepath.Join(root, kustomizationPath))
	if err != nil {
		return fmt.Errorf("snapshot %s: %w", kustomizationPath, err)
	}
	kustomizationSnapshot = data
	return nil
}

// restoreKustomization rewrites the manager kustomization with the contents
// captured before the deploy, leaving the working tree as it was found.
func restoreKustomization() {
	if kustomizationSnapshot == nil {
		return
	}
	root, err := repoRoot()
	if err != nil {
		ginkgo.GinkgoWriter.Printf("restoring %s: %v\n", kustomizationPath, err)
		return
	}
	path := filepath.Join(root, kustomizationPath)
	if err := os.WriteFile(path, kustomizationSnapshot, 0o644); err != nil {
		ginkgo.GinkgoWriter.Printf("restoring %s: %v\n", path, err)
	}
}

// writeInterval paces the suite's writes against the Tsuga API's rate limit.
//
// Measured against a real org: roughly 100 writes exhausts the budget, after
// which the API returns 429 for an extended period. The operator reacts
// correctly, requeuing every 30s (rateLimitRequeueAfter), but that cadence
// keeps spending against a window that has not drained, so a CR sits in Error
// for longer than any sensible per-resource timeout. Sleeping between writes
// keeps the suite under the budget rather than recovering from exhausting it.
//
// The interval comes from a measurement of the live API, not a guess. Polling
// a throttled org for six minutes produced 40 successes with a mean gap of 10s
// between them: the limiter is a token bucket refilling at roughly one token
// per 10s, so the sustainable rate is about 6 requests per minute.
//
// Each resource costs TWO requests — the operator's create and the suite's
// verification read — so 30s per resource runs at 4 requests per minute and
// leaves headroom under that ceiling. Tier C's 146 resources then take a little
// over an hour.
//
// Pacing only helps from a full bucket. A run that starts with an empty one is
// immediately in deficit, and the operator's 30s requeues then consume tokens
// about as fast as they appear, which is why waiting for a single successful
// call before starting is not enough. Set E2E_WRITE_INTERVAL=0s for an org that
// does not throttle.
func writeInterval() time.Duration {
	if raw := os.Getenv("E2E_WRITE_INTERVAL"); raw != "" {
		if parsed, err := time.ParseDuration(raw); err == nil {
			return parsed
		}
	}
	return 30 * time.Second
}

// paceWrite sleeps for writeInterval, holding the suite's request rate under
// the API's budget.
func paceWrite() {
	if d := writeInterval(); d > 0 {
		time.Sleep(d)
	}
}

// managerDeployment is the operator's Deployment, as the kustomize namePrefix
// renders it.
const managerDeployment = "deploy/tsuga-operator-controller-manager"

// collectionControllerRunning reports whether the operator registered its
// TsugaCollectorConfig controller.
func collectionControllerRunning() bool {
	out, err := kubectlStdout("-n", operatorNS, "logs", managerDeployment, "--tail=400")
	if err != nil {
		return false
	}
	return strings.Contains(out, `"controller":"tsugacollectorconfig"`)
}

// requireCollectionControllers makes sure the operator is reconciling
// TsugaCollectorConfig before Tier A depends on it.
//
// The operator probes for the OpenTelemetryCollector CRD once, at startup, and
// logs "OpenTelemetryCollector CRD not found; disabling TsugaCollectorConfig
// controller" if it is absent. A pod that starts before the OpenTelemetry
// Operator is serving its CRDs — after a machine restart, a node drain, an
// eviction — therefore stops reconciling collector configs for the rest of its
// life, while every CR applies cleanly and simply never gets a status. Tier A
// then waits out its timeout 36 times over for a phase nothing will ever write.
//
// A restart re-runs the probe, so one is enough when the CRDs are present by
// then. Failing loudly beats two hours of scenarios that cannot pass.
func requireCollectionControllers() error {
	if collectionControllerRunning() {
		return nil
	}
	ginkgo.GinkgoWriter.Printf(
		"TsugaCollectorConfig controller is not running; restarting the operator to re-probe for the CRD\n")

	if _, err := kubectl("-n", operatorNS, "rollout", "restart", managerDeployment); err != nil {
		return fmt.Errorf("restarting the operator: %w", err)
	}
	if _, err := kubectl("-n", operatorNS, "rollout", "status", managerDeployment, "--timeout=180s"); err != nil {
		return fmt.Errorf("waiting for the operator to roll out: %w", err)
	}

	deadline := time.Now().Add(3 * time.Minute)
	for time.Now().Before(deadline) {
		if collectionControllerRunning() {
			return nil
		}
		time.Sleep(5 * time.Second)
	}
	return fmt.Errorf(
		"the operator is not reconciling TsugaCollectorConfig even after a restart; " +
			"check that the OpenTelemetry Operator's CRDs are installed and served " +
			"(kubectl get crd opentelemetrycollectors.opentelemetry.io), since the " +
			"operator disables that controller when it cannot find them at startup")
}
