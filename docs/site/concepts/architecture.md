# Architecture and ownership

The Tsuga operator has two independent responsibilities. It synchronizes Dashboard, Monitor, and SLO custom resources with the Tsuga API, and it translates collection resources into objects understood by the OpenTelemetry Operator.

## Control flow

```text
Your manifests / GitOps
        │
        ▼
Kubernetes API ──► Tsuga operator
                       │
                       ├─ Dashboard / Monitor / SLO ──► Tsuga management API
                       │
                       └─ TsugaCollectorConfig / TsugaMonitoring / TsugaPostgresMonitoring
                                      │
                                      ▼
                          OpenTelemetry custom resources
                                      │
                                      ▼
                          OpenTelemetry Operator
                                      │
                                      ▼
                       Collector pods / injected applications
```

The Tsuga operator does not run collectors itself. The upstream OpenTelemetry Operator creates collector workloads and injects instrumentation when eligible application pods are created.

## Data flow

| Component | Deployment shape | Role |
| --- | --- | --- |
| `tsuga-agent` | DaemonSet | Host and kubelet metrics, pod logs, OTLP receiver for applications |
| `tsuga-gateway` | Deployment | Cluster metrics and Kubernetes objects |
| `tsuga-scraper` | StatefulSet, optional | Prometheus annotation scraping |
| `tsuga-scraper-ta` | TargetAllocator, optional | Distribute scrape targets across scraper replicas |
| `<name>-pg` | Deployment, optional | PostgreSQL metrics forwarded to the agent over OTLP/gRPC port 4317 |

The agent, gateway, and scraper each export to the configured Tsuga OTLP endpoint. The gateway is a cluster-data collector, not an intermediate hop for all agent traffic. Auto-instrumented applications export to the agent's cluster Service on port 4318.

## Resource scope

`Dashboard`, `Monitor`, `SLO`, `TsugaMonitoring`, and `TsugaPostgresMonitoring` are namespaced. A Monitor's Dashboard reference cannot cross namespaces. `TsugaCollectorConfig` is cluster-scoped and places generated collectors in `spec.collectorNamespace` (default `tsuga-operator-system`).

Use one cluster config named `cluster` and one `TsugaMonitoring` resource per namespace. The CRD rejects any other name, because the monitoring controller looks up `cluster` by name and fixed child names make multiple cluster configs unsafe to combine.

## Ownership and deletion

Dashboard, Monitor, and SLO resources use finalizers to delete their remote Tsuga objects before Kubernetes completes deletion. Collection and Instrumentation children use Kubernetes owner references for garbage collection.

Workloads annotated for instrumentation are not owned by TsugaMonitoring. Deleting it therefore does not delete applications or automatically clean their annotations. Disable instrumentation and verify cleanup before deleting the monitoring resource.

Continue with [reconciliation and status](reconciliation.md) for the exact meaning of readiness and current lifecycle limitations.
