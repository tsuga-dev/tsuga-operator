package e2efull

import (
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/tsuga-dev/tsuga-operator/test/e2efull/tsugacli"
)

const (
	tierCNamespace = "e2e-tier-c"
	// crTimeout is generous on purpose, and costs nothing on the happy path:
	// waitForCRPhase and the Eventually blocks return as soon as the condition
	// holds, so this only bounds how long the suite is willing to wait while
	// the Tsuga API is throttling it.
	//
	// Measured against a real org: creating Tier C's ~150 resources exhausts
	// the API's rate limit, after which it returns 429 for minutes — read-only
	// list calls were still throttled well after a run ended. The operator
	// handles this correctly by requeuing every 30s (rateLimitRequeueAfter in
	// generic_tsuga_reconciler.go), so a CR does reach Ready, just slowly. At
	// the previous 90s this allowed only ~3 requeues and the first monitor of
	// the 120-combination cross-product failed. Ten minutes allows ~20.
	crTimeout = 10 * time.Minute
)

// mustRenderDashboard and mustRenderMonitor fail the calling spec when the
// OpenAPI spec cannot produce a body for a variant, which is a suite defect
// rather than an operator one and should say so at the line that asked for
// it. ExpectWithOffset(1, ...) attributes the failure to the caller.
func mustRenderDashboard(name, namespace, owner, visualization string) string {
	manifest, err := renderDashboard(name, namespace, owner, visualization)
	ExpectWithOffset(1, err).NotTo(HaveOccurred(),
		"rendering dashboard %s for visualization %s", name, visualization)
	return manifest
}

func mustRenderMonitor(name, namespace, owner, configType string,
	priority int, permissions string) string {

	manifest, err := renderMonitor(name, namespace, owner, configType, priority, permissions)
	ExpectWithOffset(1, err).NotTo(HaveOccurred(),
		"rendering monitor %s for configuration %s", name, configType)
	return manifest
}

func mustRenderMonitorWithDashboardRef(name, namespace, owner, dashboardName string) string {
	manifest, err := renderMonitorWithDashboardRef(name, namespace, owner, dashboardName)
	ExpectWithOffset(1, err).NotTo(HaveOccurred(), "rendering monitor %s", name)
	return manifest
}

// tierCDashboardSpecs registers Tier C's Dashboard specs; see tierASpecs for
// why the tiers are functions rather than top-level containers.
func tierCDashboardSpecs() {
	Describe("Tier C: Dashboard", Ordered, Label("tier-c"), func() {
		var owner string
		var visualizations []string

		BeforeAll(func() {
			owner = requireTeamID()
			var err error
			visualizations, err = VisualizationVariants(openAPISpecPath)
			Expect(err).NotTo(HaveOccurred())
			Expect(applyYAML(fmt.Sprintf(
				"apiVersion: v1\nkind: Namespace\nmetadata:\n  name: %s\n", tierCNamespace))).To(Succeed())
		})

		It("creates a dashboard for every visualization variant", func() {
			for i, visualization := range visualizations {
				name := fmt.Sprintf("%s-viz-%02d", runID, i)
				manifest := mustRenderDashboard(name, tierCNamespace, owner, visualization)
				paceWrite()

				By("applying " + visualization)
				Expect(applyYAML(manifest)).To(Succeed())
				Expect(waitForCRPhase("dashboard", tierCNamespace, name, "Ready", crTimeout)).
					To(Succeed(), "visualization %s", visualization)

				id, err := crStatusID("dashboard", tierCNamespace, name)
				Expect(err).NotTo(HaveOccurred())
				Expect(id).NotTo(BeEmpty(), "operator must record the Tsuga id in status")

				By("reading it back through the CLI")
				out, err := cli.Run("dashboards", "get", id)
				Expect(err).NotTo(HaveOccurred())
				gotName, err := jsonField(out, "name")
				Expect(err).NotTo(HaveOccurred())
				Expect(gotName).To(Equal(name))

				// name is identical across every variant, so it cannot prove the
				// visualization round-tripped; the type must be checked too. If the
				// API normalizes or renames the type on read, the fix is to compare
				// against its canonical value, not to drop this check.
				gotType, err := jsonField(out, "graphs", "0", "visualization", "type")
				Expect(err).NotTo(HaveOccurred())
				Expect(gotType).To(Equal(visualization), "dashboard %s: visualization type did not round-trip", name)
			}
		})

		It("propagates an update to Tsuga", func() {
			name := runID + "-update"
			Expect(applyYAML(mustRenderDashboard(name, tierCNamespace, owner, "timeseries"))).To(Succeed())
			Expect(waitForCRPhase("dashboard", tierCNamespace, name, "Ready", crTimeout)).To(Succeed())
			id, err := crStatusID("dashboard", tierCNamespace, name)
			Expect(err).NotTo(HaveOccurred())

			updated := mustRenderDashboard(name, tierCNamespace, owner, "table")
			Expect(applyYAML(updated)).To(Succeed())

			Eventually(func() (string, error) {
				out, err := cli.Run("dashboards", "get", id)
				if err != nil {
					return "", err
				}
				return jsonField(out, "graphs", "0", "visualization", "type")
			}, crTimeout, signalPollInterval).Should(Equal("table"))
		})

		It("deletes the Tsuga dashboard when the CR is deleted", func() {
			name := runID + "-delete"
			Expect(applyYAML(mustRenderDashboard(name, tierCNamespace, owner, "timeseries"))).To(Succeed())
			Expect(waitForCRPhase("dashboard", tierCNamespace, name, "Ready", crTimeout)).To(Succeed())
			id, err := crStatusID("dashboard", tierCNamespace, name)
			Expect(err).NotTo(HaveOccurred())

			Expect(deleteYAML(mustRenderDashboard(name, tierCNamespace, owner, "timeseries"))).To(Succeed())

			// Asserting only that the get failed is not proof of deletion:
			// Client.Run errors on a 429, a 500, a network blip or an
			// expired token just as readily, so this assertion could pass
			// with the dashboard still live. Only a not-found counts.
			Eventually(func() error {
				_, err := cli.Run("dashboards", "get", id)
				if err == nil {
					return fmt.Errorf("dashboard %s is still readable from Tsuga", id)
				}
				if !tsugacli.IsNotFound(err) {
					return fmt.Errorf("want a not-found for dashboard %s, got a different failure: %w", id, err)
				}
				return nil
			}, crTimeout, signalPollInterval).Should(Succeed(),
				"the dashboard should be gone from Tsuga after the CR is deleted")
		})
	})
}
