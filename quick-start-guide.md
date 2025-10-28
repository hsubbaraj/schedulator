# Quick Start Guide: Phase 1 MVP

This guide gets you from zero to running system in ~2 hours (assuming dependencies installed).

---

## Prerequisites

```bash
# Install Go
brew install go  # macOS
# or download from https://go.dev/dl/

# Install Docker Desktop
# https://www.docker.com/products/docker-desktop/

# Install Kind
brew install kind  # macOS
# or: GO111MODULE="on" go install sigs.k8s.io/kind@latest

# Install kubectl
brew install kubectl  # macOS

# Verify installations
go version        # Should be 1.21+
docker --version
kind --version
kubectl version --client
```

---

## Step 1: Create Project Structure (5 minutes)

```bash
# Create project directory
mkdir -p ~/projects/scheduler-simulator
cd ~/projects/scheduler-simulator

# Initialize Go module
go mod init github.com/yourorg/scheduler-simulator

# Create directory structure
mkdir -p cmd/simulator
mkdir -p cmd/scheduler/random
mkdir -p pkg/simulator/{state,api,enforcer,metrics,workload}
mkdir -p pkg/scheduler/{client,types}
mkdir -p pkg/k8s/{client,resources}
mkdir -p scripts
mkdir -p scenarios
mkdir -p test/{integration,e2e}
mkdir -p output
mkdir -p docs
```

---

## Step 2: Setup Kind Clusters (10 minutes)

Create `scripts/setup-kind.sh`:

```bash
#!/bin/bash
set -e

echo "Creating 3 Kind clusters..."

# Cluster 1 (us-west-2)
cat <<EOF | kind create cluster --name cluster-1 --config=-
kind: Cluster
apiVersion: kind.x-k8s.io/v1alpha4
nodes:
- role: control-plane
EOF

# Cluster 2 (us-east-1)
cat <<EOF | kind create cluster --name cluster-2 --config=-
kind: Cluster
apiVersion: kind.x-k8s.io/v1alpha4
nodes:
- role: control-plane
EOF

# Cluster 3 (eu-west-1)
cat <<EOF | kind create cluster --name cluster-3 --config=-
kind: Cluster
apiVersion: kind.x-k8s.io/v1alpha4
nodes:
- role: control-plane
EOF

echo "✅ Clusters created successfully"
kubectl config get-contexts | grep kind-cluster
```

Create `scripts/install-kwok.sh`:

```bash
#!/bin/bash
set -e

# Install KWOK controller in each cluster
for cluster in cluster-1 cluster-2 cluster-3; do
  echo "Installing KWOK in $cluster..."
  kubectl --context kind-$cluster apply -f https://github.com/kubernetes-sigs/kwok/releases/download/v0.4.0/kwok.yaml

  # Wait for KWOK controller to be ready
  kubectl --context kind-$cluster wait --for=condition=ready pod -l app=kwok-controller -n kube-system --timeout=60s
done

echo "✅ KWOK installed in all clusters"
```

Create `scripts/create-kwok-nodes.sh`:

```bash
#!/bin/bash
set -e

# Create KWOK nodes in each cluster
create_node() {
  local cluster=$1
  local node_name=$2
  local gpu_count=$3

  kubectl --context kind-$cluster apply -f - <<EOF
apiVersion: v1
kind: Node
metadata:
  annotations:
    node.alpha.kubernetes.io/ttl: "0"
    kwok.x-k8s.io/node: fake
  labels:
    beta.kubernetes.io/arch: amd64
    beta.kubernetes.io/os: linux
    kubernetes.io/arch: amd64
    kubernetes.io/hostname: $node_name
    kubernetes.io/os: linux
    kubernetes.io/role: agent
    node-role.kubernetes.io/agent: ""
    type: kwok
    gpu-type: H100
  name: $node_name
spec:
  taints:
  - effect: NoSchedule
    key: kwok.x-k8s.io/node
    value: fake
status:
  allocatable:
    cpu: "96"
    memory: 512Gi
    nvidia.com/gpu: "$gpu_count"
    pods: "110"
  capacity:
    cpu: "96"
    memory: 512Gi
    nvidia.com/gpu: "$gpu_count"
    pods: "110"
  nodeInfo:
    architecture: amd64
    bootID: ""
    containerRuntimeVersion: ""
    kernelVersion: ""
    kubeProxyVersion: fake
    kubeletVersion: fake
    machineID: ""
    operatingSystem: linux
    osImage: ""
    systemUUID: ""
  phase: Running
EOF
}

# Cluster 1: 3 nodes with 8 GPUs each
for i in 1 2 3; do
  create_node cluster-1 "cluster-1-node-$i" 8
done

# Cluster 2: 3 nodes with 8 GPUs each
for i in 1 2 3; do
  create_node cluster-2 "cluster-2-node-$i" 8
done

# Cluster 3: 2 nodes with 8 GPUs each
for i in 1 2; do
  create_node cluster-3 "cluster-3-node-$i" 8
done

echo "✅ KWOK nodes created"
echo ""
echo "Verify nodes:"
for cluster in cluster-1 cluster-2 cluster-3; do
  echo "=== $cluster ==="
  kubectl --context kind-$cluster get nodes
  echo ""
done
```

