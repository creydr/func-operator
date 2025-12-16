# func-operator

A Kubernetes operator for managing serverless functions using the `func` CLI. This operator automates the deployment and lifecycle management of functions from Git repositories to Kubernetes clusters with Knative.

## Prerequisites

- Kubernetes cluster (1.31+)
- [Knative Serving](https://knative.dev/docs/install/) installed
- [Tekton Pipelines](https://tekton.dev/docs/installation/) installed
- Container registry for storing function images

## Installation

### Install the Operator

Deploy the operator to your cluster:

```bash
# Build and deploy the operator
make docker-build docker-push IMG=<your-registry>/func-operator:v0.0.1
make deploy IMG=<your-registry>/func-operator:v0.0.1
```

Or use the pre-built installer:

```bash
# Generate installer manifests
make build-installer IMG=<your-registry>/func-operator:v0.0.1

# Apply the installer
kubectl apply -f dist/install.yaml
```

## Usage

### Create a Function

Create a `Function` custom resource to deploy a function from a Git repository:

```yaml
apiVersion: functions.dev/v1alpha1
kind: Function
metadata:
  name: my-function
  namespace: default
spec:
  source:
    repositoryUrl: https://github.com/your-org/your-function.git
  registry:
    path: quay.io/your-username/my-function
    authSecretRef:
      name: registry-credentials
```

Apply the resource:

```bash
kubectl apply -f function.yaml
```

### Registry Authentication

For private registries, create a secret with registry credentials:

```yaml
apiVersion: v1
kind: Secret
metadata:
  name: registry-credentials
  namespace: default
type: kubernetes.io/dockerconfigjson
data:
  .dockerconfigjson: <base64-encoded-docker-config>
```

Or use kubectl:

```bash
kubectl create secret docker-registry registry-credentials \
  --docker-server=<registry-url> \
  --docker-username=<username> \
  --docker-password=<password> \
  --docker-email=<email>
```

### Check Function Status

View the status of your function:

```bash
kubectl get function my-function -o yaml
```

The status will include:
- Function name and runtime
- Deployed image reference
- Middleware version
- Deployment conditions

## Development

### Local Development Cluster

For local development, you can use the provided script to set up a Kind cluster with all prerequisites:

```bash
./hack/create-kind-cluster.sh
```

This script will:
- Create a local Kind cluster with multiple worker nodes
- Set up a local container registry on `localhost:5001`
- Install Tekton Pipelines
- Install Knative Serving with Kourier
- Configure the cluster to use the local registry

### Build and Install the Operator

```bash
make docker-build IMG=<your-registry>/func-operator:latest
make deploy IMG=<your-registry>/func-operator:latest
```

### Debugging

For debugging the operator with [Delve](https://github.com/go-delve/delve), use the debug targets:

```bash
# Build the debug image (includes Delve debugger and debug symbols)
make docker-debugger-build IMG=<your-registry>/func-operator:debug

# Push the debug image
make docker-push IMG=<your-registry>/func-operator:debug

# Deploy the operator in debug mode
make deploy-debugger IMG=<your-registry>/func-operator:debug
```

The debug deployment runs the operator under Delve in headless mode, listening on port 40000. To connect your debugger:

```bash
# Port-forward to access the debugger
kubectl port-forward -n func-operator-system deployment/func-operator-controller-manager 40000:40000

# Connect with Delve CLI
dlv connect localhost:40000
```

You can also connect using your IDE's remote debugging features (VS Code, GoLand, etc.) by configuring it to connect to `localhost:40000`.

### Run Tests

```bash
# Unit tests
make test

# E2E tests
make test-e2e
```

### Linting

```bash
# Run linter
make lint
```

## API Reference

### Function Spec

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `source.repositoryUrl` | string | Yes | Git repository URL containing the function source code |
| `registry.path` | string | Yes | Container registry path for the function image |
| `registry.insecure` | boolean | No | Allow insecure registry connections |
| `registry.authSecretRef` | object | No | Reference to registry authentication secret |

### Function Status

| Field | Type | Description |
|-------|------|-------------|
| `name` | string | Function name from metadata |
| `runtime` | string | Detected function runtime |
| `deployedImage` | string | Full image reference of deployed function |
| `middlewareVersion` | string | Version of function middleware |
| `conditions` | array | Status conditions |

## Uninstallation

Remove the operator and CRDs:

```bash
# Undeploy operator
make undeploy

# Uninstall CRDs
make uninstall
```