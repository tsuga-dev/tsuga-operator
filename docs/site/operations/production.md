# Production checklist

Use this checklist after a successful development-cluster walkthrough. Capacity and security settings depend on your cluster; the repository defaults are a starting point, not a sizing guarantee.

## Versions and change control

- Pin the operator release and collector images. Review changes to the alpha CRD API before upgrading.
- Use the OpenTelemetry Operator version covered by the repository's schema tests. Revalidate rendered resources before changing that dependency.
- Keep CRs in version control, with team IDs and cluster names specific to each environment.
- Test updates and deletion behavior in staging, including finalizers and instrumentation rollout impact.

## Access and credentials

The operator can create cluster RBAC and patch application workloads. Limit who can edit its configuration and CRs. The agent mounts host paths for node metrics and pod logs; check compatibility with your cluster's admission and Pod Security policies.

Each generated collector gets its own read-only ClusterRole and ClusterRoleBinding, named `tsuga-operator-collector-<collector>`, created only while that collector is enabled:

| Collector | Access beyond pod metadata (pods, namespaces, ReplicaSets, Jobs) |
| --- | --- |
| `tsuga-agent` | `get` on `nodes/stats` and `nodes/proxy` for the kubelet |
| `tsuga-gateway` | Cluster state read by `k8s_cluster`: nodes, workloads, services, quotas, persistent volumes and claims, HPAs |
| `tsuga-scraper` | Services, endpoints and EndpointSlices for Prometheus discovery through the Target Allocator |

The agent does not verify the kubelet's serving certificate (`insecure_skip_verify: true`), because many clusters serve self-signed kubelet certificates the cluster CA cannot validate. The connection targets the pod's own node IP, and the token it sends only carries the agent's role above. If your policy requires verification, report it so a verified mode can be added.

Provision credentials through a secret manager and document rotation. Restrict application access to the internal OTLP service as appropriate, and allow collector egress to the intended OTLP endpoint. Do not assume the default install includes a network policy tailored to your environment.

## Permissions and security tradeoffs

- The operator's own ClusterRole can create ClusterRoles and ClusterRoleBindings, and patch or delete its generated names, because it provisions the per-collector RBAC above. Kubernetes cannot restrict top-level creation by resource name. It only lets the operator grant permissions it already holds, so its role also carries the union of the collector rules.
- The PostgreSQL controller can get Secrets across namespaces to verify that a pre-existing monitor-password Secret belongs to its resource. It requests metadata only and does not use Secret values. Restrict who can edit `TsugaPostgresMonitoring` resources and run the operator under a dedicated ServiceAccount.
- The agent's `nodes/proxy` `get` is broader than its use: `kubelet_stats` reads the kubelet's `/pods` endpoint, and the kubelet authorizes that path, like most of its API, as `nodes/proxy`. Kubernetes 1.33 and later (`KubeletFineGrainedAuthz`, on by default from 1.33, GA in 1.36) also accept the narrower `nodes/pods`, but older kubelets only check `nodes/proxy`, so the operator keeps it.
- The TsugaMonitoring controller can patch Deployments, StatefulSets and DaemonSets in every namespace, to add and remove the OTel inject annotations on their pod templates. Anyone who controls the operator can therefore change what any workload runs. This is the broadest grant the operator holds.
- The PostgreSQL controller can create and delete Jobs, and create, patch and delete ConfigMaps, in any namespace, to run the setup Job next to its `TsugaPostgresMonitoring`. A Job can mount any Secret in its namespace, so this grant already reaches Secrets there, beyond the metadata-only Secret `get` above.
- On `TsugaPostgresMonitoring`, the default `sslMode: require` encrypts the connection but does not verify the server certificate, matching libpq's `require`. It does not protect against an attacker who can intercept traffic to the database host.

## Capacity

By default, each generated collector requests `100m` CPU and `128Mi` memory, with limits of `500m` CPU and `512Mi` memory. The agent runs per eligible node, so account for its multiplied footprint.

`spec.resources` replaces the defaults for all collector modes. Set complete requests and limits, then observe actual usage under representative load. The embedded memory limiter relies on a memory limit; do not remove it casually.

The gateway runs as a single replica because its cluster receivers do not elect a leader. Adding replicas would duplicate collection. Increase resources when needed instead. Scraper replicas can be increased only with the Target Allocator enabled.

## Operability

Monitor operator availability, reconcile errors, collector pod restarts, memory pressure, export failures, and data freshness. A `Ready` CR condition does not establish end-to-end telemetry health.

Start application instrumentation with selected workloads, validate runtime compatibility, and observe rollout behavior before enabling namespace-wide injection. Manage scraping cardinality and volume by opting in only the pods you intend to collect.

## Recovery

Keep desired manifests and secret-manager configuration recoverable. Record remote Dashboard, Monitor, and SLO IDs when investigating state loss; recreating a CR without its status can create a new remote object. Test your recovery process rather than assuming resource recreation adopts existing Tsuga objects.

Follow the documented [upgrade and uninstall sequence](lifecycle.md), keeping the operator running until remote-resource cleanup completes.
