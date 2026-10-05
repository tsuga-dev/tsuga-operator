# Tsuga Operator

Manage Tsuga observability and Kubernetes telemetry as code.

The operator provides six resources in `observability.tsuga.com/v1alpha1`:

| Resource | Scope | Purpose |
| --- | --- | --- |
| `Dashboard` (`tdb`) | Namespaced | Create, update, and delete Tsuga dashboards |
| `Monitor` (`tmon`) | Namespaced | Manage Tsuga monitors, optionally linked to a Dashboard |
| `SLO` (`tslo`) | Namespaced | Manage Tsuga service-level objectives |
| `TsugaCollectorConfig` (`tcc`) | Cluster | Render OpenTelemetry collection resources and supporting RBAC |
| `TsugaPostgresMonitoring` (`tspg`) | Namespaced | Collect PostgreSQL metrics and provision monitoring access |
| `TsugaMonitoring` (`tsmon`) | Namespaced | Configure application auto-instrumentation |

## Documentation

**[Documentation website](https://tsuga-dev.github.io/tsuga-operator/)** · [Browse documentation in this repository](docs/site/index.md)

- [Getting started](docs/site/getting-started/index.md): install the operator.
- [Kubernetes monitoring](docs/site/kubernetes-monitoring/index.md): cluster collection, auto-instrumentation, PostgreSQL monitoring, and Prometheus scraping.
- [Dashboards](docs/site/dashboards/index.md): manage dashboards as code.
- [Monitors](docs/site/monitors/index.md): configure monitors and dashboard links.
- [SLOs](docs/site/slos/index.md): define service-level objectives and alerts.
- Supporting guides cover [troubleshooting](docs/site/operations/troubleshooting.md), [credentials](docs/site/how-to/credentials.md), [GitOps](docs/site/how-to/gitops.md), and [API configuration](docs/site/reference/index.md).

Documentation tracks `main`. For a deployed version, check its [release notes](https://github.com/tsuga-dev/tsuga-operator/releases) and matching source tag.

## Quick orientation

Dashboard, Monitor, and SLO management needs a Tsuga **operation key**. Telemetry collection additionally needs an **OTLP ingestion key**, **OTLP endpoint**, cert-manager, and the pinned **OpenTelemetry Operator v0.159.0**. The Tsuga operator's process requires the operation key even for collection-only use. Create the keys and find your OTLP endpoint in the Tsuga app under Settings.

The Tsuga operator creates OpenTelemetry custom resources; the upstream operator runs the collectors. Use one `TsugaCollectorConfig` named `cluster` and one `TsugaMonitoring` per application namespace. See the [installation guide](docs/site/getting-started/index.md) for the full sequence.

Deleting a Dashboard, Monitor, or SLO CR deletes its remote Tsuga object. Deleting a collection config removes its owned collection resources. Review [cleanup behavior](docs/site/operations/lifecycle.md) before uninstalling.

## Development

See [the development guide](docs/site/contributing/development.md) for Go requirements, local Kind setup, build commands, and tests.

```sh
make help
make test
make test-otel-schema
```

## Work on the documentation

```sh
make docs-setup
make docs-serve
```

Open `http://127.0.0.1:8000`. Run `make docs-build` to check generated API reference, examples, and internal site links. Run `make docs-reference` after changing CRD schemas.

The GitHub Actions workflow validates pull requests and publishes from `main`. One-time setup: select **GitHub Actions** in **Settings → Pages**. See [documentation and publishing](docs/site/contributing/documentation.md) for details.
