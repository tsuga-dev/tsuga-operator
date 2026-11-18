package e2efull

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// folderKind is one folder CRD and the asset kind filed in it.
type folderKind struct {
	folder, asset         string // CR kinds
	cliFolders, cliAssets string // tsuga CLI resources
}

var folderKinds = []folderKind{
	{folder: "MonitorFolder", asset: "Monitor", cliFolders: "monitor-folders", cliAssets: "monitors"},
	{folder: "DashboardFolder", asset: "Dashboard", cliFolders: "dashboard-folders", cliAssets: "dashboards"},
}

// renderFolder produces a folder CR, nested under parent when parent is set.
func renderFolder(kind, name, namespace, owner, parent string, annotations map[string]string) string {
	var meta, extra strings.Builder
	if len(annotations) > 0 {
		meta.WriteString("  annotations:\n")
		for k, v := range annotations {
			fmt.Fprintf(&meta, "    %s: %q\n", k, v)
		}
	}
	if parent != "" {
		fmt.Fprintf(&extra, "  parentFolderRef:\n    name: %s\n", parent)
	}
	return fmt.Sprintf(`apiVersion: observability.tsuga.com/v1alpha1
kind: %s
metadata:
  name: %s
  namespace: %s
%sspec:
  name: %s
  owner: %q
%s  tags:
    - key: e2e-run
      value: %s
`, kind, name, namespace, meta.String(), name, owner, extra.String(), runID)
}

// renderFiledAsset produces a Monitor or Dashboard filed in folder by folderRef.
func renderFiledAsset(kind, name, namespace, owner, folder string) string {
	var manifest string
	if kind == "Monitor" {
		manifest = mustRenderMonitor(name, namespace, owner, "metric", 3, "all")
	} else {
		manifest = mustRenderDashboard(name, namespace, owner, "timeseries")
	}
	return strings.Replace(manifest, "\nspec:\n", fmt.Sprintf("\nspec:\n  folderRef:\n    name: %s\n", folder), 1)
}

func crStatusMessage(kind, namespace, name string) (string, error) {
	return kubectl(crArgs(kind, namespace, name, "-o", "jsonpath={.status.message}")...)
}

// deleteCRNoWait starts deleting a CR without waiting for its finalizer.
func deleteCRNoWait(kind, namespace, name string) {
	_, err := kubectl("delete", kind, name, "-n", namespace, "--wait=false")
	ExpectWithOffset(1, err).NotTo(HaveOccurred())
}

// eventuallyCRGone waits until kubectl cannot find the CR.
func eventuallyCRGone(kind, namespace, name string) {
	EventuallyWithOffset(1, func() string {
		out, _ := kubectlStdout("get", kind, name, "-n", namespace, "--ignore-not-found", "-o", "name")
		return out
	}, crTimeout, 3*time.Second).Should(BeEmpty(), "%s/%s should be gone from the cluster", kind, name)
}

