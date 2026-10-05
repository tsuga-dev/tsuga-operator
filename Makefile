ENV_FILE ?= .env
ifneq ($(wildcard $(ENV_FILE)),)
include $(ENV_FILE)
export $(shell sed -n 's/^\([A-Za-z_][A-Za-z0-9_]*\)=.*/\1/p' $(ENV_FILE))
endif

# Image URL to use all building/pushing image targets
IMG ?= controller:latest
KIND_DEV_CLUSTER ?= tsuga-operator-dev
KIND_IMG ?= tsuga-operator:kind
OPERATOR_NAMESPACE ?= tsuga-operator-system
MANAGER_DEPLOYMENT ?= tsuga-operator-controller-manager
TSUGA_SECRET_NAME ?= tsuga-credentials

# Get the currently used golang install path (in GOPATH/bin, unless GOBIN is set)
ifeq (,$(shell go env GOBIN))
GOBIN=$(shell go env GOPATH)/bin
else
GOBIN=$(shell go env GOBIN)
endif

# CONTAINER_TOOL defines the container tool to be used for building images.
# Be aware that the target commands are only tested with Docker which is
# scaffolded by default. However, you might want to replace it to use other
# tools. (i.e. podman)
CONTAINER_TOOL ?= docker

# Setting SHELL to bash allows bash commands to be executed by recipes.
# Options are set to exit when a recipe line exits non-zero or a piped command fails.
SHELL = /usr/bin/env bash -o pipefail
.SHELLFLAGS = -ec

.PHONY: all
all: build

##@ General

# The help target prints out all targets with their descriptions organized
# beneath their categories. The categories are represented by '##@' and the
# target descriptions by '##'. The awk command is responsible for reading the
# entire set of makefiles included in this invocation, looking for lines of the
# file as xyz: ## something, and then pretty-format the target and help. Then,
# if there's a line with ##@ something, that gets pretty-printed as a category.
# More info on the usage of ANSI control characters for terminal formatting:
# https://en.wikipedia.org/wiki/ANSI_escape_code#SGR_parameters
# More info on the awk command:
# http://linuxcommand.org/lc3_adv_awk.php

.PHONY: help
help: ## Display this help.
	@awk 'BEGIN {FS = ":.*##"; printf "\nUsage:\n  make \033[36m<target>\033[0m\n"} /^[a-zA-Z_0-9-]+:.*?##/ { printf "  \033[36m%-15s\033[0m %s\n", $$1, $$2 } /^##@/ { printf "\n\033[1m%s\033[0m\n", substr($$0, 5) } ' $(MAKEFILE_LIST)

##@ Development

# OpenAPI document describing the Tsuga API contract the operator sends payloads against.
OPENAPI_SPEC ?= public-open-api.json
# Git ref to diff OPENAPI_SPEC against for drift detection (the PR base branch in CI).
OPENAPI_BASE_REF ?= origin/main
# Pinned oasdiff version used by the drift gate.
OASDIFF_VERSION ?= v1.23.0
# API packages scanned by the api-only generation targets (manifests-crd, generate-api).
API_PATHS ?= ./api/...

.PHONY: manifests
manifests: controller-gen ## Generate ClusterRole and CustomResourceDefinition objects.
	$(CONTROLLER_GEN) rbac:roleName=manager-role crd:allowDangerousTypes=true paths="./..." output:crd:artifacts:config=config/crd/bases

.PHONY: manifests-crd
manifests-crd: controller-gen ## Generate CustomResourceDefinition YAML from api types only (no RBAC; skips scanning cmd/ and internal/).
	$(CONTROLLER_GEN) crd:allowDangerousTypes=true paths="$(API_PATHS)" output:crd:artifacts:config=config/crd/bases

.PHONY: generate
generate: controller-gen ## Generate code containing DeepCopy, DeepCopyInto, and DeepCopyObject method implementations.
	$(CONTROLLER_GEN) object:headerFile="hack/boilerplate.go.txt" paths="./..."

.PHONY: generate-api
generate-api: controller-gen ## Generate DeepCopy only for api types (skips scanning cmd/ and internal/).
	$(CONTROLLER_GEN) object:headerFile="hack/boilerplate.go.txt" paths="$(API_PATHS)"

