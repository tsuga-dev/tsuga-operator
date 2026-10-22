# eBPF profiling

**Prerequisite: [working cluster collection](getting-started.md)**

The profiling collector samples CPU profiles from every process on each node with eBPF. Applications need no SDK, no re-instrumentation and no restart. Profiles appear in Tsuga, where they can be flame-graphed and compared across versions.

It runs as a separate DaemonSet, `tsuga-profiler`, because the `profiling` receiver ships only in the OpenTelemetry eBPF profiler distribution, and because it needs privileges the agent does not.

## Before enabling

- **The collector namespace must admit privileged pods.** The profiler runs `privileged` with `hostPID` and root. A namespace enforcing the `baseline` or `restricted` Pod Security Standard rejects it. The agent's host-path volumes already require more than `baseline`, so a namespace that runs the agent today usually admits it.
- **Nodes need kernel 5.10 or newer** with tracefs available. Amazon Linux 2023 works. Bottlerocket is untested.

## Enable profiling

```yaml
spec:
  profiling:
    enabled: true
```

Apply the `TsugaCollectorConfig`, then check the pods:

```sh
kubectl -n tsuga-operator-system get opentelemetrycollector tsuga-profiler
kubectl -n tsuga-operator-system get pods -l app.kubernetes.io/instance=tsuga-operator-system.tsuga-profiler
```

If no pods appear, Pod Security admission or the kernel likely rejected them. The DaemonSet shows `FailedCreate` events:

```sh
kubectl -n tsuga-operator-system describe daemonset tsuga-profiler-collector
```

`spec.image` does not apply to the profiler. To pull from a mirror, set `profiling.image` to an eBPF profiler build of the same version.

## Service names

eBPF has no SDK to ask, so each profile's `service.name` is resolved in this order:

1. The process itself: the OpenTelemetry process context an SDK publishes, or the process's `OTEL_SERVICE_NAME` and `OTEL_RESOURCE_ATTRIBUTES`. Set these on your workloads so profiles join traces and logs on the same `service.name`.
2. Kubernetes: the `resource.opentelemetry.io/service.name` pod annotation, then the `app.kubernetes.io/instance` and `app.kubernetes.io/name` labels, then the owning workload.
3. Host processes no pod matches: containerd shims become `containerd-shim`, everything else `unknown_service:<executable>`.

## Sampling

The profiler samples at 20 Hz, the rate Tsuga assumes when converting samples into CPU time.

## Turn profiling off

Set `profiling.enabled: false`. The operator deletes the `tsuga-profiler` collector and its RBAC.
