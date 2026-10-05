# Kubernetes monitoring

Use the Tsuga Operator to collect telemetry from Kubernetes and send it to Tsuga. A cluster-scoped `TsugaCollectorConfig` defines the collectors, and a `TsugaMonitoring` resource configures auto-instrumentation in an application namespace.

The [OpenTelemetry Operator](https://opentelemetry.io/docs/platforms/kubernetes/operator/) runs the generated collectors and handles application instrumentation. Install it before enabling collection, following the versions in our [installation guide](../getting-started/index.md).

## Configure monitoring

| Guide | What you will configure |
| --- | --- |
| [Getting started](getting-started.md) | Credentials, node agents, and cluster collection |
| [Auto-instrumentation](auto-instrumentation.md) | Instrumentation for selected applications or a whole namespace |
| [DBM](dbm.md) | Monitor PostgreSQL with managed or manual database provisioning |
| [Prometheus scraping](prometheus.md) | Metrics collection from annotated pods, with optional target allocation |

Start with [cluster collection](getting-started.md), then add application instrumentation or scraping as needed.

## Configuration reference

- [TsugaCollectorConfig](../reference/tsugacollectorconfig.md): collection signals, export destination, resources, and scraping.
- [TsugaMonitoring](../reference/tsugamonitoring.md): application languages and instrumentation behavior.
- [Credentials](../how-to/credentials.md): provisioning and rotation.
- [Troubleshooting](../operations/troubleshooting.md): collector startup, missing data, and injection issues.
