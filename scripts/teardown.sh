#!/bin/bash
set -e

echo "🧹 Tearing down Schedulator clusters..."

CLUSTERS=("cluster-1" "cluster-2" "cluster-3")

for CLUSTER in "${CLUSTERS[@]}"; do
    echo "Deleting ${CLUSTER}..."
    kind delete cluster --name "${CLUSTER}"
done

echo "✅ Cleanup complete."