.PHONY: openapi-drift-check
openapi-drift-check: ## Fail on breaking changes to the Dashboard/Monitor/SLO payload envelope vs OPENAPI_BASE_REF.
	@tmp=$$(mktemp -d); \
	git show $(OPENAPI_BASE_REF):./$(OPENAPI_SPEC) | python3 hack/openapi-envelope.py > $$tmp/base.json; \
	python3 hack/openapi-envelope.py < $(OPENAPI_SPEC) > $$tmp/revision.json; \
	go run github.com/oasdiff/oasdiff@$(OASDIFF_VERSION) breaking \
		$$tmp/base.json $$tmp/revision.json \
		--match-path '^/v1/(dashboards|monitors|slos)$$' \
		--fail-on ERR; \
	status=$$?; rm -rf $$tmp; exit $$status

.PHONY: generate-tsuga-client
generate-tsuga-client: ## Regenerate the Dashboard/Monitor/SLO API client from public-open-api.json.
	./hack/generate-tsuga-client.sh

.PHONY: check-generated-tsuga-client
check-generated-tsuga-client: ## Fail when the checked-in Tsuga API client differs from the OpenAPI spec.
	./hack/generate-tsuga-client.sh --check

.PHONY: fmt
fmt: ## Run go fmt against code.
	go fmt ./...

.PHONY: vet
vet: ## Run go vet against code.
	go vet ./...

.PHONY: test
test: manifests generate fmt vet setup-envtest otel-bundle ## Run tests.
	KUBEBUILDER_ASSETS="$(shell $(ENVTEST) use $(ENVTEST_K8S_VERSION) --bin-dir $(LOCALBIN) -p path)" \
	OTEL_OPERATOR_BUNDLE="$(OTEL_OPERATOR_BUNDLE)" \
	go test $$(go list ./... | grep -v /e2e) -coverprofile cover.out
	cd test/fixtures/otlp-emitter && go test ./...

# Renders every OpenTelemetryCollector / TargetAllocator / Instrumentation this
# operator produces against an API server carrying the pinned operator release's
# CRDs. Split out from `test` so CI can gate on the pin without also needing
# controller-gen for the manifest/codegen steps.
.PHONY: test-otel-schema
test-otel-schema: setup-envtest otel-bundle ## Validate the rendered OTel custom resources against the pinned operator's CRD schemas.
	KUBEBUILDER_ASSETS="$$($(ENVTEST) use $(ENVTEST_K8S_VERSION) --bin-dir $(LOCALBIN) -p path)" \
	OTEL_OPERATOR_BUNDLE="$(OTEL_OPERATOR_BUNDLE)" \
	go test ./internal/collection/ -run TestRendersValidateAgainstPinnedOperatorCRDs -count=1

# To use a different vendor for e2e tests, modify the setup under 'test/e2e'.
# The default setup assumes Kind is pre-installed and builds/loads the Manager Docker image locally.
# CertManager is installed by default; skip with:
# - CERT_MANAGER_INSTALL_SKIP=true
KIND_CLUSTER ?= tsuga-operator-test-e2e

.PHONY: setup-test-e2e
setup-test-e2e: ## Set up a Kind cluster for e2e tests if it does not exist
	@command -v $(KIND) >/dev/null 2>&1 || { \
		echo "Kind is not installed. Please install Kind manually."; \
		exit 1; \
	}
	@case "$$($(KIND) get clusters)" in \
		*"$(KIND_CLUSTER)"*) \
			echo "Kind cluster '$(KIND_CLUSTER)' already exists. Skipping creation." ;; \
		*) \
			echo "Creating Kind cluster '$(KIND_CLUSTER)'..."; \
			$(KIND) create cluster --name $(KIND_CLUSTER) ;; \
	esac

.PHONY: test-e2e
test-e2e: setup-test-e2e manifests generate fmt vet ## Run the e2e tests. Expected an isolated environment using Kind.
	KIND_CLUSTER=$(KIND_CLUSTER) go test ./test/e2e/ -v -ginkgo.v
	$(MAKE) cleanup-test-e2e

.PHONY: cleanup-test-e2e
cleanup-test-e2e: ## Tear down the Kind cluster used for e2e tests
	@$(KIND) delete cluster --name $(KIND_CLUSTER)

.PHONY: lint
lint: golangci-lint ## Run golangci-lint linter
	$(GOLANGCI_LINT) run

