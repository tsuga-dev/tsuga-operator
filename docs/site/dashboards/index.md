# Dashboards

**Prerequisite: [operator installed](../getting-started/index.md)**

This walkthrough creates a dashboard in Tsuga and manages it through a Kubernetes resource. You need a Tsuga team ID for `spec.owner` and permission for your operation key to manage that team's dashboards.

## 1. Save the manifest

Download [dashboard.yaml](../examples/dashboard.yaml), or save the following as `dashboard.yaml`. Replace `<YOUR_TEAM_ID>` with your team's ID. The sample metric must exist in your Tsuga account for the chart to show data.

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

## 4. Clean up, or keep learning

To remove the example **and its remote Tsuga dashboard**:

```sh
kubectl -n default delete dashboard example-dashboard
```

Keep it if you want to [create a monitor linked to this dashboard](../monitors/index.md). See the [Dashboard reference](../reference/dashboard.md) for fields and validation.
