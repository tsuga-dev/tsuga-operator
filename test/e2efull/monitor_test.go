package e2efull

import (
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/tsuga-dev/tsuga-operator/test/e2efull/tsugacli"
)

// tierCMonitorSpecs registers Tier C's Monitor specs; see tierASpecs for why
// the tiers are functions rather than top-level containers.
func tierCMonitorSpecs() {
	Describe("Tier C: Monitor", Ordered, Label("tier-c"), func() {
		var owner string
		var configurations []string

		BeforeAll(func() {
			owner = requireTeamID()
			var err error
			configurations, err = MonitorConfigurationVariants(openAPISpecPath)
			Expect(err).NotTo(HaveOccurred())
			Expect(applyYAML(fmt.Sprintf(
				"apiVersion: v1\nkind: Namespace\nmetadata:\n  name: %s\n", tierCNamespace))).To(Succeed())
		})

		It("cross-products configuration, priority and permissions", func() {
			index := 0
			for _, configType := range configurations {
				for priority := 1; priority <= 5; priority++ {
					for _, permissions := range []string{
						"all", "owning-team-and-public", "owning-team-only",
					} {
						name := fmt.Sprintf("%s-mon-%03d", runID, index)
						index++
						paceWrite()

						manifest := mustRenderMonitor(name, tierCNamespace, owner,
							configType, priority, permissions)
						Expect(applyYAML(manifest)).To(Succeed(),
							"config=%s priority=%d permissions=%s", configType, priority, permissions)
						Expect(waitForCRPhase("monitor", tierCNamespace, name, "Ready", crTimeout)).
							To(Succeed(), "config=%s priority=%d permissions=%s",
								configType, priority, permissions)

						id, err := crStatusID("monitor", tierCNamespace, name)
						Expect(err).NotTo(HaveOccurred(),
							"config=%s priority=%d permissions=%s", configType, priority, permissions)
						Expect(id).NotTo(BeEmpty(),
							"config=%s priority=%d permissions=%s", configType, priority, permissions)

						out, err := cli.Run("monitors", "get", id)
						Expect(err).NotTo(HaveOccurred(),
							"config=%s priority=%d permissions=%s", configType, priority, permissions)
						gotName, err := jsonField(out, "name")
						Expect(err).NotTo(HaveOccurred(),
							"config=%s priority=%d permissions=%s", configType, priority, permissions)
						Expect(gotName).To(Equal(name),
							"config=%s priority=%d permissions=%s", configType, priority, permissions)
					}
				}
			}
			Expect(index).To(Equal(len(configurations)*5*3),
				"the cross-product must cover every combination")
		})

		It("resolves a dashboardRef created after the monitor", func() {
			dashboardName := runID + "-ref-target"
			monitorName := runID + "-ref-monitor"

			By("creating the monitor first, so the reference cannot resolve yet")
			Expect(applyYAML(mustRenderMonitorWithDashboardRef(
				monitorName, tierCNamespace, owner, dashboardName))).To(Succeed())

			By("creating the dashboard it points at")
			Expect(applyYAML(mustRenderDashboard(
				dashboardName, tierCNamespace, owner, "timeseries"))).To(Succeed())
			Expect(waitForCRPhase("dashboard", tierCNamespace, dashboardName, "Ready", crTimeout)).
				To(Succeed())

			By("waiting for the monitor to retry and resolve")
			Expect(waitForCRPhase("monitor", tierCNamespace, monitorName, "Ready", crTimeout)).
				To(Succeed())

			dashboardID, err := crStatusID("dashboard", tierCNamespace, dashboardName)
			Expect(err).NotTo(HaveOccurred())
			monitorID, err := crStatusID("monitor", tierCNamespace, monitorName)
			Expect(err).NotTo(HaveOccurred())

			out, err := cli.Run("monitors", "get", monitorID)
			Expect(err).NotTo(HaveOccurred())
			linked, err := jsonField(out, "dashboardId")
			Expect(err).NotTo(HaveOccurred())
			Expect(linked).To(Equal(dashboardID))
		})

		It("deletes the Tsuga monitor when the CR is deleted", func() {
			name := runID + "-delete"
			Expect(applyYAML(mustRenderMonitor(
				name, tierCNamespace, owner, "metric", 3, "all"))).To(Succeed())
			Expect(waitForCRPhase("monitor", tierCNamespace, name, "Ready", crTimeout)).To(Succeed())
			id, err := crStatusID("monitor", tierCNamespace, name)
			Expect(err).NotTo(HaveOccurred())

			Expect(deleteYAML(mustRenderMonitor(
				name, tierCNamespace, owner, "metric", 3, "all"))).To(Succeed())

			// See the dashboard delete spec: any CLI failure would satisfy
			// HaveOccurred, including a 429 or an expired token, so this
			// requires the failure to be a not-found.
			Eventually(func() error {
				_, err := cli.Run("monitors", "get", id)
				if err == nil {
					return fmt.Errorf("monitor %s is still readable from Tsuga", id)
				}
				if !tsugacli.IsNotFound(err) {
					return fmt.Errorf("want a not-found for monitor %s, got a different failure: %w", id, err)
				}
				return nil
			}, crTimeout, 3*time.Second).Should(Succeed(),
				"the monitor should be gone from Tsuga after the CR is deleted")
		})

		It("tolerates deleting a monitor already absent from Tsuga", func() {
			name := runID + "-double-delete"
			Expect(applyYAML(mustRenderMonitor(
				name, tierCNamespace, owner, "metric", 3, "all"))).To(Succeed())
			Expect(waitForCRPhase("monitor", tierCNamespace, name, "Ready", crTimeout)).To(Succeed())
			id, err := crStatusID("monitor", tierCNamespace, name)
			Expect(err).NotTo(HaveOccurred())

			By("deleting it out from under the operator")
			_, err = cli.Run("monitors", "delete", id)
			Expect(err).NotTo(HaveOccurred())

			By("deleting the CR, which must still finalize")
			Expect(deleteYAML(mustRenderMonitor(
				name, tierCNamespace, owner, "metric", 3, "all"))).To(Succeed())
			Eventually(func() string {
				out, _ := kubectlStdout("get", "monitor", name, "-n", tierCNamespace,
					"--ignore-not-found", "-o", "name")
				return out
			}, crTimeout, 3*time.Second).Should(BeEmpty(),
				"a 404 on delete must not wedge the finalizer")
		})
	})
}
