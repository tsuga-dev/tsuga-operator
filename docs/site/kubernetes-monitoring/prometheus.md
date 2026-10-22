# Scrape Prometheus metrics

**Prerequisite: working collection and OpenTelemetry Operator v0.160.0**

The optional scraper discovers pods with `prometheus.io/scrape: "true"`. By default it uses pod annotations. To scrape Prometheus Operator `ServiceMonitor` and `PodMonitor` resources too, see [ServiceMonitors and PodMonitors](#servicemonitors-and-podmonitors).

## Enable the scraper

Save [collector-prometheus.yaml](../examples/collector-prometheus.yaml). It is a complete cluster config using the same Secret as the telemetry tutorial. If you already have a customized `cluster` manifest, merge its `prometheus` block into that manifest instead of replacing your other settings.

```yaml
--8<-- "examples/collector-prometheus.yaml"
```

```sh
kubectl apply -f collector-prometheus.yaml
kubectl -n tsuga-operator-system get opentelemetrycollector tsuga-scraper
```

## Annotate the target pods

Add these annotations to the application's pod template in its Deployment or StatefulSet manifest. Adjust the port and path to the application's metrics endpoint; the annotations do not create that endpoint.

```yaml
prometheus.io/scrape: "true"
prometheus.io/port: "8080"
prometheus.io/path: /metrics
prometheus.io/scheme: http
```

Apply the application manifest. Confirm its new pods carry the annotations and that the metrics endpoint is reachable from the scraper's namespace. Check scraper pod logs and confirm fresh samples in Tsuga.

## Scale scraping across replicas

Set `prometheus.replicas: 2` and `prometheus.targetAllocator.enabled: true` together in the cluster manifest, then reapply. The operator creates `tsuga-scraper-ta`; the allocator distributes targets to prevent every replica scraping the same pods.

```sh
kubectl -n tsuga-operator-system get targetallocator tsuga-scraper-ta
kubectl -n tsuga-operator-system get statefulsets,pods
```

The CRD rejects more than one replica without the allocator. The default allocation strategy is `consistent-hashing`; `least-weighted` and `per-node` are also accepted. `per-node` excludes targets that cannot resolve to a node, so choose it deliberately.

`scrapeInterval` controls each target's scrape frequency. The upstream operator manages the allocator polling interval.

## ServiceMonitors and PodMonitors

The scraper can also discover targets from Prometheus Operator `ServiceMonitor` and `PodMonitor` resources, through the Target Allocator.

Prerequisites:

- `prometheus.targetAllocator.enabled: true`. The CRD rejects `prometheusCR.enabled` without it.
- The Prometheus Operator CRDs (`monitoring.coreos.com`) installed in the cluster. The operator does not install them.

```yaml
spec:
  prometheus:
    enabled: true
    targetAllocator:
      enabled: true
    prometheusCR:
      enabled: true
      serviceMonitorSelector:
        matchLabels:
          team: payments
```

An unset or empty selector matches every monitor of that kind. `podMonitorSelector`, `serviceMonitorNamespaceSelector`, `podMonitorNamespaceSelector`, `allowNamespaces`, `denyNamespaces` and `scrapeInterval` (the default for monitors that set none) narrow discovery further.

Annotation discovery and monitor discovery run together by default. Set `prometheus.annotationDiscovery: false` to scrape only monitors. The scraper then renders a placeholder job, `tsuga-placeholder`, that scrapes the collector's own `localhost:8888`; it exists only to keep the config valid and reports an `up=0` series. The CRD rejects a scraper with annotation discovery off and `prometheusCR` off.

If the CRDs are missing, the config reports the condition `PrometheusOperatorCRDsMissing` and annotation scraping and the rest of collection keep working. The operator rechecks every 5 minutes while the CRDs are missing, so the condition clears within about 5 minutes of installing them. After installing the CRDs, restart the Target Allocator Deployment, `tsuga-scraper-ta-targetallocator` (`kubectl -n tsuga-operator-system rollout restart deployment tsuga-scraper-ta-targetallocator`): it starts without the monitor informers and does not recheck until restarted. The condition is raised only when both the `ServiceMonitor` and `PodMonitor` kinds are missing. Status `phase` stays `Ready` in that state, so check the Ready condition, not the phase.

The manager's ClusterRole needs `get`, `list`, `watch` on `monitoring.coreos.com` `servicemonitors`, `podmonitors`, `probes` and `scrapeconfigs`, and on `secrets`, plus `roles` and `rolebindings` management. With `rbac.create` on, the operator grants the scraper's ServiceAccount the same monitoring access in its ClusterRole, and Secret read only in `collectorNamespace` through a Role and RoleBinding named `tsuga-operator-collector-tsuga-scraper`.

Limitations:

- Monitors that reference Secrets (basic auth, bearer token, OAuth2, TLS from a Secret) work only in `collectorNamespace`: the Target Allocator reads a monitor's Secrets in the monitor's own namespace, and a monitor elsewhere whose Secret it cannot read is rejected.
- When `prometheusCR.scrapeInterval` is unset, the Target Allocator uses its own 30s default, not `prometheus.scrapeInterval`.
- On clusters with the full Prometheus Operator, the Target Allocator also watches `Probe` and `ScrapeConfig` resources, but the operator sets no selector for them, so none are scraped.
- Exclude `job="tsuga-placeholder"` from `up == 0` alerts.

## Disable scraping

Set `prometheus.enabled: false` in the desired manifest. The controller deletes the `tsuga-scraper` and `tsuga-scraper-ta` resources it created. Disabling only `targetAllocator.enabled` deletes just `tsuga-scraper-ta`.
