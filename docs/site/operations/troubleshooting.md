# Troubleshooting

Start with the desired resource, then its controller, then any generated workloads. Avoid copying Secret contents into logs or issue reports.

## First checks

```sh
kubectl -n tsuga-operator-system get pods
kubectl -n tsuga-operator-system logs deployment/tsuga-operator-controller-manager --tail=100
kubectl get dashboards,monitors,slos -A
kubectl get tsugacollectorconfigs
kubectl get tsugamonitorings -A
```

Describe the specific resource and compare `metadata.generation` with `status.observedGeneration`. A previously successful phase can be stale after a spec change.

## Operator will not start

| Symptom | Check | Action |
| --- | --- | --- |
| `CreateContainerConfigError` | Pod events identify a missing Secret or key | Provision `tsuga-credentials` with `api-token` in the operator namespace |
| Missing `TSUGA_API_TOKEN` | Startup logs | Supply an operation key, including for collection-only use |
| `ImagePullBackOff` | Image tag, registry access, supported architecture | Use a published image tag and appropriate pull credentials |
| Collection controllers disabled | OpenTelemetry CRDs missing at operator startup | Install dependencies, then restart the Tsuga operator |
| Missing TargetAllocator kind / cache sync error | Installed upstream version | Install the repository's pinned OpenTelemetry Operator, including its TargetAllocator CRD |

```sh
kubectl -n tsuga-operator-system describe pod <OPERATOR_POD>
kubectl get crd opentelemetrycollectors.opentelemetry.io instrumentations.opentelemetry.io targetallocators.opentelemetry.io
```

## Dashboard, Monitor, or SLO is in Error

```sh
kubectl -n default describe dashboard example-dashboard
kubectl -n default describe monitor example-monitor
kubectl -n default describe slo example-event-slo
```

For authentication failures, check the operation key and its permissions. For API validation failures, inspect `spec.owner`, widget visualization, monitor configuration, or SLO SLI/alert configuration. Kubernetes deliberately does not validate the opaque Tsuga payload sections.

A Monitor with `dashboardRef` needs a Dashboard in the same namespace with a populated `status.id`. Use `dashboardRef: {name: example-dashboard}`. Correct the resource and apply it again.

If a corrected Secret has not taken effect, restart the operator. A client error has no explicit timed retry, so make a meaningful spec correction if the resource remains unchanged and unsynced.

## Collectors are Ready but data is missing

1. Check the actual collector workloads and pod readiness, not only the TsugaCollectorConfig condition.
2. Inspect pod events for missing Secret keys, image failures, resource limits, or permission errors.
3. Inspect collector logs for configuration, authentication, or export failures.
4. Check network reachability to the configured OTLP endpoint and that the ingestion key matches it.
5. Confirm you are querying the right cluster and recent time range in Tsuga; generate traffic if testing traces.

```sh
kubectl -n tsuga-operator-system get opentelemetrycollectors,pods
kubectl -n tsuga-operator-system describe pod <COLLECTOR_POD>
kubectl -n tsuga-operator-system logs <COLLECTOR_POD> --all-containers=true --tail=100
```

Secret environment variables are not hot-reloaded. After updating keys or endpoint, follow [credential rotation](../how-to/credentials.md).

## Instrumentation is not appearing

Check that the config named `cluster` exists, `instrumentation.enabled` is true, and the language list matches your workload. In selective mode, place injection annotations on `spec.template.metadata.annotations`, not the Deployment's top-level metadata.

Inspect a newly created pod, the upstream operator's logs, and its webhook health. Existing pods are not retroactively injected; follow your workload rollout strategy. Check runtime support and feature flags for your pinned upstream operator. Jobs and CronJobs are not patched by this controller.

## Scraping is absent or duplicated

Check annotations on the **pods**, endpoint reachability, and `prometheus.enabled`. For more than one scraper replica, enable the Target Allocator. Also look for a separate collector or Prometheus deployment already scraping the same targets.

If scraping continues after disabling it, check the TsugaCollectorConfig status for an apply error: the controller deletes the `tsuga-scraper` and `tsuga-scraper-ta` resources it owns once they are no longer desired.

## A resource is stuck deleting

Dashboard, Monitor, and SLO finalizers wait for remote deletion. Keep the operator running, restore Tsuga connectivity and permissions, and inspect its logs. Removing finalizers manually can orphan remote objects and should only be part of a deliberate recovery procedure after checking their state.

For an issue report, include the operator version, upstream operator version, redacted spec/status, relevant error messages, and reproduction steps. See [help and releases](../contributing/support.md).
