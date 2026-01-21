# Scheduler/Simulator Architecture Analysis

## Executive Summary

**YES, this abstraction works well.** The design cleanly separates concerns between:
- **What to measure** (Simulator - the scientific instrument)
- **How to decide** (Scheduler - the algorithm under test)

This enables apples-to-apples comparison of scheduling strategies on identical workloads.

---

## How the Components Work Together

### 1. The Simulator as a "Distributed Systems Wind Tunnel"

The Simulator acts like a wind tunnel for testing aircraft designs, but for scheduling algorithms:

- **Real K8s clusters**: Not mocks—actual Kind clusters with KWOK (fake nodes that behave realistically)
- **Controlled conditions**: Deterministic workloads, reproducible failures, time dilation
- **Instrumentation**: Captures everything—state snapshots, decision latency, utilization, fragmentation
- **Neutral enforcement**: Applies scheduler decisions faithfully, then measures outcomes

**Key insight**: By maintaining a consistent global state snapshot across 3-10 clusters, the Simulator provides what individual cluster schedulers never have—a god's-eye view.

### 2. The Scheduler as a Pluggable Strategy

Schedulers are **pure functions** (mostly):
```
f(GlobalClusterState, PendingQueue) → PlacementDecisions
```

Different implementations can be swapped:
- **RandomCluster**: Control group (how bad can it get?)
- **Greedy/BestFit**: Heuristic-based (fast, decent)
- **CP-SAT/ILP**: Optimization-based (slow, optimal)
- **Your Custom Logic**: The thing you're actually testing

All consume the same `ClusterState` schema and produce the same `PlacementDecision` schema.

### 3. Three-Tier Decision Enforcement

The magic is in how decisions are enforced based on **placement mode**:

#### Tier 1: ClusterOnly (Baseline)
```
Scheduler says: "Put 10 replicas of mistral-7b in cluster-2"
Simulator creates: Deployment with 10 replicas in cluster-2
Default kube-scheduler: Picks individual nodes using standard K8s logic
```

**Why this matters**: You can test "should we use cluster-level schedulers at all?" by comparing this against just letting each cluster's scheduler handle everything independently.

#### Tier 2: NodePlacement (Advanced)
```
Scheduler says: "Put replica-1 on cluster-2/node-7, replica-2 on cluster-2/node-8"
Simulator creates: Pods with strict nodeAffinity OR schedulerName: custom-scheduler
Custom plugin/controller: Enforces exact placements
```

**Why this matters**: For GPU workloads, node-level decisions matter (H100 vs A100, NVLink topology, memory bandwidth). This mode lets you test bin-packing algorithms.

#### Tier 3: Gang (Multi-pod Jobs)
```
Scheduler says: "Place this 8-GPU job across specific nodes"
Simulator creates: PodGroup with minAvailable=8
Gang controller: All-or-nothing admission
```

**Why this matters**: Distributed training jobs need all pods to start together. Partial admission → deadlock or wasted resources.

---

## Critical Design Decisions (Principal Engineer Perspective)

### ✅ What Works Well

1. **Optimistic Concurrency Control**
   - ETags on state versions prevent stale decisions
   - Bounded staleness (default 30s) allows slightly-stale-but-still-valid decisions
   - **Reasoning**: Global consistency is impossible; bounded inconsistency is pragmatic

2. **Explainability Built-in**
   - Every decision includes `explain[]` with alternatives considered
   - Decision logs are append-only (audit trail)
   - **Reasoning**: You can't improve what you can't debug

3. **Separation of Concerns**
   - Simulator never knows about scoring functions
   - Scheduler never talks to K8s directly
   - **Reasoning**: Testability. Swap schedulers without touching simulator code.

4. **Metrics at Every Layer**
   - Simulator measures: utilization, fragmentation, churn, recovery time
   - Scheduler reports: solver time, alternatives, scores
   - **Reasoning**: Enables multi-objective optimization analysis

5. **Support for Both Push & Pull**
   - Scheduler can POST decisions (push)
   - Simulator can POST to /decide (pull)
   - **Reasoning**: Flexibility for different deployment models

### 🔶 Potential Issues & Mitigations

