#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"

CLUSTER_1="schedulator-1"
CLUSTER_2="schedulator-2"
KWOK_RELEASE="v0.7.0"

# --- Pre-flight checks ---
for cmd in kind kubectl docker jq go; do
  if ! command -v "$cmd" &>/dev/null; then
    echo "ERROR: '$cmd' is required but not found in PATH." >&2
    exit 1
  fi
done

# Kind >= 0.20.0 is needed for kindest/node:v1.31.x images.
KIND_VERSION=$(kind version | grep -oE 'v[0-9]+\.[0-9]+\.[0-9]+' | head -1)
KIND_MINOR=$(echo "$KIND_VERSION" | cut -d. -f2)
if [ "${KIND_MINOR:-0}" -lt 20 ]; then
  echo "ERROR: kind $KIND_VERSION is too old. Please upgrade to kind >= v0.20.0" >&2
  echo "       brew install kind   OR   go install sigs.k8s.io/kind@latest" >&2
  exit 1
fi

echo "==> Pre-flight OK (kind $KIND_VERSION, kwok $KWOK_RELEASE)"
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
CGO_ENABLED=0 go run ./cmd/schedulator/
