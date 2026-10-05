# Example manifests

Download an example, replace placeholders, and follow its guide before applying it. Dashboard, Monitor, and SLO examples use `<YOUR_TEAM_ID>`; collection examples reference a Secret that you must create separately.

| Manifest | Use it with |
| --- | --- |
| [dashboard.yaml](dashboard.yaml) | [Your first dashboard](../dashboards/index.md) |
| [monitor.yaml](monitor.yaml) | [Create a monitor](../monitors/index.md) |
| [slo-event.yaml](slo-event.yaml) | [Event-based SLO](../slos/index.md#create-an-event-based-slo) |
| [slo-time.yaml](slo-time.yaml) | [Time-based SLO](../slos/index.md#create-a-time-based-slo) |
| [collector.yaml](collector.yaml) | [Collect cluster telemetry](../kubernetes-monitoring/getting-started.md) |
| [postgres.yaml](postgres.yaml) | [PostgreSQL with managed provisioning](../kubernetes-monitoring/dbm.md#managed-provisioning) |
| [postgres-manual.yaml](postgres-manual.yaml) | [PostgreSQL with manual provisioning](../kubernetes-monitoring/dbm.md#manual-provisioning) |
| [monitoring.yaml](monitoring.yaml) | [Instrument selected workloads](../kubernetes-monitoring/auto-instrumentation.md) |
| [collector-prometheus.yaml](collector-prometheus.yaml) | [Scrape Prometheus metrics](../kubernetes-monitoring/prometheus.md) |

`postgres.yaml` and `postgres-manual.yaml` are alternative definitions for the same database; choose one.

`collector.yaml` and `collector-prometheus.yaml` are alternative definitions of the same resource, `cluster`. Do not apply the directory wholesale. Choose one and merge changes into your authoritative manifest.

CI validates these files against the checked-in CRD schemas and checks the scraper replica and PostgreSQL provisioning/name constraints. It does not contact Tsuga or verify your team IDs, metric availability, credentials, or API acceptance of opaque visualization/configuration payloads.
