#!/usr/bin/env bash
set -euo pipefail

# --- Configuration ---
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
KIND_SCRIPT="$SCRIPT_DIR/kind/start.sh"

echo "==> [1/3] Starting Observability Stack (SigNoz + ClickHouse)..."
cd "$PROJECT_ROOT"
docker compose up -d

echo "==> [2/3] Setting up Kind Clusters with KWOK..."
# We run the kind start script but we'll stop it before it runs the app.
# Since the kind script ends with 'go run', we'll extract the setup logic or 
# just set the environment variables it produces.
export CLUSTER_1="schedulator-1"
export CLUSTER_2="schedulator-2"
export KUBECONFIG_SCHEDULATOR_1="${HOME}/.kube/kind-config-${CLUSTER_1}"
export KUBECONFIG_SCHEDULATOR_2="${HOME}/.kube/kind-config-${CLUSTER_2}"
export APPS_CONFIG="$SCRIPT_DIR/kind/apps.yaml"
export DB_PATH="/tmp/schedulator.db"
export PORT=8080

# Run kind setup (this will also try to start the app at the end, so we use a trick 
# or just re-implement the setup call here to be safe and clean).
# For now, let's just ensure the clusters exist and configs are exported.

for CLUSTER in "$CLUSTER_1" "$CLUSTER_2"; do
  if ! kind get clusters 2>/dev/null | grep -q "^${CLUSTER}$"; then
    echo "    Creating cluster ${CLUSTER}..."
    kind create cluster --name "$CLUSTER" --config "$SCRIPT_DIR/kind/kind-config.yaml"
  fi
  
  # Export kubeconfig
  kind export kubeconfig --name "$CLUSTER" --kubeconfig "${HOME}/.kube/kind-config-${CLUSTER}"
done

# Install KWOK if needed (simplified check)
for CLUSTER in "$CLUSTER_1" "$CLUSTER_2"; do
  CTX="kind-${CLUSTER}"
  if ! kubectl --context "$CTX" get deployment -n kube-system kwok-controller &>/dev/null; then
    echo "    Installing KWOK in ${CLUSTER}..."
    kubectl --context "$CTX" apply -f "https://github.com/kubernetes-sigs/kwok/releases/download/v0.7.0/kwok.yaml"
    kubectl --context "$CTX" apply -f "https://github.com/kubernetes-sigs/kwok/releases/download/v0.7.0/stage-fast.yaml"
    kubectl --context "$CTX" patch deployment kwok-controller -n kube-system 
      --type=json -p '[{"op":"add","path":"/spec/template/spec/nodeSelector","value":{"node-role.kubernetes.io/control-plane":""}}]'
    
    # Create KWOK GPU nodes
    for i in 0 1 2; do
      NODE_NAME="kwok-gpu-node-${i}"
      sed "s/kwok-gpu-node-0/${NODE_NAME}/" "$SCRIPT_DIR/kind/kwok-nodes.yaml" | kubectl --context "$CTX" apply -f -
    done
  fi
done

echo "==> [3/3] Launching Schedulator with Tracing Enabled..."
export OTEL_EXPORTER_OTLP_ENDPOINT="localhost:4317"
export APP_ENV="development"
export OTEL_SAMPLING_RATIO="1.0"

echo ""
echo "🚀 Schedulator is starting!"
echo "📊 SigNoz Dashboard: http://localhost:3301"
echo "🖥️  Schedulator UI:  http://localhost:8080"
echo "📈 Metrics:         http://localhost:8080/metrics"
echo ""

cd "$PROJECT_ROOT"
# We use CGO_ENABLED=1 because modernc.org/sqlite (pure go) is used, 
# but we want to be sure it's clean.
go run ./cmd/schedulator/