.PHONY: lint-fix
lint-fix: golangci-lint ## Run golangci-lint linter and perform fixes
	$(GOLANGCI_LINT) run --fix

.PHONY: lint-config
lint-config: golangci-lint ## Verify golangci-lint linter configuration
	$(GOLANGCI_LINT) config verify

##@ Build

.PHONY: build
build: manifests generate fmt vet ## Build manager binary.
	go build -o bin/manager cmd/main.go

.PHONY: run
run: manifests generate fmt vet ## Run a controller from your host.
	go run ./cmd/main.go

.PHONY: ensure-tsuga-token
ensure-tsuga-token: ## Verify TSUGA_API_TOKEN is set in the environment or .env.
	@test -n "$$TSUGA_API_TOKEN" || { \
		echo "TSUGA_API_TOKEN must be set in $(ENV_FILE) or the environment."; \
		exit 1; \
	}

# Extra flags forwarded to the docker build command.
DOCKER_BUILD_ARGS ?=

# If you wish to build the manager image targeting other platforms you can use the --platform flag.
# (i.e. docker build --platform linux/arm64). However, you must enable docker buildKit for it.
# More info: https://docs.docker.com/develop/develop-images/build_enhancements/
.PHONY: docker-build
docker-build: ## Build docker image with the manager.
	DOCKER_BUILDKIT=1 $(CONTAINER_TOOL) build $(DOCKER_BUILD_ARGS) -t ${IMG} .

.PHONY: docker-push
docker-push: ## Push docker image with the manager.
	$(CONTAINER_TOOL) push ${IMG}

# PLATFORMS defines the target platforms for the manager image be built to provide support to multiple
# architectures. (i.e. make docker-buildx IMG=myregistry/mypoperator:0.0.1). To use this option you need to:
# - be able to use docker buildx. More info: https://docs.docker.com/build/buildx/
# - have enabled BuildKit. More info: https://docs.docker.com/develop/develop-images/build_enhancements/
# - be able to push the image to your registry (i.e. if you do not set a valid value via IMG=<myregistry/image:<tag>> then the export will fail)
# To adequately provide solutions that are compatible with multiple platforms, you should consider using this option.
PLATFORMS ?= linux/arm64,linux/amd64,linux/s390x,linux/ppc64le
.PHONY: docker-buildx
docker-buildx: ## Build and push docker image for the manager for cross-platform support
	# copy existing Dockerfile and insert --platform=${BUILDPLATFORM} into Dockerfile.cross, and preserve the original Dockerfile
	sed -e '1 s/\(^FROM\)/FROM --platform=\$$\{BUILDPLATFORM\}/; t' -e ' 1,// s//FROM --platform=\$$\{BUILDPLATFORM\}/' Dockerfile > Dockerfile.cross
	- $(CONTAINER_TOOL) buildx create --name tsuga-operator-builder
	$(CONTAINER_TOOL) buildx use tsuga-operator-builder
	- $(CONTAINER_TOOL) buildx build --push --platform=$(PLATFORMS) --tag ${IMG} -f Dockerfile.cross .
	- $(CONTAINER_TOOL) buildx rm tsuga-operator-builder
	rm Dockerfile.cross

.PHONY: build-installer
build-installer: manifests generate kustomize ## Generate a consolidated YAML with CRDs and deployment.
	mkdir -p dist
	cd config/manager && $(KUSTOMIZE) edit set image controller=${IMG}
	$(KUSTOMIZE) build config/default > dist/install.yaml

##@ Deployment

ifndef ignore-not-found
  ignore-not-found = false
endif

.PHONY: install
install: manifests kustomize ## Install CRDs into the K8s cluster specified in ~/.kube/config.
	$(KUSTOMIZE) build config/crd | $(KUBECTL) apply -f -

.PHONY: uninstall
uninstall: manifests kustomize ## Uninstall CRDs from the K8s cluster specified in ~/.kube/config. Call with ignore-not-found=true to ignore resource not found errors during deletion.
	$(KUSTOMIZE) build config/crd | $(KUBECTL) delete --ignore-not-found=$(ignore-not-found) -f -

