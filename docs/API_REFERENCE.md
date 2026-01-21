# Simulator vs Scheduler: Responsibilities & APIs

**Version:** 1.0  
**Date:** Oct 28, 2025

This document cleanly splits responsibilities between the **Simulator** (the neutral testbed) and **Schedulers** (pluggable strategies), and defines the APIs between them. It supports both the baseline case—global scheduler picks only a cluster and the default kube-scheduler picks the node—and more advanced schedulers that return explicit node placements.

---

## 1) Responsibilities

### A. Simulator (Scheduler-agnostic testbed)

**Core**

- **Env orchestration**: Create/teardown 3–10 Kind+KWOK clusters; bootstrap namespaces, PriorityClasses, CRDs.
- **State capture**: Maintain a consistent global snapshot across clusters (nodes, pods, capacities, labels, taints, health, events).
- **Workload driver**: Generate scenario workloads (arrivals, scaling schedules), admission queue, and inject failures (cordon, drain, delete, partitions).
- **Time control**: Real time and accelerated/discrete-event clocks; deterministic seeds.
- **Apply decisions**: Enforce scheduler outputs according to placement mode (see §3).
- **Observability**: Export time-series metrics and structured event logs; produce run reports and comparisons across schedulers.
- **Safety & correctness**: Handle optimistic concurrency, retries, and drift reconciliation; never deadlock clusters on bad decisions.

**Out of scope**

- Selecting algorithms, scoring nodes, or solving optimization problems (that's the scheduler's job).

### B. Scheduler (Pluggable decision maker)

**Core**

- **Decision logic**: From a state snapshot, produce a PlacementDecision (cluster-only or node-level).
- **Explainability**: Emit structured decision logs (why chosen, alternatives rejected, costs/scores).
- **Stability**: Prefer incremental changes; support hysteresis and move penalties.
- **Resilience**: Be idempotent; tolerate stale-but-bounded snapshots; retry on conflicts.

**Optional**

- HA/leader election if running as a long-lived service.
- Multiple strategies: RandomCluster, RoundRobin, Greedy/BestFit, CP-SAT/ILP, hybrid.

---

## 2) Placement Modes (capability levels)

| Mode | Who chooses cluster? | Who chooses node? | How it's enforced |
|------|---------------------|-------------------|-------------------|
| **ClusterOnly** (Baseline) | Scheduler | Default kube-scheduler | Simulator updates ModelDeployment/Deployment in that cluster; pods scheduled by default scheduler |
| **NodePlacement** | Scheduler | Scheduler | Simulator applies restrictive affinities or binds via scheduler-specific schedulerName or CRD/controller |
| **Gang** (Multi-node jobs) | Scheduler | Scheduler + in-cluster gang controller | Simulator creates PodGroup/Kueue objects; admission is all-or-nothing |

The simulator must support all three to compare the default behavior vs custom schedulers fairly.

---

## 3) API Contracts

APIs are HTTP+JSON (or gRPC with the same messages). The simulator hosts the API; the scheduler can run in-process or as an external service.

### 3.1 Data Models (shared)

