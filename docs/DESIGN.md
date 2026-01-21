# Schedulator: Multi-Cluster GPU Scheduling Simulator
## Master Design Document

**Version:** 2.0  
**Date:** October 28, 2025  
**Status:** Implementation (Phase 1 Complete)

---

## 1. Executive Summary

**Schedulator** is a dedicated simulation environment designed to evaluate and compare multi-cluster LLM scheduling algorithms. It focuses on measuring **scheduling decisions, GPU utilization, and resource fragmentation** across heterogeneous Kubernetes clusters.

Instead of building a simulator from scratch or testing in production, Schedulator uses a **"Distributed Systems Wind Tunnel"** approach:
*   **Real Kubernetes APIs**: Uses **Kind** (Kubernetes in Docker) to provide authentic API behavior.
*   **Simulated Nodes**: Uses **KWOK** (Kubernetes WithOut Kubelet) to simulate hundreds of GPU nodes with zero resource overhead.
*   **Pluggable Schedulers**: A clean API separation allows different scheduling strategies (Random, Greedy, CP-SAT) to be swapped and compared apples-to-apples.

**Primary Goal**: To answer "Is scheduling Algorithm A better than Algorithm B?" with rigorous data on utilization, fragmentation, and decision latency.

---

## 2. Design Requirements

### Functional Requirements
*   **FR-1 Multi-Cluster**: Support 3-10 clusters running locally, simulating heterogeneous regions.
*   **FR-2 GPU Simulation**: Accurately track Extended Resources (`nvidia.com/gpu`) and simulate node topology (1x, 2x, 4x, 8x GPU nodes).
*   **FR-3 Workloads**: Simulate LLM inference workloads using K8s Deployments with specific resource requests and anti-affinity rules.
*   **FR-4 Pluggable Strategy**: Schedulers must be interchangeable without modifying the core simulator.
*   **FR-5 Failure Injection**: Support controlled failures (node cordon/drain, cluster partition) to test resilience.

### Non-Functional Requirements
*   **NFR-1 Determinism**: Re-running a scenario with the same seed must produce identical results.
*   **NFR-2 Performance**: Support 100+ concurrent simulated nodes and 500+ pods on a standard developer laptop.
*   **NFR-3 Observability**: Export detailed metrics for every decision, state change, and resource transition.

---

## 3. System Architecture

The architecture separates the **Testbed (Simulator)** from the **Strategy (Scheduler)**.

```mermaid
graph TB
    subgraph "Harness & Control"
        Scenario[Scenario Loader<br/>(YAML)]
        Driver[Workload Driver]
    end

    subgraph "Simulator (The Testbed)"
        StateManager[State Manager<br/>(Aggregates Multi-Cluster State)]
        Enforcer[Decision Enforcer<br/>(Applies Placements)]
        API[HTTP API Server]
        Metrics[Metrics Collector]
    end

    subgraph "Strategy"
        Scheduler[Pluggable Scheduler<br/>(Random, Greedy, CP-SAT)]
    end

    subgraph "Infrastructure (Kind + KWOK)"
        C1[Cluster 1<br/>(us-west)]
        C2[Cluster 2<br/>(us-east)]
        C3[Cluster 3<br/>(eu-west)]
    end

    Scenario --> Driver
    Driver --> API
    Scheduler <-->|GET State / POST Decision| API
    StateManager <-->|Watch| C1 & C2 & C3
    Enforcer -->|Create Deployment| C1 & C2 & C3
    Metrics <-->|Scrape| C1 & C2 & C3
```

### Components

1.  **Simulator**: The neutral arbiter.
    *   **State Manager**: Maintains a real-time, consistent snapshot of all clusters (nodes, pods, capacities).
    *   **Enforcer**: Applies scheduling decisions to the actual K8s clusters based on the specific **Placement Mode**.
    *   **API Server**: Exposes state and accepts decisions via a versioned HTTP API.

2.  **Scheduler**: The logic under test.
    *   Consumes `ClusterState` snapshots.
    *   Produces `PlacementDecisions`.
    *   Stateless (mostly) and interchangeable.

3.  **Infrastructure**:
    *   **Kind**: Runs the actual K8s control planes.
    *   **KWOK**: Runs as a controller to simulate nodes and update Pod statuses to "Running" instantly, mimicking resource usage without actual containers.