Run the scripts:

```bash
chmod +x scripts/*.sh
./scripts/setup-kind.sh
./scripts/install-kwok.sh
./scripts/create-kwok-nodes.sh
```

---

## Step 3: Implement Core Types (15 minutes)

Create `pkg/simulator/state/types.go`:

```go
package state

import "time"

// ClusterState represents the aggregated state across all clusters
type ClusterState struct {
    Version   string    `json:"version"`
    Timestamp time.Time `json:"timestamp"`
    Clusters  []Cluster `json:"clusters"`
    PendingQueue []Workload `json:"pendingQueue"`
}

// Cluster represents a single K8s cluster
type Cluster struct {
    ID      string            `json:"id"`
    Region  string            `json:"region"`
    Nodes   []Node            `json:"nodes"`
    Pods    []Pod             `json:"pods"`
    Summary ClusterSummary    `json:"summary"`
}

// Node represents a K8s node
type Node struct {
    Name        string            `json:"name"`
    Labels      map[string]string `json:"labels"`
    Capacity    ResourceList      `json:"capacity"`
    Allocatable ResourceList      `json:"allocatable"`
    Allocated   ResourceList      `json:"allocated"`
    Conditions  map[string]string `json:"conditions"`
    Taints      []string          `json:"taints"`
}

// Pod represents a running pod
type Pod struct {
    Name      string       `json:"name"`
    Namespace string       `json:"namespace"`
    ModelID   string       `json:"modelId"`
    Phase     string       `json:"phase"`
    NodeName  string       `json:"nodeName"`
    Requests  ResourceList `json:"requests"`
}

// ResourceList represents resource quantities
type ResourceList struct {
    CPU    int64  `json:"cpu"`
    Memory string `json:"memory"`
    GPU    int    `json:"nvidia.com/gpu,omitempty"`
}

// ClusterSummary provides aggregate statistics
type ClusterSummary struct {
    TotalGPUs     int     `json:"totalGPUs"`
    AllocatedGPUs int     `json:"allocatedGPUs"`
    AvailableGPUs int     `json:"availableGPUs"`
    Utilization   float64 `json:"utilization"`
}

// Workload represents a pending workload
type Workload struct {
    WorkloadID       string `json:"workloadId"`
    ModelID          string `json:"modelId"`
    Replicas         int    `json:"replicas"`
    GPUsPerReplica   int    `json:"gpusPerReplica"`
    CPUPerReplica    int    `json:"cpuPerReplica"`
    MemoryPerReplica string `json:"memoryPerReplica"`
    PriorityClass    string `json:"priorityClassName"`
}
```

Create `pkg/scheduler/types/types.go`:

```go
package types

// PlacementDecision represents scheduler's decision
type PlacementDecision struct {
    StateVersion  string     `json:"stateVersion"`
    PlacementMode string     `json:"placementMode"` // "ClusterOnly"
    Decisions     []Decision `json:"decisions"`
    Explain       []Explanation `json:"explain"`
}

// Decision represents placement for one workload
type Decision struct {
    WorkloadID string `json:"workloadId"`
    Cluster    string `json:"cluster"`
    Replicas   int    `json:"replicas"`
}

// Explanation provides decision rationale
type Explanation struct {
    WorkloadID   string              `json:"workloadId"`
    Reason       string              `json:"reason"`
    Considered   []Alternative       `json:"considered"`
    SolverTimeMs int                 `json:"solverTimeMs"`
}

// Alternative represents a cluster that was considered
type Alternative struct {
    Cluster string `json:"cluster"`
    Reason  string `json:"reason"`
}

// ApplyPlacementResult is simulator's response
type ApplyPlacementResult struct {
    Accepted        bool     `json:"accepted"`
    AppliedVersion  string   `json:"appliedVersion"`
    Conflicts       []string `json:"conflicts"`
    Warnings        []string `json:"warnings"`
    TransitionPlanID string  `json:"transitionPlanId"`
    Message         string   `json:"message"`
}
```

