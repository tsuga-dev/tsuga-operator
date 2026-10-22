package e2efull

import (
	"encoding/json"
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
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
			Expect(applyNamespace(tierCNamespace)).To(Succeed())
		})

		It("rotates every configuration through every priority and permissions level", func() {
			levels := []string{"all", "owning-team-and-public", "owning-team-only"}
			for i, config := range configurations {
				priority := 1 + i%5
				permissions := levels[i%len(levels)]
				name := fmt.Sprintf("%s-mon-%02d", runID, i)
				label := fmt.Sprintf("config=%s priority=%d permissions=%s",
					config, priority, permissions)
				paceWrite()

				manifest := mustRenderMonitor(name, tierCNamespace, owner,
					config, priority, permissions)
				Expect(applyYAML(manifest)).To(Succeed(), label)
				Expect(waitForCRPhase("monitor", tierCNamespace, name, "Ready", crTimeout)).
					To(Succeed(), label)

				id, err := crStatusID("monitor", tierCNamespace, name)
				Expect(err).NotTo(HaveOccurred(), label)
				Expect(id).NotTo(BeEmpty(), label)

				out, err := cli.Run("monitors", "get", id)
				Expect(err).NotTo(HaveOccurred(), label)
				var got struct {
					Name        string `json:"name"`
					Priority    int    `json:"priority"`
					Permissions string `json:"permissions"`
				}
				Expect(json.Unmarshal(out, &got)).To(Succeed(), "%s: %s", label, out)
				Expect(got.Name).To(Equal(name), label)
				Expect(got.Priority).To(Equal(priority), "%s: %s", label, out)
				Expect(got.Permissions).To(Equal(permissions), "%s: %s", label, out)
			}
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

			eventuallyGoneFromTsuga("monitors", id)
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
