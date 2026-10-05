# Database monitoring (DBM)

Use `TsugaPostgresMonitoring` to collect PostgreSQL health and query metrics from an in-cluster or managed database. Each resource creates a dedicated OpenTelemetry collector that connects to the database and forwards metrics to the Tsuga agent. Collection includes connections, locks, table statistics and bloat, replication, WAL, vacuum progress, and top-query statistics.

## Prerequisites

1. [Install the operator](../getting-started/index.md) with the OpenTelemetry dependencies and CRDs that include `TsugaPostgresMonitoring`.
2. [Configure cluster collection](getting-started.md) with a `TsugaCollectorConfig` named `cluster` and agent metrics enabled.
3. Allow connections from the resource's namespace to the database port (default `5432`) and to `tsuga-agent-collector.<collectorNamespace>.svc.cluster.local:4317`.
4. Enable `pg_stat_statements` in PostgreSQL's `shared_preload_libraries`, using your database provider's configuration process. Without it, top-query metrics fail even if other collection works.

Use **one resource per PostgreSQL server**. The database role is always `otel_monitor`; two resources targeting the same server can overwrite each other's password. Resource names must be at most 43 characters. The host must be a DNS name or IPv4 address.

The defaults are `database: postgres`, `port: 5432`, and `sslMode: require`. The selected database contains the `otel` schema and monitoring functions. `require` encrypts the connection but does not verify the server certificate. Only `require` and `disable` are supported; use `disable` only when your database connection intentionally has no TLS.

## Managed provisioning

The operator generates the monitoring password and runs a setup Job to create the `otel_monitor` role, `pg_stat_statements` extension, and `otel` functions. The admin role must be a superuser or your provider's admin role (`rds_superuser`, `cloudsqlsuperuser`, or `azure_pg_admin`); `CREATEROLE` and `CREATEDB` alone cannot create the extension or schema. PostgreSQL 13 or later is required.

Create an admin Secret in the same namespace as the resource. This example assumes the application namespace `orders` already exists; replace all placeholders before running it, or provision the Secret through your secret manager.

```sh
kubectl -n orders create secret generic orders-pg-admin \
  --from-literal=username='<DATABASE_ADMIN_USER>' \
  --from-literal=password='<DATABASE_ADMIN_PASSWORD>'
kubectl -n orders label secret orders-pg-admin observability.tsuga.com/postgres-admin=true
```

The operator only runs the setup Job for an admin Secret labeled `observability.tsuga.com/postgres-admin=true`. Without the label, the resource reports `ProvisioningFailed`. The label stops someone who can create a `TsugaPostgresMonitoring` but cannot read Secrets from sending another Secret in the namespace to a host they control. Label only Secrets meant for database provisioning.

Save [postgres.yaml](../examples/postgres.yaml), change the host and namespace to match your database, then apply it:

```yaml
--8<-- "examples/postgres.yaml"
```

```sh
kubectl apply -f postgres.yaml
kubectl -n orders wait --for=condition=Ready tspg/orders-db --timeout=660s
kubectl -n orders get opentelemetrycollector orders-db-pg
kubectl -n orders rollout status deployment/orders-db-pg-collector --timeout=180s
```

The operator creates these resources in `orders`:

| Resource | Purpose |
| --- | --- |
| Secret `orders-db-pg-monitor` | Generated password in the `password` key |
| OpenTelemetryCollector `orders-db-pg` | Dedicated database collector |
| ConfigMap `orders-db-pg-setup` | Database setup SQL |
| Job `orders-db-pg-setup-<hash>` | Run setup and verify the monitoring login |

The collector uses the monitoring password, not the admin credentials. It forwards metrics through the cluster agent, so it does not need its own Tsuga ingestion key.

## Manual provisioning

Choose manual mode when a DBA must apply database changes. No admin Secret or setup Job is needed. The operator still creates the collector and monitoring-password Secret. Switching an existing resource from managed to manual removes its owned setup Job and ConfigMap.

```yaml
--8<-- "examples/postgres-manual.yaml"
```

Save [postgres-manual.yaml](../examples/postgres-manual.yaml), customize it, and apply:

