package e2efull

import (
	"encoding/json"
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
	// Once the API's rate limit is exhausted it returns 429 for minutes. The
	// operator requeues every 30s (rateLimitRequeueAfter in
	// generic_tsuga_reconciler.go), so a CR does reach Ready, just slowly.
	// Ten minutes allows ~20 requeues.
	crTimeout = 10 * time.Minute
)

// mustRenderDashboard and mustRenderMonitor fail the calling spec when the
// OpenAPI spec cannot produce a body for a variant, which is a suite defect
// rather than an operator one and should say so at the line that asked for
// it. ExpectWithOffset(1, ...) attributes the failure to the caller.
func mustRenderDashboard(name, namespace, owner, visualization string) string {
	manifest, err := renderDashboardWithGraphs(name, namespace, owner, []string{visualization})
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

// eventuallyGoneFromTsuga waits until `tsuga <kind> get <id>` reports a
// not-found. A failed get alone is not proof of deletion: Client.Run errors on
// a 429, a 500, a network blip or an expired token just as readily.
func eventuallyGoneFromTsuga(kind, id string) {
	EventuallyWithOffset(1, func() error {
		_, err := cli.Run(kind, "get", id)
		if err == nil {
			return fmt.Errorf("%s %s is still readable from Tsuga", kind, id)
		}
		if !tsugacli.IsNotFound(err) {
			return fmt.Errorf("want a not-found for %s %s, got a different failure: %w", kind, id, err)
		}
		return nil
	}, crTimeout, signalPollInterval).Should(Succeed(),
		"%s %s should be gone from Tsuga after the CR is deleted", kind, id)
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
			Expect(applyNamespace(tierCNamespace)).To(Succeed())
		})

		It("creates one dashboard carrying every visualization variant", func() {
			name := runID + "-viz"
			manifest, err := renderDashboardWithGraphs(name, tierCNamespace, owner, visualizations)
			Expect(err).NotTo(HaveOccurred())
			paceWrite()
			Expect(applyYAML(manifest)).To(Succeed())
			Expect(waitForCRPhase("dashboard", tierCNamespace, name, "Ready", crTimeout)).To(Succeed())

			id, err := crStatusID("dashboard", tierCNamespace, name)
			Expect(err).NotTo(HaveOccurred())
			Expect(id).NotTo(BeEmpty(), "operator must record the Tsuga id in status")

			By("reading it back through the CLI")
			out, err := cli.Run("dashboards", "get", id)
			Expect(err).NotTo(HaveOccurred())
			var got struct {
				Name       string `json:"name"`
				TimePreset string `json:"timePreset"`
				Filters    []struct {
					Key    string   `json:"key"`
					Values []string `json:"values"`
				} `json:"filters"`
				Graphs []struct {
					Visualization struct {
						Type string `json:"type"`
					} `json:"visualization"`
					Layout *struct {
						W float64 `json:"w"`
						H float64 `json:"h"`
					} `json:"layout"`
				} `json:"graphs"`
			}
			Expect(json.Unmarshal(out, &got)).To(Succeed(), string(out))
			Expect(got.Name).To(Equal(name))
			Expect(got.TimePreset).To(Equal(dashboardTimePreset), "timePreset did not round-trip")
			Expect(got.Filters).To(HaveLen(1), "filters did not round-trip: %s", out)
			Expect(got.Filters[0].Key).To(Equal("service"))
			Expect(got.Filters[0].Values).To(Equal([]string{"e2e"}))

			// If the API normalizes or renames a type on read, the fix is to
			// compare against its canonical value, not to drop this check.
			Expect(got.Graphs).To(HaveLen(len(visualizations)))
			for i, visualization := range visualizations {
				graph := got.Graphs[i]
				Expect(graph.Visualization.Type).To(Equal(visualization),
					"graph %d: visualization type did not round-trip", i)
				Expect(graph.Layout).NotTo(BeNil(), "graph %d (%s): layout did not round-trip", i, visualization)
				Expect(graph.Layout.W).To(BeEquivalentTo(dashboardGraphWidth), "graph %d (%s)", i, visualization)
				Expect(graph.Layout.H).To(BeEquivalentTo(dashboardGraphHeight), "graph %d (%s)", i, visualization)
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

			eventuallyGoneFromTsuga("dashboards", id)
		})
	})
}