1. **State Snapshot Consistency**
   - **Problem**: Aggregating state from 10 clusters takes time; snapshot may be inconsistent
   - **Mitigation**: Version numbers + staleness bounds + conflict detection
   - **Trade-off**: Slight staleness vs locking everything (which would prevent real work)

2. **Decision Application Lag**
   - **Problem**: Creating Deployments in K8s isn't instant; pods take time to schedule/start
   - **Mitigation**: 
     - Simulator tracks "in-flight transitions" separately from "running state"
     - Metrics measure time-to-ready (includes this lag)
     - Schedulers can factor in cold-start costs
   - **Implication**: Schedulers must account for real-world latencies

3. **Failure Injection Timing**
   - **Problem**: If failure happens between decision and application, system state diverges
   - **Mitigation**: 
     - Failure injector coordinates with state manager
     - Conflict detection catches this (409 Conflict response)
     - Scheduler retries with fresh state
   - **This is actually a feature**: Tests resilience to reality

4. **ClusterOnly Mode Limitations**
   - **Problem**: Default kube-scheduler might make suboptimal node choices
   - **Why it's okay**: That's what we're testing! If ClusterOnly+default performs poorly, that proves the value of custom node placement.

5. **Metric Attribution**
   - **Problem**: In ClusterOnly mode, poor outcomes could be scheduler's fault OR default kube-scheduler's fault
   - **Mitigation**: 
     - Simulator logs both cluster decision and eventual node placements
     - Can analyze "scheduler picked optimal cluster, but default scheduler fragmented it"
     - This is visible in packability vs utilization deltas

---

## Data Flow: A Complete Cycle

```
1. Scenario Start
   ├─ Harness loads scenario YAML (arrivals, scaling, failures)
   └─ Simulator bootstraps 3 clusters with KWOK nodes

2. State Aggregation (continuous)
   ├─ Simulator watches all cluster APIs
   ├─ Aggregates into ClusterState v1689
   └─ Exposes via GET /v1/state (ETag: "1689")

3. Workload Arrival
   ├─ WorkloadDriver adds "mistral-7b, 10 replicas, 1 GPU each" to pendingQueue
   └─ State bumps to v1690

4. Scheduler Decision
   ├─ GreedyScheduler calls GET /v1/state (gets v1690)
   ├─ Runs bin-packing algorithm (35ms)
   ├─ Chooses cluster-2 (fragmentation score: 0.77 vs 0.62 for cluster-1)
   └─ POSTs PlacementDecision (If-Match: "1690", cluster: "cluster-2")

5. Simulator Enforcement (ClusterOnly mode)
   ├─ Validates v1690 still current (or within staleness bound)
   ├─ Creates Deployment "mistral-7b" in cluster-2 with 10 replicas
   ├─ Returns 202 Accepted (appliedVersion: "1691")
   └─ Logs decision to DecisionLog

6. Default Scheduling (in cluster-2)
   ├─ cluster-2's kube-scheduler sees 10 pending pods
   ├─ Runs node filtering/scoring
   └─ Binds pods to specific nodes (node-5, node-7, node-8...)

7. Metrics Collection
   ├─ Simulator observes pods becoming Running
   ├─ Measures: GPU utilization +12%, fragmentation -7%, decision latency 35ms
   ├─ Exports to Prometheus
   └─ Updates state to v1692

8. Failure Injection (optional)
   ├─ At t=300s, Harness triggers "cordon node-7 in cluster-2"
   ├─ State updates to v1693 (node-7 becomes unschedulable)
   └─ Scheduler reacts in next cycle

9. Comparison
   ├─ Run same scenario with RandomCluster scheduler
   ├─ Compare: utilization, fragmentation, recovery time
   └─ Generate report: "Greedy reduced fragmentation by 23%, improved recovery by 40s"
```

---

## Why This Design Enables Rigorous Testing

### 1. Controlled Variables
- **Same workload arrivals**: Deterministic seeds
- **Same failure patterns**: Scripted or replayed from production traces
- **Same starting state**: Identical cluster configs

### 2. Isolated Variables
- **Only scheduler changes**: Swap GreedyScheduler for CP-SAT, re-run
- **Only placement mode changes**: Test ClusterOnly vs NodePlacement with same algorithm

