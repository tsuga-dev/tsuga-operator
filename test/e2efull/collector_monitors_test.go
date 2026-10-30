package e2efull

import (
	"encoding/json"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// tierCCollectorMonitorSpecs applies the shipped collector-monitors.yaml, so a
// configuration the Tsuga API rejects fails here rather than for a user.
func tierCCollectorMonitorSpecs() {
	Describe("Tier C: collector monitors", Ordered, Label("tier-c"), func() {
		var monitors []collectorMonitor

		BeforeAll(func() {
			Expect(applyNamespace(tierCNamespace)).To(Succeed())
			var err error
			monitors, err = renderCollectorMonitors(tierCNamespace, requireTeamID())
			Expect(err).NotTo(HaveOccurred())
		})

		AfterAll(func() {
			for _, m := range monitors {
				_ = deleteYAML(m.manifest)
			}
		})

		It("syncs every example monitor to Tsuga", func() {
			for _, m := range monitors {
				paceWrite()
				Expect(applyYAML(m.manifest)).To(Succeed(), m.name)
			}
			for _, m := range monitors {
				Expect(waitForCRPhase("monitor", tierCNamespace, m.name, "Ready", crTimeout)).
					To(Succeed(), m.name)
				id, err := crStatusID("monitor", tierCNamespace, m.name)
				Expect(err).NotTo(HaveOccurred(), m.name)
				Expect(id).NotTo(BeEmpty(), m.name)

				out, err := cli.Run("monitors", "get", id)
				Expect(err).NotTo(HaveOccurred(), m.name)
				var got struct {
					Priority int `json:"priority"`
					Tags     []struct {
						Key   string `json:"key"`
						Value string `json:"value"`
					} `json:"tags"`
				}
				Expect(json.Unmarshal(out, &got)).To(Succeed(), "%s: %s", m.name, out)
				Expect(got.Priority).To(Equal(m.priority), m.name)
				Expect(got.Tags).To(ContainElement(HaveField("Key", "source")), m.name)
			}
		})
	})
}
