package e2efull

import (
	"encoding/json"
	"fmt"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

const sloNamespace = "e2e-slo"

// tierSLOSpecs registers the SLO specs; see tierASpecs for why tiers are
// functions rather than top-level containers.
func tierSLOSpecs() {
	Describe("SLO", Ordered, Label("slo"), func() {
		var owner string

		BeforeAll(func() {
			owner = requireTeamID()
			Expect(applyNamespace(sloNamespace)).To(Succeed())
		})

		name := runID + "-slo"
		var id string

		It("creates an SLO in Tsuga", func() {
			paceWrite()
			Expect(applyYAML(renderSLO(name, sloNamespace, owner, 99.5))).To(Succeed())
			Expect(waitForCRPhase("slo", sloNamespace, name, "Ready", crTimeout)).To(Succeed())
			var err error
			id, err = crStatusID("slo", sloNamespace, name)
			Expect(err).NotTo(HaveOccurred())
			Expect(id).NotTo(BeEmpty(), "operator must record the Tsuga id in status")

			out, err := cli.Run("slos", "get", id)
			Expect(err).NotTo(HaveOccurred())
			var got struct {
				Name          string  `json:"name"`
				Target        float64 `json:"target"`
				TimeframeDays int     `json:"timeframeDays"`
				Tags          []struct {
					Key   string `json:"key"`
					Value string `json:"value"`
				} `json:"tags"`
			}
			Expect(json.Unmarshal(out, &got)).To(Succeed(), string(out))
			Expect(got.Name).To(Equal(name))
			Expect(got.Target).To(Equal(99.5))
			Expect(got.TimeframeDays).To(Equal(7))
			Expect(got.Tags).To(ContainElement(SatisfyAll(HaveField("Key", "e2e-run"), HaveField("Value", runID))),
				"the e2e-run tag did not reach Tsuga: %s", out)
		})

		It("propagates a target update to Tsuga", func() {
			paceWrite()
			Expect(applyYAML(renderSLO(name, sloNamespace, owner, 99.0))).To(Succeed())

			Eventually(func() (float64, error) {
				out, err := cli.Run("slos", "get", id)
				if err != nil {
					return 0, err
				}
				var got struct {
					Target float64 `json:"target"`
				}
				if err := json.Unmarshal(out, &got); err != nil {
					return 0, fmt.Errorf("decoding slos get: %w: %s", err, out)
				}
				return got.Target, nil
			}, crTimeout, signalPollInterval).Should(Equal(99.0))
		})

		It("deletes the Tsuga SLO when the CR is deleted", func() {
			paceWrite()
			Expect(deleteYAML(renderSLO(name, sloNamespace, owner, 99.0))).To(Succeed())
			eventuallyGoneFromTsuga("slos", id)
		})
	})
}
