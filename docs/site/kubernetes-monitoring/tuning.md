# Tuning collection

Every field below lives on the cluster-scoped `TsugaCollectorConfig`. An unset field keeps the built-in value listed in [Configuration and compatibility](../reference/configuration.md). The full field list is in the [API reference](../reference/tsugacollectorconfig.md).

## Reducing cost

Exclude noisy logs, slow the scrape intervals, drop metric groups, and trim span-metric dimensions.

```yaml
spec:
  agent:
    fileLog:
      exclude:
        - /var/log/pods/kube-system_*/*/*.log
    hostMetrics:
      collectionInterval: 30s
    kubeletStats:
      collectionInterval: 60s
      metricGroups: [node, pod]
    spanMetrics:
      dimensions:
        - name: http.request.method
          default: GET
        - name: http.response.status_code
  gateway:
    collectionInterval: 30s
```

- `fileLog.exclude` is added to the built-in excludes. `fileLog.include` replaces the built-in include glob.
- `hostMetrics.scrapers` replaces the built-in scrapers wholesale; it is not merged. To drop one built-in scraper, list every scraper you want to keep. A `null` entry only removes an entry from your own map, and a map that ends up empty fails the render. `collectNetwork` and `collectProcesses` do not apply to a custom map.
- `kubeletStats.metricGroups` accepts `node`, `pod`, `container` and `volume`.
- `spanMetrics.dimensions` replaces the built-in dimensions. `spanMetrics.enabled: false` removes the span metrics connector.
- `agent.collectLogs: false` stops reading pod logs from `/var/log/pods` but keeps the logs pipeline for OTLP logs. With it off, `fileLog` and `collectOtelLogs` have no effect.
- `fileLog` is the pod-log receiver: keep its `include` globs under `/var/log/pods`. To read other host files, add a receiver on the agent that reads them under `/hostfs`, where the host filesystem is always mounted; see [Custom collector config](custom-config.md).
- `agent.collectOtelLogs: true` collects the collectors' own logs, which are excluded by default.
- `batch.sendBatchSize`, `batch.sendBatchMaxSize` and `batch.timeout` apply to the agent, gateway and scraper.

## GKE Autopilot

Autopilot denies the kubelet `/pods` endpoint, which fails every `kubelet_stats` scrape. Turn the endpoint off:

```yaml
spec:
  agent:
    kubeletStats:
      usePodsEndpoint: false
```

Without `/pods`, the volume type label (`k8s.volume.type`) and the request and limit utilization metrics (`k8s.container.*_limit_utilization`, `k8s.container.*_request_utilization` and the `k8s.pod.*` equivalents) are not collected.

## Verifying the kubelet certificate

The agent skips kubelet certificate verification by default, because many kubelets serve self-signed certificates. To verify it, turn skipping off and point at a CA bundle:

```yaml
spec:
  agent:
    kubeletStats:
      insecureSkipVerify: false
      caFile: /etc/kubelet-ca/ca.crt
```

The file must already exist in the agent container. The operator does not create or mount it.

## OTLP ports

The agent listens for OTLP on `${env:MY_POD_IP}:4317` (gRPC) and `${env:MY_POD_IP}:4318` (HTTP). With `agent.hostNetwork: true` these ports bind on every node. Move them if another process holds them:

```yaml
spec:
  agent:
    otlp:
      grpcEndpoint: ${env:MY_POD_IP}:14317
      httpEndpoint: ${env:MY_POD_IP}:14318
```

`TsugaMonitoring` (HTTP) and `TsugaPostgresMonitoring` (gRPC) follow the endpoint you set. Moving `httpEndpoint` updates the `Instrumentation`, but pods already running keep the old endpoint until they restart, so restart the auto-instrumented workloads after moving it.

`agent.healthCheckEndpoint` moves the health check, which defaults to `${env:MY_POD_IP}:13133`. An empty string removes the health check extension and the liveness probe derived from it. The same field exists under `gateway`, `prometheus` and `profiling`.

## Environment, team and service attributes

Set resource attributes per workload on the pod template:

```yaml
spec:
  template:
    metadata:
      labels:
        resource.opentelemetry.io/team: payments
      annotations:
        resource.opentelemetry.io/deployment.environment.name: production
```