.PHONY: deploy
deploy: manifests kustomize ## Deploy controller to the K8s cluster specified in ~/.kube/config.
	cd config/manager && $(KUSTOMIZE) edit set image controller=${IMG}
	$(KUSTOMIZE) build config/default | $(KUBECTL) apply -f -
	@if [ -n "$(TSUGA_BASE_URL)" ]; then \
		$(KUBECTL) -n $(OPERATOR_NAMESPACE) set env deployment/$(MANAGER_DEPLOYMENT) TSUGA_BASE_URL="$(TSUGA_BASE_URL)"; \
	fi

.PHONY: undeploy
undeploy: kustomize ## Undeploy controller from the K8s cluster specified in ~/.kube/config. Call with ignore-not-found=true to ignore resource not found errors during deletion.
	$(KUSTOMIZE) build config/default | $(KUBECTL) delete --ignore-not-found=$(ignore-not-found) -f -

.PHONY: setup-kind-dev
setup-kind-dev: ## Set up a local Kind cluster for development if it does not exist.
	@command -v $(KIND) >/dev/null 2>&1 || { \
		echo "Kind is not installed. Please install Kind manually."; \
		exit 1; \
	}
	@case "$$($(KIND) get clusters)" in \
		*"$(KIND_DEV_CLUSTER)"*) \
			echo "Kind cluster '$(KIND_DEV_CLUSTER)' already exists. Skipping creation." ;; \
		*) \
			echo "Creating Kind cluster '$(KIND_DEV_CLUSTER)'..."; \
			$(KIND) create cluster --name $(KIND_DEV_CLUSTER) ;; \
	esac

.PHONY: create-tsuga-secret
create-tsuga-secret: ensure-tsuga-token ## Create or update the Tsuga API secret from .env for the active cluster.
	@$(KUBECTL) create namespace $(OPERATOR_NAMESPACE) --dry-run=client -o yaml | $(KUBECTL) apply -f -
	@# The token is read from the exported environment, not expanded by make,
	@# so it never appears in a process's argv.
	@printf '%s' "$$TSUGA_API_TOKEN" | $(KUBECTL) -n $(OPERATOR_NAMESPACE) create secret generic $(TSUGA_SECRET_NAME) \
		--from-file=api-token=/dev/stdin \
		--dry-run=client -o yaml | $(KUBECTL) apply -f -

.PHONY: kind-load
kind-load: docker-build ## Build the operator image and load it into the Kind dev cluster.
	$(KIND) load docker-image $(IMG) --name $(KIND_DEV_CLUSTER)

.PHONY: install-otel-prereqs
install-otel-prereqs: ## Install cert-manager and the OpenTelemetry Operator (the CRDs the collection controllers watch). Idempotent.
	$(KUBECTL) apply -f https://github.com/cert-manager/cert-manager/releases/download/$(CERT_MANAGER_VERSION)/cert-manager.yaml
	$(KUBECTL) wait --for=condition=Available -n cert-manager deploy --all --timeout=180s
	$(KUBECTL) apply -f https://github.com/open-telemetry/opentelemetry-operator/releases/download/$(OTEL_OPERATOR_VERSION)/opentelemetry-operator.yaml
	$(KUBECTL) wait --for=condition=Available -n opentelemetry-operator-system deploy --all --timeout=180s

.PHONY: kind-deploy
kind-deploy: setup-kind-dev install-otel-prereqs create-tsuga-secret ## Build the image, load it into Kind, and deploy the operator locally.
	$(MAKE) kind-load IMG=$(KIND_IMG) KIND_DEV_CLUSTER=$(KIND_DEV_CLUSTER)
	$(MAKE) deploy IMG=$(KIND_IMG) OPERATOR_NAMESPACE=$(OPERATOR_NAMESPACE) MANAGER_DEPLOYMENT=$(MANAGER_DEPLOYMENT)

.PHONY: kind-undeploy
kind-undeploy: ## Remove the operator from the Kind dev cluster.
	$(MAKE) undeploy OPERATOR_NAMESPACE=$(OPERATOR_NAMESPACE)

.PHONY: e2e-fixtures
e2e-fixtures: ## Build the end-to-end suite's fixture images.
	docker build -f test/fixtures/otlp-emitter/Dockerfile -t tsuga-e2e/otlp-emitter:latest .
	docker build -t tsuga-e2e/app-java:latest   test/fixtures/java
	docker build -t tsuga-e2e/app-nodejs:latest test/fixtures/nodejs
	docker build -t tsuga-e2e/app-python:latest test/fixtures/python
	docker build -t tsuga-e2e/app-dotnet:latest test/fixtures/dotnet

