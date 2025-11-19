#!/bin/bash
set -e

echo "🚀 Setting up 3 Kind clusters for Schedulator Phase 1..."

CLUSTERS=("cluster-1" "cluster-2" "cluster-3")
BASE_PORT=30080

for i in "${!CLUSTERS[@]}"; do
    CLUSTER="${CLUSTERS[$i]}"
    PORT=$((BASE_PORT + i))
    
    echo "--------------------------------------------------"
    echo "🛠️  Processing ${CLUSTER} (Port: ${PORT})..."
    echo "--------------------------------------------------"
    
    # Create Cluster
    ./scripts/create-kind-cluster.sh "${CLUSTER}" "${PORT}"
    
    # Install KWOK
    ./scripts/install-kwok.sh "${CLUSTER}"
    
    # Create Nodes
    ./scripts/create-kwok-nodes.sh "${CLUSTER}"
    
    echo "✅ ${CLUSTER} setup complete."
    echo ""
done

echo "🎉 All clusters are ready!"
echo ""
echo "To verify:"
echo "  kubectl get nodes --context kind-cluster-1"
echo "  kubectl get nodes --context kind-cluster-2"
echo "  kubectl get nodes --context kind-cluster-3"
