# Alert on collector health

The agent, gateway, scraper and profiler send their own metrics (`otelcol_*`) to Tsuga as delta counters, with `k8s.cluster.name` and `k8s.pod.name` on each point. [collector-monitors.yaml](../examples/collector-monitors.yaml) turns them into six monitors that alert, per cluster and collector pod, when telemetry is lost or about to be.

## What the monitors catch

| Monitor | Priority | Fires when |
| --- | --- | --- |
| Telemetry dropped (export queue full) | 1 | An exporter queue is full and new data is dropped, over 5 minutes. |
| Export failures | 2 | More than 5% of exported items fail, over 10 minutes. |
| Memory limiter refusing data | 2 | The memory limiter refuses any data, over 10 minutes. |
| Self-telemetry missing in a Kubernetes cluster | 2 | A cluster's collectors send no metrics for 15 minutes. |
| Receivers refusing or failing | 3 | More than 1% of received items are refused or fail, over 10 minutes. |
| Exporter queue fill high | 3 | An exporter queue is more than 70% full, over 5 minutes. |

Every query is filtered on `context.k8s.pod.name:tsuga-*` and pins its metric kind (`delta-counter`, or `gauge` for queue sizes), so collectors whose pod names do not start with `tsuga-`, or that still send cumulative counters, do not affect them. Exporter queue fill is grouped by signal, because each signal has its own queue. The same conditions are in Tsuga's monitor templates under OpenTelemetry Collector, without that filter.

## Apply them

Replace `<YOUR_TEAM_ID>` in the file and apply it once for your organization, in one cluster whose operator has a Tsuga API token. Each monitor already alerts per `k8s.cluster.name`.

```sh
kubectl apply -f collector-monitors.yaml
kubectl -n default get monitors
```

!!! warning "Do not apply it in every cluster"
    Each cluster's operator creates its own copy of every monitor in Tsuga. Syncing the file to all clusters through GitOps gives you one copy per cluster and as many notifications.

When a cluster is decommissioned, Self-telemetry missing alerts for it until its group ages out. Resolve the alert in Tsuga.

## Route the alerts

Monitors do not notify on their own. Create a notification rule that matches their tags, owned by the team that runs the collectors, and target its on-call integration. There is no custom resource for notification rules; use the Tsuga UI or the CLI:

```sh
tsuga notification-rules create --generate-skeleton > rule.json
# Set queryString to "source:tsuga-operator component:otel-collector",
# then fill owner, prioritiesFilter and targets.
tsuga notification-rules create -f rule.json
```

## When one fires

Start from the collector's logs: search `context.k8s.pod.name:tsuga-*` in the alerting cluster.

- **Export failures**: usually `429` rate limits, intermittent errors from the destination, or a failing exporter you added with `extraExporters`.
- **Telemetry dropped** or **Exporter queue fill high**: the destination is slow or partly unavailable and the queue filled up.
- **Memory limiter refusing data** or **Receivers refusing or failing**: the collector is at its memory limit. Raise it with the collector's `resources`, see [Production checklist](../operations/production.md#capacity).
- **Self-telemetry missing**: no collector in that cluster reaches Tsuga. Check the pods, the ingestion key Secret (`401`/`403` after a rotation) and network egress.

The collectors send their own metrics with the same ingestion key and endpoint as your telemetry. When Tsuga rejects the key or is unreachable, those metrics are lost too: Export failures and Telemetry dropped stay quiet, and Self-telemetry missing is the alert that fires.

## Custom config and disabled export

A collector with `customConfig` keeps your `service.telemetry` as written. The monitors expect these two settings on its metrics reader:

```yaml
service:
  telemetry:
    resource:
      k8s.cluster.name: __CLUSTER_NAME__
      k8s.pod.name: ${POD_NAME}
    metrics:
      readers:
        - periodic:
            exporter:
              otlp:
                protocol: http/protobuf
                temporality_preference: delta
                endpoint: __TSUGA_ENDPOINT__/v1/metrics
                headers:
                  - name: Authorization
                    value: Bearer ${env:TSUGA_API_KEY}
```

A collector with `exportToTsuga: false` sends no metrics of its own, so the monitors do not cover it.

## Not covered

- Scrape errors (`otelcol_scraper_errored_metric_points`): noisy on `kubelet_stats` in some environments.
- Collector pod restarts and OOM kills: alert on them with Kubernetes monitors.
- The DBM collectors of TsugaPostgresMonitoring: they send no metrics of their own.

CI validates the file against the Monitor schema. It does not contact Tsuga or check your team ID.