```json
// ClusterState (snapshot)
{
  "version": "1689",                 // monotonically increasing, also used as ETag
  "sim_time": 123456.0,              // seconds since scenario start (accelerated clock ok)
  "clusters": [
    {
      "id": "cluster-1",
      "labels": {"region":"us-west-2"},
      "nodes": [
        {
          "name": "node-1",
          "labels": {"gpu-type":"H100","storage-type":"shared"},
          "capacity": {"cpu": 96, "memory": "512Gi", "nvidia.com/gpu": 8},
          "allocatable": {"cpu": 96, "memory": "512Gi", "nvidia.com/gpu": 8},
          "allocated": {"cpu": 40, "memory": "120Gi", "nvidia.com/gpu": 3},
          "taints": [],
          "conditions": {"Ready": "True"}
        }
      ],
      "pods": [
        {
          "name": "llama-70b-3",
          "namespace": "models",
          "modelId": "llama-70b",
          "phase": "Running",
          "nodeName": "node-2",
          "requests": {"nvidia.com/gpu": 4, "cpu": 16, "memory": "80Gi"}
        }
      ]
    }
  ],
  "pendingQueue": [
    {
      "workloadId": "mistral-7b",
      "replicas": 5,
      "gpusPerReplica": 1,
      "priorityClassName": "serverless",
      "topology": "single-node"
    }
  ],
  "metrics": {
    "gpuUtilization": {"cluster-1": 0.83},
    "fitCapacity": {"1gpu": 12, "2gpu": 7, "4gpu": 3, "8gpu": 1}  // packability hints
  }
}

// PlacementDecision (scheduler → simulator)
{
  "state_version": "1689",           // must match or be within staleness bound
  "placement_mode": "ClusterOnly",   // or "NodePlacement" or "Gang"
  "ttlSeconds": 60,                  // optional; simulator may reject after expiry
  "decisions": [
    {
      "modelId": "llama-70b",
      "replicas": 6,
      "gpusPerReplica": 4,
      "topology": "single-node",
      "cluster": "cluster-2",        // required for ClusterOnly
      "nodeHints": [                 // optional for NodePlacement; interpreted as allow-list
        {"cluster":"cluster-2","node":"node-7"},
        {"cluster":"cluster-2","node":"node-8"}
      ],
      "constraints": {
        "nodeSelector": {"gpu-type":"H100","storage-type":"shared"},
        "priorityClassName": "dedicated-high",
        "spread": {"topologyKey":"kubernetes.io/hostname","maxSkew":1}
      }
    }
  ],
  "explain": [
    {
      "modelId":"llama-70b",
      "reason":"Best H100 availability; lower frag score",
      "considered": [
        {"cluster":"cluster-1","score":0.62,"rejectedReason":"high_frag"},
        {"cluster":"cluster-2","score":0.77,"accepted":true}
      ],
      "solver": {"name":"greedy-bestfit","timeMs":35}
    }
  ]
}

// ApplyPlacementResult (simulator → scheduler)
{
  "accepted": true,
  "appliedVersion": "1690",
  "conflicts": [],                   // list of items that failed with reasons
  "warnings": ["stale_state_within_bound"],
  "executionPlanId": "tp-0042"       // transition plan tracking id
}

// DecisionLog (append-only, for auditability)
{
  "ts": "2025-10-28T23:15:10Z",
  "state_version": "1689",
  "placement_mode": "ClusterOnly",
  "modelId": "llama-70b",
  "decision": {"cluster":"cluster-2","replicas":6},
  "alternatives": [{"cluster":"cluster-1","score":0.62}],
  "metrics": {"solverTimeMs": 35, "utilDelta": 0.03, "fragDelta": -0.07}
}
```

### 3.2 Simulator API (HTTP)

**GET /v1/state** → ClusterState
- Headers: `ETag: <version>`
- Query: `maxStalenessSeconds` (optional; default 30)

**POST /v1/placement** (scheduler → simulator)
- Body: PlacementDecision
- Headers: `If-Match: <state_version>` (optimistic concurrency)
- Responses:
  - `202 Accepted` → ApplyPlacementResult (with appliedVersion)
  - `409 Conflict` (state too stale or resource conflict)
  - `422 Unprocessable Entity` (invalid or infeasible decision)

**GET /v1/events** → Server-Sent Events / gRPC stream
- Events: ClusterChange, FailureInjected, QueueUpdated, PlanStarted, PlanCompleted.

**POST /v1/failures** (test harness)
- Injects cordon/drain/delete/partition with timing.

**POST /v1/time/advance** (if accelerated)
- `{ "seconds": 300 }` → advances simulation clock.

**GET /v1/metrics**
- Prometheus/OpenMetrics; also JSON snapshot on request.

### 3.3 Scheduler API (optional, if simulator calls out)

If the simulator pulls decisions:

**POST /decide** (simulator → scheduler)
- Body: ClusterState
- Response: PlacementDecision

**GET /logs?since=\<ts>**
- Response: list of DecisionLog

Choose **push** (scheduler posts decisions) or **pull** (simulator requests decisions) per deployment. Keep message shapes identical.

---

## 4) Conflict Handling & Consistency

**Optimistic locking**: `If-Match: state_version`; simulator accepts if state_version equals current or within maxStalenessSeconds and resources still feasible.

**Conflict cases (409)**:
- Nodes/pods changed materially (capacity shrank, taints added).
- A competing decision or in-flight transition updates the same model.

**Retry contract**: Scheduler may re-fetch state and re-decide.

