# Monitors

**Prerequisite: a running operator and a Tsuga team ID or team name**

This example monitors a metric and links the alert to `example-dashboard` in the same namespace. Complete the [dashboard tutorial](../dashboards/index.md) first, or remove `dashboardRef` if you do not need the link.

## Create the resource

Save [monitor.yaml](../examples/monitor.yaml) and replace `<YOUR_TEAM_ID>`, or swap `owner` for `ownerName: <YOUR_TEAM_NAME>`. Adapt the metric, filter, threshold, and no-data behavior to your environment; the sample threshold is illustrative and the monitor may alert if it has no data.

```yaml
--8<-- "examples/monitor.yaml"
```

```sh
kubectl apply -f monitor.yaml
kubectl -n default wait --for=jsonpath='{.status.phase}'=Ready monitor/example-monitor --timeout=120s
kubectl -n default describe monitor example-monitor
```

Expect a remote `status.id`. Confirm the query, threshold, dashboard link, and alert behavior in Tsuga before relying on the monitor.

## Reference an existing Tsuga dashboard

Replace `dashboardRef` with `dashboardId: "<YOUR_DASHBOARD_ID>"` in the manifest. Supply only one: if both are present, `dashboardId` takes precedence. `dashboardRef` is an object with a `name` field, not a plain string, and only resolves within the Monitor's namespace.

The referenced Dashboard must have a remote ID before the Monitor can sync. After changing a Monitor, compare `status.observedGeneration` with `metadata.generation` as well as checking `Ready`.

## File the monitor in a folder

Declare folders as `MonitorFolder` resources. A folder can sit inside one top-level folder through `parentFolderRef` (or `parentFolderId` for a folder created in Tsuga); Tsuga allows two levels at most and rejects a third with an `Error` phase.

```yaml
--8<-- "examples/monitor-folders.yaml"
```

Then add `folderRef: {name: example-monitor-subfolder}` to the Monitor, or `folderId: "<YOUR_MONITOR_FOLDER_ID>"` for a folder created in Tsuga. If both are present, `folderId` takes precedence. A referenced folder must have a remote ID before the Monitor can sync, and the Monitor is pushed again when that folder is recreated with a new ID. Filing a monitor does not change its ownership, data access, or evaluation.

Without `folderId` or `folderRef`, the operator leaves the monitor's folder unchanged, so a folder chosen in the Tsuga UI is kept. Removing either field from the manifest therefore does not unfile the monitor: move it out of the folder in Tsuga instead. The same holds for a folder's parent.

Deleting a `MonitorFolder` deletes the Tsuga folder only once it is empty. Until its monitors and child folders are gone, the resource stays in `Deleting`, `status.message` shows the reason Tsuga gave, and the operator retries every 30 seconds. Deleting a folder never deletes or moves the monitors in it.

If the folder holds monitors filed outside Kubernetes (in the Tsuga UI, or from another namespace or cluster), it never empties. Either empty it in Tsuga, or annotate the folder with `observability.tsuga.com/orphan-if-not-empty: "true"` before deleting it: when Tsuga refuses the delete because the folder is not empty, the operator removes the resource and leaves the folder in Tsuga. It does so only once no Monitor, and no child `MonitorFolder`, in the namespace still references the folder by name or ID, whether or not those are being deleted, so a namespace teardown still cleans up the folder. The orphaned folder is not adopted again if you recreate the resource.

As a last resort, `kubectl -n <namespace> patch monitorfolder <name> --type=merge -p '{"metadata":{"finalizers":null}}'` removes the resource without touching Tsuga.

Install this release's CRDs and ClusterRole before using folders. An older chart's `Monitor` CRD has no `folderRef`: `kubectl apply` rejects the field, and tools that skip validation have it pruned, so the monitor syncs without being filed. Without the `MonitorFolder` CRD the operator still starts and keeps syncing Monitors. See [MonitorFolder reference](../reference/monitorfolder.md).

## Update or remove

Edit the manifest and reapply it to update the remote monitor. Delete the resource to delete the remote monitor:

```sh
kubectl -n default delete monitor example-monitor
```

`configuration` is opaque to Kubernetes and is validated by the Tsuga API. A Kubernetes apply succeeding does not guarantee the configuration is accepted remotely. See [Monitor reference](../reference/monitor.md) and [status troubleshooting](../operations/troubleshooting.md).
