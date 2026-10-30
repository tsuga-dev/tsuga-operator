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

The CRD rejects more than one replica without the allocator. The default allocation strategy is `consistent-hashing`; `least-weighted` is also accepted. The CRD rejects `per-node`: it only assigns targets on nodes that run a scraper replica, and `tsuga-scraper` is a StatefulSet on a few nodes, so most targets would go unscraped. For one scraper per node, use [node-local scraping](#scrape-on-each-node).

Each replica change moves targets between replicas, and every moved series loses one datapoint while the new replica builds its delta state.

## Scrape on each node

Set `prometheus.nodeLocal: true` to scrape annotated pods from the `tsuga-agent` DaemonSet instead of `tsuga-scraper`. Each agent scrapes only pods on its own node, so every target is scraped exactly once, with no Target Allocator and no single scraper pod for the whole cluster.

```yaml
spec:
  prometheus:
    enabled: true
    nodeLocal: true
```

Without `prometheusCR`, the operator then deletes `tsuga-scraper`. With `prometheusCR`, `tsuga-scraper` keeps scraping ServiceMonitors and PodMonitors, and the agent scrapes annotations. The job keeps its name, `tsuga-pod-annotations`, so queries on `service.name` still match.

Requirements and effects:

- The agent must be enabled with `agent.metrics` on, and `agent.customConfig` unset; the CRD rejects the other combinations. `annotationDiscovery: false` is rejected too. An agent `extraReceivers` entry named `prometheus` clashes with the generated receiver and gives `Ready=False` with reason `RenderFailed`; rename it.
- Annotation metrics go through the agent's metrics pipeline: `agent.extraProcessors`, `agent.pipelineProcessors`, `agent.pipelineExporters`, `agent.exportToTsuga`, `resourceDetection` and redaction apply. `prometheus.resources`, `prometheus.extra*`, `prometheus.replicas` and `prometheus.targetAllocator` apply only to monitor scraping.
- Scrape memory moves to the agent. Raise `agent.resources` by what `tsuga-scraper` used, divided across nodes. A burst of series from one app counts against the agent's memory limit on that node, which also carries its logs and traces.
- Pods on nodes without an agent pod are not scraped, for example EKS Fargate, or tainted nodes the agent does not tolerate. Check `agent.tolerations`.
- NetworkPolicies that admit `tsuga-scraper` to a metrics port must admit `tsuga-agent` instead. With `agent.hostNetwork: true` the scrape comes from the node IP, so a `podSelector` does not match it; allow the node CIDR with an `ipBlock`, or rely on your CNI's node-to-local-pod rule.
- When you switch, `tsuga-scraper` is deleted right away while agent pods roll node by node, so each node has a short scrape gap, and each series loses one datapoint.

## Avoid scraping a pod twice

A pod with `prometheus.io/scrape: "true"` that is also selected by a ServiceMonitor or PodMonitor is scraped by both jobs. Tsuga receives two copies of each series under different `service.name` values, and sums over the metric count it twice.

When `prometheusCR.enabled` is on and `annotationDiscovery` is unset, `tsuga-scraper` scrapes only monitors. With `nodeLocal: true` the agent still scrapes annotated pods, and `annotationDiscovery: false` is rejected, so do not annotate pods that a monitor selects. To use both discovery paths, set `annotationDiscovery: true` and make sure no pod is both annotated and selected by a monitor. Configs created with an earlier operator version that had a `prometheus` block have `annotationDiscovery: true` stored, so they keep both jobs until you change it. A `prometheus.customConfig` is used as written, so keep the `tsuga-pod-annotations` job out of it if you need monitors only.

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

With `prometheusCR.enabled`, `tsuga-scraper` does not scrape annotated pods unless `prometheus.annotationDiscovery: true` is set; with `nodeLocal: true` the agent scrapes them instead (see [Avoid scraping a pod twice](#avoid-scraping-a-pod-twice)). When `tsuga-scraper` scrapes only monitors (annotation discovery off, or `nodeLocal` on), it renders a placeholder job, `tsuga-placeholder`, that scrapes the collector's own `localhost:8888`; it exists only to keep the config valid and reports an `up=0` series. The CRD rejects a scraper with annotation discovery off and `prometheusCR` off.

If the CRDs are missing, the config reports the condition `PrometheusOperatorCRDsMissing` and the rest of collection keeps working. Annotated pods are still scraped only when `annotationDiscovery: true` or `nodeLocal: true` is set; otherwise nothing is scraped until the CRDs are installed. The operator rechecks every 5 minutes while the CRDs are missing, so the condition clears within about 5 minutes of installing them. After installing the CRDs, restart the Target Allocator Deployment, `tsuga-scraper-ta-targetallocator` (`kubectl -n tsuga-operator-system rollout restart deployment tsuga-scraper-ta-targetallocator`): it starts without the monitor informers and does not recheck until restarted. The condition is raised only when both the `ServiceMonitor` and `PodMonitor` kinds are missing. Status `phase` stays `Ready` in that state, so check the Ready condition, not the phase.

The manager's ClusterRole needs `get`, `list`, `watch` on `monitoring.coreos.com` `servicemonitors`, `podmonitors`, `probes` and `scrapeconfigs`, and on `secrets`, plus `roles` and `rolebindings` management. With `rbac.create` on, the operator grants the scraper's ServiceAccount the same monitoring access in its ClusterRole, and Secret read only in `collectorNamespace` through a Role and RoleBinding named `tsuga-operator-collector-tsuga-scraper`.

Limitations:

- Monitors that reference Secrets (basic auth, bearer token, OAuth2, TLS from a Secret) work only in `collectorNamespace`: the Target Allocator reads a monitor's Secrets in the monitor's own namespace, and a monitor elsewhere whose Secret it cannot read is rejected.
- When `prometheusCR.scrapeInterval` is unset, the Target Allocator uses its own 30s default, not `prometheus.scrapeInterval`.
- On clusters with the full Prometheus Operator, the Target Allocator also watches `Probe` and `ScrapeConfig` resources, but the operator sets no selector for them, so none are scraped.
- Exclude `job="tsuga-placeholder"` from `up == 0` alerts.

## Disable scraping

Set `prometheus.enabled: false` in the desired manifest. The controller deletes the `tsuga-scraper` and `tsuga-scraper-ta` resources it created. Disabling only `targetAllocator.enabled` deletes just `tsuga-scraper-ta`.
