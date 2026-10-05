# Manage credentials

The operator's operation key and the collectors' ingestion key serve different purposes.

| Key in the default Secret | Consumer | Purpose |
| --- | --- | --- |
| `api-token` | Tsuga operator | Dashboard, Monitor, and SLO API calls; required for process startup |
| `api-key` | Collectors | OTLP ingestion authentication |
| `otlp-endpoint` | Collectors | OTLP export destination |

The default Secret is `tsuga-credentials` in `tsuga-operator-system`. The operator Deployment references this name directly. Collection Secret references are configurable in `TsugaCollectorConfig.spec.export` and resolve in `collectorNamespace`.

## Provision credentials

Follow the complete Secret command in [collect cluster telemetry](../kubernetes-monitoring/getting-started.md#1-provide-all-credential-keys), or have your secret manager provision the same name and keys. Use placeholders in Git and keep real values in your organization's secret store.

`make create-tsuga-secret` manages only `api-token`. Recreating an apply-managed Secret with that target can remove ingestion keys. Prefer one authoritative manifest or secret-manager resource for all keys in a shared Secret.

## Rotate keys or change the endpoint

Update the Secret through its authoritative source, preserving any keys you are not rotating. Environment variables are read when pods start; changing a Secret alone does not refresh them.

For an operation key rotation:

```sh
kubectl -n tsuga-operator-system rollout restart deployment/tsuga-operator-controller-manager
kubectl -n tsuga-operator-system rollout status deployment/tsuga-operator-controller-manager --timeout=180s
```

For an ingestion key or endpoint rotation, list the collector workloads, then restart only the Tsuga collectors that exist:

```sh
kubectl -n tsuga-operator-system get daemonsets,deployments,statefulsets
kubectl -n tsuga-operator-system rollout restart daemonset/tsuga-agent-collector
kubectl -n tsuga-operator-system rollout restart deployment/tsuga-gateway-collector
# If Prometheus scraping is enabled:
kubectl -n tsuga-operator-system rollout restart statefulset/tsuga-scraper-collector
```

Watch those rollouts, check for export errors, and verify fresh telemetry in Tsuga before retiring the previous ingestion key. Collector restarts can interrupt collection.

## Namespace overrides

There are no per-namespace ingestion credentials. Instrumented applications send to the shared agent, which uses the cluster collection export Secret.
