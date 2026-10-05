# Getting started with Kubernetes monitoring

**Prerequisite: [operator and collection dependencies installed](../getting-started/index.md)**

This tutorial creates node agents and a cluster gateway. You need your Tsuga OTLP endpoint and ingestion key. The operation key remains required for the operator process.

## 1. Provide all credential keys

Replace the three placeholders. Keep all keys together when recreating this Secret: an apply generated with only `api-token` can remove collection keys previously managed by that apply.

```sh
kubectl -n tsuga-operator-system create secret generic tsuga-credentials \
  --from-literal=api-token='<YOUR_OPERATION_KEY>' \
  --from-literal=api-key='<YOUR_INGESTION_KEY>' \
  --from-literal=otlp-endpoint='https://<YOUR_OTLP_ENDPOINT>' \
  --dry-run=client -o yaml | kubectl apply -f -
```

## 2. Declare the collection topology

Save [collector.yaml](../examples/collector.yaml) as `collector.yaml`. Set `clusterName` to a meaningful name for your environment.

```yaml
--8<-- "examples/collector.yaml"
```

Keep the resource name `cluster`: namespace instrumentation looks up that exact name. Do not create multiple configs; the managed collector names are fixed.

```sh
kubectl apply -f collector.yaml
kubectl wait --for=condition=Ready tsugacollectorconfig/cluster --timeout=120s
kubectl -n tsuga-operator-system get opentelemetrycollectors
kubectl -n tsuga-operator-system get pods
```

Expect `tsuga-agent` (DaemonSet) and `tsuga-gateway` (Deployment) collector resources. The OpenTelemetry Operator creates their pods asynchronously.

## 3. Verify data, not just configuration

`Ready` means the Tsuga operator applied its child resources. It does **not** confirm pod health or successful delivery to Tsuga.

```sh
kubectl -n tsuga-operator-system get daemonsets,deployments,statefulsets
kubectl -n tsuga-operator-system logs \
  -l app.kubernetes.io/component=opentelemetry-collector \
  --all-containers=true --prefix=true --tail=100
```

Check that collector pods are running and ready. Investigate export, authentication, and connectivity errors. In Tsuga, select a recent time range and verify fresh metrics or logs from your `k8s.cluster.name`. Absence of errors alone is not proof of delivery.

## Next steps

- [Instrument an application namespace](auto-instrumentation.md) to collect application traces.
- [Enable Prometheus scraping](prometheus.md) for annotated pods.
- [Troubleshoot missing telemetry](../operations/troubleshooting.md#collectors-are-ready-but-data-is-missing).

To remove the collection topology, first disable namespace instrumentation if enabled, then follow [uninstall and cleanup](../operations/lifecycle.md). Deleting the cluster config removes its owned collection resources.
