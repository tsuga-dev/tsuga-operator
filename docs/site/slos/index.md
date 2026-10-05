# SLOs

Manage service-level objectives in Tsuga with an `SLO` Kubernetes resource. Define the service-level indicator (SLI), target percentage, rolling evaluation window, and optional alerts in a manifest.

**Prerequisite: [operator installed](../getting-started/index.md)** with an operation key and a Tsuga team ID. No OpenTelemetry Operator installation is needed to manage SLOs; the data queried by the SLI must already be available in Tsuga.

This is Tsuga's native `observability.tsuga.com/v1alpha1` SLO resource, not an OpenSLO manifest.

## Choose an SLI

| Configuration | What it measures | Example |
| --- | --- | --- |
| `event` | Good events divided by total events | Share of checkout requests without errors |
| `time` | Time slices whose query meets a threshold | Slices where checkout latency meets a limit |

`target` is a percentage strictly between 0 and 100, such as `99.9`. `timeframeDays` must be `7`, `30`, or `90`. Set `owner` to your team's ID and choose `permissions` for the data the SLO may access.

## Create an event-based SLO

Save [slo-event.yaml](../examples/slo-event.yaml), replace `<YOUR_TEAM_ID>`, and adjust the query filters to your service. The example treats missing data as good; choose no-data behavior deliberately for your objective.

```yaml
--8<-- "examples/slo-event.yaml"
```

```sh
kubectl apply -f slo-event.yaml
kubectl -n default wait --for=jsonpath='{.status.phase}'=Ready slo/example-event-slo --timeout=120s
kubectl -n default get slos
kubectl -n default get slo example-event-slo -o jsonpath='{.status.id}{"\n"}'
```

A successful reconcile sets `status.phase: Ready` and stores the Tsuga SLO ID in `status.id`. Open the SLO in Tsuga and check its queries, target, data availability, and alert configuration. `Ready` confirms synchronization, not that the service meets its objective.

## Create a time-based SLO

Save [slo-time.yaml](../examples/slo-time.yaml) and replace the team ID. This example evaluates a latency query in 60-minute slices over seven days. Check the query field's units and adapt the threshold to your data before applying it.

```yaml
--8<-- "examples/slo-time.yaml"
```

```sh
kubectl apply -f slo-time.yaml
kubectl -n default wait --for=jsonpath='{.status.phase}'=Ready slo/example-time-slo --timeout=120s
```

## Alerts and cluster scope

`alerts` is optional. Each alert has a priority from `1` (highest) to `5` (lowest) and a configuration for a burn-rate or threshold alert. Omitting alerts sends an empty array to Tsuga, leaving the SLO without alerts.

Omit `clusterIds` to evaluate across all clusters, or provide Tsuga cluster IDs to restrict evaluation. Removing a previously specified list sends an empty array, clearing the cluster restriction. These IDs are not Kubernetes namespace names or the collection config's `clusterName`.

SLI and alert configurations are opaque JSON in the CRD. Kubernetes validates the surrounding fields; Tsuga validates the inner configuration. See the [API reference](../reference/slo.md) for constraints and the [OpenAPI contract](https://github.com/tsuga-dev/tsuga-operator/blob/main/public-open-api.json) for configuration shapes.

## Update an SLO

Edit the manifest and apply it again. Verify that `status.observedGeneration` matches `metadata.generation`, the phase is `Ready`, and the updated settings appear in Tsuga.

!!! warning "Updates recreate alerts"
    The operator does not send alert IDs. Every remote SLO update, including a description-only change, replaces the SLO's existing alerts with new IDs. Do not rely on those alert IDs staying stable outside the resource.

The operator updates the existing SLO using `status.id`. If a remote update reports the SLO is missing, it attempts to recreate it. It does not re-push an SLO on a timer, since each update recreates its alerts; see [reconciliation](../concepts/reconciliation.md).

## Delete an SLO

Deleting the Kubernetes resource also deletes the SLO in Tsuga. Keep the operator running and its credentials valid until its finalizer completes:

```sh
kubectl -n default delete slo example-event-slo
kubectl -n default delete slo example-time-slo
```

For synchronization errors or resources stuck deleting, use the [troubleshooting guide](../operations/troubleshooting.md).