**Partial apply**: Simulator applies per-decision item atomically; returns `conflicts[]` for rejects and continues with the rest (unless `allOrNothing=true` is requested).

---

## 5) How the Simulator "Applies" Decisions

### ClusterOnly (baseline)
- Creates/updates ModelDeployment (or Deployment/StatefulSet) in target cluster with replicas & selectors.
- Leaves `schedulerName` unset → default kube-scheduler places pods.
- Records packability/utilization/fragmentation outcomes for comparison.

### NodePlacement
Uses either:
- `schedulerName: schedulator` + in-cluster scheduler plugin/controller, or
- Strict nodeAffinity allow-lists (enforced by admission webhook) to realize node choices.

### Gang (multi-node jobs)
- Creates Kueue/PodGroup with `minAvailable = sum` of required pods for the job.
- Admission is all-or-nothing; if denied, the item remains Pending with reason `InsufficientGangCapacity`.

---

## 6) Metrics & Scoring (reported by Simulator)

- **Utilization**: allocated / allocatable GPUs (global / per cluster / per GPU type).
- **Fragmentation (packability)**: free GPUs vs. how many N-GPU replicas are placeable now per size class (1/2/4/8).
  - `frag_score = (free - packable) / max(1, free)`.
- **Churn**: pod migrations / hour, replicas changed / cycle.
- **Decision latency**: state snapshot time → decision received time (and solver time from scheduler logs).
- **Recovery time**: time to restore desired replicas after failure.
- **Queue health**: pending depth, reasons distribution.

All metrics are available per placement mode to compare baseline (ClusterOnly) vs custom schedulers.

---

## 7) Baseline & Advanced Schedulers (reference implementations)

**RandomClusterScheduler (baseline)**:
- Picks a cluster uniformly (or weighted by free GPUs).
- Mode: ClusterOnly.
- Purpose: Control for comparing against sophisticated strategies.

**Greedy/BestFit**:
- Chooses cluster minimizing fragmentation and maximizing packability.
- Mode: ClusterOnly or NodePlacement.

**CP-SAT/ILP**:
- Two-level: decide replicas per cluster (L1) + feasibility bin-pack check (L2).
- Mode: ClusterOnly initially; NodePlacement if plugin present.

---

## 8) Scenario & Policy Inputs (owned by Simulator)

**Scenario YAML**: arrivals, scaling schedules, time dilation, failure injections.

**Policy knobs** (exposed to schedulers via state):
- Priority classes (dedicated vs serverless), preemption policy.
- Region/cost weights, storage constraints.
- Optional penalties: move cost, cold-start cost.

Schedulers should not mutate policy; they read it from state.

---

## 9) Error Codes (Simulator responses)

| Code | Meaning | Scheduler Action |
|------|---------|------------------|
| 202 | Accepted | Decision accepted (fully or partially) | Track appliedVersion |
| 409 | Conflict | Stale/conflicting state | Re-fetch state & re-decide |
| 412 | Precondition Failed | Missing/incorrect If-Match | Re-send with correct version |
| 422 | Unprocessable Entity | Infeasible/invalid decision | Fix logic or constraints |
| 429 | Too Many Requests | Rate limited | Backoff |

---

## 10) Minimal Example (Baseline run)

**1. Scheduler pulls state:**

```
GET /v1/state
ETag: "1689"
```

**2. Scheduler decides ClusterOnly random placement:**

```
POST /v1/placement
If-Match: "1689"

{
  "state_version":"1689",
  "placement_mode":"ClusterOnly",
  "decisions":[
    {"modelId":"mistral-7b","replicas":10,"gpusPerReplica":1,"cluster":"cluster-3"}
  ],
  "explain":[{"modelId":"mistral-7b","reason":"random-cluster"}]
}
```

**3. Simulator applies**, default kube-scheduler picks nodes, metrics flow; later you can swap in an advanced scheduler and compare results apples-to-apples.

---

## TL;DR

- The **Simulator** is a neutral, deterministic, metrics-rich harness that can work with any scheduler.
- The **Scheduler** is a pluggable strategy that consumes snapshots and returns decisions in one of three modes (ClusterOnly, NodePlacement, Gang).
- Clear APIs, consistency rules, and metrics make the baseline (default) vs custom comparisons meaningful and fair.