// tierCFolderSpecs registers Tier C's folder specs; see tierASpecs for why
// the tiers are functions rather than top-level containers.
func tierCFolderSpecs() {
	Describe("Tier C: folders", Ordered, Label("tier-c"), func() {
		var owner string

		BeforeAll(func() {
			owner = requireTeamID()
			Expect(applyNamespace(tierCNamespace)).To(Succeed())
		})

		for _, k := range folderKinds {
			parent := strings.ToLower(runID + "-" + k.folder + "-parent")
			child := strings.ToLower(runID + "-" + k.folder + "-child")
			asset := strings.ToLower(runID + "-" + k.asset + "-filed")
			var parentID, childID string

			It("files a "+k.asset+" in a nested "+k.folder, func() {
				paceWrite()
				Expect(applyYAML(renderFolder(k.folder, parent, tierCNamespace, owner, "", nil))).To(Succeed())
				Expect(applyYAML(renderFolder(k.folder, child, tierCNamespace, owner, parent, nil))).To(Succeed())
				Expect(applyYAML(renderFiledAsset(k.asset, asset, tierCNamespace, owner, child))).To(Succeed())
				for _, cr := range []struct{ kind, name string }{{k.folder, parent}, {k.folder, child}, {k.asset, asset}} {
					Expect(waitForCRPhase(cr.kind, tierCNamespace, cr.name, "Ready", crTimeout)).To(Succeed())
				}

				var err error
				parentID, err = crStatusID(k.folder, tierCNamespace, parent)
				Expect(err).NotTo(HaveOccurred())
				childID, err = crStatusID(k.folder, tierCNamespace, child)
				Expect(err).NotTo(HaveOccurred())
				assetID, err := crStatusID(k.asset, tierCNamespace, asset)
				Expect(err).NotTo(HaveOccurred())

				By("reading the child folder's parent back through the CLI")
				out, err := cli.Run(k.cliFolders, "get", childID)
				Expect(err).NotTo(HaveOccurred())
				Expect(jsonField(out, "parentFolderId")).To(Equal(parentID), string(out))

				By("reading the asset's folder back through the CLI")
				out, err = cli.Run(k.cliAssets, "get", assetID)
				Expect(err).NotTo(HaveOccurred())
				Expect(jsonField(out, "folderId")).To(Equal(childID), string(out))
			})

			It("keeps the "+k.folder+"s until their contents are gone", func() {
				Expect(parentID).NotTo(BeEmpty(), "the previous spec must have created the folders")

				By("deleting both folder CRs while the asset is still filed")
				deleteCRNoWait(k.folder, tierCNamespace, child)
				deleteCRNoWait(k.folder, tierCNamespace, parent)
				for _, name := range []string{child, parent} {
					Expect(waitForCRPhase(k.folder, tierCNamespace, name, "Deleting", crTimeout)).To(Succeed())
					Expect(crStatusMessage(k.folder, tierCNamespace, name)).To(ContainSubstring("not empty"))
				}
				_, err := cli.Run(k.cliFolders, "get", parentID)
				Expect(err).NotTo(HaveOccurred(), "a non-empty folder must stay in Tsuga")

				By("deleting the asset, after which both folders go")
				Expect(deleteYAML(renderFiledAsset(k.asset, asset, tierCNamespace, owner, child))).To(Succeed())
				eventuallyGoneFromTsuga(k.cliFolders, childID)
				eventuallyGoneFromTsuga(k.cliFolders, parentID)
				eventuallyCRGone(k.folder, tierCNamespace, parent)
			})
		}

		It("leaves a non-empty folder in Tsuga under orphan-if-not-empty", func() {
			name := runID + "-orphan"
			paceWrite()
			Expect(applyYAML(renderFolder("MonitorFolder", name, tierCNamespace, owner, "",
				map[string]string{"observability.tsuga.com/orphan-if-not-empty": "true"}))).To(Succeed())
			Expect(waitForCRPhase("MonitorFolder", tierCNamespace, name, "Ready", crTimeout)).To(Succeed())
			folderID, err := crStatusID("MonitorFolder", tierCNamespace, name)
			Expect(err).NotTo(HaveOccurred())

			By("filling the folder from outside Kubernetes")
			body, err := json.Marshal(map[string]string{
				"name": runID + "-outside", "owner": owner, "parentFolderId": folderID,
			})
			Expect(err).NotTo(HaveOccurred())
			out, err := cli.Run("monitor-folders", "create", "-d", string(body))
			Expect(err).NotTo(HaveOccurred())
			outsideID, err := jsonField(out, "id")
			Expect(err).NotTo(HaveOccurred())
			DeferCleanup(func() {
				_, _ = cli.Run("monitor-folders", "delete", outsideID)
				_, _ = cli.Run("monitor-folders", "delete", folderID)
			})

			By("deleting the CR, which goes while the folder stays")
			deleteCRNoWait("MonitorFolder", tierCNamespace, name)
			eventuallyCRGone("MonitorFolder", tierCNamespace, name)
			out, err = cli.Run("monitor-folders", "get", folderID)
			Expect(err).NotTo(HaveOccurred(), "the orphaned folder must stay in Tsuga")
			Expect(jsonField(out, "name")).To(Equal(name))
		})
	})
}
