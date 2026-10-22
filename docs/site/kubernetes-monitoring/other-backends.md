# Other backends and export options

Every collector sends to Tsuga by default. The fields below add exporters for other backends, choose which signals go where, and turn Tsuga export off per collector. The full field list is in the [API reference](../reference/tsugacollectorconfig.md).

## Adding an exporter

`extraExporters` adds exporters to one collector's config, keyed by component name. `pipelineExporters` then lists which pipelines send to them. Never inline credentials in `extraExporters`: they are stored in plain text in the TsugaCollectorConfig, the OpenTelemetryCollector and the generated ConfigMap. Keep them in a Secret, expose them with `extraEnvs` and `secretKeyRef`, and reference them with `${env:...}`.

```yaml
spec:
  agent:
    extraEnvs:
      - name: BACKUP_TOKEN
        valueFrom:
          secretKeyRef:
            name: backup-backend
            key: token
    extraExporters:
      otlp_http/backup:
        endpoint: https://otlp.backup.example.com
        headers:
          Authorization: Bearer ${env:BACKUP_TOKEN}
    pipelineExporters:
      traces: [otlp_http/backup]
```

The exporter is passed to the collector as written. The Secret must exist in the collector namespace. The operator does not validate the exporter's own settings.

Treat edit rights on TsugaCollectorConfig like read access to Secrets in `collectorNamespace`: exporter configs can send any env value to any endpoint.

Exporters that need an `auth:` extension (`bearertokenauth`, `oauth2client`, `sigv4auth`) cannot be configured yet. Use `headers` with `${env:...}` for bearer tokens.

## Which exporters each collector has

Each collector runs its own distribution (see the image constants in `internal/collection/collector.go`):

- the agent and the scraper run the contrib distribution
- the gateway runs the k8s distribution
- the profiler runs the eBPF profiler distribution

An exporter must exist in that collector's distribution, or the collector fails at startup with an unknown-type error. The component lists are in the [upstream manifests](https://github.com/open-telemetry/opentelemetry-collector-releases/tree/main/distributions). `spec.image` and `profiling.image` can point a collector at another build.

## Routing per signal

`pipelineExporters` accepts `traces`, `metrics`, `logs` and `profiles`. Each name is added to every pipeline named after the signal or starting with `<signal>/`. On the gateway, `logs` therefore also covers the Kubernetes Warning events pipeline (`logs/events`). Each name must be defined in the same collector's `extraExporters` or `extraConnectors`, or be a connector already in the config (such as `span_metrics`); otherwise the collector fails at startup, so check its logs. The names also reach your own `extraPipelines`. Entries for a signal the collector has no pipeline for are ignored (for example `gateway.pipelineExporters.traces`).

```yaml
spec:
  gateway:
    extraExporters:
      otlp_http/backup:
        endpoint: https://otlp.backup.example.com
    pipelineExporters:
      logs: [otlp_http/backup]
      metrics: [otlp_http/backup]
```

Listed exporters are added next to the Tsuga exporter, so the signal goes to both.

## Stopping Tsuga export for one collector

`exportToTsuga: false` removes the Tsuga exporter from that collector, along with its pipeline references and the `TSUGA_API_KEY` and `TSUGA_OTLP_ENDPOINT` environment variables. Every pipeline must then keep at least one exporter, so add `pipelineExporters` for each signal the collector produces.

```yaml
spec:
  gateway:
    exportToTsuga: false
    extraExporters:
      otlp_http/backup:
        endpoint: https://otlp.backup.example.com
    pipelineExporters:
      metrics: [otlp_http/backup]
      logs: [otlp_http/backup]
```

- With `exportToTsuga: false` on the agent and no `pipelineExporters.traces`, the traces pipeline still feeds the span metrics connector. Raw spans are dropped and only span metrics leave the collector. Add `pipelineExporters.traces` to keep spans.
- The collector's own metrics are turned off (`service.telemetry.metrics.level: none`), because they were pushed to Tsuga.

## Tsuga-less installs

Omit `spec.export` and set `exportToTsuga: false` on every enabled collector (`agent`, `gateway`, `prometheus`, `profiling`), each with an extra exporter for its pipelines.

```yaml
apiVersion: observability.tsuga.com/v1alpha1
kind: TsugaCollectorConfig
metadata:
  name: cluster
spec:
  clusterName: my-cluster
  agent:
    exportToTsuga: false
    extraExporters:
      otlp_http/backup:
        endpoint: https://otlp.backup.example.com
    pipelineExporters:
      traces: [otlp_http/backup]
      metrics: [otlp_http/backup]
      logs: [otlp_http/backup]
  gateway:
    exportToTsuga: false
    extraExporters:
      otlp_http/backup:
        endpoint: https://otlp.backup.example.com
    pipelineExporters:
      metrics: [otlp_http/backup]
      logs: [otlp_http/backup]
```

## Encoding and compression

`export.encoding` (`json` or `proto`, default `json`) and `export.compression` (`gzip`, `zlib`, `deflate`, `snappy`, `zstd`, `lz4` or `none`, default `gzip`) apply to the Tsuga exporter of every collector.

```yaml
spec:
  export:
    encoding: proto
    compression: zstd
```

The profiler always sends `proto`, whatever `export.encoding` says: the profiles intake accepts nothing else.

## Errors

A config that breaks these rules is not rendered, and the error names the collector (for example `tsuga-agent`):

- `<collector>: exporter <name> is built in; choose another name`: the name is a connector's, or `otlp_http/tsuga` while `exportToTsuga` is on
- `<collector>: pipeline <name> has no exporter; keep exportToTsuga or add pipelineExporters.<signal>`: a third fix is turning that signal off (for example `agent.traces: false`)
- `<collector>: exportToTsuga needs spec.export (tokenSecretRef and an endpoint)`
