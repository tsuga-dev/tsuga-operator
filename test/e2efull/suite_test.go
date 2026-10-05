package e2efull

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/tsuga-dev/tsuga-operator/test/e2efull/tsugacli"
)

const (
	// The tag must not be "latest". config/manager/manager.yaml sets no
	// imagePullPolicy, and Kubernetes defaults it to Always for a :latest tag —
	// so the kubelet would try to pull this from a registry and the operator
	// would sit in ImagePullBackOff, since the image only exists side-loaded
	// into the kind node. Any other tag defaults to IfNotPresent and resolves
	// locally. The repo's own `make kind-deploy` avoids this the same way, with
	// KIND_IMG ?= tsuga-operator:kind.
	operatorImage = "tsuga-e2e/tsuga-operator:e2e"
	minimumCLI    = "1.3.0"
)

// cli and runID are declared in cluster.go; this file only assigns them.

func TestE2EFull(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "tsuga-operator full e2e suite")
}

var _ = BeforeSuite(func() {
	By("reading credentials from the environment")
	cfg, err := tsugacli.ConfigFromEnv()
	Expect(err).NotTo(HaveOccurred())

	cli = tsugacli.New(cfg, GinkgoWriter)
	// runID is initialized eagerly at package-variable-initialization time
	// (see cluster.go), before Ginkgo builds the spec tree, so it is only
	// logged here rather than assigned.
	GinkgoWriter.Printf("run id: %s\n", runID)

	By("checking the tsuga CLI version")
	Expect(cli.CheckVersion(minimumCLI)).To(Succeed())

	By("creating the kind cluster")
	Expect(ensureKindCluster()).To(Succeed())

	// utils.Run is deliberately not used for any of the commands below. It
	// sets cmd.Dir from utils.GetProjectDir, which resolves the repo root by
	// deleting the substring "/test/e2e" from the working directory - from
	// test/e2efull that cuts characters out of the middle of the path and
	// produces a directory that does not exist, so every command fails
	// before it starts. runMake and runAtRepoRoot (cluster.go) walk up to
	// go.mod instead. test/utils keeps its current behaviour because the
	// existing smoke suite in test/e2e depends on it.
	By("installing cert-manager and the OpenTelemetry Operator")
	_, err = runMake("install-otel-prereqs", "KUBECTL=kubectl --context "+kubeContext())
	Expect(err).NotTo(HaveOccurred())

	By("building and loading the operator image")
	_, err = runMake("docker-build", "IMG="+operatorImage)
	Expect(err).NotTo(HaveOccurred())
	_, err = runAtRepoRoot("kind", "load", "docker-image",
		operatorImage, "--name", KindClusterName)
	Expect(err).NotTo(HaveOccurred())

	By("building and loading the fixture images")
	_, err = runMake("e2e-fixtures")
	Expect(err).NotTo(HaveOccurred())
	for _, image := range []string{
		"tsuga-e2e/otlp-emitter:latest", "tsuga-e2e/app-java:latest",
		"tsuga-e2e/app-nodejs:latest", "tsuga-e2e/app-python:latest",
		"tsuga-e2e/app-dotnet:latest",
	} {
		_, err = runAtRepoRoot("kind", "load", "docker-image",
			image, "--name", KindClusterName)
		Expect(err).NotTo(HaveOccurred(), "loading %s", image)
	}

	By("creating the Tsuga credential secrets")
	Expect(createCredentialSecrets(cfg)).To(Succeed())

	By("deploying the operator")
	// `make deploy` runs `kustomize edit set image`, which rewrites the
	// checked-in config/manager/kustomization.yaml in place to point at the
	// image it is given. The snapshot and restore keep the operator's real
	// deploy manifest free of this suite's test image.
	Expect(snapshotKustomization()).To(Succeed())
	DeferCleanup(restoreKustomization)

	_, err = runMake("deploy",
		"IMG="+operatorImage,
		"OPERATOR_NAMESPACE="+operatorNS,
		"TSUGA_BASE_URL="+cfg.BaseURL,
		"KUBECTL=kubectl --context "+kubeContext())
	Expect(err).NotTo(HaveOccurred())

	_, err = kubectl("wait", "--for=condition=Available",
		"-n", operatorNS, "deploy", "--all", "--timeout=180s")
	Expect(err).NotTo(HaveOccurred())

	By("confirming the operator is reconciling TsugaCollectorConfig")
	Expect(requireCollectionControllers()).To(Succeed())
})

var _ = AfterSuite(func() {
	if cli != nil && runID != "" {
		By("sweeping Tsuga resources created by this run")
		sweepTsugaResources(runID)
	}
	if keepCluster() {
		GinkgoWriter.Printf(
			"E2E_KEEP_CLUSTER set, leaving kind cluster %s in place\n", KindClusterName)
		return
	}
	By("deleting the kind cluster")
	_, _ = runAtRepoRoot("kind", "delete", "cluster", "--name", KindClusterName)
})

// The suite's single top-level container.
//
// Ginkgo randomizes the order of top-level containers by default, so four
// sibling Describes would run in seed order - and the README's promise that
// Tier C runs first, so a bad token or a broken org fails the run in ten
// minutes rather than ninety, would be true only by luck. Specs inside an
// Ordered container run in the order they are registered, so nesting every
// tier here is what makes that promise real.
//
// ContinueOnFailure keeps the outer container from turning one failure into
// a skip of everything after it: a Tier C failure must not silently discard
// Tier A and Tier B. Each tier is itself Ordered and keeps its own fail-fast
// behaviour internally, which is what the tiers' shared baseline needs.
var _ = Describe("tsuga-operator", Ordered, ContinueOnFailure, func() {
	tierCDashboardSpecs()
	tierCMonitorSpecs()
	tierASpecs()
	tierBSpecs()
})

var _ = AfterEach(func() {
	if CurrentSpecReport().Failed() {
		dumpDiagnostics(CurrentSpecReport().FullText())
	}
})
