# Custom collectors

Use `TsugaCollector` to run your own OpenTelemetry collector, with its own receivers, next to the collectors the operator generates. Typical uses are network device monitoring (SNMP, NetFlow), a dedicated OTLP endpoint for devices outside the cluster, or any receiver the agent does not run.

You write a native `OpenTelemetryCollector` spec under `spec.collector`. The operator renders it as `OpenTelemetryCollector` `tsuga-<name>` in the same namespace and adds only what the toggles below enable. To extend the agent, gateway or scraper themselves, use the `extra*` fields in [Tuning](tuning.md) instead.

## Prerequisites

1. [Install the operator](../getting-started/index.md) with the OpenTelemetry dependencies and CRDs that include `TsugaCollector`.
2. [Configure cluster collection](getting-started.md) with a `TsugaCollectorConfig` named `cluster` and the node agent enabled.
3. Allow traffic from the collector's namespace to `tsuga-agent-collector.<collectorNamespace>.svc.cluster.local` on ports 4317 and 4318.

Resource names must be at most 36 characters.

Creating a `TsugaCollector` is equivalent to creating pods in its namespace, with access to every Secret and ServiceAccount there: `spec.collector` is passed through, including `env`, `volumes`, `serviceAccount` and `image`. Grant it like pod create, not like `TsugaPostgresMonitoring`.

## Create a collector

Save [custom-collector.yaml](../examples/custom-collector.yaml), replace the placeholders, create the `snmp-community` Secret, then apply it:

```yaml
--8<-- "examples/custom-collector.yaml"
```

```sh
kubectl apply -f custom-collector.yaml
kubectl -n observability wait --for=condition=Ready tscol/ndm --timeout=120s
kubectl -n observability get opentelemetrycollector tsuga-ndm
```

Senders reach the collector through the Service the OpenTelemetry Operator creates, `tsuga-<name>-collector.<namespace>.svc.cluster.local` (`tsuga-ndm-collector` above). Ports your receivers listen on that the OpenTelemetry Operator cannot infer, such as NetFlow over UDP, go in `spec.collector.ports`.

## What the toggles add

All three toggles default to `true`. None of them replaces anything your spec already defines.

| Toggle | Adds |
| --- | --- |
| `tsugaExporter` | An `otlp/tsuga-agent` exporter pointing at the node agent's OTLP/gRPC port, appended to every pipeline. The agent adds the Tsuga credentials and cluster enrichment, so no API key is needed in your namespace. |
| `selfTelemetry` | A `service.telemetry.metrics` reader that sends the collector's internal metrics to the agent with delta temporality. Skipped when you set `service.telemetry.metrics` yourself. |
| `defaultProcessors` | `memory_limiter` first and `batch` last in every pipeline. A pipeline that already has a processor of that type, such as `memory_limiter/custom` or `batch/big`, gets none added, and your own `memory_limiter` or `batch` settings are kept. |

A pipeline that exports to a connector which another pipeline of the same signal receives from, such as `forward`, `routing`, `failover` or `round_robin`, is left alone by `tsugaExporter` and `defaultProcessors`, so that data is processed and exported by the pipeline it ends up in. A pipeline feeding a connector that turns it into another signal, such as `spanmetrics`, `count` or `datadog`, still exports its own data.

`selfTelemetry` also sets `service.name: tsuga-<name>`, `service.instance.id` and `k8s.pod.name` on the internal metrics where you have not set them, and adds `POD_UID` and `POD_NAME` environment variables unless you define variables with those names.

The operator also fills `image` (the cluster config's `image`, else the operator's pinned contrib image) and `resources` when your spec leaves them unset, and `podSecurityContext` and `securityContext` when it sets neither. With `mode: sidecar`, the pod-level defaults (`runAsNonRoot`, `seccompProfile`) go on the container instead, because sidecar injection copies only container fields into the app pod.

## Health monitoring

Collector pods are named `tsuga-<name>-collector-*`, so the monitors in [Alert on collector health](alerting.md), which match `context.k8s.pod.name:tsuga-*`, cover them with no changes.

## Status and troubleshooting

```sh
kubectl -n observability get tscol
kubectl -n observability describe tscol ndm
```

| Reason | Meaning |
| --- | --- |
| `ClusterConfigMissing` | There is no `TsugaCollectorConfig` named `cluster`. |
| `AgentDisabled` | `tsugaExporter` or `selfTelemetry` is on and the cluster config disables the node agent. The operator deletes the collector until the agent is back. |
| `RenderFailed` | The spec cannot be rendered: `spec.collector.config` is missing or not an object, there are no pipelines, you defined `otlp/tsuga-agent` yourself, or a pipeline carries a signal the agent does not accept (the agent has no profiles pipeline, and `agent.traces`, `agent.metrics` and `agent.logs` turn signals off). The last collector that rendered keeps running. |
| `ApplyFailed` | An `OpenTelemetryCollector` named `tsuga-<name>` already exists and is not owned by this resource, for example one of the operator's own collectors. Pick another name. |

The operator does not validate the rest of your collector config. The OpenTelemetry Operator and the collector reject invalid config; check `kubectl -n <namespace> logs deploy/tsuga-<name>-collector`.

## Limitations

- Data exported through the agent is enriched by the agent's `k8s_attributes` processor with the collector pod's attributes, not those of the device or service that sent it.
- The agent Service uses `internalTrafficPolicy: Local`, so the collector sends to the agent on its own node. A collector pod scheduled on a node without an agent, for example because of agent tolerations or affinity, cannot export. The same applies to the PostgreSQL collector.
- To export somewhere other than Tsuga, turn `tsugaExporter` off and define your own exporters.
