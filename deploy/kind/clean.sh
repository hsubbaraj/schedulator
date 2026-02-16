#!/usr/bin/env bash
set -euo pipefail

CLUSTER_1="schedulator-1"
CLUSTER_2="schedulator-2"
CLUSTERS=("$CLUSTER_1" "$CLUSTER_2")

usage() {
  cat <<EOF
Usage: $(basename "$0") [--deployments] [--nodes] [--clusters]

Clean up schedulator Kind environments.

Flags:
  --deployments   Delete all schedulator-managed deployments (keeps nodes and clusters)
  --nodes         Delete KWOK fake GPU nodes (keeps clusters)
  --clusters      Delete the Kind clusters entirely

If no flags are given, all three are cleaned (deployments, nodes, clusters).
EOF
  exit 0
}

do_deployments=false
do_nodes=false
do_clusters=false
explicit=false

while [[ $# -gt 0 ]]; do
  case "$1" in
    --deployments) do_deployments=true; explicit=true; shift ;;
    --nodes)       do_nodes=true;       explicit=true; shift ;;
    --clusters)    do_clusters=true;     explicit=true; shift ;;
    -h|--help)     usage ;;
    *) echo "Unknown flag: $1" >&2; usage ;;
  esac
done

# Default: clean everything.
if ! $explicit; then
  do_deployments=true
  do_nodes=true
  do_clusters=true
fi

# --- Deployments ---
if $do_deployments; then
  echo "==> Deleting schedulator deployments..."
  for CLUSTER in "${CLUSTERS[@]}"; do
    CTX="kind-${CLUSTER}"
    if ! kubectl --context "$CTX" cluster-info &>/dev/null 2>&1; then
      echo "    Cluster ${CLUSTER} not reachable, skipping"
      continue
    fi
    deps=$(kubectl --context "$CTX" get deployments -l app.schedulator.io/app-id -o name 2>/dev/null || true)
    if [ -z "$deps" ]; then
      echo "    No schedulator deployments in ${CLUSTER}"
    else
      echo "$deps" | while read -r dep; do
        echo "    Deleting ${dep} in ${CLUSTER}"
        kubectl --context "$CTX" delete "$dep" --wait=false
      done
    fi
  done
fi

# --- Nodes ---
if $do_nodes; then
  echo "==> Deleting KWOK fake nodes..."
  for CLUSTER in "${CLUSTERS[@]}"; do
    CTX="kind-${CLUSTER}"
    if ! kubectl --context "$CTX" cluster-info &>/dev/null 2>&1; then
      echo "    Cluster ${CLUSTER} not reachable, skipping"
      continue
    fi
    nodes=$(kubectl --context "$CTX" get nodes -l type=kwok -o name 2>/dev/null || true)
    if [ -z "$nodes" ]; then
      echo "    No KWOK nodes in ${CLUSTER}"
    else
      echo "$nodes" | while read -r node; do
        echo "    Deleting ${node} in ${CLUSTER}"
        kubectl --context "$CTX" delete "$node"
      done
    fi
  done
fi

# --- Clusters ---
if $do_clusters; then
  echo "==> Deleting Kind clusters..."
  for CLUSTER in "${CLUSTERS[@]}"; do
    if kind get clusters 2>/dev/null | grep -q "^${CLUSTER}$"; then
      echo "    Deleting cluster ${CLUSTER}..."
      kind delete cluster --name "$CLUSTER"
    else
      echo "    Cluster ${CLUSTER} does not exist, skipping"
    fi
  done
  # Clean up kubeconfig files.
  rm -f "${HOME}/.kube/kind-config-${CLUSTER_1}" "${HOME}/.kube/kind-config-${CLUSTER_2}"
  # Clean up SQLite DB.
  rm -f /tmp/schedulator.db
fi

echo "==> Done."
