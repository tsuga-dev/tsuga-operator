# Upgrade and uninstall

## Upgrade

Read the target release notes and compare API schemas, collector images, and the pinned OpenTelemetry dependency. Back up your desired manifests and inspect existing resource status. For alpha APIs, do not assume a downgrade can restore data pruned by an incompatible schema.

Test the new release in staging, then download and inspect its `install.yaml` as in the [installation guide](../getting-started/index.md). Apply it and verify the operator rollout, CR status, collector workloads, and fresh telemetry in Tsuga.

If the OpenTelemetry CRDs were installed after the Tsuga operator started, restart the Tsuga operator to register its collection controllers.

If you built from source, update the checkout and image together, regenerate manifests as required, and use `make deploy IMG=<registry>/tsuga-operator:<tag>`.

## Roll back

Restore the previous desired CR specs and operator image only after checking schema compatibility. Review whether CRDs or remote API objects changed during the upgrade. Revalidate synchronization and telemetry afterward; an image rollback alone does not reverse remote resource changes or pod-template annotations.

## Uninstall in dependency order

!!! warning "Deletion affects Tsuga and running telemetry"
    Deleting Dashboard, Monitor, and SLO CRs deletes their remote objects. Deleting collection configs removes owned collectors. Review which resources you intend to remove before running cleanup commands.

1. Disable each TsugaMonitoring with `instrumentation.enabled: false`. Wait for annotation cleanup, remove manual injection annotations, and complete the appropriate workload rollouts.
2. Delete the TsugaMonitoring resources after cleanup.
3. Delete SLOs and Monitors, then Dashboards you intend to remove. Wait for finalizers while the operator and operation key are still available.
4. Delete the cluster collection config. Verify its owned OpenTelemetry resources and supporting RBAC are removed, including any old children from earlier configurations.
5. Remove the Tsuga operator Deployment and CRDs using the matching installer or checkout. Remove credentials only after they are no longer needed.
6. Remove shared dependencies such as the OpenTelemetry Operator or cert-manager only if no other workloads need them.

For the tutorial resources, the CR cleanup commands are:

```sh
# Run only after disabling instrumentation and verifying annotation cleanup.
kubectl -n payments delete tsugamonitoring default --ignore-not-found
kubectl -n default delete slo example-event-slo example-time-slo --ignore-not-found
kubectl -n default delete monitor example-monitor --ignore-not-found
kubectl -n default delete dashboard example-dashboard --ignore-not-found
kubectl delete tsugacollectorconfig cluster --ignore-not-found
```

For a release installation, remove the operator using the same reviewed installer:

```sh
kubectl delete -f install.yaml
```

That installer contains the operator namespace and CRDs. Deleting a namespace removes its other contents too; deleting a CRD removes all instances. Confirm cleanup and namespace contents first. For source installations, `make undeploy` removes the deployed bundle and `make uninstall` removes CRDs; review the rendered resources before using either.

For PostgreSQL, delete `TsugaPostgresMonitoring` resources before removing the shared cluster collection config. Database objects remain after Kubernetes cleanup; see [DBM cleanup](../kubernetes-monitoring/dbm.md#permissions-and-cleanup).