---

## 4. Placement Modes

To support progressive complexity, Schedulator supports three modes of operation:

| Mode | Description | Decision Granularity | Enforcement Mechanism |
| :--- | :--- | :--- | :--- |
| **ClusterOnly** (Phase 1) | Scheduler picks the *cluster*; local K8s scheduler picks the *node*. | Cluster-level | Simulator creates a Deployment in the target cluster. `schedulerName` is left default. |
| **NodePlacement** (Phase 2) | Scheduler picks specific *nodes* for each replica. | Node-level | Simulator sets `nodeSelector` or `nodeAffinity` to pin pods to specific nodes. |
| **Gang** (Phase 3) | Scheduler picks a set of nodes for a *group* of pods (all-or-nothing). | Job-level | Simulator creates `PodGroup` or uses Kueue to enforce gang admission. |

---

## 5. API Specification

The Simulator and Scheduler communicate via a REST-like JSON API.

### Data Models

**ClusterState** (Snapshot)
```json
{
  "version": "1024",
  "timestamp": "2025-10-28T12:00:00Z",
  "clusters": [
    {
      "id": "cluster-1",
      "nodes": [
        { "name": "node-1", "capacity": {"nvidia.com/gpu": 8}, "allocated": {...} }
      ],
      "pods": [...]
    }
  ],
  "pendingQueue": [
    { "workloadId": "job-123", "replicas": 1, "gpusPerReplica": 4 }
  ]
}
```

**PlacementDecision** (Action)
```json
{
  "stateVersion": "1024",
  "placementMode": "ClusterOnly",
  "decisions": [
    {
      "workloadId": "job-123",
      "cluster": "cluster-1",
      "replicas": 1
    }
  ],
  "explain": [{ "reason": "random-choice" }]
}
```

### Endpoints

*   `GET /v1/state`: Returns the current `ClusterState`. Supports ETags for optimistic concurrency.
*   `POST /v1/placement`: Accepts a `PlacementDecision`. Returns 202 (Accepted) or 409 (Conflict) if state is stale.
*   `POST /v1/workloads`: Injects new workloads (used by the Driver).
*   `GET /v1/metrics`: Returns current utilization and fragmentation metrics.

---

## 6. Metrics Specification

Schedulator's primary value is precise measurement.

### 1. GPU Utilization
Global and per-cluster fraction of allocated capacity.
`Utilization = Allocated GPUs / Total GPUs`

### 2. Fragmentation (Packability Score)
Measures "wasted" free GPUs that are too fragmented to be useful. Calculated per size class (1, 2, 4, 8 GPUs).

**Formula**:
```
Packable Count[N] = Sum(Node.Free / N) for all nodes
Packable GPUs[N]  = Packable Count[N] * N
Wasted GPUs[N]    = Total Free GPUs - Packable GPUs[N]
Frag Score[N]     = Wasted GPUs[N] / Total Free GPUs
```
*   **0.0**: Perfect packing (no fragmentation).
*   **1.0**: Maximal fragmentation (free GPUs exist but none can hold a replica of size N).

---

## 7. Implementation Plan

### Phase 1: Foundation (Completed)
*   **Goal**: Working end-to-end loop with `ClusterOnly` mode and `RandomScheduler`.
*   **Status**:
    *   ✅ Infrastructure (Kind + KWOK) scripts ready.
    *   ✅ State Manager & API Server implemented.
    *   ✅ Random Scheduler implemented.
    *   ✅ Basic utilization metrics.
    *   ✅ Scenario loader & Workload driver.

### Phase 2: Intelligence (Current Focus)
*   **Goal**: Implement smarter schedulers and advanced placement.
*   **Tasks**:
    *   Implement `GreedyScheduler` (BestFit bin packing).
    *   Implement `NodePlacement` mode enforcement.
    *   Detailed fragmentation metrics reporting.
    *   Comparative analysis framework.

### Phase 3: Realism (Future)
*   **Goal**: Advanced scenarios and failure modes.
*   **Tasks**:
    *   Failure injection (node cordoning, network partitions).
    *   Gang scheduling support.
    *   `CP-SAT` (Constraint Programming) solver integration.
    *   Time-accelerated simulation mode.
