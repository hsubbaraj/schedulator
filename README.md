# Schedulator

A global GPU scheduler for LLM inference workloads across a fleet of Kubernetes clusters. Schedulator monitors cluster state, makes scaling and placement decisions, handles preemption and rebalancing, and executes plans — all visible through a real-time browser dashboard.

## Prerequisites

- **Go 1.25+**
- **Node.js 18+** and npm (for the dashboard)
- **kind** — local Kubernetes clusters ([install](https://kind.sigs.k8s.io/docs/user/quick-start/#installation))
- **kubectl**
- **KWOK** — Kubernetes Without Kubelet, for simulating GPU nodes ([install](https://kwok.sigs.k8s.io/docs/user/install/))
- **jq** (used by the launch script)

## Project Structure

```
cmd/schedulator/         # main binary
internal/
  controlloop/           # top-level orchestration
  engine/
    scaling/             # how many replicas?
    placement/           # which cluster, what affinity?
    preemption/          # preemption cascade logic
    rebalancing/         # consolidate fragmentation
  ingestion/             # event ingestion (informers, config watch, timers)
  executor/              # plan executor (orders ops, tracks in-flight)
  plangen/               # plan generator
  worldstate/            # in-memory world state (RWMutex-protected)
  k8saggregator/         # K8s cluster aggregator (client-go informers)
  k8sclient/             # K8s cluster client (deployment CRUD)
  configstore/           # YAML-based application config store
  eventlog/              # SQLite event persistence
  apiserver/             # HTTP API + SSE streaming
  leaderelect/           # leader election (AlwaysLeader stub)
  observability/         # OTel tracing + Prometheus metrics
pkg/
  model/                 # shared domain types
  ports/                 # interfaces for external dependencies
web/                     # React dashboard (Vite + TypeScript + Tailwind)
deploy/kind/             # Kind + KWOK cluster configs
docs/                    # Architecture diagrams and design docs
```

## Quick Start

The fastest way to get a running system with Kind clusters, KWOK nodes, and a full observability stack (SigNoz + ClickHouse):

```bash
# Clone and build the dashboard
cd web && npm install && npm run build && cd ..

# Launch everything (Kind, SigNoz, Schedulator)
./deploy/start.sh
```

This script will:
1. Start the **SigNoz observability stack** (ClickHouse, UI, OTel Gateway) via Docker Compose.
2. Create two Kind clusters (`schedulator-1` and `schedulator-2`) with **KWOK GPU nodes**.
3. Start the schedulator binary with **Tail-based Sampling** enabled (forwarding traces to SigNoz).
4. Serve the dashboard at **http://localhost:8080** and SigNoz at **http://localhost:3301**.

## Manual Setup

If you want more control over the setup, you can run each step yourself.

### 1. Build

```bash
make build
# or
CGO_ENABLED=0 go build -o bin/schedulator ./cmd/schedulator/
```

### 2. Build the Dashboard

For production (static files served by the Go binary):

```bash
cd web
npm install
npm run build    # outputs to web/dist/
cd ..
```

For development (Vite dev server with hot reload, proxies API to Go backend):

```bash
cd web
npm install
npm run dev      # starts on http://localhost:5173, proxies /api → localhost:8080
```

### 3. Configure Clusters

Schedulator discovers clusters from environment variables matching the pattern `KUBECONFIG_<CLUSTER_ID>`. The cluster ID is derived from the env var name (lowercased, underscores replaced with dashes).

```bash
# Each KUBECONFIG_* env var registers a cluster
export KUBECONFIG_US_WEST_1=/path/to/us-west-1.kubeconfig
export KUBECONFIG_EU_CENTRAL_1=/path/to/eu-central-1.kubeconfig
```

This would register clusters `us-west-1` and `eu-central-1`.

### 4. Configure Applications

Create an `apps.yaml` file defining the LLM workloads to schedule:

```yaml
applications:
  - app_id: llama-70b
    model_id: meta-llama-70b
    gpus_per_replica: 4
    priority: 0           # highest priority
    min_replicas: 1
    failure_domain_rule: spread_clusters

  - app_id: codegen-7b
    model_id: codegen-mono-7b
    gpus_per_replica: 1
    priority: 2           # lower priority
    min_replicas: 0
```

### 5. Run

```bash
export KUBECONFIG_CLUSTER_1=~/.kube/schedulator-1.kubeconfig
export KUBECONFIG_CLUSTER_2=~/.kube/schedulator-2.kubeconfig
export APPS_CONFIG=deploy/kind/apps.yaml

./bin/schedulator
# or
go run ./cmd/schedulator/
```

Open **http://localhost:8080** for the dashboard.

### Environment Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `KUBECONFIG_<CLUSTER_ID>` | — | Path to kubeconfig for each cluster. One per cluster. |
| `APPS_CONFIG` | `deploy/kind/apps.yaml` | Path to the application configuration YAML file. |
| `PORT` | `8080` | HTTP server port (API + dashboard + metrics). |
| `DB_PATH` | `schedulator.db` | SQLite database file for event persistence. |
| `STATIC_DIR` | `web/dist` | Directory containing built dashboard static files. |
| `SCHEDULATOR_NAMESPACE` | `default` | Kubernetes namespace to watch for managed pods. |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | — | OTLP gRPC endpoint for distributed tracing. Tracing is disabled if unset. |

## Manipulating the System

Once the schedulator is running with the dashboard open, you can interact with the system and watch it react in real time.

### Add a New Application

Edit `deploy/kind/apps.yaml` (or your configured `APPS_CONFIG` file) and add a new entry:

```yaml
applications:
  - app_id: llama-70b
    model_id: meta-llama-70b
    gpus_per_replica: 4
    priority: 0
    min_replicas: 1
    failure_domain_rule: spread_clusters

  - app_id: codegen-7b
    model_id: codegen-mono-7b
    gpus_per_replica: 1
    priority: 2
    min_replicas: 0

  # Add this new application
  - app_id: mistral-7b
    model_id: mistralai-7b-v0.1
    gpus_per_replica: 1
    priority: 1
    min_replicas: 2
```

The config store watches the file via `fsnotify` — the scheduler picks up the change within seconds and scales up replicas for the new application. Watch the event stream in the dashboard for scale-up events.

### Remove a Node (Simulate Failure)

```bash
# List nodes in a cluster
kubectl get nodes --context kind-schedulator-1

# Delete a KWOK node to simulate failure
kubectl delete node kwok-node-0 --context kind-schedulator-1
```

The K8s aggregator detects the node-down event via its informer watch. The scheduler reacts by rescheduling affected replicas to other nodes or clusters. The dashboard shows the node disappearing and new placement events.

### Add a Node

```bash
# Add a new GPU node to a cluster
kubectl apply --context kind-schedulator-1 -f - <<EOF
apiVersion: v1
kind: Node
metadata:
  name: kwok-node-new
  annotations:
    node.alpha.kubernetes.io/ttl: "0"
    kwok.x-k8s.io/node: fake
  labels:
    type: kwok
spec:
  taints:
    - effect: NoSchedule
      key: kwok.x-k8s.io/node
      value: fake
status:
  allocatable:
    cpu: "32"
    memory: 256Gi
    nvidia.com/gpu: "8"
    pods: "110"
  capacity:
    cpu: "32"
    memory: 256Gi
    nvidia.com/gpu: "8"
    pods: "110"
  conditions:
    - type: Ready
      status: "True"
  phase: Running
EOF
```

### Cordon a Node

```bash
# Mark a node as unschedulable (cordon)
kubectl cordon kwok-node-1 --context kind-schedulator-2
```

The scheduler sees the node status change to `Cordoned` and avoids placing new replicas there.

### Change Application Priority

Edit `apps.yaml` and change a priority value. Lower numbers = higher priority. A priority change can trigger preemption if a higher-priority app needs GPUs currently held by a lower-priority app.

```yaml
  - app_id: codegen-7b
    model_id: codegen-mono-7b
    gpus_per_replica: 1
    priority: 0           # promoted from priority 2 to 0
    min_replicas: 1       # now requires at least 1 replica
```

### Scale an Application

Change `min_replicas` or `gpus_per_replica` in `apps.yaml`:

```yaml
  - app_id: llama-70b
    model_id: meta-llama-70b
    gpus_per_replica: 4
    priority: 0
    min_replicas: 3       # increased from 1 to 3
    failure_domain_rule: spread_clusters
```

### Remove a Cluster

To simulate losing an entire cluster:

```bash
kind delete cluster --name schedulator-2
```

All replicas on that cluster become unavailable. The scheduler detects the loss and reschedules workloads to the remaining cluster(s).

## API Endpoints

The HTTP API is available at the same port as the dashboard:

| Method | Path | Description |
|--------|------|-------------|
| GET | `/api/v1/state` | Full world state snapshot (clusters, nodes, apps) |
| GET | `/api/v1/clusters` | All clusters with node details |
| GET | `/api/v1/applications` | All apps with replica counts |
| GET | `/api/v1/events/stream` | SSE stream of live scheduler events |
| GET | `/api/v1/events/history?since=&limit=` | Historical events from SQLite |
| GET | `/api/v1/snapshots?cluster_id=&since=` | GPU utilization time-series |
| GET | `/healthz` | Health check |
| GET | `/metrics` | Prometheus metrics |

Examples:

```bash
# Get current world state
curl -s http://localhost:8080/api/v1/state | jq .

# List clusters
curl -s http://localhost:8080/api/v1/clusters | jq .

# Get recent events
curl -s 'http://localhost:8080/api/v1/events/history?limit=10' | jq .

# Stream live events
curl -N http://localhost:8080/api/v1/events/stream

# GPU utilization for a cluster in the last hour
curl -s "http://localhost:8080/api/v1/snapshots?cluster_id=cluster-1&since=$(date -u -v-1H +%Y-%m-%dT%H:%M:%SZ)" | jq .
```

## Testing

```bash
# Run all unit tests (CGO_ENABLED=0 required on Mac)
CGO_ENABLED=0 go test ./...

# Run with race detector (Linux/CI)
go test -race ./...

# Run via Makefile
make test

# Integration tests (requires Kind clusters)
make test-integration

# Lint
make lint
```

## Architecture

See `docs/` for detailed design documentation:

- `docs/multi-cluster-gpu-scheduler-proposal-v2.md` — full design proposal with algorithms
- `docs/01-architecture-overview.mermaid` — system architecture diagram
- `docs/05-data-model.mermaid` — domain model
- `docs/06-placement-engine-flow.mermaid` — placement and preemption flow
- `docs/07-control-loop-state.mermaid` — control loop state machine

## Cleanup

```bash
# Delete Kind clusters
kind delete cluster --name schedulator-1
kind delete cluster --name schedulator-2

# Remove SQLite database
rm -f schedulator.db

# Clean build artifacts
make clean
```
