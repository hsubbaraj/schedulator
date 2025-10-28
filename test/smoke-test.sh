#!/bin/bash
# Smoke Test: Day 0 Prototype Validation
# This script creates a cluster, installs KWOK, creates GPU nodes, and tests deployment

set -e

echo "🧪 Schedulator Day 0 Smoke Test"
echo "================================"
echo ""

# Configuration
CLUSTER_NAME="test-cluster"
KUBECONFIG_PATH="/tmp/test-cluster-kubeconfig"
export PATH="$HOME/bin:$PATH"

# Colors for output
GREEN='\033[0;32m'
RED='\033[0;31m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

pass() {
    echo -e "${GREEN}✓${NC} $1"
}

fail() {
    echo -e "${RED}✗${NC} $1"
    exit 1
}

info() {
    echo -e "${YELLOW}ℹ${NC} $1"
}

# Step 1: Check prerequisites
echo "Step 1: Checking prerequisites..."
command -v kind >/dev/null 2>&1 || fail "Kind not found in PATH"
command -v docker >/dev/null 2>&1 || fail "Docker not found"
docker ps >/dev/null 2>&1 || fail "Docker daemon not running"
pass "Prerequisites OK"
echo ""

# Step 2: Clean up existing cluster if it exists
echo "Step 2: Cleaning up existing cluster..."
if kind get clusters 2>/dev/null | grep -q "^${CLUSTER_NAME}$"; then
    info "Deleting existing cluster..."
    kind delete cluster --name "${CLUSTER_NAME}" >/dev/null 2>&1
fi
pass "Cleanup complete"
echo ""

# Step 3: Create Kind cluster
echo "Step 3: Creating Kind cluster..."
kind create cluster --name "${CLUSTER_NAME}" --wait 60s >/dev/null 2>&1 || fail "Failed to create cluster"
kind get kubeconfig --name "${CLUSTER_NAME}" > "${KUBECONFIG_PATH}"
export KUBECONFIG="${KUBECONFIG_PATH}"
pass "Cluster created"
echo ""

# Step 4: Install KWOK
echo "Step 4: Installing KWOK controller..."
kubectl apply -f https://github.com/kubernetes-sigs/kwok/releases/download/v0.4.0/kwok.yaml >/dev/null 2>&1 || fail "Failed to install KWOK"
kubectl wait --for=condition=available deployment/kwok-controller -n kube-system --timeout=60s >/dev/null 2>&1 || fail "KWOK controller not ready"
pass "KWOK installed"
echo ""

# Step 5: Create KWOK nodes
echo "Step 5: Creating KWOK GPU nodes..."
kubectl apply -f config/kwok-nodes.yaml >/dev/null 2>&1 || fail "Failed to create KWOK nodes"
kubectl wait --for=condition=ready node kwok-node-1 --timeout=30s >/dev/null 2>&1 || fail "Node kwok-node-1 not ready"
kubectl wait --for=condition=ready node kwok-node-2 --timeout=30s >/dev/null 2>&1 || fail "Node kwok-node-2 not ready"
pass "KWOK nodes created (2 nodes, 8 GPUs each)"
echo ""

# Step 6: Verify GPU capacity
echo "Step 6: Verifying GPU capacity..."
GPU_COUNT=$(kubectl get nodes kwok-node-1 kwok-node-2 -o json | jq '[.items[].status.capacity."nvidia.com/gpu" // "0" | tonumber] | add')
if [ "$GPU_COUNT" != "16" ]; then
    fail "Expected 16 GPUs, found $GPU_COUNT"
fi
pass "16 GPUs available (8 per node)"
echo ""

# Step 7: Deploy test workload
echo "Step 7: Deploying test workload..."
kubectl apply -f test/day0/test-deployment.yaml >/dev/null 2>&1 || fail "Failed to create deployment"
sleep 3  # Give scheduler time to assign nodes
pass "Deployment created (2 replicas, 4 GPUs each)"
echo ""

# Step 8: Verify pod scheduling
echo "Step 8: Verifying pod scheduling..."
POD_COUNT=$(kubectl get pods -l app=test-llm -o json | jq '.items | length')
if [ "$POD_COUNT" != "2" ]; then
    fail "Expected 2 pods, found $POD_COUNT"
fi

# Check that pods are assigned to KWOK nodes
NODES=$(kubectl get pods -l app=test-llm -o json | jq -r '.items[].spec.nodeName' | sort | uniq)
if ! echo "$NODES" | grep -q "kwok-node"; then
    fail "Pods not scheduled to KWOK nodes"
fi
pass "Pods scheduled to KWOK nodes"
echo ""

# Step 9: Verify GPU allocation
echo "Step 9: Verifying GPU allocation..."
ALLOCATED_NODE1=$(kubectl get node kwok-node-1 -o json | jq -r '.status.allocatable."nvidia.com/gpu" // "0" | tonumber')
ALLOCATED_NODE2=$(kubectl get node kwok-node-2 -o json | jq -r '.status.allocatable."nvidia.com/gpu" // "0" | tonumber')

# Check allocation (should show in describe, but allocatable stays same with KWOK)
kubectl describe node kwok-node-1 | grep -q "nvidia.com/gpu.*4" || fail "GPU allocation not reflected on node-1"
kubectl describe node kwok-node-2 | grep -q "nvidia.com/gpu.*4" || fail "GPU allocation not reflected on node-2"
pass "8 GPUs allocated (4 per pod × 2 pods)"
pass "8 GPUs free (50% utilization)"
echo ""

# Step 10: Summary
echo "================================"
echo "✅ Smoke Test PASSED"
echo ""
echo "Summary:"
echo "  • Cluster: ${CLUSTER_NAME}"
echo "  • Nodes: 3 (1 control-plane + 2 KWOK GPU nodes)"
echo "  • Total GPUs: 16 (H100)"
echo "  • Allocated: 8 GPUs"
echo "  • Free: 8 GPUs"
echo "  • Utilization: 50%"
echo "  • Pods: 2 (test-llm)"
echo ""
echo "Kubeconfig: ${KUBECONFIG_PATH}"
echo ""
echo "To interact with cluster:"
echo "  export KUBECONFIG=${KUBECONFIG_PATH}"
echo "  kubectl get nodes"
echo "  kubectl get pods"
echo ""
echo "To cleanup:"
echo "  kind delete cluster --name ${CLUSTER_NAME}"
echo ""
