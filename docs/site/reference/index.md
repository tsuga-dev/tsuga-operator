# API reference

All six resources use `apiVersion: observability.tsuga.com/v1alpha1`.

| Kind | Scope | Short name | Purpose |
| --- | --- | --- | --- |
| [Dashboard](dashboard.md) | Namespaced | `tdb` | Manage a Tsuga dashboard |
| [Monitor](monitor.md) | Namespaced | `tmon` | Manage a Tsuga monitor |
| [SLO](slo.md) | Namespaced | `tslo` | Manage a Tsuga service-level objective |
| [TsugaCollectorConfig](tsugacollectorconfig.md) | Cluster | `tcc` | Configure generated collectors and RBAC |
| [TsugaPostgresMonitoring](tsugapostgresmonitoring.md) | Namespaced | `tspg` | Monitor a PostgreSQL server |
| [TsugaMonitoring](tsugamonitoring.md) | Namespaced | `tsmon` | Configure application instrumentation |

The field tables are generated from this repository's CRD schemas. Runtime defaults and limitations are documented separately in [configuration and compatibility](configuration.md) and [reconciliation](../concepts/reconciliation.md).

For the schema actually installed on your cluster:

```sh
kubectl explain dashboard.spec --api-version=observability.tsuga.com/v1alpha1
kubectl explain monitor.spec --api-version=observability.tsuga.com/v1alpha1
kubectl explain slo.spec --api-version=observability.tsuga.com/v1alpha1
kubectl explain tsugacollectorconfig.spec --api-version=observability.tsuga.com/v1alpha1
kubectl explain tsugapostgresmonitoring.spec --api-version=observability.tsuga.com/v1alpha1
kubectl explain tsugamonitoring.spec --api-version=observability.tsuga.com/v1alpha1
```

Dashboard `visualization`, Monitor `configuration`, and SLO SLI/alert `configuration` are opaque JSON payloads passed to Tsuga. Their inner fields are validated by Tsuga rather than the CRDs. Consult the repository's [OpenAPI contract](https://github.com/tsuga-dev/tsuga-operator/blob/main/public-open-api.json) for those payload shapes.
