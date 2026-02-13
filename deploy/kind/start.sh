#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"

CLUSTER_1="schedulator-1"
CLUSTER_2="schedulator-2"

echo "==> Creating Kind clusters..."

for CLUSTER in "$CLUSTER_1" "$CLUSTER_2"; do
  if kind get clusters 2>/dev/null | grep -q "^${CLUSTER}$"; then
    echo "    Cluster ${CLUSTER} already exists, skipping"
  else
    kind create cluster --name "$CLUSTER" --config "$SCRIPT_DIR/kind-config.yaml"
  fi
done

echo "==> Installing KWOK in clusters..."

KWOK_REPO=kubernetes-sigs/kwok
KWOK_RELEASE=$(curl -s "https://api.github.com/repos/${KWOK_REPO}/releases/latest" | jq -r '.tag_name')

for CLUSTER in "$CLUSTER_1" "$CLUSTER_2"; do
  CTX="kind-${CLUSTER}"

  # Install KWOK controller if not present.
  if ! kubectl --context "$CTX" get deployment -n kube-system kwok-controller &>/dev/null; then
    echo "    Installing KWOK ${KWOK_RELEASE} in ${CLUSTER}..."
    kubectl --context "$CTX" apply -f "https://github.com/${KWOK_REPO}/releases/download/${KWOK_RELEASE}/kwok.yaml"
    kubectl --context "$CTX" apply -f "https://github.com/${KWOK_REPO}/releases/download/${KWOK_RELEASE}/stage-fast.yaml"
  else
    echo "    KWOK already installed in ${CLUSTER}"
  fi

  # Create KWOK GPU nodes (3 per cluster).
  for i in 0 1 2; do
    NODE_NAME="kwok-gpu-node-${i}"
    if kubectl --context "$CTX" get node "$NODE_NAME" &>/dev/null; then
      echo "    Node ${NODE_NAME} already exists in ${CLUSTER}"
    else
      echo "    Creating ${NODE_NAME} in ${CLUSTER}..."
      sed "s/kwok-gpu-node-0/${NODE_NAME}/" "$SCRIPT_DIR/kwok-nodes.yaml" | kubectl --context "$CTX" apply -f -
    fi
  done
done

echo "==> Waiting for KWOK nodes to be ready..."
for CLUSTER in "$CLUSTER_1" "$CLUSTER_2"; do
  CTX="kind-${CLUSTER}"
  for i in 0 1 2; do
    kubectl --context "$CTX" wait --for=condition=Ready "node/kwok-gpu-node-${i}" --timeout=60s 2>/dev/null || true
  done
done

echo "==> Setting up environment variables..."

export KUBECONFIG_SCHEDULATOR_1="${HOME}/.kube/kind-config-${CLUSTER_1}"
export KUBECONFIG_SCHEDULATOR_2="${HOME}/.kube/kind-config-${CLUSTER_2}"

# Kind stores kubeconfigs in a merged file; extract per-cluster configs.
kind get kubeconfig --name "$CLUSTER_1" > "$KUBECONFIG_SCHEDULATOR_1" 2>/dev/null || \
  kind export kubeconfig --name "$CLUSTER_1" --kubeconfig "$KUBECONFIG_SCHEDULATOR_1"

kind get kubeconfig --name "$CLUSTER_2" > "$KUBECONFIG_SCHEDULATOR_2" 2>/dev/null || \
  kind export kubeconfig --name "$CLUSTER_2" --kubeconfig "$KUBECONFIG_SCHEDULATOR_2"

export APPS_CONFIG="$SCRIPT_DIR/apps.yaml"
export DB_PATH="/tmp/schedulator.db"
export PORT=8080

echo ""
echo "==> Starting Schedulator..."
echo "    Clusters: ${CLUSTER_1}, ${CLUSTER_2}"
echo "    Apps config: ${APPS_CONFIG}"
echo "    Dashboard: http://localhost:${PORT}"
echo ""

cd "$PROJECT_ROOT"
go run ./cmd/schedulator/
