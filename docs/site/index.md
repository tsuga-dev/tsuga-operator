# Tsuga Operator

The Tsuga Operator manages observability from Kubernetes. Use it to collect telemetry from your cluster and applications, and to manage Tsuga dashboards, monitors, and SLOs through YAML manifests.

It watches custom resources in your cluster and applies their configuration to Tsuga or to the OpenTelemetry Operator. You can keep these resources alongside your application manifests and deploy them through your existing GitOps workflow.

[Install the operator](getting-started/index.md){ .md-button .md-button--primary }

## What you can use it for

### Monitor Kubernetes and applications

Collect node metrics, pod logs, and cluster state. Enable application auto-instrumentation and scrape Prometheus endpoints on annotated pods. The Tsuga Operator configures the collection resources; the OpenTelemetry Operator runs the collectors and injects instrumentation.

[Set up Kubernetes monitoring →](kubernetes-monitoring/getting-started.md)

### Monitor PostgreSQL

Collect database health and query metrics from in-cluster or managed PostgreSQL servers. Provision monitoring access automatically or let a DBA manage it.

[Set up database monitoring →](kubernetes-monitoring/dbm.md)

### Manage dashboards as code

Define dashboard widgets, queries, and layout in a Kubernetes `Dashboard` resource. Changes to the manifest update the corresponding dashboard in Tsuga.

[Create a dashboard →](dashboards/index.md)

### Manage monitors as code

Define alert queries and thresholds in a `Monitor` resource, assign an owning team, and optionally link it to a dashboard.

[Create a monitor →](monitors/index.md)

### Manage service-level objectives

Define event- or time-based SLIs, reliability targets, and alerts in an `SLO` resource. Keep service objectives in Git alongside your dashboards and monitors.

[Create an SLO →](slos/index.md)

## How it works

| Resource | What it manages |
| --- | --- |
| `TsugaCollectorConfig` | Cluster collection: node agents, a cluster gateway, and optional Prometheus scraping |
| `TsugaMonitoring` | Application auto-instrumentation in a namespace |
| `TsugaPostgresMonitoring` | PostgreSQL collection and optional database provisioning |
| `Dashboard` | A dashboard in Tsuga |
| `Monitor` | A monitor in Tsuga |
| `SLO` | A service-level objective in Tsuga |

You can use Kubernetes monitoring, dashboards, monitors, and SLOs independently or together. All resources use the `observability.tsuga.com/v1alpha1` API. See [architecture and ownership](concepts/architecture.md) for the data flow and resource lifecycle.

## Get started

[Install the operator](getting-started/index.md), then choose the feature you want to configure from the sidebar. Installation lists the credentials and dependencies needed for each feature.
