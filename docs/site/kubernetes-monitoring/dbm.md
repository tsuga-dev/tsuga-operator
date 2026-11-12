# Database monitoring (DBM)

Use `TsugaPostgresMonitoring` to collect PostgreSQL health and query metrics from an in-cluster or managed database. Each resource creates a dedicated OpenTelemetry collector that connects to the database and forwards metrics to the Tsuga agent. Collection includes connections, locks, table statistics and bloat, replication, WAL, vacuum progress, and top-query statistics.

## Prerequisites

1. [Install the operator](../getting-started/index.md) with the OpenTelemetry dependencies and CRDs that include `TsugaPostgresMonitoring`.
2. [Configure cluster collection](getting-started.md) with a `TsugaCollectorConfig` named `cluster` and agent metrics enabled.
3. Allow connections from the resource's namespace to the database port (default `5432`) and to `tsuga-agent-collector.<collectorNamespace>.svc.cluster.local:4317`.
4. Enable `pg_stat_statements` in PostgreSQL's `shared_preload_libraries`, using your database provider's configuration process. Without it, top-query metrics fail even if other collection works.

Resource names must be at most 43 characters. The host must be a DNS name or IPv4 address.

The defaults are `database: postgres`, `port: 5432`, `sslMode: require` and `role: otel_monitor`. The selected database contains the `otel` schema and monitoring functions. See [TLS](#tls) for `sslMode` and [Roles](#roles) for `role`.

## Roles

The collector connects as the role in `spec.role`, default `otel_monitor`. The role name is immutable. To change it, delete the resource and create a new one.

The operator only manages roles it created. When setup creates a role, it puts the comment `managed-by: tsuga-operator` on it, and only a role with that comment gets its password changed. A role without the comment belongs to your DBA: setup never alters its password, so the password in the Secret the collector reads must already match it. If it does not, the setup Job fails.

Use **one resource per role on a server**. Two resources with the same host, port and role would overwrite each other's password. The operator does not allow it: the oldest resource keeps the role, and the others get phase `Error` with Ready reason `RoleConflict`, no collector and no setup Job. Fix it with a distinct `spec.role` on each resource, or have the resources share one `passwordSecretRef` in the same namespace. Hosts are compared ignoring case. A host without a dot, such as `postgres`, is the Service in the resource's namespace, and `<svc>.<ns>`, `<svc>.<ns>.svc` and `<svc>.<ns>.svc.cluster.local` count as one host. Other DNS names, or an IP and a DNS name for one server, are not detected.

When upgrading from an operator without role conflict detection, two existing resources with the default role on one server can leave no collector working: the oldest keeps the role, but the role holds the newer resource's password, so the oldest one's setup fails with `ProvisioningFailed` while the newer one reports `RoleConflict`. To recover, have a DBA run `COMMENT ON ROLE otel_monitor IS 'managed-by: tsuga-operator'` and delete the oldest resource's setup Job so it runs again and sets its password. Then recreate the `RoleConflict` resource with a distinct `spec.role`, or point both resources at one `passwordSecretRef` in the same namespace.

Each setup run first checks that it can log in as the role with the Secret's password and that a random wrong password is refused. That check logs one failed login on the server per setup run. An existing `otel_monitor` without the comment is adopted only when the check passes.

Setup sets the password with psql's `\password`, which sends the server a SCRAM hash instead of the password, so `pg_stat_statements`, which every `pg_monitor` member can read, never holds it. A server with `password_encryption = md5` stores an MD5 hash instead, which `md5` authentication accepts in place of the password.

A role with the comment is granted `pg_monitor`. A role without it is granted `pg_monitor` only when the login check passed, so naming someone else's role with a wrong password grants it nothing. Access to the `otel` schema and functions is granted to `pg_monitor` too, so every monitoring role on the server can read them, whichever resource ran setup last. The schema and functions belong to the admin that first ran setup on that database. A setup run by an admin that is neither a superuser nor a member of that role leaves them as they are and prints `otel schema belongs to a role this admin does not hold`. The Job then fails, after checking that the role can use the `otel` schema, unless an earlier setup already granted the schema to `pg_monitor`. Function updates in later releases are applied only by the first admin's setup.

## TLS

`sslMode` applies to the collector and the setup Job.

| `sslMode` | Behavior |
| --- | --- |
| `disable` | No TLS. Use only when the connection intentionally has no TLS. |
| `require` (default) | Encrypts the connection and does not verify the server certificate. |
| `verify-full` | Verifies the certificate chain and the host name against `tls.caFile`. Without `caFile`, the collector uses its system roots; this needs `provisioning.mode: manual`, because the setup Job image has no system CA store. |

`verify-ca` is not supported: the collector's PostgreSQL receiver cannot verify the chain without also checking the host name.

`spec.tls` mounts one volume read-only at `/etc/tsuga/pg-tls` in the collector and the setup Job. The volume is a `secret`, `configMap`, `projected` or `csi` source, with exactly one set. `caFile`, `certFile` and `keyFile` are paths inside the volume. `caFile` needs `verify-full`, `certFile` and `keyFile` must be set together, and `tls` needs `caFile` or `certFile`. `tls` is not allowed with `sslMode: disable`.

Every Secret the volume mounts, as a `secret` source or inside a `projected` one, must carry the label `observability.tsuga.com/postgres-credentials=true`, for the same reason as the admin Secret label. Without it the resource reports `ProvisioningFailed`. ConfigMaps and `csi` sources need no label.

```sh
kubectl -n orders label secret orders-pg-ca observability.tsuga.com/postgres-credentials=true
```

When a `keyFile` is set, the operator sets `defaultMode: 0440` on a `secret`, `configMap` or `projected` volume that has no `defaultMode`, and sets `fsGroup` on the pods: 10001 for the collector and 999 for the setup Job. A `csi` driver must provide a key readable by that group.

A CA in a Secret, for example the `ca.crt` key that cert-manager writes. Save [postgres-verify-full.yaml](../examples/postgres-verify-full.yaml) and change the host and names:

```yaml
--8<-- "examples/postgres-verify-full.yaml"
```

A CA in a ConfigMap, for example the RDS global bundle:

```yaml
spec:
  host: orders.abc123.eu-west-1.rds.amazonaws.com
  sslMode: verify-full
  tls:
    volume:
      configMap:
        name: rds-global-bundle
    caFile: global-bundle.pem
```

System roots, for a server whose certificate a public CA signed. Leave `tls` out. This works only with manual provisioning; with managed provisioning, mount a public CA bundle from a `configMap` as `caFile`, as in the RDS example above:

```yaml
spec:
  host: orders.example.com
  sslMode: verify-full
  provisioning:
    mode: manual
```

Mutual TLS, with a CA and a client certificate:

```yaml
spec:
  host: orders-pg-rw.orders.svc
  sslMode: verify-full
  tls:
    volume:
      secret:
        secretName: orders-pg-tls
    caFile: ca.crt
    certFile: tls.crt
    keyFile: tls.key
```

The setup Job presents the same client certificate on its admin connection. A server that requires certificate authentication matching the user name rejects it for the admin user; use manual provisioning in that case.

## Managed provisioning

The operator generates the monitoring password and runs a setup Job to create the monitoring role, `pg_stat_statements` extension, and `otel` functions. The admin role must be a superuser or your provider's admin role (`rds_superuser`, `cloudsqlsuperuser`, or `azure_pg_admin`); `CREATEROLE` and `CREATEDB` alone cannot create the extension or schema. PostgreSQL 13 or later is required.

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
| Secret `orders-db-pg-monitor` | Generated password in the `password` key. Not created with `passwordSecretRef`. |
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

Have your DBA run the [setup SQL](https://github.com/tsuga-dev/tsuga-operator/blob/main/internal/collection/assets/postgres-setup.sql) from the operator version you deploy against the configured database, then set the monitoring role's password to the generated Secret's `password` value. Run the script as the admin role with `psql -v role=<name> -f postgres-setup.sql`; `role` defaults to `otel_monitor`. Retrieve the password only in a secure session:

```sh
kubectl -n orders get secret orders-db-pg-monitor -o jsonpath='{.data.password}' | base64 -d
```

With `passwordSecretRef`, there is no generated Secret: the DBA creates the role with the password from your Secret. See [Bring your own role and password](#bring-your-own-role-and-password).

Manual mode reports `Ready` after Kubernetes resources are reconciled; it does not verify that the DBA has completed setup. The collector may report authentication or missing-function errors until setup finishes.

## Sidecar mode

Set `mode: sidecar` to run the collector inside the database pod instead of as its own Deployment, so it connects to its own pod rather than over the network. The operator renders `OpenTelemetryCollector` `orders-db-pg` in sidecar mode, and the OpenTelemetry Operator injects it into pods annotated with:

```yaml
sidecar.opentelemetry.io/inject: orders-db-pg
```

Add the annotation to the database pod template; the `Ready` condition message repeats it. Pods pick up the sidecar when they are next created, so restart the database after adding it.

Create the resource in the database's namespace: the sidecar reads the monitoring password Secret from the pod's namespace. The collector connects to the pod on `port`, so the database must listen on the pod's address, and `sslMode` applies to that connection. `host` still sets `server.address` and is where the managed setup Job connects. The setup Job reruns only when its inputs change, not when the database pod restarts, so a database without persistent storage loses `otel_monitor` on restart; rerun provisioning as in [Rotate the monitoring password](#rotate-the-monitoring-password).

The sidecar runs inside the database pod, so a mistake in its settings could keep the database from starting. Sidecar mode therefore takes only `sslMode: disable` or `require`, with no `tls`: the sidecar connects by pod name, which the server certificate does not cover. `nodeSelector` and `tolerations` are rejected, since the database pod's own settings apply. `extraExtensions` and `serviceExtensions` are rejected too, since an extension the collector cannot start would crash it. A sidecar that still fails, from a bad `image` or an OOM kill under its `resources` limit, marks the database pod NotReady and removes it from its Service endpoints. With a bad `image` and a native sidecar (Kubernetes 1.29+), a restarted database pod does not start at all. The sidecar reads the password Secret as optional, so deleting the resource leaves the database pod able to restart, with a sidecar that fails to log in until you remove the annotation and restart the pod. Changing `mode` on an existing resource deletes the collector and creates it again.

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
| `RoleConflict` | Another resource uses the same role on this server. Phase is `Error`. Set a distinct `spec.role` or share one `passwordSecretRef`. See [Roles](#roles) |
| `ProvisioningFailed` | Check the admin Secret exists and has the `observability.tsuga.com/postgres-admin=true` label, then admin credentials, database permissions, network access, and setup SQL errors. If the role already exists without the `managed-by: tsuga-operator` comment, the Secret's password must match it |

Pending provisioning shows phase `Pending` and failed provisioning shows phase `Error`. Read the Job logs using its full name from the command above:

```sh
kubectl -n orders logs job/orders-db-pg-setup-<hash>
```

After correcting a failed setup, delete its Job to trigger a retry:

```sh
kubectl -n orders delete job -l observability.tsuga.com/postgres-monitoring=orders-db
```

Changing connection settings, `role`, `tls` or the admin or password Secret **name** creates a new setup Job. Editing the existing admin Secret's contents does not automatically rerun a completed or failed Job. If only top-query collection fails, check `shared_preload_libraries` and the extension in the selected database.

## Bring your own role and password

Use `spec.passwordSecretRef` when your DBA owns the role or the server needs a password the operator did not generate. The Secret must be in the resource's namespace, hold the password in the `password` key, and carry the label `observability.tsuga.com/postgres-credentials=true`. Without the label the resource reports `ProvisioningFailed`, for the same reason as the admin Secret label.

```sh
kubectl -n orders create secret generic orders-pg-monitor-credentials \
  --from-literal=password='<MONITORING_ROLE_PASSWORD>'
kubectl -n orders label secret orders-pg-monitor-credentials observability.tsuga.com/postgres-credentials=true
```

```yaml
spec:
  host: orders-pg-rw.orders.svc
  role: tsuga_monitor
  passwordSecretRef:
    name: orders-pg-monitor-credentials
  provisioning:
    mode: manual
```

The operator does not generate a `<name>-pg-monitor` Secret and the collector reads yours. With managed provisioning, setup creates the role if it is missing and sets your password on it. It does not alter a role that exists without the operator comment, and the Job fails if your password does not match that role. Switching an existing resource to `passwordSecretRef` leaves the old generated `<name>-pg-monitor` Secret in place until the resource is deleted.

## Rotate the monitoring password

This procedure applies only to roles the operator created. For managed provisioning, delete the generated Secret and explicitly trigger reconciliation. Secret changes alone do not trigger reconciliation.

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

With `passwordSecretRef`, the DBA runs `ALTER ROLE` in PostgreSQL and then updates the Secret; the operator does not change the password. Restart the collector afterwards. In sidecar mode, restart the database pod instead of the collector Deployment. In manual mode, have the DBA set the new password in PostgreSQL before restarting the collector. If managed rotation is interrupted by an operator restart and authentication continues to fail, repeat Secret deletion and annotation to rerun provisioning.

## Add collector extensions

`extraExtensions` adds extensions to the PostgreSQL collector's config, keyed by component name, and `serviceExtensions` enables them. For example, to report the collector to an OpAMP server:

```yaml
spec:
  extraExtensions:
    opamp:
      server:
        ws:
          endpoint: ws://<OPAMP_SERVER>:4320/v1/opamp
  serviceExtensions: [opamp]
```

A name the collector already defines is a render error.

## Permissions and cleanup

The operator can create Secrets and Jobs across namespaces. Although it does not read database Secret values itself, permission to create Jobs allows mounting Secrets or using ServiceAccounts in their namespace, including when resources use manual provisioning. Account for this capability when granting access to the operator and its CRDs.

Deleting a `TsugaPostgresMonitoring` removes its owned collector, generated Secret (a `passwordSecretRef` Secret is yours and stays), ConfigMap, and Jobs. A pre-existing monitoring Secret, setup ConfigMap, or collector that the CR does not control is neither overwritten nor adopted. Database roles, extensions, and the `otel` schema remain; arrange any database cleanup with your DBA. Remove database-monitoring resources before deleting the shared cluster collection config.

If migrating from the `opentelemetry-database-monitoring` Helm chart, the collector connects over `host:port` by default; set `mode: sidecar` to keep the chart's sidecar. A sidecar resource lives in the database's namespace, so the inject annotation becomes `<name>-pg` instead of `<releaseNamespace>/<sidecar-name>`. Argo Events is not needed. The database default changes from `otel` to `postgres`, and TLS defaults to `require` instead of `disable`; set `sslMode: disable` to match the chart.

See the [PostgreSQL API reference](../reference/tsugapostgresmonitoring.md) for every field and [runtime defaults](../reference/configuration.md#postgresql-runtime-defaults) for collector sizing.