.PHONY: e2e-fixtures-smoke
e2e-fixtures-smoke: e2e-fixtures ## Verify each fixture serves traffic and logs to stdout.
	@for image in app-java app-nodejs app-python app-dotnet; do \
		test/fixtures/smoke.sh tsuga-e2e/$$image:latest || exit 1; \
	done

KIND_E2E_FULL_CLUSTER ?= tsuga-operator-e2e-full
FOCUS ?=
LABEL ?=
# Tier C paces ~146 resources at 30s apiece to stay under the API's rate limit,
# so it alone runs well over an hour against a real org. This value feeds BOTH
# `go test -timeout` and `-ginkgo.timeout`: Ginkgo enforces its own suite
# deadline, which defaults to one hour, and silently cuts a run short regardless
# of what go test allows. Override for a shorter focused run.
E2E_TIMEOUT ?= 480m

.PHONY: test-e2e-full
test-e2e-full: ## Run the full e2e suite against a real Tsuga test org. Requires TSUGA_E2E_* credentials.
	@test -n "$$TSUGA_E2E_API_TOKEN"     || { echo "TSUGA_E2E_API_TOKEN is required";     exit 1; }
	@test -n "$$TSUGA_E2E_OTLP_ENDPOINT" || { echo "TSUGA_E2E_OTLP_ENDPOINT is required"; exit 1; }
	@test -n "$$TSUGA_E2E_INGESTION_KEY" || { echo "TSUGA_E2E_INGESTION_KEY is required"; exit 1; }
	@test -n "$$TSUGA_E2E_CLUSTER_ID"    || { echo "TSUGA_E2E_CLUSTER_ID is required";    exit 1; }
	go test ./test/e2efull/ -v -timeout $(E2E_TIMEOUT) -ginkgo.timeout=$(E2E_TIMEOUT) $(if $(FOCUS),-ginkgo.focus='$(FOCUS)',) $(if $(LABEL),-ginkgo.label-filter='$(LABEL)',)

.PHONY: e2e-sweep
e2e-sweep: ## Delete Tsuga resources left by a crashed run. Usage: make e2e-sweep RUNID=e2e-ab12cd34
	@test -n "$(strip $(RUNID))" || { echo "RUNID is required"; exit 1; }
	go run ./test/e2efull/sweep -run $(RUNID)

.PHONY: cleanup-test-e2e-full
cleanup-test-e2e-full: ## Delete the full e2e suite's kind cluster.
	$(KIND) delete cluster --name $(KIND_E2E_FULL_CLUSTER)

##@ Dependencies

## Location to install dependencies to
LOCALBIN ?= $(shell pwd)/bin
$(LOCALBIN):
	mkdir -p $(LOCALBIN)

## Tool Binaries
KUBECTL ?= kubectl
KIND ?= kind
KUSTOMIZE ?= $(LOCALBIN)/kustomize
CONTROLLER_GEN ?= $(LOCALBIN)/controller-gen
ENVTEST ?= $(LOCALBIN)/setup-envtest
GOLANGCI_LINT = $(LOCALBIN)/golangci-lint

## Tool Versions
KUSTOMIZE_VERSION ?= v5.6.0
CONTROLLER_TOOLS_VERSION ?= v0.18.0
#ENVTEST_VERSION is the version of controller-runtime release branch to fetch the envtest setup script (i.e. release-0.20)
ENVTEST_VERSION ?= $(shell go list -m -f "{{ .Version }}" sigs.k8s.io/controller-runtime | awk -F'[v.]' '{printf "release-%d.%d", $$2, $$3}')
#ENVTEST_K8S_VERSION is the version of Kubernetes to use for setting up ENVTEST binaries (i.e. 1.31)
ENVTEST_K8S_VERSION ?= $(shell go list -m -f "{{ .Version }}" k8s.io/api | awk -F'[v.]' '{printf "1.%d", $$3}')
GOLANGCI_LINT_VERSION ?= v2.13.2
CERT_MANAGER_VERSION ?= v1.18.2
# Single source of truth for the OTel Operator version: the README install command,
# `make kind-deploy`, and the CRDs test-otel-schema validates renders against.
OTEL_OPERATOR_VERSION ?= v0.159.0
OTEL_OPERATOR_BUNDLE ?= $(LOCALBIN)/opentelemetry-operator-$(OTEL_OPERATOR_VERSION).yaml
# Bump together with OTEL_OPERATOR_VERSION: the release asset is not signed.
OTEL_OPERATOR_BUNDLE_SHA256 ?= 576ff70530e40c1acbe617b6eadf579ed49c0415935301c10146596baa9a8df4

