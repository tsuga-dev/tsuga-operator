package e2efull

import (
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

const postgresNamespace = "e2e-pg"

// postgresBackendsMetric is confirmed live with `tsuga metrics list`. The sqlquery
// receiver emits it with db.namespace, not postgresql.database.name.
const postgresBackendsMetric = "postgresql.backends"

// tierPostgresSpecs registers the Postgres spec; see tierASpecs for why tiers
// are functions rather than top-level containers.
func tierPostgresSpecs() {
	Describe("Postgres monitoring", Ordered, Label("postgres"), func() {
		var pgCluster string

		BeforeAll(func() {
			By("applying an all-on collector config for the Postgres collector to forward through")
			baseline := CollectorScenarios(runID)[0]
			baseline.ClusterName = runID + "-pg"
			Expect(applyYAML(renderCollectorConfig(baseline, cli.Config().OTLPEndpoint, operatorNS))).To(Succeed())
			DeferCleanup(func() {
				_, _ = kubectl("delete", "tsugacollectorconfig", collectorConfigName,
					"--ignore-not-found", "--timeout=60s")
				_ = waitForCollectorsGone(operatorNS, 90*time.Second)
			})
			Expect(waitForCRPhase("tsugacollectorconfig", "", collectorConfigName,
				"Ready", collectorTimeout)).To(Succeed())
			Expect(waitForCollectorReady(operatorNS, "tsuga-agent", collectorTimeout)).To(Succeed())
			pgCluster = baseline.ClusterName

			By("deploying Postgres and its TsugaPostgresMonitoring")
			Expect(applyYAML(renderPostgresFixture(postgresNamespace))).To(Succeed())
			DeferCleanup(func() {
				_, _ = kubectl("delete", "namespace", postgresNamespace, "--ignore-not-found", "--timeout=120s")
			})
			Expect(applyYAML(renderPostgresMonitoring(postgresNamespace))).To(Succeed())
			Expect(waitForCRPhase("tsugapostgresmonitoring", postgresNamespace, "e2e-pg",
				"Ready", crTimeout)).To(Succeed())
		})

		It("delivers Postgres metrics to Tsuga", func() {
			Eventually(func() (int, error) {
				return cli.CountMetricSeriesWhere(postgresBackendsMetric, pgCluster,
					"db.namespace:*", "-15m")
			}, ingestTimeout(), signalPollInterval).Should(BeNumerically(">", 0),
				fmt.Sprintf("no %s series for cluster %s reached Tsuga", postgresBackendsMetric, pgCluster))
		})
	})
}
