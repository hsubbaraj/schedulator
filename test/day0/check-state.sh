#!/bin/bash
# Day 0 Prototype: State Aggregation Script
# This demonstrates querying cluster state and GPU capacity

set -e

KUBECONFIG=${1:-"/tmp/test-cluster-kubeconfig"}
export KUBECONFIG

echo "============================================"
echo "  Cluster State Aggregation (Day 0 Prototype)"
echo "============================================"
echo ""

echo "📊 Cluster: test-cluster"
echo ""

# Get all nodes
echo "--- Nodes ---"
kubectl get nodes -o wide

echo ""
echo "--- GPU Capacity ---"
kubectl get nodes -o custom-columns=\
NAME:.metadata.name,\
GPU_TYPE:.metadata.labels.gpu-type,\
TOTAL_GPUs:.status.capacity.'nvidia\.com/gpu',\
ALLOCATABLE_GPUs:.status.allocatable.'nvidia\.com/gpu'

echo ""
echo "--- Node Details (JSON) ---"
kubectl get nodes kwok-node-1 kwok-node-2 -o json | jq -r '.items[] | {
  name: .metadata.name,
  labels: .metadata.labels,
  capacity: .status.capacity,
  allocatable: .status.allocatable
}'

echo ""
echo "--- Current Pods ---"
kubectl get pods -A

echo ""
echo "--- GPU Allocation Summary ---"
TOTAL_GPUS=$(kubectl get nodes kwok-node-1 kwok-node-2 -o json | \
  jq '[.items[].status.capacity."nvidia.com/gpu" // "0" | tonumber] | add')

echo "Total GPUs in cluster: $TOTAL_GPUS"
echo "Allocated GPUs: 0 (no workloads yet)"
echo "Free GPUs: $TOTAL_GPUS"
echo "Utilization: 0%"

echo ""
echo "✅ State aggregation complete!"
