# Monitors

**Prerequisite: a running operator and a Tsuga team ID**

This example monitors a metric and links the alert to `example-dashboard` in the same namespace. Complete the [dashboard tutorial](../dashboards/index.md) first, or remove `dashboardRef` if you do not need the link.

## Create the resource

Save [monitor.yaml](../examples/monitor.yaml) and replace `<YOUR_TEAM_ID>`. Adapt the metric, filter, threshold, and no-data behavior to your environment; the sample threshold is illustrative and the monitor may alert if it has no data.

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

## Update or remove

Edit the manifest and reapply it to update the remote monitor. Delete the resource to delete the remote monitor:

```sh
kubectl -n default delete monitor example-monitor
```

`configuration` is opaque to Kubernetes and is validated by the Tsuga API. A Kubernetes apply succeeding does not guarantee the configuration is accepted remotely. See [Monitor reference](../reference/monitor.md) and [status troubleshooting](../operations/troubleshooting.md).
