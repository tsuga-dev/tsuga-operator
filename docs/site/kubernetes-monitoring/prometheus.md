# Scrape Prometheus metrics

**Prerequisite: working collection and OpenTelemetry Operator v0.159.0**

The optional scraper discovers pods with `prometheus.io/scrape: "true"`. This feature uses pod annotations; it does not configure discovery from Prometheus `ServiceMonitor` or `PodMonitor` resources.

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

## Disable scraping

Set `prometheus.enabled: false` in the desired manifest. The controller deletes the `tsuga-scraper` and `tsuga-scraper-ta` resources it created. Disabling only `targetAllocator.enabled` deletes just `tsuga-scraper-ta`.
