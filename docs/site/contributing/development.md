# Development

You need Go 1.26+, Docker, `kubectl`, and Kind for local cluster tests.

```sh
git clone https://github.com/tsuga-dev/tsuga-operator.git
cd tsuga-operator
cp .env.example .env
```

Set `TSUGA_API_TOKEN` in `.env`. The Makefile loads it automatically. Do not commit credentials.

## Local Kind workflow

```sh
make kind-deploy
```

This creates the development Kind cluster if needed, installs the pinned collection dependencies, creates the operation-key Secret, builds and loads the image, and deploys the operator. This target installs collection prerequisites even if you only plan to use Dashboard/Monitor/SLO.

Apply individual manifests from the documentation examples. Do not apply the whole `config/samples` directory unless you intend to exercise all capabilities and have replaced its environment-specific values. For collection, provision all Secret keys after the deploy helper; see [credentials](../how-to/credentials.md).

## Run the controller on your machine

Install CRDs on the selected development cluster and provide an operation key:

```sh
make install
export TSUGA_API_TOKEN='<YOUR_OPERATION_KEY>'
make run
```

Stop any competing in-cluster Tsuga controller before using this workflow. Install upstream OpenTelemetry CRDs before starting if you want collection controllers enabled.

## Deploy a source build to an existing cluster

Check out the commit you want to deploy and select the intended kube-context. Install collection dependencies if needed and create the operation-key Secret using the [installation guide](../getting-started/index.md). Choose an image repository your cluster can pull from:

```sh
make docker-build IMG=<registry>/tsuga-operator:<tag>
make docker-push IMG=<registry>/tsuga-operator:<tag>
make deploy IMG=<registry>/tsuga-operator:<tag>
kubectl -n tsuga-operator-system rollout status deployment/tsuga-operator-controller-manager --timeout=180s
```

The deploy target applies CRDs as part of the bundle and updates `config/manager/kustomization.yaml` with the chosen image. Review that local change before committing. To produce a distributable installer without applying it, use `make build-installer IMG=<registry>/tsuga-operator:<tag>`; the result is `dist/install.yaml`.

## Build and test

| Command | Purpose |
| --- | --- |
| `make build` | Generate manifests/code, format, vet, and build the manager |
| `make test` | Main test suite with envtest |
| `make test-otel-schema` | Validate rendered upstream CRs against the pinned OTel Operator CRDs |
| `make test-e2e` | End-to-end suite on an isolated Kind cluster |
| `make lint` | Go lint checks |
| `make docs-build` | Documentation reference, example, and site checks |

Use an isolated environment for end-to-end tests. Consult [test/e2e](https://github.com/tsuga-dev/tsuga-operator/tree/main/test/e2e) for suite-specific configuration.

## Full end-to-end suite

`test/e2efull` exercises dashboards, monitors, cluster collection, and application instrumentation against Kind and a real Tsuga organization. Use a dedicated test organization: the suite creates remote objects and sends telemetry that remains until retention expires. It runs locally, outside CI.

Install Docker, Kind, kubectl, and Tsuga CLI v1.3.0 or newer. Supply `TSUGA_E2E_API_TOKEN`, `TSUGA_E2E_OTLP_ENDPOINT`, `TSUGA_E2E_INGESTION_KEY`, and `TSUGA_E2E_CLUSTER_ID` through your environment. `TSUGA_E2E_BASE_URL` optionally selects a different API endpoint.

```sh
make test-e2e-full
# Or select one tier:
make test-e2e-full LABEL=tier-a
# Run its unit tests without the live suite:
go test ./test/e2efull/... -skip TestE2EFull
```

Set `E2E_KEEP_CLUSTER=1` to retain the cluster for debugging. After a crashed run, `make e2e-sweep RUNID=<run-id>` cleans up its dashboards and monitors; `make cleanup-test-e2e-full` removes the suite's Kind cluster. Ingested telemetry is not removed. See [the suite source](https://github.com/tsuga-dev/tsuga-operator/tree/main/test/e2efull) for scenarios and cleanup implementation.

## Change an API

The Kubernetes envelope types live in `api/v1alpha1`. After changing them:

```sh
make manifests-crd
make generate-api
make docs-reference
make docs-build
```

When updating `public-open-api.json`, run `make generate-tsuga-client` and review the generated client diff. Run `make check-generated-tsuga-client` to verify it is current, and `make openapi-drift-check` against the intended base branch. CI runs both checks. The client covers Dashboard, Monitor, and SLO HTTP operations; the Kubernetes API, payload mappers, and reconciliation remain hand-owned. Opaque configuration unions stay as pass-through JSON. Run `go test ./internal/tsuga ./internal/controller` to exercise the mapper-to-generated-model contract, the reviewed inventory of API fields absent from the CRDs, and controller HTTP behavior. When that inventory test fails, add the field to the Kubernetes API and mapper or explicitly review and record why it remains unsupported.

Update the relevant tutorial, runtime-default documentation, and example manifests with behavior changes. The [documentation guide](documentation.md) explains the site workflow.
