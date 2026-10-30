# Configuration and compatibility

## Operator environment

| Variable | Required | Default | Use |
| --- | --- | --- | --- |
| `TSUGA_API_TOKEN` | Yes | None | Raw operation key; do not prefix with `Bearer` |
| `TSUGA_BASE_URL` | No | `https://api.tsuga.com` | Tsuga management API base URL; must use `https://` or the operator exits at startup |

The deployed manager reads `api-token` from `tsuga-credentials`. Collection-only usage still requires the operation key at startup.

## Collection runtime defaults

| Setting | Default / behavior |
| --- | --- |
| Collection config name | Use `cluster`; required by the monitoring controller's lookup |
| Collector namespace | `tsuga-operator-system`; create it before using a custom namespace |
| Agent image | `ghcr.io/open-telemetry/opentelemetry-collector-releases/opentelemetry-collector-contrib:0.161.0` |
| Gateway image | `ghcr.io/open-telemetry/opentelemetry-collector-releases/opentelemetry-collector-k8s:0.161.0` |
| Scraper image | Same default as agent |
| Profiler image | `ghcr.io/open-telemetry/opentelemetry-collector-releases/opentelemetry-collector-ebpf-profiler:0.161.0`; override with `profiling.image` |
| Profiling | Disabled unless enabled; samples at 20 Hz |
| Image override | `spec.image` applies to the agent, gateway and scraper; must contain all required components. The profiler uses `profiling.image` |
| Resource requests | CPU `100m`, memory `128Mi` |
| Resource limits | CPU `500m`, memory `512Mi` |
| Resource override | `spec.resources` is the default for agent, gateway and scraper; `agent.resources`, `gateway.resources`, `prometheus.resources` replace it per collector |
| Profiler resources | Requests `200m` / `256Mi`, limits `1` / `1Gi`; replace with `profiling.resources`. `spec.resources` does not apply |
| Scheduling | `spec.nodeSelector`, `spec.tolerations`, `spec.affinity` apply to every collector; the same fields under `agent`, `gateway`, `prometheus` or `profiling` replace them for that collector. When no affinity is set, the agent keeps off Windows, EKS Fargate and virtual-kubelet nodes and, unless tolerations are set (`[]` opts out), tolerates every taint ([Agent placement](../kubernetes-monitoring/tuning.md#agent-placement)) |
| Extra env | `extraEnvs` per collector, added after the operator's variables |
| Agent host network | Off by default (`agent.hostNetwork`); when on, OTLP ports bind on the node IP |
| ServiceAccounts | One per collector, named after it (`tsuga-agent`, ...), annotated with `serviceAccount.annotations`. The profiler's account exists only while profiling is enabled. `serviceAccount.create: false` runs everything as `serviceAccount.name` |
| RBAC | Per-collector ClusterRoles created unless `rbac.create: false` |
| Export encoding | `export.encoding` defaults to `json`; the profiler always uses `proto` |
| Export compression | `export.compression` defaults to `gzip` |
| Export optional | `spec.export` may be omitted when no collector exports to Tsuga (`exportToTsuga: false` on each); see [Other backends](../kubernetes-monitoring/other-backends.md) |
| Export endpoint | `endpointSecretRef` takes precedence over literal `endpoint`; supply a valid source |
| Custom components | `extraReceivers`, `extraProcessors`, `extraConnectors`, `extraExtensions`, `extraPipelines` per collector; names that clash with generated ones fail with `RenderFailed`; references to undefined components fail at collector startup. `customConfig` replaces the whole config and ignores every other config field on that collector. See [Custom collector config](../kubernetes-monitoring/custom-config.md) |
| Resource detection | Off unless `resourceDetection.enabled`; detectors `[env]`, timeout `15s`, `override: false`; applies to the agent, gateway, scraper and profiler. See [Tuning collection](../kubernetes-monitoring/tuning.md) |
| Pod resource attributes | Every `resource.opentelemetry.io/<attr>` pod annotation becomes `<attr>`; the `resource.opentelemetry.io/team` pod label becomes `team`. See [Tuning collection](../kubernetes-monitoring/tuning.md#environment-team-and-service-attributes) |
| Redaction | Off unless `redaction.enabled`; agent only, before `batch`; built-in credential rules unless `redaction.config` is set, which replaces them |
| Secret location | All collection export references resolve in `collectorNamespace` |
| Scraping | Disabled unless enabled; default interval `30s`, replicas `1` |
| Target allocation | Disabled by default; default strategy `consistent-hashing` |
| Annotation discovery | On by default (`prometheus.annotationDiscovery`), off by default when `prometheusCR.enabled` is on; scrapes pods with `prometheus.io/scrape: "true"` |
| Node-local scraping | Off by default (`prometheus.nodeLocal`); the agent scrapes annotated pods on its own node instead of `tsuga-scraper` |
| Prometheus Operator discovery | Off by default (`prometheus.prometheusCR.enabled`); requires `prometheus.targetAllocator.enabled` and the Prometheus Operator CRDs |
| Agent OTLP endpoints | `${env:MY_POD_IP}:4317` (gRPC), `${env:MY_POD_IP}:4318` (HTTP); `""` disables a protocol |
| Health check | `${env:MY_POD_IP}:13133` on every collector; `healthCheckEndpoint: ""` removes it |
| Host metrics | Interval `10s` |
| Kubelet stats | Interval `20s`, ServiceAccount token auth, `insecureSkipVerify: true`, groups `node`, `pod`, `container`, `volume`, `/pods` endpoint on (`usePodsEndpoint: true`) |
| Gateway cluster metrics | Interval `10s`; node conditions `Ready`, `MemoryPressure`, `DiskPressure`, `PIDPressure`; allocatable `cpu`, `memory`, `ephemeral-storage`, `pods` |
| Batch | `sendBatchSize` 5000, `sendBatchMaxSize` 5000 |
| Span metrics | On, `AGGREGATION_TEMPORALITY_DELTA`, dimensions `http.request.method` (default `GET`), `http.response.status_code`, `http.route` |
| Node agent | On (`agent.enabled: true`); see [Tuning](../kubernetes-monitoring/tuning.md) |
| Application export | Agent Service, OTLP/HTTP port from `agent.otlp.httpEndpoint` (default `4318`) |

At least one agent signal must stay enabled. Turning all three gateway signals off stops rendering the gateway. Omitted optional nested objects do not necessarily receive nested schema defaults; use explicit toggles as in the examples.

Collector images must support the rendered components and emit `container.image.tags` (the repository expects v0.161.0 or newer). An arbitrary newer image is not automatically validated; test it before rollout.

## PostgreSQL runtime defaults

The dedicated PostgreSQL collector runs as a single-replica Deployment in the database resource namespace. It uses the agent image default or `cluster.spec.image`, with fixed requests of `50m` CPU / `64Mi` memory and limits of `200m` / `256Mi`. It does not inherit `cluster.spec.resources`. Managed provisioning uses `postgres:17`, a 600-second Job deadline, and a backoff limit of 3. See [DBM](../kubernetes-monitoring/dbm.md) for credentials, TLS, and readiness behavior.

## Repository version pins

| Dependency | Repository pin / requirement | Validation |
| --- | --- | --- |
| Go | `1.27.2` module directive | Build and test toolchain |
| cert-manager | `v1.18.2` | Makefile installation target |
| OpenTelemetry Operator | `v0.160.0` | `make test-otel-schema` validates generated resources against this release's CRDs |
| Collector distributions | `0.161.0` | Explicit images in collection renderer |

The repository does not declare a general Kubernetes support matrix. Kubernetes Go library versions and envtest versions are not a support guarantee. Validate your cluster and admission policies in staging.

## Manager flags

| Flag | Binary default | Purpose |
| --- | --- | --- |
| `--leader-elect` | `false` | Single active controller manager; enabled in the Deployment |
| `--health-probe-bind-address` | `:8081` | `/healthz` and `/readyz` probes |
| `--metrics-bind-address` | `0` (disabled) | Default Kustomize overlay enables `:8443` |
| `--metrics-secure` | `true` | Serve metrics over HTTPS with authentication/authorization |
| `--enable-http2` | `false` | Enable HTTP/2 for metrics and webhook servers |

See [cmd/main.go](https://github.com/tsuga-dev/tsuga-operator/blob/main/cmd/main.go) for certificate and logging flags. Deployed arguments can override binary defaults.
