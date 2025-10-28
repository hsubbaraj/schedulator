#!/bin/bash
set -e

CLUSTER_NAME=${1:-"test-cluster"}

echo "🖥️  Creating KWOK nodes in cluster: ${CLUSTER_NAME}"

# Set context
kubectl config use-context "kind-${CLUSTER_NAME}"

# Apply KWOK nodes
kubectl apply -f config/kwok-nodes.yaml

# Wait for nodes to be ready
echo "⏳ Waiting for nodes to be ready..."
kubectl wait --for=condition=ready node kwok-node-1 --timeout=30s
kubectl wait --for=condition=ready node kwok-node-2 --timeout=30s

echo "✅ KWOK nodes created successfully"
echo ""
echo "📊 Node status:"
kubectl get nodes -o wide

echo ""
echo "🎮 GPU capacity:"
kubectl get nodes -o jsonpath='{range .items[*]}{.metadata.name}{"\t"}{.status.capacity.nvidia\.com/gpu}{"\n"}{end}'
