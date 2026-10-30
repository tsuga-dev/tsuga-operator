# Custom collector config

The operator renders each collector's config from your `TsugaCollectorConfig`. When a field you need does not exist, add your own components next to the generated ones, or replace the whole config. These fields exist on `agent`, `gateway`, `prometheus` and `profiling`. The full field list is in the [API reference](../reference/tsugacollectorconfig.md).

## Adding components

| Field | Adds to the collector config |
|---|---|
| `extraReceivers` | `receivers` |
| `extraProcessors` | `processors` |
| `extraConnectors` | `connectors` |
| `extraExtensions` | `extensions` |
| `extraPipelines` | `service.pipelines` |

Each field is a map keyed by component or pipeline name, with the component's own settings as the value, exactly as it would appear in a collector config. For exporters, see [Other backends](other-backends.md). The operator does not validate the settings of a component. The component must exist in that collector's distribution, or the collector fails at startup with an unknown-type error.

An added component does nothing until a pipeline uses it. Wire it with one of:

- `extraPipelines`, which adds whole pipelines.
- `pipelineReceivers`, `pipelineProcessors` and `pipelineExporters`, which take `traces`, `metrics`, `logs` and `profiles` lists. Each name is added to every pipeline named after the signal or starting with `<signal>/`, which includes your own `extraPipelines` such as `logs/audit`. `pipelineExporters` names an entry in `extraExporters` or `extraConnectors`, or a connector already in the config such as `span_metrics`. Processors are inserted just before `batch` in each matching pipeline, or appended last in a pipeline without `batch`, so filters and transforms run before data is batched. The kube-stack chart appends them after `batch` instead. Names already in a pipeline are skipped.
- `serviceExtensions`, which appends extension names to `service.extensions`.

## Reading host files

The agent mounts the host read-only at `/hostfs`, plus `/var/log/pods` and `/var/lib/docker/containers` when `agent.collectLogs` is on (the default) or `agent.addLogsVolumes` is `true`. A file-based receiver can read only under those paths. A host file `/var/log/audit/audit.log` is `/hostfs/var/log/audit/audit.log` inside the agent.

```yaml
spec:
  agent:
    extraReceivers:
      file_log/audit:
        include: [/hostfs/var/log/audit/audit.log]
        start_at: end
    extraPipelines:
      logs/audit:
        receivers: [file_log/audit]
        processors: [batch]
        exporters: [otlp_http/tsuga]
```

The exporter name `otlp_http/tsuga` is the operator's Tsuga exporter. It is present while the collector exports to Tsuga (see [Other backends](other-backends.md)).

## Dropping health-check spans

```yaml
spec:
  agent:
    extraProcessors:
      filter/health:
        error_mode: ignore
        traces:
          span:
            - attributes["http.route"] == "/healthz"
    pipelineProcessors:
      traces: [filter/health]
```

## Connectors

A connector is both an exporter in one pipeline and a receiver in another. Wire both sides, or the collector fails to start.

```yaml
spec:
  agent:
    extraConnectors:
      count: {}
    extraPipelines:
      traces/count:
        receivers: [otlp]
        exporters: [count]
      metrics/count:
        receivers: [count]
        exporters: [otlp_http/tsuga]
```

## Errors

A problem sets `Ready=False` with reason `RenderFailed`. The message starts with the collector name, then one of:

- `<kind> <name> is built in; choose another name`, where kind is `receiver`, `processor`, `connector` or `extension`. The name is already in the generated config. A connector also clashes with a receiver or exporter of that name, and a receiver with a connector of that name.
- `pipeline <name> is built in; choose another name`. An `extraPipelines` name matches a generated pipeline such as `logs`.
- `pipeline <name> needs receivers and exporters`. An `extraPipelines` entry is not an object, or its `receivers` list is empty or missing. An empty `exporters` list gives `pipeline <name> has no exporter`.
- `customConfig has no service.pipelines`.