- Every `resource.opentelemetry.io/<attr>` pod annotation becomes the `<attr>` resource attribute, on every collector.
- The `resource.opentelemetry.io/team` pod label becomes `team`. Other `resource.opentelemetry.io/` labels are not read; use annotations.
- Tsuga sets `env` from your ingestion key's tags. Pod labels no longer set it; use `deployment.environment.name` per workload. A `resource.opentelemetry.io/env` annotation is still read like any other.
- Attributes the application sets itself, for example through `OTEL_RESOURCE_ATTRIBUTES`, win over all of the above. They apply to the telemetry it sends over OTLP; its container logs, kubelet and Prometheus metrics only get the pod's annotations and labels.

## Extra attributes

Add Kubernetes labels and annotations as resource attributes, and change which metadata `k8s_attributes` extracts:

```yaml
spec:
  k8sAttributes:
    metadata:
      - k8s.namespace.name
      - k8s.pod.name
      - k8s.node.name
      - service.name
  agent:
    extraLabelMapping:
      - tagName: app.tier
        key: example.com/tier
        from: pod
    extraAnnotationsMapping:
      - tagName: owner
        key: example.com/owner
        from: namespace
```

- `extraLabelMapping` and `extraAnnotationsMapping` are added to the built-in mappings. `from` is `pod`, `namespace` or `node`, and defaults to `pod`. On a pod that also carries a `resource.opentelemetry.io/<attr>` annotation for the same attribute, the annotation wins.
- `k8sAttributes.metadata` replaces the built-in metadata list for the agent, gateway and scraper. The profiler has its own list, `profiling.k8sAttributesMetadata`.
- Both mapping fields exist under `agent`, `gateway`, `prometheus` and `profiling`.

## Cloud attributes and redaction

Resource detection adds cloud and host attributes (`cloud.provider`, `cloud.region`, `cloud.account.id`, `host.id`) to the agent, gateway, scraper and profiler. It is off by default.

```yaml
spec:
  resourceDetection:
    enabled: true
    detectors: [env, eks, ec2]
    timeout: 15s
```

List the detectors that match where you run:

| Where | `detectors` |
| --- | --- |
| EKS | `[env, eks, ec2]` |
| GKE | `[env, gcp]` |
| AKS | `[env, aks, azure]` |
| Anywhere else | `[env]` (the default) |

- Detectors run in order. The first to supply an attribute wins, and attributes already on the telemetry are never replaced.
- List `ec2` after `eks`, or `cloud.platform` loses `aws_eks`. The API server rejects the other order.
- `detectors` accepts `env`, `eks`, `ec2`, `gcp`, `aks` and `azure`, at most six entries, and cannot be empty.
- `ec2`, `gcp`, `aks` and `azure` are safe to list anywhere: when their metadata service is unreachable they return an empty resource without error. `[env, ec2]` is valid on self-managed Kubernetes on EC2.
- List `eks` only on EKS. Off EKS it no longer stops the collector on the 0.161.0 collectors this operator runs, because missing metadata is not an error, but it makes needless API and IMDS calls. Older collector images set through `spec.image` may still fail startup when `eks` cannot reach the API.
- `k8s_api` and `k8snode` are not offered. They fail startup when the node read fails and only add `k8s.node.uid` and `k8s.cluster.uid`.
- No extra RBAC is needed. Off EKS or without IMDS, the eks detector also tries to read the collector's node and call ec2:DescribeInstances; those calls fail quietly and only feed attributes that are off by default.
- The gateway and the Prometheus scraper run on one node but collect cluster-wide data (`k8s_cluster`, `k8s_objects`, scrape targets on any node, unless `prometheus.nodeLocal` moves annotation scraping to the agent). With `ec2`, `azure` or `gcp` they add their own node's `host.*` and `cloud.availability_zone` to everything they send, as the kube-stack chart does. Views that group by `host.name` show that data under the gateway's or scraper's node.
- On EC2 with IMDSv2 and a hop limit of 1 (the Karpenter EC2NodeClass default), collectors on pod networking cannot reach IMDS: the gateway, the scraper, the profiler, and the agent unless `agent.hostNetwork: true`. `ec2` and `eks` then return only `cloud.provider` and `cloud.platform`, without error. Set the node's metadata hop limit to 2 to get region, account and host on every collector.

Redaction masks credentials in the agent's traces, metrics and logs, `extraPipelines` included. It runs before `batch` (last in a pipeline without `batch`), on the agent only; the gateway, scraper and profiler are not covered. `resourceDetection` covers `extraPipelines` too.

```yaml
spec:
  redaction:
    enabled: true
```

