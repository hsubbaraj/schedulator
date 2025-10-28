#!/bin/bash
set -e

CLUSTER_NAME=${1:-"test-cluster"}
KWOK_VERSION="v0.4.0"

echo "🔧 Installing KWOK in cluster: ${CLUSTER_NAME}"

# Set context
kubectl config use-context "kind-${CLUSTER_NAME}"

# Install KWOK controller
echo "📦 Installing KWOK controller (version ${KWOK_VERSION})..."
kubectl apply -f "https://github.com/kubernetes-sigs/kwok/releases/download/${KWOK_VERSION}/kwok.yaml"

# Wait for KWOK controller to be ready
echo "⏳ Waiting for KWOK controller to be ready..."
kubectl wait --for=condition=ready pod -l app=kwok-controller -n kube-system --timeout=60s

echo "✅ KWOK controller installed successfully"