---

## Step 4: Minimal Working Simulator (30 minutes)

Create `cmd/simulator/main.go`:

```go
package main

import (
    "context"
    "encoding/json"
    "flag"
    "fmt"
    "log"
    "net/http"
    "os"
    "os/signal"
    "sync"
    "syscall"
    "time"

    "k8s.io/client-go/kubernetes"
    "k8s.io/client-go/tools/clientcmd"
)

var (
    port = flag.Int("port", 8080, "API server port")
)

type Simulator struct {
    mu           sync.RWMutex
    state        ClusterState
    stateVersion int
    clusters     map[string]*kubernetes.Clientset
}

type ClusterState struct {
    Version      string    `json:"version"`
    Timestamp    time.Time `json:"timestamp"`
    Clusters     []string  `json:"clusters"`
    PendingQueue []string  `json:"pendingQueue"`
}

func main() {
    flag.Parse()

    // Initialize simulator
    sim := &Simulator{
        clusters:     make(map[string]*kubernetes.Clientset),
        stateVersion: 1,
    }

    // Connect to Kind clusters
    contexts := []string{"kind-cluster-1", "kind-cluster-2", "kind-cluster-3"}
    for _, ctx := range contexts {
        clientset, err := getClientset(ctx)
        if err != nil {
            log.Fatalf("Failed to connect to %s: %v", ctx, err)
        }
        sim.clusters[ctx] = clientset
        log.Printf("✅ Connected to %s", ctx)
    }

    // Start state aggregation
    go sim.aggregateState()

    // Start API server
    http.HandleFunc("/v1/state", sim.handleGetState)
    http.HandleFunc("/v1/placement", sim.handlePostPlacement)

    addr := fmt.Sprintf(":%d", *port)
    log.Printf("🚀 Simulator API listening on %s", addr)

    server := &http.Server{Addr: addr}

    // Graceful shutdown
    sigCh := make(chan os.Signal, 1)
    signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)

    go func() {
        if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
            log.Fatalf("Server error: %v", err)
        }
    }()

    <-sigCh
    log.Println("Shutting down...")
    ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
    defer cancel()
    server.Shutdown(ctx)
}

func getClientset(context string) (*kubernetes.Clientset, error) {
    kubeconfig := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
        clientcmd.NewDefaultClientConfigLoadingRules(),
        &clientcmd.ConfigOverrides{CurrentContext: context},
    )

    config, err := kubeconfig.ClientConfig()
    if err != nil {
        return nil, err
    }

    return kubernetes.NewForConfig(config)
}

func (s *Simulator) aggregateState() {
    ticker := time.NewTicker(5 * time.Second)
    defer ticker.Stop()

    for range ticker.C {
        s.mu.Lock()
        s.state = ClusterState{
            Version:   fmt.Sprintf("%d", s.stateVersion),
            Timestamp: time.Now(),
            Clusters:  []string{"cluster-1", "cluster-2", "cluster-3"},
            PendingQueue: []string{},
        }
        s.stateVersion++
        s.mu.Unlock()

        log.Printf("📸 State updated to v%d", s.stateVersion-1)
    }
}

func (s *Simulator) handleGetState(w http.ResponseWriter, r *http.Request) {
    s.mu.RLock()
    state := s.state
    s.mu.RUnlock()

    w.Header().Set("Content-Type", "application/json")
    w.Header().Set("ETag", fmt.Sprintf("\"%s\"", state.Version))

    json.NewEncoder(w).Encode(state)
}

func (s *Simulator) handlePostPlacement(w http.ResponseWriter, r *http.Request) {
    var decision map[string]interface{}
    if err := json.NewDecoder(r.Body).Decode(&decision); err != nil {
        http.Error(w, err.Error(), http.StatusBadRequest)
        return
    }

    log.Printf("📥 Received decision: %+v", decision)

    result := map[string]interface{}{
        "accepted":        true,
        "appliedVersion":  fmt.Sprintf("%d", s.stateVersion),
        "message":         "Decision accepted (stub)",
    }

    w.Header().Set("Content-Type", "application/json")
    w.WriteHeader(http.StatusAccepted)
    json.NewEncoder(w).Encode(result)
}
```