With no `config`, the built-in rules apply. They mask attribute keys that contain `password`, `passwd`, `pwd`, `secret`, `credential`, `authorization`, `api key`, `session id` or `private key` (case-insensitive, with an optional `.`, `_` or `-` between the two words), keys that contain `access token`, `refresh token` or `auth token` in the same form, and keys that are `token` or end in `.token`, `_token` or `-token`. Other keys containing `token`, such as `gen_ai.usage.input_tokens`, pass through. They also mask values that look like AWS and GCP keys, GitHub, GitLab, Slack and Stripe tokens, JWTs, PEM private keys and passwords embedded in URLs. The built-in key rules also mask the semantic-convention `session.id`, which matches the session-id key pattern. Redaction runs after Kubernetes enrichment, so a custom `extraLabelMapping` or `extraAnnotationsMapping` tag whose name matches these key rules is masked too. Keys starting with `service.`, `otel.` or `k8s.` are never redacted. The rules cover credentials, not personal data: names, emails and IP addresses pass through.

To change the rules, set `config` to a [redaction processor](https://github.com/open-telemetry/opentelemetry-collector-contrib/tree/main/processor/redactionprocessor) configuration:

```yaml
spec:
  redaction:
    enabled: true
    config:
      allow_all_keys: true
      redact_all_types: true
      ignored_key_patterns: ['^service\..*', '^otel\..*', '^k8s\..*']
      blocked_key_patterns:
        - '(?i).*(password|passwd|pwd).*'
        - '(?i).*ssn.*'
      blocked_values:
        - '\b\d{3}-\d{2}-\d{4}\b'
```

`config` replaces the built-in rules wholesale; it is not merged. Copy the built-in patterns you want to keep, as the example does for the first key pattern. A config that would delete every attribute, with neither `allow_all_keys: true` nor `allowed_keys`, fails the render with `tsuga-agent: redaction config would delete every attribute; set allow_all_keys: true or list allowed_keys`.

## Profiling

```yaml
spec:
  profiling:
    enabled: true
    serviceNameEnvVar: OTEL_SERVICE_NAME
```

`serviceNameEnvVar` names an environment variable of the profiled process whose value becomes the profile's service name, ahead of the other [service name sources](profiling.md).

## Agent placement

Applications send OTLP to the agent Service, which only routes to the agent on the sending pod's node: a node without an agent drops its applications' telemetry, and nothing reports it. So when neither `agent.affinity` nor `spec.affinity` is set, the agent gets a node affinity that keeps it off nodes a Linux DaemonSet cannot run on, and, unless `agent.tolerations` or `spec.tolerations` is set, a toleration for every taint, so it also runs on GPU, spot, dedicated and control-plane nodes:

```yaml
tolerations:
  - operator: Exists
affinity:
  nodeAffinity:
    requiredDuringSchedulingIgnoredDuringExecution:
      nodeSelectorTerms:
        - matchExpressions:
            - {key: kubernetes.io/os, operator: In, values: [linux]}
            - {key: eks.amazonaws.com/compute-type, operator: NotIn, values: [fargate]}
            - {key: type, operator: NotIn, values: [virtual-kubelet]}
```

Each setting replaces its default, it is not merged. Setting any affinity, even `{}`, also drops the default toleration, because tolerating every taint without the exclusions puts agent pods on nodes where they stay `Pending` (Fargate, virtual nodes) or fail to start (Windows). With a custom affinity, add the tolerations you need and keep the expressions above if the cluster has such nodes. `tolerations: []` opts out of the default toleration and keeps the default affinity.

On self-managed clusters the agent runs on control-plane nodes and collects their pod logs (etcd, kube-apiserver), which adds log volume. Managed control planes (EKS, GKE, AKS) have no such nodes.

## Turning the agent off

```yaml
spec:
  agent:
    enabled: false
```

The operator stops rendering the node agent, its ServiceAccount and its RBAC. Auto-instrumentation and database monitoring send through the agent, so they stop working:

- Every `TsugaMonitoring` reports `Ready=False` with reason `AgentDisabled`, removes the inject annotations the operator added, and deletes its `Instrumentation`.
- Every `TsugaPostgresMonitoring` reports `Ready=False` with reason `AgentDisabled` and stops its collector. It keeps its monitor Secret and setup Job, so turning the agent back on reuses the same password.

!!! warning
    Removing the inject annotations rolls out every auto-instrumented workload without instrumentation. Turning the agent back on adds the annotations again and rolls them a second time.

See [reconciliation](../concepts/reconciliation.md) for all condition reasons.
