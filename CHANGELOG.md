# Changelog

All notable changes to the Tsuga Operator are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- `TsugaCollectorConfig` `extraRules` on `agent`, `gateway`, `prometheus` and `profiling`, appended to that collector's ClusterRole for components added through `extraReceivers` or `extraExtensions`. The operator must hold any rule it grants: bind rules beyond its ClusterRole to the operator's ServiceAccount, or the reconcile fails with `ApplyFailed`. See [Cluster access](docs/site/kubernetes-monitoring/custom-config.md#cluster-access).
- `TsugaPostgresMonitoring` `sslMode: verify-full`, with `spec.tls` to mount a CA bundle and client certificate from a `secret`, `configMap`, `projected` or `csi` volume at `/etc/tsuga/pg-tls`. Without `caFile`, `verify-full` uses the collector's system roots and needs `provisioning.mode: manual`: the setup Job image has no system CA store, so managed mode requires `caFile`. A `keyFile` gets `defaultMode: 0440` when you set none, and `fsGroup` on the collector (10001) and setup Job (999) pods. Secrets mounted through `spec.tls.volume` need the label `observability.tsuga.com/postgres-credentials=true`. `verify-ca` is not supported. See [TLS](docs/site/kubernetes-monitoring/dbm.md#tls).
- `TsugaPostgresMonitoring` `spec.role`, the monitoring role name, default `otel_monitor` and immutable. The operator sets the comment `managed-by: tsuga-operator` on roles it creates and only changes the password of roles that carry it. See [Roles](docs/site/kubernetes-monitoring/dbm.md#roles).
- `TsugaPostgresMonitoring` `spec.passwordSecretRef`, a Secret with a `password` key for a role the operator did not generate a password for. It needs the label `observability.tsuga.com/postgres-credentials=true`. See [Bring your own role and password](docs/site/kubernetes-monitoring/dbm.md#bring-your-own-role-and-password).
- Role conflicts: two resources with the same host, port and role no longer overwrite each other's password. The oldest keeps the role and the others report phase `Error`, reason `RoleConflict`, with no collector and no setup Job.
- `TsugaPostgresMonitoring` `spec.mode: sidecar` runs the PostgreSQL collector as an OpenTelemetry Operator sidecar in the database pod, as the `opentelemetry-database-monitoring` chart did. Annotate the database pod template with `sidecar.opentelemetry.io/inject: <name>-pg`; the `Ready` message names it. The resource must be in the database's namespace. Sidecar mode takes no `tls`, `verify-full`, `nodeSelector` or `tolerations`, sets the security context on the container, and reads the password Secret as optional so it never blocks the database pod. Changing `mode` recreates the collector. The default, `deployment`, is unchanged. Ship this release's TsugaPostgresMonitoring CRD with the image: the old CRD drops `mode` and renders a Deployment. See [Sidecar mode](docs/site/kubernetes-monitoring/dbm.md#sidecar-mode).
- `TsugaPostgresMonitoring` accepts `spec.image`, `spec.resources`, `spec.nodeSelector` and `spec.tolerations` for its collector. Previously the 256Mi memory limit could not be raised for busy servers. See [Configuration](docs/site/reference/configuration.md#postgresql-runtime-defaults).
- `TsugaCollector` runs your own OpenTelemetry collector from a native `OpenTelemetryCollector` spec, rendered as `tsuga-<name>`. The `tsugaExporter`, `selfTelemetry` and `defaultProcessors` toggles (all on by default) add export through the node agent, internal metrics and `memory_limiter`/`batch` without replacing anything the spec defines. See [Custom collectors](docs/site/kubernetes-monitoring/custom-collectors.md).
- `TsugaPostgresMonitoring` accepts `extraExtensions` and `serviceExtensions`, like the agent, gateway and scraper. See [DBM](docs/site/kubernetes-monitoring/dbm.md#add-collector-extensions).
- The operator starts without an operation key (`api-token` is optional in `tsuga-credentials`), for collection-only installs. Without one it does not start the Dashboard, Monitor and SLO controllers, so those resources keep their status and finalizers until it restarts with a key.

### Changed

- The `otel` schema and function grants go to `pg_monitor` instead of to the monitoring role.
- Manual setup takes the role name: `psql -v role=<name> -f postgres-setup.sql`.
- The setup Job sets the monitoring password with psql's `\password`, so `pg_stat_statements`, readable by every `pg_monitor` member, holds a hash instead of the password.
- The setup Job fails when the monitoring role cannot use the `otel` schema, for example when another admin owns it, instead of completing with collection broken.
- A role without the `managed-by: tsuga-operator` comment gets `pg_monitor` only when the setup Job can log in as it with the Secret's password and a random wrong password is refused. Before, setup granted `pg_monitor` to any role it named, even when the password check then failed.

### Fixed

- Re-running setup no longer removes EXECUTE on `otel.pg_top_queries` from other monitoring roles on the same server.

### Upgrade notes

- Install the `TsugaCollector` CRD and the manager ClusterRole from this release with the image. Without the CRD the operator starts with the `TsugaCollector` controller disabled; with the CRD but without the RBAC, cache sync fails and the operator exits.
- The setup Job re-runs once per resource after the upgrade, because the setup SQL changed.
- An existing `otel_monitor` without the marker comment is adopted when the setup Job can log in as it with the generated password and a random wrong password is refused. Every setup run makes this check, which logs one failed login per run and may show up in auth-failure alerts.
- A DBA-created role under `trust` or `cert` authentication in `pg_hba.conf` no longer gets `pg_monitor` from setup, and the setup Job now fails with `is not in pg_monitor or cannot use the otel schema`. Grant `pg_monitor` to the role by hand, or switch it to password authentication.
- A setup Job that ran as an admin that does not own the `otel` schema used to complete; it now fails with the same message unless `pg_monitor` already has access to the schema. Run setup once as the admin that owns `otel`, or as a superuser, so the grants go to `pg_monitor`.
- If the generated `<name>-pg-monitor` Secret was re-created, so its password no longer matches the role, run `COMMENT ON ROLE otel_monitor IS 'managed-by: tsuga-operator'` as an admin and let setup run again.
- Two resources on one server that both use the default role: after the upgrade the oldest keeps the role and the other becomes `RoleConflict`. The role likely holds the newer resource's password, so the oldest one's setup cannot prove the role is its own and fails with `ProvisioningFailed`, and neither resource collects. To recover, have a DBA run `COMMENT ON ROLE otel_monitor IS 'managed-by: tsuga-operator'`, then delete the oldest resource's setup Job so it runs again and sets its password. `spec.role` is immutable, so recreate the `RoleConflict` resource with a distinct `spec.role`, or point both resources at one `passwordSecretRef` in the same namespace.

## [0.5.1] - 2026-11-02

### Changed

- Collectors default to OTel Collector 0.164.0, from 0.161.0: the agent, scraper and PostgreSQL collectors on the contrib distribution, the gateway on the k8s distribution and the profiler on the eBPF profiler distribution. If you set `spec.image` or `profiling.image`, test the new release before moving to it.
- The PostgreSQL collector config uses the `sql_query` receiver name instead of its deprecated `sqlquery` alias (renamed upstream in 0.159.0). Emitted metrics and logs are unchanged.

## [0.5.0] - 2026-10-30

### Added

- Every `resource.opentelemetry.io/<attr>` pod annotation becomes the `<attr>` resource attribute on the agent, gateway, scraper and profiler, so `deployment.environment.name` and other semantic-convention attributes can be set per workload. See [Tuning collection](docs/site/kubernetes-monitoring/tuning.md#environment-team-and-service-attributes).

### Changed

- **BREAKING** A `resource.opentelemetry.io/<attr>` annotation now wins over an `extraAnnotationsMapping` or `extraLabelMapping` to the same attribute. A mapping that used to set `service.name`, `service.version`, `env` or `team` on a pod that also carries the annotation now loses to it.
- **BREAKING** The `resource.opentelemetry.io/env` pod label no longer sets `env`; `env` comes from your ingestion key's tags. To set the environment per workload, annotate pods with `resource.opentelemetry.io/deployment.environment.name`.

## [0.4.0] - 2026-10-30

### Added

- Example monitors for collector health, [collector-monitors.yaml](docs/site/examples/collector-monitors.yaml): telemetry dropped, export failures, memory limiter refusals, receiver errors, exporter queue fill and missing self-telemetry, per cluster and collector pod. See [Alert on collector health](docs/site/kubernetes-monitoring/alerting.md).
- Node-local Prometheus scraping, `prometheus.nodeLocal: true`: the `tsuga-agent` DaemonSet scrapes annotated pods on its own node, so each target is scraped once with no Target Allocator and no single scraper pod. `tsuga-scraper` is then rendered only for `prometheusCR`. Annotation metrics go through the agent's metrics pipeline, so size `agent.resources` for them; pods on nodes without an agent are not scraped. Requires `agent.enabled` and `agent.metrics`, and is rejected with `agent.customConfig` or `annotationDiscovery: false`. See [Scrape on each node](docs/site/kubernetes-monitoring/prometheus.md#scrape-on-each-node).
- The gateway sends `k8s.container.status.reason`, so monitors can catch containers stuck in `CrashLoopBackOff`, `ImagePullBackOff` or `CreateContainerConfigError`. The metric is experimental upstream and sends 9 points per container per collection, one per known reason, with 1 for the current one. To turn it off, use a gateway `customConfig`.
- `TsugaMonitoring` `spec.injection`: `Namespace`, `Workloads` or `Manual`. `Namespace` puts the inject annotations on the Namespace, so the OpenTelemetry Operator injects pods when they are created and nothing is rolled. Running pods are instrumented when they next restart. It also covers pods from Jobs, CronJobs and bare pods. A pod template opts out with `instrumentation.opentelemetry.io/inject-<language>: "false"`. It is refused in the collector namespace (`Ready=False`, reason `CollectorNamespace`). The manager ClusterRole gains `patch` on `namespaces`. Installs that manage it themselves, including the `tsuga-operator` Helm chart, must add it before upgrading, or `Namespace`-mode TsugaMonitorings go `Ready=False` with reason `WorkloadPatchFailed`. Ship this release's TsugaMonitoring CRD with the image: on the old CRD the API server drops `injection` and defaults `injectExistingWorkloads: true`, so a TsugaMonitoring meant for `Namespace` mode runs in `Workloads` mode and rolls every workload in its namespace. See [Auto-instrumentation](docs/site/kubernetes-monitoring/auto-instrumentation.md). Before rolling back to an earlier operator, set `injection: Manual` on every `Namespace`-mode TsugaMonitoring, or delete them, and wait for them to reconcile. Earlier operators never remove Namespace annotations, so new pods keep being injected while the `Instrumentation` exists, and a TsugaMonitoring recreated later under the same name picks them up again.
- `spec.ownerName` on `Dashboard`, `Monitor` and `SLO`, an alternative to `spec.owner` that takes a team name instead of a team ID. The operator looks the ID up with `GET /v1/teams?name=` when the spec changes and records it in `status.ownerId`; drift resyncs and retries reuse it. Exactly one of `owner` and `ownerName` must be set; `owner` is no longer required. A name with no matching team puts the resource in `Error` and is retried. The operation key needs read access to teams. A team deleted and recreated under the same name gets a new ID, which resources only pick up at their next spec change. Install the updated CRDs with the new image.

### Changed

- **BREAKING** Collector self-metrics (`otelcol_*`) are sent with delta temporality instead of cumulative, and carry `k8s.pod.name`. Queries and monitors that apply `rate` or `increase` to them read wrong; use `sum`, or per-second normalization. Collectors restart once on upgrade to pick up the new config. A query on a bare `otelcol_*` name picks the delta kind once one cluster sends it, so queries about other collectors that still send cumulative counters return nothing: set `metricKind: cumulative-counter` on them.
- **BREAKING** `prometheus.annotationDiscovery` left unset is off when `prometheusCR.enabled` is on, so a pod with both the `prometheus.io/scrape` annotation and a ServiceMonitor or PodMonitor is no longer scraped twice. Configs created with an earlier version and a `prometheus` block store `true` and are unchanged. Any config written without the field, with `prometheusCR` on, stops scraping annotated pods that no monitor selects: a new cluster from an existing manifest, a delete and recreate, `kubectl replace`, or Argo CD `Replace=true`. Set `annotationDiscovery: true` to keep annotation scraping. With `nodeLocal: true` the agent scrapes annotated pods regardless, so do not annotate pods that a monitor selects.
- **BREAKING** The CRD rejects `prometheus.targetAllocator.allocationStrategy: per-node`. On the `tsuga-scraper` StatefulSet it left every target on a node without a scraper replica unscraped. Switch to `consistent-hashing` or `least-weighted`, or use `prometheus.nodeLocal`. Any update that touches the `prometheus` block of such a config is rejected until the strategy is changed, and on Kubernetes before 1.30 any update of the config is.
- `pipelineProcessors` now run just before `batch`, or last in a pipeline without `batch`, so filters and transforms act before data is batched. They used to run after `batch`, as in the kube-stack chart. Filters, transforms and other stateless processors give the same output. On the agent with `redaction` on, they now run before redaction instead of after it. With a redaction config that lists `allowed_keys`, attributes a `pipelineProcessors` transform adds are now dropped unless they are in that list; the default `allow_all_keys: true` is unaffected.
- A `TsugaMonitoring` created without `injection` or `injectExistingWorkloads` now uses `Namespace` mode and no longer rolls every Deployment, StatefulSet and DaemonSet in the namespace. Existing TsugaMonitorings are unchanged: `injectExistingWorkloads` was stored when they were created, and `true` maps to `Workloads`, `false` to `Manual`. `injectExistingWorkloads` is deprecated; set `injection` instead.
- The agent tolerates every taint by default (`operator: Exists`) and keeps off Windows, EKS Fargate and virtual-kubelet nodes through a default node affinity, so applications on tainted nodes (GPU, spot, dedicated pools) have an agent to send to. The agent Service only routes to the agent on the sending pod's node, so their OTLP was dropped before. Both defaults apply only when neither `agent.affinity` nor `spec.affinity` is set, and the toleration only when neither `agent.tolerations` nor `spec.tolerations` is set; `tolerations: []` opts out of it. Configs that set an affinity render as before. On self-managed clusters the agent now also runs on control-plane nodes and collects their pod logs (etcd, kube-apiserver), which adds log volume. Configs without an affinity roll the agent DaemonSet once on upgrade. See [Agent placement](docs/site/kubernetes-monitoring/tuning.md#agent-placement).
- **BREAKING** (Go API only): `TsugaMonitoringSpec.InjectExistingWorkloads` is a `*bool`; YAML is unchanged.

## [0.3.0] - 2026-10-22

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
