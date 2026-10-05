# Reconciliation and status

A custom resource's `spec` describes your desired configuration. Its `status` describes what the controller last observed or completed. Applying a valid Kubernetes manifest only confirms acceptance by Kubernetes; reconciliation happens afterward.

## Dashboards, monitors, and SLOs

When `status.id` is empty, the operator creates a remote object and stores its Tsuga ID. When the spec generation changes, it updates that object. If the remote update returns not-found, it attempts to recreate it.

The operator pushes a remote update when `status.observedGeneration` differs from `metadata.generation`. It also re-pushes a `Ready` dashboard every 10 minutes, which reverts edits made in the Tsuga UI and recreates a dashboard deleted there.

Monitors and SLOs are not re-pushed on a timer, because updating them has side effects: a monitor update clears its snooze, and an SLO update recreates its alerts with new IDs. For those, remote drift is corrected only when a spec change triggers a remote update.

| Status field | Meaning |
| --- | --- |
| `id` | Remote Tsuga resource ID |
| `phase` | `Ready`, `Error`, or `Deleting` |
| `message` | Human-readable outcome or error |
| `observedGeneration` | Last successfully reconciled spec generation |
| `lastSyncedAt` | Time of the latest status update, including a dashboard's 10-minute resync |

Rate-limited (429), timed-out (408), conflicting (409), too-early (425) and authentication (401/403) failures retry after 30 seconds. Other transient errors use controller retries. Other client errors, and specs the operator itself rejects, are recorded without a retry; correct the configuration and trigger reconciliation with a meaningful spec change. The API token is read once at startup, so restart the manager after rotating it.

SLO updates recreate their alerts with new IDs, even when only the description changes. See [SLO update behavior](../slos/index.md#update-an-slo) before referencing alerts externally.

## Collection and instrumentation

These controllers use server-side apply to manage OpenTelemetry resources and publish a `Ready` condition. `Ready=True` confirms the controller applied its resources or successfully handled disabled instrumentation. It does not test collector pod readiness, webhook injection success, endpoint connectivity, or data arrival.

Check condition `observedGeneration` against the CR's `metadata.generation`, inspect the child workloads, and verify fresh data in Tsuga.

| Condition reason | Meaning |
| --- | --- |
| `Reconciled` | Desired children and/or annotations applied |
| `InstrumentationDisabled` | Instrumentation disabled, owned annotations removed, and the owned `Instrumentation` deleted |
| `ClusterConfigMissing` | Monitoring cannot find the config named `cluster` |
| `RenderFailed` | Collection config could not be rendered, such as all agent signals disabled. It is not retried; the previously applied collectors keep running until the spec is fixed |
| `ApplyFailed` | Kubernetes rejected a generated resource, or an object with the same name already exists without a controller reference to this CR. The operator does not adopt such objects; delete or rename them to let it proceed |
| `WorkloadPatchFailed` | Annotation updates failed |

## Current lifecycle limits

- Changing `collectorNamespace` deletes the collectors and allocator in the old namespace and recreates them in the new one. The token Secret and namespace instrumentation endpoints are not migrated; treat the change as a migration.
- Namespace instrumentation depends on the config named `cluster`. Changes to that config are not directly watched by the monitoring controller; reconcile affected monitoring resources when moving the collector endpoint.
- Deleting a TsugaMonitoring does not clean workload annotations. Disable it first and remove any manually managed annotations separately.

These are implementation limits, not guarantees of future behavior. Check the code and release notes for the version you deploy.
