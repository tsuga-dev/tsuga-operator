# Auto-instrumentation

**Prerequisite: [working cluster collection](getting-started.md)**

Use one `TsugaMonitoring` per application namespace. Supported language values are `java`, `nodejs`, `python`, and `dotnet`. Choose the language your workload actually uses; upstream runtime compatibility still applies.

## Start with selected workloads

This example prepares Python instrumentation in `payments` without automatically annotating every workload. Save [monitoring.yaml](../examples/monitoring.yaml):

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

There is no implemented opt-in label selector in the current controller. In this mode, manually annotate each selected workload. `status.instrumentedWorkloads` counts annotated Deployment, StatefulSet, and DaemonSet templates pointing at this Instrumentation; it does not prove that injection or export succeeded.

## Instrument a whole namespace

Set `injectExistingWorkloads: true` in your TsugaMonitoring manifest and reapply it. The operator adds injection annotations for all selected languages to Deployments, StatefulSets, and DaemonSets in that namespace. Use this mode for namespaces with compatible workloads; it does not detect each application's language.

Changes to pod templates can trigger rollouts according to the workload's update strategy. Jobs and CronJobs are not patched by this controller.

## Verify

For your application Deployment, substitute its name below:

```sh
kubectl -n payments rollout status deployment/<APP_NAME> --timeout=180s
kubectl -n payments get deployment <APP_NAME> -o jsonpath='{.spec.template.metadata.annotations}{"\n"}'
kubectl -n payments get pods
kubectl -n payments describe tsugamonitoring default
```

Inspect a newly created pod for injected initialization containers and environment variables. Generate application traffic and confirm recent traces in Tsuga. Applications export to `http://tsuga-agent-collector.tsuga-operator-system.svc.cluster.local:4318` with the default collector namespace; the agent adds export credentials.

## Disable instrumentation

Set `instrumentation.enabled: false` and apply the manifest **before deleting** the TsugaMonitoring. The operator removes annotations it previously added; manual annotations must be removed from your application manifests separately. Wait for the new pod templates and the appropriate workload rollout. Simply deleting the TsugaMonitoring does not clean annotations from workloads.