### 3. Real Behavior
- **Actual K8s schedulers**: Not simulated; KWOK provides fake nodes but real API behavior
- **Actual latencies**: Network delays, API server queuing, pod startup times
- **Actual failure modes**: Conflicts, preemptions, OOMKills (if you use real nodes)

### 4. Comprehensive Observability
- **Before**: ClusterState snapshot (what did scheduler see?)
- **During**: Decision logs (what did it decide? why?)
- **After**: Metrics (what happened? how long? how well?)

---

## Answering Key Questions

### Q: Can you test "should we do global scheduling at all?"
**A**: Yes. 
- **Baseline**: No simulator, just 3 independent clusters with default schedulers
- **ClusterOnly**: Simulator + RandomCluster (dumb global)
- **Advanced**: Simulator + GreedyScheduler (smart global)

If Advanced doesn't beat Baseline significantly → maybe global scheduling isn't worth it.

### Q: How do you handle partial failures?
**A**: 
- Simulator applies decisions atomically per model
- Returns `conflicts[]` for items that failed (e.g., "couldn't place in cluster-2, capacity changed")
- Scheduler can retry just the failed items

### Q: What if scheduler is buggy and makes infeasible decisions?
**A**: 
- Simulator validates decisions before applying (422 Unprocessable Entity)
- K8s admission webhooks reject invalid pods
- Simulator doesn't deadlock—just logs error and continues
- **This is a test outcome**: "Scheduler X produced infeasible decisions in 5% of cases"

### Q: How do you test gang scheduling?
**A**: 
- Simulator creates PodGroup CRD with minAvailable
- Gang controller (Kueue) handles admission
- Simulator measures: "time until all 8 pods Running" or "job starved for 10m"

### Q: Can schedulers be stateful?
**A**: 
- Yes, but discouraged in ClusterState snapshots
- If needed, scheduler can maintain its own DB and use state_version to sync
- Prefer reactive (re-decide on every state change) over proactive (schedule in advance)

---

## Comparison to Alternatives

### vs. Pure Simulation (no real K8s)
- **Pro**: Faster, easier
- **Con**: Misses real scheduler behavior, resource contention, API latencies
- **Verdict**: Good for quick iteration, but not for production confidence

### vs. Production A/B Testing
- **Pro**: Real traffic, real outcomes
- **Con**: Risky (bad scheduler → real outages), slow (weeks to gather data), confounded (traffic changes)
- **Verdict**: Do this AFTER simulator validates the approach

### vs. Shadow Scheduling (log-only)
- **Pro**: Safe (no actual changes)
- **Con**: Can't measure real outcomes (utilization, failures)
- **Verdict**: Useful for debugging, but insufficient for validation

**This design**: Best of all worlds—real K8s behavior, controlled conditions, safe (isolated test clusters), fast (time dilation).

---

## Implementation Recommendations

### Phase 1: Baseline (2 weeks)
1. Simulator with ClusterOnly mode
2. RandomCluster scheduler
3. Single scenario (gradual arrivals, no failures)
4. Basic metrics (utilization, decision latency)

### Phase 2: Comparison (2 weeks)
1. GreedyScheduler (minimize fragmentation)
2. Run same scenario, compare metrics
3. Add failure injection (cordon 1 node)
4. Measure recovery time

### Phase 3: Advanced (4 weeks)
1. NodePlacement mode with custom plugin
2. CP-SAT/ILP scheduler
3. Gang mode with Kueue
4. Complex scenarios (bursty arrivals, cascading failures)

### Phase 4: Validation (2 weeks)
1. Replay production traces
2. Shadow schedule in prod, compare decisions
3. A/B test in staging
4. Rollout to prod with feature flags

---

## Conclusion

This is a **well-designed, production-ready architecture** for testing scheduling algorithms.

**Key strengths**:
- Clean separation of concerns
- Supports apples-to-apples comparisons
- Real K8s behavior in controlled environment
- Comprehensive observability

**Minor improvements**:
- Add retry/backoff strategies to API contract
- Specify eviction/preemption semantics
- Define SLOs (e.g., "decision latency p99 < 500ms")

**Go build it.** Start with Phase 1, iterate based on learnings.