```sh
kubectl apply -f postgres-manual.yaml
```

Have your DBA run the [setup SQL](https://github.com/tsuga-dev/tsuga-operator/blob/main/internal/collection/assets/postgres-setup.sql) from the operator version you deploy against the configured database, then set the `otel_monitor` password to the generated Secret's `password` value. Retrieve that value only in a secure session:

```sh
kubectl -n orders get secret orders-db-pg-monitor -o jsonpath='{.data.password}' | base64 -d
```

Manual mode reports `Ready` after Kubernetes resources are reconciled; it does not verify that the DBA has completed setup. The collector may report authentication or missing-function errors until setup finishes.

## Verify collection and troubleshoot

```sh
kubectl -n orders describe tspg orders-db
kubectl -n orders get jobs -l observability.tsuga.com/postgres-monitoring=orders-db
kubectl -n orders logs deployment/orders-db-pg-collector --tail=100
```

In managed mode, `Ready` means the setup Job completed, not that all metrics reached Tsuga. Check the collector logs and confirm PostgreSQL metrics in Tsuga for the configured server.

| Ready reason | Meaning / next step |
| --- | --- |
| `ClusterConfigMissing` | Create `TsugaCollectorConfig/cluster` |
| `ProvisioningPending` | Setup is still running; inspect the Job and its logs |
| `ProvisioningFailed` | Check the admin Secret exists and has the `observability.tsuga.com/postgres-admin=true` label, then admin credentials, database permissions, network access, and setup SQL errors |

Pending provisioning shows phase `Pending` and failed provisioning shows phase `Error`. Read the Job logs using its full name from the command above:

```sh
kubectl -n orders logs job/orders-db-pg-setup-<hash>
```

After correcting a failed setup, delete its Job to trigger a retry:

```sh
kubectl -n orders delete job -l observability.tsuga.com/postgres-monitoring=orders-db
```

Changing connection settings or the admin Secret **name** creates a new setup Job. Editing the existing admin Secret's contents does not automatically rerun a completed or failed Job. If only top-query collection fails, check `shared_preload_libraries` and the extension in the selected database.

## Rotate the monitoring password

For managed provisioning, delete the generated Secret and explicitly trigger reconciliation. Secret changes alone do not trigger reconciliation.

```sh
kubectl -n orders delete secret orders-db-pg-monitor
kubectl -n orders annotate tspg orders-db rotate="$(date +%s)" --overwrite
```

Wait for the new setup Job to complete, then restart the collector to load the replacement password:

```sh
kubectl -n orders get jobs -l observability.tsuga.com/postgres-monitoring=orders-db
# Use the new Job name shown above.
kubectl -n orders wait --for=condition=Complete job/orders-db-pg-setup-<hash> --timeout=660s
kubectl -n orders rollout restart deployment/orders-db-pg-collector
```

In manual mode, have the DBA set the new password in PostgreSQL before restarting the collector. If managed rotation is interrupted by an operator restart and authentication continues to fail, repeat Secret deletion and annotation to rerun provisioning.

## Permissions and cleanup

The operator can create Secrets and Jobs across namespaces. Although it does not read database Secret values itself, permission to create Jobs allows mounting Secrets or using ServiceAccounts in their namespace, including when resources use manual provisioning. Account for this capability when granting access to the operator and its CRDs.

Deleting a `TsugaPostgresMonitoring` removes its owned collector, generated Secret, ConfigMap, and Jobs. A pre-existing monitoring Secret, setup ConfigMap, or collector that the CR does not control is neither overwritten nor adopted. Database roles, extensions, and the `otel` schema remain; arrange any database cleanup with your DBA. Remove database-monitoring resources before deleting the shared cluster collection config.

If migrating from the `opentelemetry-database-monitoring` Helm chart, the collector now connects over `host:port` rather than running as a database sidecar. No workload annotations or Argo Events are needed. The database default changes from `otel` to `postgres`, and TLS defaults to `require` instead of `disable`.

See the [PostgreSQL API reference](../reference/tsugapostgresmonitoring.md) for every field and [runtime defaults](../reference/configuration.md#postgresql-runtime-defaults) for collector sizing.