A reference to a component the collector does not define, or a connector wired on one side only, is not caught at render time: the collector rejects the config at startup and crash-loops. Check its logs.

## What the operator does not grant

- A component that needs Kubernetes API access needs RBAC that you bind to the collector's ServiceAccount: `tsuga-agent`, `tsuga-gateway`, `tsuga-scraper` or `tsuga-profiler`, unless `serviceAccount.create` is `false`.
- Cluster-scoped receivers such as `k8s_cluster`, `k8s_objects` and `k8s_events` belong on the gateway, which already reads cluster state.
- A listening receiver on the agent binds on the node while `hostNetwork` is on.

## Coming from the kube-stack chart

- A name clash is an error here. The chart silently keeps its own component.
- A built-in pipeline cannot be replaced through `extraPipelines`. Use `customConfig` or the dedicated field.
- Pipeline lists match by signal, so `logs` also covers `logs/events` and your own `logs/*` pipelines. The chart matches the exact pipeline name.
- There is one `TsugaCollectorConfig` per cluster, so the chart's pattern of installing it twice has no equivalent. Each reason to do that has a field:
    - A second backend: `extraExporters` and `pipelineExporters` (see [Other backends](other-backends.md)).
    - Only some collectors: `agent.enabled`, `prometheus.enabled` and `profiling.enabled`. The gateway runs while any of `gateway.clusterMetrics`, `gateway.kubernetesObjects` or `gateway.kubernetesEvents` is on.
    - A collector set up differently: the extra components above, or `customConfig`.

## Replacing the config

`customConfig` is the collector's entire config. The operator uses it as written, so the collector runs only what you define.

```yaml
spec:
  prometheus:
    customConfig:
      receivers:
        otlp:
          protocols:
            grpc:
              endpoint: 0.0.0.0:4317
      processors:
        batch: {}
      exporters:
        otlp_http/tsuga:
          endpoint: __TSUGA_ENDPOINT__
          headers:
            Authorization: Bearer ${env:TSUGA_API_KEY}
      service:
        pipelines:
          traces:
            receivers: [otlp]
            processors: [batch]
            exporters: [otlp_http/tsuga]
```

Still applied:

- Pod settings, ServiceAccount, RBAC, `extraEnvs` and the image.
- The operator's mode settings for each collector:
    - `agent`: `hostNetwork` (default `false`), the `/hostfs` mount and the log host mounts.
    - `profiling`: `hostPID`, the privileged root user, the sysfs, tracefs and modules mounts, and the profiles feature gate.
    - `gateway`: one replica.
    - `prometheus` (the scraper): the Target Allocator wiring when enabled. It needs a `prometheus` receiver in your custom config.
- The collector's env: the pod-metadata variables, and while it exports to Tsuga (`exportToTsuga` not `false`) `TSUGA_API_KEY`, plus `TSUGA_OTLP_ENDPOINT` when `export.endpointSecretRef` is set. `${env:TSUGA_API_KEY}` then resolves.
- `__TSUGA_ENDPOINT__`, `__CLUSTER_NAME__` and `__SCRAPE_INTERVAL__` are replaced in the config text.

Ignored: every other config field on that collector, such as `kubeletStats`, `resourceDetection`, `redaction`, `export.encoding`, `export.compression`, `extraReceivers`, `pipelineExporters` and `serviceExtensions`.

`export.tokenSecretRef` and the endpoint settings still feed `TSUGA_API_KEY`, `TSUGA_OTLP_ENDPOINT` and `__TSUGA_ENDPOINT__`. For a `customConfig` that does not send to Tsuga, set that collector's `exportToTsuga: false`; otherwise `spec.export` must be set with an endpoint, or the render fails.

A collector is still created only when its own switch is on (for example `prometheus.enabled`), and a config with no pipelines fails to render.

The operator no longer guarantees that a `customConfig` works. It does not check references, and a collector with a bad config fails at startup. Other collectors keep their generated config. Features that depend on the generated config (for example `TsugaMonitoring` expecting the agent's OTLP ports) are your responsibility.
