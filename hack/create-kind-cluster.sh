#!/bin/bash

set -e
set -o errexit
set -o nounset
set -o pipefail

# Default configuration
DELETE_CLUSTER_BEFORE=true
CLUSTER_NAME=${CLUSTER_NAME:-kind}
NODE_VERSION="v1.34.0"
REGISTRY_NAME="kind-registry"
REGISTRY_PORT="5001"

header=$'\e[1;33m'
reset=$'\e[0m'

function header_text {
	echo "$header$*$reset"
}

function delete_existing_cluster() {
  header_text "Deleting existing Kind cluster..."
  kind delete cluster --name "$CLUSTER_NAME" || true
}

function setup_local_registry() {
  if [ "$(docker inspect -f '{{.State.Running}}' "${REGISTRY_NAME}" 2>/dev/null || true)" != 'true' ]; then
    header_text "create registry container"
    docker run -d --restart=always -p "127.0.0.1:${REGISTRY_PORT}:5000" --name "${REGISTRY_NAME}" docker.io/registry:2
  fi
}

function create_kind_cluster() {
  header_text "Creating Kind cluster '$CLUSTER_NAME'..."
  cat <<EOF | kind create cluster --name "$CLUSTER_NAME" --config=-
kind: Cluster
apiVersion: kind.x-k8s.io/v1alpha4
nodes:
- role: control-plane
  image: kindest/node:$NODE_VERSION
- role: worker
  image: kindest/node:$NODE_VERSION
containerdConfigPatches:
- |-
  [plugins."io.containerd.grpc.v1.cri".registry.mirrors."localhost:$REGISTRY_PORT"]
    endpoint = ["http://$REGISTRY_NAME:5000"]
EOF
}

function connect_registry_to_cluster() {
  if [ "$(docker inspect -f='{{json .NetworkSettings.Networks.kind}}' "${REGISTRY_NAME}")" = 'null' ]; then
    header_text "connect the registry to the cluster network"
    docker network connect "kind" "${REGISTRY_NAME}"
  fi

  # Document the local registry
  kubectl apply -f - <<EOF
apiVersion: v1
kind: ConfigMap
metadata:
  name: local-registry-hosting
  namespace: kube-public
data:
  localRegistryHosting.v1: |
    host: "localhost:$REGISTRY_PORT"
    help: "https://kind.sigs.k8s.io/docs/user/local-registry/"
EOF
}

if [ "$DELETE_CLUSTER_BEFORE" = "true" ]; then
  delete_existing_cluster
fi

setup_local_registry
create_kind_cluster
connect_registry_to_cluster

header_text "All components installed"