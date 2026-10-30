# Auto-instrumentation

**Prerequisite: [working cluster collection](getting-started.md)**

Use one `TsugaMonitoring` per application namespace. Supported language values are `java`, `nodejs`, `python`, `dotnet`, `go`, `apache-httpd`, and `nginx`. Choose the language your workload actually uses; upstream runtime compatibility still applies.

## Language requirements in the OTel Operator

The operator creates the `Instrumentation`; the OpenTelemetry Operator does the injection, and some languages need its feature flags:

- `go` needs `--enable-go-instrumentation`. Per upstream, Go instrumentation runs as a privileged sidecar in each instrumented pod.
- `nginx` needs `--enable-nginx-instrumentation`.

Check the upstream OpenTelemetry Operator documentation for the other languages.

## Start with selected workloads

This example prepares Python instrumentation in `payments` with `injection: Manual`, so nothing is instrumented until you annotate a workload. Save [monitoring.yaml](../examples/monitoring.yaml):

```yaml
--8<-- "examples/monitoring.yaml"
```

```sh
kubectl create namespace payments --dry-run=client -o yaml | kubectl apply -f -
kubectl apply -f monitoring.yaml
kubectl -n payments wait --for=condition=Ready tsugamonitoring/default --timeout=120s
kubectl -n payments get instrumentation default
```

Add this annotation to your application's **pod template**, under `spec.template.metadata.annotations` in its Deployment manifest:

```yaml
instrumentation.opentelemetry.io/inject-python: default
```

Apply the application manifest and watch its rollout. The annotation references the generated `Instrumentation` named `default`. Replace `python` if you selected another language.

There is no implemented opt-in label selector in the current controller. In this mode, manually annotate each selected workload. `status.instrumentedWorkloads` counts the Deployment, StatefulSet and DaemonSet templates annotated for this Instrumentation. It does not prove that injection or export succeeded.

## Instrument a whole namespace

`spec.injection` decides how workloads are opted in. Either way, every selected language except `go` is injected into every pod in the namespace. The operator does not detect each application's language, so use these modes in namespaces whose workloads match `languages`.

### Namespace (default)

Leave `injection` unset, or set `injection: Namespace`. The operator puts one `instrumentation.opentelemetry.io/inject-<language>` annotation per language on the Namespace. The OpenTelemetry Operator injects pods when they are created, so nothing is rolled. Running pods stay uninstrumented until they restart. To instrument them now, restart them yourself:

```sh
kubectl -n payments rollout restart deployment,statefulset,daemonset
```

New pods from Jobs and CronJobs, and bare pods, are injected too. This includes pods that other controllers create in the namespace. With `apache-httpd` or `nginx`, every such pod gets the webserver module's init container, which copies its config out of the application image, so a pod that is not that webserver can stay stuck in `Init`. Keep webserver languages out of namespaces that run anything else, or opt the other workloads out. To keep one workload out, set the language's annotation to `"false"` on its pod template; a pod-template annotation always wins over the Namespace one:

```yaml
instrumentation.opentelemetry.io/inject-python: "false"
```

`status.instrumentedWorkloads` counts the Deployments, StatefulSets and DaemonSets whose new pods get at least one language from the Namespace or from their own annotation. It does not prove that running pods are injected.

The operator refuses `Namespace` in the collector namespace (`Ready=False`, reason `CollectorNamespace`), because it would instrument the Tsuga collectors. An inject annotation already on the Namespace that the operator did not set is left alone and reported as `InjectAnnotationConflict`.

### Workloads

Set `injection: Workloads` to have the operator add the inject annotations to every Deployment, StatefulSet and DaemonSet pod template instead. **Changing a pod template rolls the workload**, following its update strategy, so applying the TsugaMonitoring restarts every workload in the namespace at once. Jobs and CronJobs are not patched.

TsugaMonitorings created before `injection` existed have `injectExistingWorkloads` stored: `true` keeps them in `Workloads` mode and `false` in `Manual`. Set `injection` to change mode. Switching from `Workloads` to `Namespace` leaves the existing pod-template annotations in place, so nothing rolls.

### Go workloads

