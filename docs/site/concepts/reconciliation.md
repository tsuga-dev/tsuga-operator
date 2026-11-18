# Reconciliation and status

A custom resource's `spec` describes your desired configuration. Its `status` describes what the controller last observed or completed. Applying a valid Kubernetes manifest only confirms acceptance by Kubernetes; reconciliation happens afterward.

## Dashboards, monitors, and SLOs

When `status.id` is empty, the operator creates a remote object and stores its Tsuga ID. When the spec generation changes, it updates that object. If the remote update returns not-found, it attempts to recreate it. See [Drift detection](#drift-detection) for edits made in the Tsuga UI.

The operator pushes a remote update when the CR spec changed (`status.observedGeneration` differs from `metadata.generation`) or when it finds drift under `driftPolicy: Overwrite`.

### Drift detection

The operator checks every resource that has a Tsuga ID every 10 minutes, and before each push. Set a different interval for the whole operator with `--drift-check-interval` (default `10m`, minimum `1m`). A resource is drifted when a field the operator manages differs in Tsuga from what Tsuga returned right after the operator's last push, or when the resource was deleted in Tsuga.

Set `spec.driftPolicy` on a Dashboard, Monitor or SLO to choose what happens:

| Policy | No drift | Drift |
| --- | --- | --- |
| `Overwrite` (default) | Pushes only if the CR spec changed | Pushes the CR (recreates a deleted resource) and emits a `Warning` event `DriftReverted` |
| `Warn` | Pushes only if the CR spec changed | Pushes nothing, even a pending CR change. Sets phase `Drifted` and emits a `Warning` event `DriftDetected` |

The event and the operator log name the drifted top-level fields, for example `drifted fields: graphs, tags`, or `deleted in Tsuga`. The status message of a `Drifted` resource carries the same text. While the same drift persists, the `DriftDetected` event repeats on later reconciles and `lastDriftCheckAt` is not refreshed.

`Drifted` clears when a later check finds no drift (the UI edit was undone), when you set `driftPolicy: Overwrite` (the next check reverts it), or when you accept the Tsuga version.

A resource deleted in Tsuga under `Warn` is reported as `Drifted`. Set `driftPolicy: Overwrite` to recreate it; accepting cannot keep a deletion.

A resource deleted in Tsuga before the operator recorded its first baseline is reported as `Drifted` with a `DriftDetected` event under both policies, and is not recreated. Change the CR to recreate it.

While a CR change is pending, a field that already holds the CR's value in Tsuga is not reported as drift.

#### Keep a change made in the Tsuga UI

1. Copy the edit into the CR spec.
2. Annotate the CR:

   ```bash
   kubectl annotate monitor my-monitor observability.tsuga.com/accept-remote=true
   ```

3. On the next reconcile the operator reads the resource, stores it as the new baseline, sets the phase to `Ready`, emits a `Normal` event `DriftAccepted` and removes the annotation. If the CR differs from what the operator last pushed, that change is pushed on the next pass.

The annotation works under both policies. Under `Overwrite` it only prevents the next revert.

#### What is compared

Only the fields your CR sets are compared. Fields the CR does not send, such as a monitor's `pauseUntil`, are ignored, so snoozing a monitor in the UI is not drift. When `Overwrite` re-pushes a drifted monitor, the update clears its snooze. Likewise, an SLO update recreates its alerts with new IDs.

Each pushed resource is read back and the hashes of its fields are stored in `status.remoteFieldHashes`. A resource without hashes, such as one created before upgrading to a version with drift detection, records them on its first check and nothing is reverted. If a CR change is pending, it is pushed in that same pass. UI edits made before that first check are not detected. If the cluster runs CRDs older than the operator image, Kubernetes drops these status fields: the operator then reads each resource once per check interval and still pushes CR changes. If a push succeeds but the read-back fails, the hashes are cleared and the next pass records a fresh baseline.

A phase `Error` makes the next drift check run immediately; it does not trigger a push. A check with no drift moves `Error` back to `Ready`. The Tsuga API key needs read access to dashboards, monitors and SLOs. If the API key lacks read access, the check fails with 401/403: the resource stays in `Error` and nothing is pushed. The operator retries every 30 seconds, so it recovers on its own once read access is granted. A permanent client error such as 400 or 422 on the check leaves `Error` until the CR changes.

The operator needs `create` and `patch` on core `events`.

| Status field | Meaning |
| --- | --- |
| `id` | Remote Tsuga resource ID |
| `phase` | `Ready`, `Drifted`, `Error`, or `Deleting` |
| `message` | Human-readable outcome or error |
| `observedGeneration` | Last successfully reconciled spec generation |
| `lastSyncedAt` | Time of the latest phase or message change |
| `remoteFieldHashes` | Hash of each managed field as Tsuga returned it after the last push; the drift baseline |
| `lastDriftCheckAt` | Time of the latest drift check |

Rate-limited (429), timed-out (408), conflicting (409), too-early (425) and authentication (401/403) failures retry after 30 seconds. Other transient errors use controller retries. Other client errors, and specs the operator itself rejects, are recorded without a retry; correct the configuration and trigger reconciliation with a meaningful spec change. The API token is read once at startup, so restart the manager after rotating it.

SLO updates recreate their alerts with new IDs, even when only the description changes. See [SLO update behavior](../slos/index.md#update-an-slo) before referencing alerts externally.

## Collection and instrumentation

These controllers use server-side apply to manage OpenTelemetry resources and publish a `Ready` condition. `Ready=True` confirms the controller applied its resources or successfully handled disabled instrumentation. It does not test collector pod readiness, webhook injection success, endpoint connectivity, or data arrival.

Check condition `observedGeneration` against the CR's `metadata.generation`, inspect the child workloads, and verify fresh data in Tsuga.

| Condition reason | Meaning |
| --- | --- |
| `Reconciled` | Desired children and/or annotations applied |
| `InstrumentationDisabled` | Instrumentation disabled, owned annotations removed, and the owned `Instrumentation` deleted |
| `AgentDisabled` | The cluster config turns the node agent off; auto-instrumentation and database monitoring removed their children |
| `ClusterConfigMissing` | Monitoring cannot find the config named `cluster` |
| `RenderFailed` | Collection config could not be rendered, such as all agent signals disabled. It is not retried; the previously applied collectors keep running until the spec is fixed |
| `ApplyFailed` | Kubernetes rejected a generated resource, or an object with the same name already exists without a controller reference to this CR. The operator does not adopt such objects; delete or rename them to let it proceed |
| `WorkloadPatchFailed` | Annotation updates failed |
| `InjectAnnotationConflict` | Another TsugaMonitoring or a manual annotation already sets an inject annotation on a workload; the phase is `Error` and the operator rechecks when the workload changes |
| `ProvisioningPending` / `ProvisioningFailed` | Database monitoring setup is running or failed; see [Database monitoring](../kubernetes-monitoring/dbm.md) |
| `PrometheusOperatorCRDsMissing` | prometheusCR is enabled but the Prometheus Operator CRDs are not installed; other collection is applied and the operator rechecks every 5 minutes |

## Current lifecycle limits

- Changing `collectorNamespace` deletes the collectors and allocator in the old namespace and recreates them in the new one. The token Secret and namespace instrumentation endpoints are not migrated; treat the change as a migration.
- Namespace instrumentation depends on the config named `cluster`. The monitoring controller watches that config and reconciles every TsugaMonitoring when it changes.
- Deleting a TsugaMonitoring removes the inject annotations the operator added, through a finalizer. Annotations set by hand stay in place and must be removed separately. The operator must be running for the deletion to complete.

These are implementation limits, not guarantees of future behavior. Check the code and release notes for the version you deploy.
