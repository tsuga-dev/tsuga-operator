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
| Agent image | `ghcr.io/open-telemetry/opentelemetry-collector-releases/opentelemetry-collector-contrib:0.157.0` |
| Gateway image | `ghcr.io/open-telemetry/opentelemetry-collector-releases/opentelemetry-collector-k8s:0.157.0` |
| Scraper image | Same default as agent |
| Image override | `spec.image` applies to all collector modes; must contain all required components |
| Resource requests | CPU `100m`, memory `128Mi` |
| Resource limits | CPU `500m`, memory `512Mi` |
| Resource override | `spec.resources` replaces defaults for agent, gateway, and scraper |
| Export endpoint | `endpointSecretRef` takes precedence over literal `endpoint`; supply a valid source |
| Secret location | All collection export references resolve in `collectorNamespace` |
| Scraping | Disabled unless enabled; default interval `30s`, replicas `1` |
| Target allocation | Disabled by default; default strategy `consistent-hashing` |
| Application export | Agent Service, OTLP/HTTP port `4318` |

At least one agent signal must stay enabled. Turning both gateway signals off stops rendering the gateway. Omitted optional nested objects do not necessarily receive nested schema defaults; use explicit toggles as in the examples.

Collector images must support the rendered components, including `cumulative_to_delta` (the repository expects v0.157.0 or newer). An arbitrary newer image is not automatically validated; test it before rollout.

## PostgreSQL runtime defaults

The dedicated PostgreSQL collector runs as a single-replica Deployment in the database resource namespace. It uses the agent image default or `cluster.spec.image`, with fixed requests of `50m` CPU / `64Mi` memory and limits of `200m` / `256Mi`. It does not inherit `cluster.spec.resources`. Managed provisioning uses `postgres:17`, a 600-second Job deadline, and a backoff limit of 3. See [DBM](../kubernetes-monitoring/dbm.md) for credentials, TLS, and readiness behavior.

## Repository version pins

| Dependency | Repository pin / requirement | Validation |
| --- | --- | --- |
| Go | `1.26.8` module directive | Build and test toolchain |
| cert-manager | `v1.18.2` | Makefile installation target |
| OpenTelemetry Operator | `v0.159.0` | `make test-otel-schema` validates generated resources against this release's CRDs |
| Collector distributions | `0.157.0` | Explicit images in collection renderer |

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