Create `go.mod`:

```
module github.com/yourorg/scheduler-simulator

go 1.21

require (
    k8s.io/api v0.28.3
    k8s.io/apimachinery v0.28.3
    k8s.io/client-go v0.28.3
)
```

Install dependencies:

```bash
go mod tidy
```

Test the simulator:

```bash
# Terminal 1: Run simulator
go run cmd/simulator/main.go

# Terminal 2: Test API
curl http://localhost:8080/v1/state | jq .

# Should see:
# {
#   "version": "1",
#   "timestamp": "...",
#   "clusters": ["cluster-1", "cluster-2", "cluster-3"],
#   "pendingQueue": []
# }
```

---

## Step 5: Minimal Random Scheduler (20 minutes)

Create `cmd/scheduler/random/main.go`:

```go
package main

import (
    "bytes"
    "encoding/json"
    "flag"
    "fmt"
    "io"
    "log"
    "math/rand"
    "net/http"
    "time"
)

var (
    simulatorURL = flag.String("simulator-url", "http://localhost:8080", "Simulator API URL")
)

type ClusterState struct {
    Version      string   `json:"version"`
    Clusters     []string `json:"clusters"`
    PendingQueue []string `json:"pendingQueue"`
}

type PlacementDecision struct {
    StateVersion  string      `json:"stateVersion"`
    PlacementMode string      `json:"placementMode"`
    Decisions     []Decision  `json:"decisions"`
    Explain       []Explain   `json:"explain"`
}

type Decision struct {
    WorkloadID string `json:"workloadId"`
    Cluster    string `json:"cluster"`
    Replicas   int    `json:"replicas"`
}

type Explain struct {
    WorkloadID string `json:"workloadId"`
    Reason     string `json:"reason"`
}

func main() {
    flag.Parse()
    rand.Seed(time.Now().UnixNano())

    log.Printf("🎲 RandomCluster scheduler starting")
    log.Printf("   Simulator: %s", *simulatorURL)

    ticker := time.NewTicker(5 * time.Second)
    defer ticker.Stop()

    for range ticker.C {
        if err := makeDecision(); err != nil {
            log.Printf("❌ Error: %v", err)
        }
    }
}

func makeDecision() error {
    // 1. Fetch state
    state, err := fetchState()
    if err != nil {
        return fmt.Errorf("fetch state: %w", err)
    }

    log.Printf("📥 Fetched state v%s with %d pending workloads",
        state.Version, len(state.PendingQueue))

    // 2. Make random decisions
    if len(state.PendingQueue) == 0 {
        return nil // Nothing to do
    }

    decisions := []Decision{}
    explains := []Explain{}

    for _, workloadID := range state.PendingQueue {
        // Pick random cluster
        cluster := state.Clusters[rand.Intn(len(state.Clusters))]

        decisions = append(decisions, Decision{
            WorkloadID: workloadID,
            Cluster:    cluster,
            Replicas:   10, // Fixed for now
        })

        explains = append(explains, Explain{
            WorkloadID: workloadID,
            Reason:     "random_selection",
        })
    }

    // 3. Submit decision
    placement := PlacementDecision{
        StateVersion:  state.Version,
        PlacementMode: "ClusterOnly",
        Decisions:     decisions,
        Explain:       explains,
    }

    if err := submitDecision(placement); err != nil {
        return fmt.Errorf("submit decision: %w", err)
    }

    log.Printf("✅ Submitted %d decisions", len(decisions))
    return nil
}

func fetchState() (*ClusterState, error) {
    resp, err := http.Get(*simulatorURL + "/v1/state")
    if err != nil {
        return nil, err
    }
    defer resp.Body.Close()

    var state ClusterState
    if err := json.NewDecoder(resp.Body).Decode(&state); err != nil {
        return nil, err
    }

    return &state, nil
}

func submitDecision(decision PlacementDecision) error {
    body, err := json.Marshal(decision)
    if err != nil {
        return err
    }

    req, err := http.NewRequest("POST", *simulatorURL+"/v1/placement", bytes.NewReader(body))
    if err != nil {
        return err
    }

    req.Header.Set("Content-Type", "application/json")
    req.Header.Set("If-Match", fmt.Sprintf("\"%s\"", decision.StateVersion))

    resp, err := http.DefaultClient.Do(req)
    if err != nil {
        return err
    }
    defer resp.Body.Close()

    if resp.StatusCode != http.StatusAccepted {
        body, _ := io.ReadAll(resp.Body)
        return fmt.Errorf("unexpected status %d: %s", resp.StatusCode, body)
    }

    return nil
}
```