Go is never injected in bulk, because each workload must name its executable. Annotate the pod template of each Go workload yourself:

```yaml
instrumentation.opentelemetry.io/inject-go: default
instrumentation.opentelemetry.io/otel-go-auto-target-exe: /app/server
```

Replace `/app/server` with the path of your binary in the container.

## Customize the Instrumentation

`spec.instrumentation.spec` is deep-merged over the `Instrumentation` spec the operator renders (the exporter endpoint, the `tracecontext` and `baggage` propagators, and one block per language in `languages`). Maps merge key by key; lists and scalars you set replace the operator's values, so setting `propagators` replaces the default list.

```yaml
apiVersion: observability.tsuga.com/v1alpha1
kind: TsugaMonitoring
metadata:
  name: default
  namespace: payments
spec:
  instrumentation:
    enabled: true
    languages:
    - java
    - nodejs
    spec:
      sampler:
        type: parentbased_traceidratio
        argument: "0.25"
      env:
      - name: OTEL_EXPORTER_OTLP_TIMEOUT
        value: "5000"
      resource:
        addK8sUIDAttributes: true
      java:
        image: registry.example.com/autoinstrumentation-java:2.10.0
```

A sampler other than the default `parentbased_always_on` drops spans before they reach the agent, so span metrics undercount request and error rates.

A language block in `spec` for a language that is not in `languages` is passed through to the `Instrumentation`, which configures it, but the operator does not inject it in bulk mode.

`exporter.endpoint` is managed by the operator, which points it at the node agent. Setting it, or setting `exporter` to anything other than an object, makes the TsugaMonitoring `Ready=False` with reason `RenderFailed`. The operator does not retry until you change the spec. Other `exporter` keys merge next to the managed endpoint; `tls` is the only other one, and it has no effect against the agent's plain-HTTP endpoint.

Language blocks in `spec` use Instrumentation keys (`apacheHttpd`, not `apache-httpd`).

Setting `OTEL_EXPORTER_OTLP_*ENDPOINT` in `spec.env` or per-language env, bypasses the managed endpoint and is unsupported.

A `RenderFailed` spec leaves the last good Instrumentation and workload annotations in place until the spec is fixed. Schema errors in `spec` (for example a bad sampler type) surface as `ApplyFailed` and are retried.

## Apache httpd and nginx

These languages use the OpenTelemetry webserver module, which exports OTLP over gRPC only. The operator sets the module endpoint (`ApacheModuleOtelExporterEndpoint` or `NginxModuleOtelExporterEndpoint`) to the agent's gRPC Service address, `http://tsuga-agent-collector.<collectorNamespace>.svc.cluster.local:<grpc port>` (4317 by default), first in `attrs`. Your other `attrs` are kept; setting the endpoint attr yourself gives `Ready=False` with reason `RenderFailed`. The module image is amd64-only, so the e2e suite checks injection but not span arrival: verify that spans arrive.

## Verify

For your application Deployment, substitute its name below:

```sh
kubectl -n payments rollout status deployment/<APP_NAME> --timeout=180s
kubectl get namespace payments -o jsonpath='{.metadata.annotations}{"\n"}'
kubectl -n payments get deployment <APP_NAME> -o jsonpath='{.spec.template.metadata.annotations}{"\n"}'
kubectl -n payments get pods
kubectl -n payments describe tsugamonitoring default
```

Inspect a newly created pod for injected initialization containers and environment variables. Generate application traffic and confirm recent traces in Tsuga. Applications export to `http://tsuga-agent-collector.tsuga-operator-system.svc.cluster.local:4318` with the default collector namespace; the agent adds export credentials.

## Disable instrumentation

Setting `instrumentation.enabled: false` removes the annotations the operator added. Deleting the TsugaMonitoring does the same while the operator is running. Manual annotations must be removed from your application manifests separately.

In `Namespace` mode, removing the Namespace annotations rolls nothing: running pods keep their agent until they restart. In `Workloads` mode, removing pod-template annotations rolls those workloads. That includes a TsugaMonitoring switched from `Workloads` to `Namespace`, which still holds the pod-template annotations it set before the switch. Wait for the rollouts to finish.