.PHONY: otel-bundle
otel-bundle: $(OTEL_OPERATOR_BUNDLE) ## Download the pinned OpenTelemetry Operator release bundle (its CRDs are what the render tests validate against).
$(OTEL_OPERATOR_BUNDLE): | $(LOCALBIN)
	curl -sSfL -o $@.tmp https://github.com/open-telemetry/opentelemetry-operator/releases/download/$(OTEL_OPERATOR_VERSION)/opentelemetry-operator.yaml
	echo "$(OTEL_OPERATOR_BUNDLE_SHA256)  $@.tmp" | shasum -a 256 -c - >/dev/null || { rm -f $@.tmp; echo "checksum mismatch for $@"; exit 1; }
	mv $@.tmp $@

.PHONY: kustomize
kustomize: $(KUSTOMIZE) ## Download kustomize locally if necessary.
$(KUSTOMIZE): $(LOCALBIN)
	$(call go-install-tool,$(KUSTOMIZE),sigs.k8s.io/kustomize/kustomize/v5,$(KUSTOMIZE_VERSION))

.PHONY: controller-gen
controller-gen: $(CONTROLLER_GEN) ## Download controller-gen locally if necessary.
$(CONTROLLER_GEN): $(LOCALBIN)
	$(call go-install-tool,$(CONTROLLER_GEN),sigs.k8s.io/controller-tools/cmd/controller-gen,$(CONTROLLER_TOOLS_VERSION))

.PHONY: setup-envtest
setup-envtest: envtest ## Download the binaries required for ENVTEST in the local bin directory.
	@echo "Setting up envtest binaries for Kubernetes version $(ENVTEST_K8S_VERSION)..."
	@$(ENVTEST) use $(ENVTEST_K8S_VERSION) --bin-dir $(LOCALBIN) -p path || { \
		echo "Error: Failed to set up envtest binaries for version $(ENVTEST_K8S_VERSION)."; \
		exit 1; \
	}

.PHONY: envtest
envtest: $(ENVTEST) ## Download setup-envtest locally if necessary.
$(ENVTEST): $(LOCALBIN)
	$(call go-install-tool,$(ENVTEST),sigs.k8s.io/controller-runtime/tools/setup-envtest,$(ENVTEST_VERSION))

.PHONY: golangci-lint
golangci-lint: $(GOLANGCI_LINT) ## Download golangci-lint locally if necessary.
$(GOLANGCI_LINT): $(LOCALBIN)
	$(call go-install-tool,$(GOLANGCI_LINT),github.com/golangci/golangci-lint/v2/cmd/golangci-lint,$(GOLANGCI_LINT_VERSION))

# go-install-tool will 'go install' any package with custom target and name of binary, if it doesn't exist
# $1 - target path with name of binary
# $2 - package url which can be installed
# $3 - specific version of package
define go-install-tool
@[ -f "$(1)-$(3)" ] || { \
set -e; \
package=$(2)@$(3) ;\
echo "Downloading $${package}" ;\
rm -f $(1) || true ;\
GOBIN=$(LOCALBIN) go install $${package} ;\
mv $(1) $(1)-$(3) ;\
} ;\
ln -sf $(1)-$(3) $(1)
endef

##@ Documentation

DOCS_PYTHON ?= .venv-docs/bin/python

.PHONY: docs-setup docs-serve docs-build docs-reference
docs-setup: ## Create an isolated documentation environment and install dependencies.
	python3 -m venv .venv-docs
	$(DOCS_PYTHON) -m pip install -r requirements-docs.txt

docs-serve: ## Preview the documentation at http://127.0.0.1:8000.
	$(DOCS_PYTHON) -m mkdocs serve

docs-build: ## Check API reference and examples, then build the site with strict link validation.
	$(DOCS_PYTHON) hack/docs.py --check
	$(DOCS_PYTHON) -m mkdocs build --strict

docs-reference: ## Regenerate documentation field tables from checked-in CRD schemas.
	$(DOCS_PYTHON) hack/docs.py
