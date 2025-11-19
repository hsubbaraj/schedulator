#!/bin/bash
set -e

CLUSTER_NAME=${1:-"test-cluster"}
HOST_PORT=${2:-30080}

echo "🚀 Creating Kind cluster: ${CLUSTER_NAME} (Port: ${HOST_PORT})"

# Check if cluster already exists
if kind get clusters 2>/dev/null | grep -q "^${CLUSTER_NAME}$"; then
    echo "⚠️  Cluster ${CLUSTER_NAME} already exists. Deleting..."
    kind delete cluster --name "${CLUSTER_NAME}"
fi

# Create cluster
cat <<EOF | kind create cluster --name "${CLUSTER_NAME}" --config=-
kind: Cluster
apiVersion: kind.x-k8s.io/v1alpha4
nodes:
- role: control-plane
  extraPortMappings:
  - containerPort: 30080
    hostPort: ${HOST_PORT}
    protocol: TCP
EOF

echo "✅ Cluster ${CLUSTER_NAME} created successfully"

# Set kubectl context
kubectl config use-context "kind-${CLUSTER_NAME}"

echo "📋 Cluster info:"
kubectl cluster-info --context "kind-${CLUSTER_NAME}"
