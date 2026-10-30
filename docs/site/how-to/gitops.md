# Use GitOps

**Prerequisite: a working installation and a GitOps controller**

Keep each resource's desired configuration in Git. Use environment overlays to change team IDs, cluster names, namespaces, and query filters without duplicating entire manifests.

## Apply in dependency order

1. Install cert-manager and the OpenTelemetry Operator if you use collection. Wait for their CRDs and webhooks.
2. Provision Secrets through your secret manager and install the Tsuga operator and CRDs.
3. Apply the cluster config named `cluster` and check its status and collector pods.
4. Apply namespace instrumentation and application pod-template annotations.
5. Apply Dashboards, then Monitors that reference them. Apply SLOs with their desired SLI configuration and alerts.

Configure the equivalent dependency ordering and health checks in your GitOps tool. For collection, check the `Ready` condition and its observed generation. For dashboards, monitors, and SLOs, check `status.phase` and compare `status.observedGeneration` with `metadata.generation`.

## Keep ownership clear

Manage Tsuga CRs in Git. The Tsuga operator owns generated OpenTelemetry resources; the OpenTelemetry Operator owns the resulting pods and services. Editing generated children produces competing field ownership and can be overwritten.

For selective application instrumentation, set `injection: Manual` and manage pod-template annotations in the application repository. For namespace-wide instrumentation, the default `injection: Namespace` annotates only the Namespace object, so pod templates in Git stay as written. If your GitOps controller also manages that Namespace, make sure it does not remove the operator's `instrumentation.opentelemetry.io/inject-*` annotations. With `injection: Workloads`, the operator annotates pod templates, so account for those annotations or your GitOps controller will keep reverting them.

## Review deletion as a product change

GitOps pruning a Dashboard, Monitor, or SLO deletes the remote Tsuga object. Pruning a cluster config removes the collectors it owns. Review removals and namespace deletion with the same care as other changes to observability coverage.

Kubernetes is the desired-state source. The operator re-pushes each `Ready` dashboard every 10 minutes, so a dashboard change made in the Tsuga UI is reverted within that interval. Monitors and SLOs are not re-pushed on a timer; UI changes to them persist until the next spec change. See [reconciliation semantics](../concepts/reconciliation.md).
