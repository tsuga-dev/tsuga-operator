# Changelog

All notable changes to the Tsuga Operator are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- Kubernetes `Warning` events, collected by the gateway and sent as logs with `WARN` severity, so Tsuga's Kubernetes Events view has data. On by default; turn off with `gateway.kubernetesEvents: false`. The gateway's ClusterRole gains read access to `events`. Installs that manage the operator's own ClusterRole outside this repository's manifests must add `get`, `list`, `watch` on `events`, or the gateway's ClusterRole is rejected. The gateway's ClusterRole only grants what the enabled gateway toggles need; installs with `gateway.clusterMetrics` and `gateway.kubernetesObjects` both `false` now get an events-only gateway unless they also set `gateway.kubernetesEvents: false`.
- Pod placement and sizing per collector, as in the kube-stack chart: `resources`, `nodeSelector`, `tolerations`, `affinity` and `extraEnvs` under `agent`, `gateway`, `prometheus` and `profiling`, with `spec.nodeSelector`, `spec.tolerations`, `spec.affinity` and `spec.resources` as defaults. The per-collector `affinity` is not validated by the CRD (the OTel Operator validates it), which keeps the CRD small enough for client-side `kubectl apply`. `serviceAccount` (`create`, `name`, `annotations`) and `rbac.create` for IRSA and bring-your-own identity. The manager ClusterRole gains `serviceaccounts` access; Installs that manage the operator's ClusterRole themselves, such as the `tsuga-operator` Helm chart, must add `list`, `watch`, `create` and, for the collector account names, `get`, `patch`, `delete` on `serviceaccounts` before upgrading: without them the controller's cache never syncs and the manager exits, stopping Dashboard, Monitor and SLO sync too.
- Opt-in host networking for the agent, `agent.hostNetwork: true`, so collectors on nodes with an IMDSv2 hop limit of 1 can still read EC2 metadata for `resourceDetection`. Its OTLP ports (4317, 4318) and health port (13133) then bind on every node IP with no authentication: anything that can reach a node can send telemetry the agent forwards with your Tsuga ingestion key, so keep those ports closed at the node security group or firewall. Another process holding one of them crash-loops the agent, policy engines that forbid host namespaces reject it, and on most CNIs NetworkPolicy does not apply to host-network pods.
- Opt-in eBPF profiling, `profiling.enabled`. A privileged `tsuga-profiler` DaemonSet runs the OpenTelemetry eBPF profiler 0.161.0 and sends CPU profiles from every process on the node, with no application changes. Requires a collector namespace that admits privileged pods and node kernels 5.10 or newer. `spec.image` does not apply; override with `profiling.image`. Resources default to requests 200m/256Mi and limits 1 CPU/1Gi; set `profiling.resources` to change them.
- Collection tuning from the kube-stack chart on `TsugaCollectorConfig` (see [Tuning collection](docs/site/kubernetes-monitoring/tuning.md)); unset keeps the current rendering. `agent.enabled: false` removes the node agent; `TsugaMonitoring` and `TsugaPostgresMonitoring` then report `Ready=False` with reason `AgentDisabled`, and `TsugaMonitoring` removes its inject annotations and `Instrumentation`, which rolls out auto-instrumented workloads uninstrumented.
- ServiceMonitor and PodMonitor discovery through the Target Allocator (`prometheus.prometheusCR`, requires `prometheus.targetAllocator.enabled`), and `prometheus.annotationDiscovery` (default on) to turn off pod-annotation scraping. The manager ClusterRole gains `get`, `list`, `watch` on `monitoring.coreos.com` `servicemonitors`, `podmonitors`, `probes` and `scrapeconfigs` and on `secrets`, and `create`, `list`, `watch`, `patch`, `delete` on `roles` and `rolebindings` for the scraper's namespaced Secret-read Role; installs that manage that role themselves, including the `tsuga-operator` Helm chart, must add them before enabling `prometheusCR`.
- Export options on `TsugaCollectorConfig`: `export.encoding` (`json` or `proto`) and `export.compression`, per-collector `exportToTsuga: false`, and `extraExporters` with `pipelineExporters` to send to other backends. With these, `spec.export` can be left out for Tsuga-less installs. Unset fields keep the current rendering. See [Other backends](docs/site/kubernetes-monitoring/other-backends.md).
- Cloud and host attributes (`resourceDetection`, off by default) and credential redaction on the agent (`redaction`) on `TsugaCollectorConfig`. See [Tuning collection](docs/site/kubernetes-monitoring/tuning.md).
- Full `Instrumentation` settings on `TsugaMonitoring`: `instrumentation.spec` is deep-merged over the operator's `Instrumentation` (sampler, env, resource, propagators, per-language image and env), and `languages` gains `go`, `apache-httpd` and `nginx`. `exporter.endpoint` stays operator-managed; setting it, or a non-object `exporter`, gives `Ready=False` with reason `RenderFailed`. With `injectExistingWorkloads`, `go` is not injected in bulk because each workload needs `otel-go-auto-target-exe`, so annotate Go workloads by hand. `apache-httpd` and `nginx` export to the agent's OTLP/gRPC port, because their modules only speak gRPC. See [Auto-instrumentation](docs/site/kubernetes-monitoring/auto-instrumentation.md).
- Custom collector config on `TsugaCollectorConfig`, per collector: `extraReceivers`, `extraProcessors`, `extraConnectors`, `extraExtensions`, `extraPipelines`, `pipelineReceivers`, `pipelineProcessors` and `serviceExtensions` add components next to the generated ones, and `customConfig` replaces the collector's whole config. A name that clashes with a generated one gives `Ready=False` with reason `RenderFailed`; a reference to an undefined component, or a connector used on one side only, is left to the collector, which rejects it at startup. `customConfig` keeps env, pod settings, ServiceAccount, RBAC and image, substitutes `__TSUGA_ENDPOINT__`, `__CLUSTER_NAME__` and `__SCRAPE_INTERVAL__`, and ignores every other config field on that collector. Unlike the chart, name clashes and built-in pipeline overrides are rejected instead of silently ignored. See [Custom collector config](docs/site/kubernetes-monitoring/custom-config.md).

