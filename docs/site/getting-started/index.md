# Getting started

Install the Tsuga Operator in your cluster, then configure Kubernetes monitoring, dashboards, monitors, or SLOs.

## Prerequisites

Have `kubectl`, access to a Kubernetes cluster, permission to install CRDs and cluster RBAC, and a Tsuga operation API key. For collection, also obtain an OTLP ingestion key and your OTLP endpoint. Create the keys and find your OTLP endpoint in the Tsuga app under Settings. Check the [compatibility notes](../reference/configuration.md) before choosing versions.

```sh
kubectl config current-context
kubectl cluster-info
```

## 1. Install collection dependencies, if needed

Skip this step for Dashboard/Monitor/SLO-only use. The versions below match this repository's Makefile and OpenTelemetry schema tests; they are not a claim about the latest upstream releases.

### Install cert-manager

cert-manager provisions and renews the TLS certificates used by the OpenTelemetry Operator's admission webhooks. Install it first and wait for its deployments to become available.

```sh
kubectl apply -f https://github.com/cert-manager/cert-manager/releases/download/v1.18.2/cert-manager.yaml
kubectl wait --for=condition=Available -n cert-manager deployment --all --timeout=180s
```

### Install the OpenTelemetry Operator

The OpenTelemetry Operator runs the collectors and injects application instrumentation configured by the Tsuga Operator. This installs its controller and custom resource definitions for collectors, instrumentation, and target allocation. Wait for the controller to become available, then check that those resource definitions are present.

```sh
kubectl apply -f https://github.com/open-telemetry/opentelemetry-operator/releases/download/v0.159.0/opentelemetry-operator.yaml
kubectl wait --for=condition=Available -n opentelemetry-operator-system deployment --all --timeout=180s
kubectl get crd opentelemetrycollectors.opentelemetry.io instrumentations.opentelemetry.io targetallocators.opentelemetry.io
```

Wait for dependency webhooks to become reachable before creating their resources. If a webhook reports it is not yet ready, inspect its pods and retry after startup completes.

## 2. Create the credentials Secret

Use the raw operation key, without a `Bearer ` prefix. Replace the placeholder before running this command.

```sh
kubectl create namespace tsuga-operator-system --dry-run=client -o yaml | kubectl apply -f -
kubectl -n tsuga-operator-system create secret generic tsuga-credentials \
  --from-literal=api-token='<YOUR_OPERATION_KEY>' \
  --dry-run=client -o yaml | kubectl apply -f -
```

For a managed environment, provision this Secret through your secret manager. Do not commit real keys to Git. The [telemetry tutorial](../kubernetes-monitoring/getting-started.md) adds the collection credentials.

## 3. Install a release

This downloads the installer from the latest [GitHub release](https://github.com/tsuga-dev/tsuga-operator/releases). It pins the operator image to that release's version.

```sh
curl -fL "https://github.com/tsuga-dev/tsuga-operator/releases/latest/download/install.yaml" -o install.yaml
# Inspect the downloaded CRDs, RBAC, namespace, and Deployment before applying.
kubectl apply -f install.yaml
```

To install a specific version instead, replace `latest/download` with `download/vX.Y.Z`, using a tag from the releases page.

## 4. Verify startup

```sh
kubectl -n tsuga-operator-system rollout status deployment/tsuga-operator-controller-manager --timeout=180s
kubectl get crd dashboards.observability.tsuga.com monitors.observability.tsuga.com slos.observability.tsuga.com
kubectl -n tsuga-operator-system logs deployment/tsuga-operator-controller-manager --tail=50
```

The Deployment should become available. When installed without OpenTelemetry CRDs, the operator logs that the collection controllers are disabled; dashboards, monitors, and SLOs still work. If you install those dependencies later, restart the operator to discover them:

```sh
kubectl -n tsuga-operator-system rollout restart deployment/tsuga-operator-controller-manager
```

## Next step

[Create a dashboard](../dashboards/index.md), [define an SLO](../slos/index.md), or [collect cluster telemetry](../kubernetes-monitoring/getting-started.md). If startup fails, use the [troubleshooting guide](../operations/troubleshooting.md).
