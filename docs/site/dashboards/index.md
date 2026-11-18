# Dashboards

**Prerequisite: [operator installed](../getting-started/index.md)**

This walkthrough creates a dashboard in Tsuga and manages it through a Kubernetes resource. You need a Tsuga team ID for `spec.owner`, or the team's name for `spec.ownerName`, and permission for your operation key to manage that team's dashboards.

## 1. Save the manifest

Download [dashboard.yaml](../examples/dashboard.yaml), or save the following as `dashboard.yaml`. Replace `<YOUR_TEAM_ID>` with your team's ID, or replace the `owner` line with `ownerName: <YOUR_TEAM_NAME>` to have the operator look the ID up by name. The sample metric must exist in your Tsuga account for the chart to show data.

```yaml
--8<-- "examples/dashboard.yaml"
```

`metadata.name` identifies the Kubernetes resource. `spec.name` is the dashboard title in Tsuga. Widget IDs must be present and unique within the dashboard.

## 2. Apply and verify

```sh
kubectl apply -f dashboard.yaml
kubectl -n default wait --for=jsonpath='{.status.phase}'=Ready dashboard/example-dashboard --timeout=120s
kubectl -n default get dashboard example-dashboard
kubectl -n default get dashboard example-dashboard -o jsonpath='{.status.id}{"\n"}'
```

A successful reconcile sets `status.phase: Ready` and a remote `status.id`. Open Tsuga and find the dashboard by its title. An empty chart means the dashboard exists but its query has no matching data; it does not necessarily indicate an operator error.

## 3. Make a change

Edit `spec.name`, save the manifest, and apply it again:

```sh
kubectl apply -f dashboard.yaml
kubectl -n default get dashboard example-dashboard -o yaml
```

Wait until `status.observedGeneration` matches `metadata.generation` and the phase is `Ready`. The existing remote dashboard should have the new title and the same ID.

## File the dashboard in a folder

Declare folders as `DashboardFolder` resources. A folder can sit inside one top-level folder through `parentFolderRef` (or `parentFolderId` for a folder created in Tsuga); Tsuga allows two levels at most and rejects a third with an `Error` phase.

```yaml
--8<-- "examples/dashboard-folders.yaml"
```

Then add `folderRef: {name: example-dashboard-subfolder}` to the Dashboard, or `folderId: "<YOUR_DASHBOARD_FOLDER_ID>"` for a folder created in Tsuga. If both are present, `folderId` takes precedence. A referenced folder must have a remote ID before the Dashboard can sync, and the Dashboard is pushed again when that folder is recreated with a new ID.

Without `folderId` or `folderRef`, the operator leaves the dashboard's folder unchanged, so a folder chosen in the Tsuga UI is kept. Removing either field does not unfile the dashboard: move it in Tsuga instead. The same holds for a folder's parent.

Deleting a `DashboardFolder` deletes the Tsuga folder only once it is empty. Until its dashboards and child folders are gone, the resource stays in `Deleting`, `status.message` shows the reason Tsuga gave, and the operator retries every 30 seconds. Deleting a folder never deletes or moves the dashboards in it.

If the folder holds dashboards filed outside Kubernetes (in the Tsuga UI, or from another namespace or cluster), it never empties. Either empty it in Tsuga, or annotate the folder with `observability.tsuga.com/orphan-if-not-empty: "true"` before deleting it: when Tsuga refuses the delete because the folder is not empty, the operator removes the resource and leaves the folder in Tsuga. It does so only once no Dashboard, and no child `DashboardFolder`, in the namespace still references the folder by name or ID, whether or not those are being deleted, so a namespace teardown still cleans up the folder. The orphaned folder is not adopted again if you recreate the resource.

As a last resort, `kubectl -n <namespace> patch dashboardfolder <name> --type=merge -p '{"metadata":{"finalizers":null}}'` removes the resource without touching Tsuga.

Install this release's CRDs and ClusterRole before using folders. An older chart's `Dashboard` CRD has no `folderRef`: `kubectl apply` rejects the field, and tools that skip validation have it pruned, so the dashboard syncs without being filed. Without the `DashboardFolder` CRD the operator still starts and keeps syncing Dashboards. See [DashboardFolder reference](../reference/dashboardfolder.md).

## 4. Clean up, or keep learning

To remove the example **and its remote Tsuga dashboard**:

```sh
kubectl -n default delete dashboard example-dashboard
```

Keep it if you want to [create a monitor linked to this dashboard](../monitors/index.md). See the [Dashboard reference](../reference/dashboard.md) for fields and validation.
