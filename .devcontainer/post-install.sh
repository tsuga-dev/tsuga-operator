#!/bin/bash
set -euo pipefail
set -x

KIND_VERSION=v0.33.0
# kubebuilder release matching the go.kubebuilder.io/v4 layout in PROJECT.
KUBEBUILDER_VERSION=v4.6.0
# Matches the default node image of KIND_VERSION (kindest/node:v1.37.0).
KUBECTL_VERSION=v1.37.0

curl --fail --location -o ./kind "https://kind.sigs.k8s.io/dl/$KIND_VERSION/kind-linux-amd64"
chmod +x ./kind
mv ./kind /usr/local/bin/kind

curl --fail --location -o kubebuilder "https://github.com/kubernetes-sigs/kubebuilder/releases/download/$KUBEBUILDER_VERSION/kubebuilder_linux_amd64"
chmod +x kubebuilder
mv kubebuilder /usr/local/bin/

curl --fail --location -O "https://dl.k8s.io/release/$KUBECTL_VERSION/bin/linux/amd64/kubectl"
chmod +x kubectl
mv kubectl /usr/local/bin/kubectl

docker network inspect kind >/dev/null 2>&1 || docker network create -d=bridge --subnet=172.19.0.0/24 kind

kind version
kubebuilder version
docker --version
go version
kubectl version --client
