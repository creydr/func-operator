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
REGISTRY_PORT=${REGISTRY_PORT:-"5001"}

CERT_MANAGER_VERSION="v1.19.2"
SERVING_VERSION="v1.19.0"
TEKTON_VERSION="v1.6.0"

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
  if [ "$(docker inspect -f '{{.State.Running}}' "${REGISTRY_NAME}" 2>/dev/null || true)" == "true" ]; then
    reg_port="$(docker inspect -f '{{ (index (index .NetworkSettings.Ports "5000/tcp") 0).HostPort}}' "${REGISTRY_NAME}" 2>/dev/null)"
    if [ "${reg_port}" != "${REGISTRY_PORT}" ]; then
      header_text "existing registry is running on another port (${reg_port}). Cleaning it up..."
      docker stop "${REGISTRY_NAME}"
      docker rm "${REGISTRY_NAME}"
    fi
  fi

  if [ "$(docker inspect -f '{{.State.Running}}' "${REGISTRY_NAME}" 2>/dev/null || true)" != 'true' ]; then
    header_text "create registry container for port ${REGISTRY_PORT}"
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

function install_tekton() {
  header_text "Install Tekton"
  kubectl apply -f https://storage.googleapis.com/tekton-releases/pipeline/previous/${TEKTON_VERSION}/release.yaml
  kubectl patch configmap feature-flags -n tekton-pipelines --type merge -p '{"data":{"coschedule":"disabled"}}'

  header_text "Waiting for Tekton to be ready..."
  kubectl wait deployment --all --timeout=-1s --for=condition=Available -n tekton-pipelines
  kubectl wait deployment --all --timeout=-1s --for=condition=Available -n tekton-pipelines-resolvers
}

function install_knative_serving() {
  header_text "Installing Knative Serving..."
  kubectl apply -f https://github.com/knative/serving/releases/download/knative-${SERVING_VERSION}/serving-crds.yaml
  kubectl apply -f https://github.com/knative/serving/releases/download/knative-${SERVING_VERSION}/serving-core.yaml
  kubectl apply -f https://github.com/knative/net-kourier/releases/download/knative-${SERVING_VERSION}/kourier.yaml

  kubectl patch configmap/config-network \
    --namespace knative-serving \
    --type merge \
    --patch '{"data":{"ingress-class":"kourier.ingress.networking.knative.dev"}}'

  header_text "Waiting for Knative Serving to be ready..."
  kubectl wait deployment --all --timeout=-1s --for=condition=Available -n knative-serving
  kubectl wait deployment --all --timeout=-1s --for=condition=Available -n kourier-system
}

function install_certmanager() {
  header_text "Installing Cert Manager"

  kubectl apply -f https://github.com/cert-manager/cert-manager/releases/download/${CERT_MANAGER_VERSION}/cert-manager.yaml
  header_text "Waiting for Cert Manager to become ready"
  kubectl wait deployment --all --timeout=-1s --for=condition=Available -n cert-manager
}

if [ "$DELETE_CLUSTER_BEFORE" = "true" ]; then
  delete_existing_cluster
fi

setup_local_registry
create_kind_cluster
connect_registry_to_cluster
install_certmanager
install_tekton
install_knative_serving

header_text "All components installed"