### Changed

- **BREAKING** Collectors default to OTel Collector 0.161.0, from 0.157.0. `container.image.tag` is now `container.image.tags`, a list of strings; on metrics it arrives JSON-encoded (`["1.2.3"]`). Queries, monitors and filters on the old key return nothing. If you set `spec.image`, move it to 0.161.0 or newer.
- `kubelet_stats` CPU usage is computed between scrapes: values shift slightly, and nothing is reported on the first scrape after a restart.
- `k8s_attributes` internal metrics moved to `otelcol.k8s.*`. Dashboards on `otelcol_otelsvc_k8s_*` go blank.
- **BREAKING** `spec.clusterName` is required and must not be empty. A TsugaCollectorConfig without it was accepted and exported `k8s.cluster.name: ""`; the API server now rejects it. On Kubernetes before 1.30, an existing config without `clusterName` fails its next update until one is set.
- **BREAKING** Collectors run as operator-created ServiceAccounts named after each collector (`tsuga-agent`, ...) instead of the OTel Operator's `<collector>-collector`. Grants made to the old names need moving. Expect a brief `kubelet_stats` gap on nodes still waiting for the agent rollout, because the role bindings move to the new accounts first.
- Helm installs with `crds.create: false` must apply this release's CRDs before upgrading the chart; on the old schema the API server silently drops the new fields, so `gateway.kubernetesEvents` reads `false`. With the chart's default `crds.create: true`, `helm upgrade` updates them. See [Upgrade](docs/site/operations/lifecycle.md#upgrade).
- **BREAKING** (Go API only): `spec.export` is optional. Go API users now set `Export: &v1alpha1.ExportSpec{...}` (a pointer); YAML is unchanged.

### Fixed

- Span metrics are delta. They were cumulative, which kept re-exporting each series after its spans stopped and showed up as an endless run of zeros.
- The gateway reports `pods` allocatable again, so `k8s.node.allocatable_pods` is available for node pod-capacity alerts.

## [0.2.0] - 2026-10-15

### Added

- `Monitor` `spec.folderId` files a monitor in an existing Tsuga monitor folder.
  When unset, the monitor keeps its current folder.

## [0.1.0] - 2026-10-07

### Added

- First public release: `Dashboard`, `Monitor`, `SLO`, `TsugaCollectorConfig`,
  `TsugaPostgresMonitoring` and `TsugaMonitoring` resources in `observability.tsuga.com/v1alpha1`.