Test the scheduler:

```bash
# Terminal 1: Simulator should already be running
# Terminal 2: Run scheduler
go run cmd/scheduler/random/main.go

# You should see logs like:
# 🎲 RandomCluster scheduler starting
#    Simulator: http://localhost:8080
# 📥 Fetched state v123 with 0 pending workloads
# (repeating every 5 seconds)
```

---

## Step 6: Create Makefile (10 minutes)

Create `Makefile`:

```makefile
.PHONY: help setup build run-simulator run-scheduler demo teardown clean

help:
	@echo "Scheduler Simulator - Make targets:"
	@echo "  setup          - Setup Kind clusters and KWOK"
	@echo "  build          - Build binaries"
	@echo "  run-simulator  - Run simulator"
	@echo "  run-scheduler  - Run random scheduler"
	@echo "  demo           - Run complete demo"
	@echo "  teardown       - Delete Kind clusters"
	@echo "  clean          - Clean build artifacts"

setup:
	./scripts/setup-kind.sh
	./scripts/install-kwok.sh
	./scripts/create-kwok-nodes.sh

build:
	go build -o bin/simulator cmd/simulator/main.go
	go build -o bin/random-scheduler cmd/scheduler/random/main.go

run-simulator:
	go run cmd/simulator/main.go

run-scheduler:
	go run cmd/scheduler/random/main.go

demo: build
	@echo "Starting simulator in background..."
	./bin/simulator &
	@sleep 3
	@echo "Starting scheduler..."
	./bin/random-scheduler

teardown:
	kind delete cluster --name cluster-1 || true
	kind delete cluster --name cluster-2 || true
	kind delete cluster --name cluster-3 || true

clean:
	rm -rf bin/
	rm -rf output/
```

---

## What You Have Now

✅ **Working simulator** that:
- Connects to 3 Kind clusters
- Aggregates state every 5 seconds
- Serves state via HTTP API
- Accepts placement decisions

✅ **Working random scheduler** that:
- Polls simulator for state
- Makes random cluster selections
- Submits decisions via HTTP API

✅ **8 KWOK nodes** across 3 clusters with GPU capacity

✅ **Scripts** to setup/teardown everything

---

## Next Steps

From here, you can follow the implementation plan to:

1. **Day 2-3**: Implement real state aggregation (nodes, pods, capacity)
2. **Day 6**: Implement decision enforcement (actually create Deployments)
3. **Day 7**: Add scenario runner (workload driver)
4. **Day 8**: Add metrics collection
5. **Day 9-10**: Testing and polish

---

## Quick Verification

```bash
# Check clusters
kind get clusters

# Check nodes in each cluster
kubectl --context kind-cluster-1 get nodes
kubectl --context kind-cluster-2 get nodes
kubectl --context kind-cluster-3 get nodes

# Verify API works
curl http://localhost:8080/v1/state | jq .
```

---

## Troubleshooting

### "Connection refused" when starting simulator
- Make sure Docker is running
- Check clusters: `kind get clusters`
- Recreate clusters: `make teardown && make setup`

### KWOK nodes not showing
- Check KWOK controller: `kubectl --context kind-cluster-1 get pods -n kube-system | grep kwok`
- Recreate nodes: `./scripts/create-kwok-nodes.sh`

### Go dependencies issues
- Run: `go mod tidy`
- Update: `go get -u k8s.io/client-go@v0.28.3`

---

You now have a minimal working system! Follow the implementation plan to build it out to the full MVP. 🚀
