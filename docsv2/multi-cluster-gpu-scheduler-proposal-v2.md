# Engineering Proposal: Multi-Cluster GPU Scheduler for LLM Inference

## 1. Executive Summary

This document proposes an architecture for a global GPU scheduler that manages LLM inference workloads across a fleet of Kubernetes clusters. The scheduler determines replica counts based on traffic and SLA requirements, computes placements across clusters, and coordinates execution.

---

## 2. Architectural Decision: Global vs. Tiered Scheduling

Before presenting the architecture, we must resolve a fundamental design question: **who decides node-level placement?**

### 2.1 The Options

| Aspect | Option A: Fully Global | Option B: Tiered |
|--------|------------------------|------------------|
| **Cluster selection** | Global scheduler | Global scheduler |
| **Node selection** | Global scheduler | In-cluster scheduler (Volcano, kube-scheduler) |
| **Execution** | Actuators create pods with `nodeName` | Actuators create pods with constraints; local scheduler binds |
| **Visibility** | Full (via Cluster Aggregator) | Full (both tiers see node-level state) |

### 2.2 Analysis

Both options have full node-level visibility via the Cluster Aggregator. The question is **where the node-binding decision is made**, not what information is available.

**Arguments for Tiered (Option B):**

1. **Leverage existing schedulers.** Volcano and kube-scheduler have mature GPU topology awareness, gang scheduling, and bin-packing plugins. Reimplementing this is costly.

2. **Fresher local state.** There's inherent latency between when the global scheduler reads state and when a pod is created. A local scheduler makes the final binding decision with more current information, reducing race conditions.

3. **Native k8s integration.** PodDisruptionBudgets, taints, tolerations, and node affinity rules are handled automatically by kube-scheduler. A global scheduler setting `nodeName` bypasses these.

4. **Operational isolation.** Bugs or misconfigurations in the global scheduler don't directly corrupt node bindings. The local scheduler provides a safety layer.

5. **Incremental adoption.** Can deploy global scheduler for cluster selection first, then optimize local scheduling separately.

**Arguments for Fully Global (Option A):**

1. **Preemption coordination.** To preempt a P2 replica and immediately place a P0 replica on the freed node, the decision-maker must control both operations atomically. Tiered scheduling introduces coordination complexity.

2. **Simpler mental model.** One scheduler makes all decisions. Debugging "why did this pod land here" has one answer.

3. **Guaranteed placement.** If the global scheduler picks node X, the pod lands on node X (or fails fast). With tiered, the local scheduler might pick a different node, potentially fragmenting.

**Resolution: Tiered with Global Constraints**

The constraints in this problem (preemption, cache affinity, failure domains) are globally coupled, but node binding can be delegated *if* the global scheduler provides sufficient constraints.

We choose **Option B (Tiered)** with the following design:

- Global scheduler computes: **cluster, target node (as preference), required GPUs, scheduling constraints**
- In-cluster scheduler resolves: **final node binding** respecting the constraints
- For **preemption**: Global scheduler explicitly deletes victim pods; once terminated, the freed capacity is available for the local scheduler to use

The global scheduler provides constraints to the in-cluster scheduler:
```yaml
# Example: Global scheduler output for a scale-up
nodeSelector:
  topology.kubernetes.io/zone: us-west-1a
nodeAffinity:
  preferredDuringSchedulingIgnoredDuringExecution:
    - weight: 100
      preference:
        matchExpressions:
          - key: node.kubernetes.io/instance-type
            operator: In
            values: ["gpu-8x-h100"]
    - weight: 50
      preference:
        matchExpressions:
          - key: cache.mycompany.io/model-xyz
            operator: Exists
resources:
  limits:
    nvidia.com/gpu: 8
```

**Why this works for preemption:**

1. Global scheduler identifies victim replica (P2 app, on node X in cluster A)
2. Global scheduler deletes victim pod (with grace period)
3. Global scheduler waits for termination confirmation
4. Global scheduler submits new P0 pod to cluster A with `preferredNode: X` affinity
5. In-cluster scheduler binds to node X (or nearby node if X became unavailable)

The preemption decision is global; the final binding is local.

---

## 3. Architecture Overview

```
┌─────────────────────────────────────────────────────────────────────────────────────────┐
│                                    EXTERNAL DEPENDENCIES                                 │
├─────────────────────┬─────────────────────┬─────────────────────┬───────────────────────┤
│   Traffic Gateway   │  Cluster Aggregator │   Cache Registry    │ Performance Profiler  │
│                     │ (pods/nodes/svc/job │  (model locations)  │  (offline metrics)    │
│                     │  across all clusters)                     │                       │
└─────────┬───────────┴──────────┬──────────┴──────────┬──────────┴───────────┬───────────┘
          │                      │                     │                      │
          │ traffic signals      │ cluster state       │ cache state          │ app profiles
          │ (push)               │ (watch stream)      │ (poll)               │ (poll)
          ▼                      ▼                     ▼                      ▼
┌─────────────────────────────────────────────────────────────────────────────────────────┐
│                                                                                         │
│                              GLOBAL SCHEDULER                                           │
│                                                                                         │
│  ┌───────────────────────────────────────────────────────────────────────────────────┐  │
│  │                            Event Ingestion Layer                                  │  │
│  │  ┌─────────────┐ ┌─────────────┐ ┌─────────────┐ ┌─────────────┐                 │  │
│  │  │Traffic Event│ │Cluster Event│ │ SLA Breach  │ │   Timer     │                 │  │
│  │  │  Listener   │ │  Listener   │ │  Listener   │ │  (periodic) │                 │  │
│  │  └──────┬──────┘ └──────┬──────┘ └──────┬──────┘ └──────┬──────┘                 │  │
│  └─────────┼───────────────┼───────────────┼───────────────┼────────────────────────┘  │
│            └───────────────┴───────┬───────┴───────────────┘                            │
│                                    ▼                                                    │
│  ┌───────────────────────────────────────────────────────────────────────────────────┐  │
│  │                           Scheduler Core                                          │  │
│  │                                                                                   │  │
│  │   ┌──────────────────────────────────────────────────────────────────────────┐   │  │
│  │   │                    World State (in-memory snapshot)                      │   │  │
│  │   │  ┌────────────┐ ┌────────────┐ ┌────────────┐ ┌────────────┐            │   │  │
│  │   │  │  Cluster   │ │Application │ │   Cache    │ │Performance │            │   │  │
│  │   │  │  Topology  │ │  Configs   │ │   State    │ │  Profiles  │            │   │  │
│  │   │  │            │ │ & Replicas │ │            │ │            │            │   │  │
│  │   │  └────────────┘ └────────────┘ └────────────┘ └────────────┘            │   │  │
│  │   └──────────────────────────────────────────────────────────────────────────┘   │  │
│  │                          ▲                                                       │  │
│  │                          │ (continuously updated via State Sync)                 │  │
│  │                          │                                                       │  │
│  │   ┌─────────────────┐    │    ┌─────────────────┐    ┌─────────────────┐        │  │
│  │   │  Scaling        │────┴───▶│  Placement      │───▶│     Plan        │        │  │
│  │   │  Engine         │         │  Engine         │    │   Generator     │        │  │
│  │   │                 │         │                 │    │                 │        │  │
│  │   │ "How many       │         │ "Which cluster, │    │ "What ops to    │        │  │
│  │   │  replicas?"     │         │  what affinity?"│    │  execute?"      │        │  │
│  │   └─────────────────┘         └─────────────────┘    └────────┬────────┘        │  │
│  │                                                               │                  │  │
│  └───────────────────────────────────────────────────────────────┼──────────────────┘  │
│                                                                  │                     │
│                                                                  ▼                     │
│  ┌───────────────────────────────────────────────────────────────────────────────────┐  │
│  │                           Plan Executor                                           │  │
│  │                                                                                   │  │
│  │   ┌─────────────────────────────────────────────────────────────────────────┐    │  │
│  │   │                      Execution Coordinator                              │    │  │
│  │   │  • Orders operations (preempt → scale-down → scale-up)                  │    │  │
│  │   │  • Tracks in-flight operations                                          │    │  │
│  │   │  • Handles rollback on failure                                          │    │  │
│  │   └─────────────────────────────────────────────────────────────────────────┘    │  │
│  │                              │                                                    │  │
│  │         ┌────────────────────┼────────────────────┐                              │  │
│  │         ▼                    ▼                    ▼                              │  │
│  │  ┌─────────────┐      ┌─────────────┐      ┌─────────────┐                       │  │
│  │  │  Cluster    │      │  Cluster    │      │  Cluster    │                       │  │
│  │  │  Client     │      │  Client     │      │  Client     │                       │  │
│  │  │ us-west-1   │      │ eu-central-1│      │ ap-northeast│                       │  │
│  │  └──────┬──────┘      └──────┬──────┘      └──────┬──────┘                       │  │
│  │         │                    │                    │                              │  │
│  └─────────┼────────────────────┼────────────────────┼──────────────────────────────┘  │
│            │                    │                    │                                  │
└────────────┼────────────────────┼────────────────────┼──────────────────────────────────┘
             │ k8s API            │ k8s API            │ k8s API
             ▼                    ▼                    ▼
      ┌─────────────┐      ┌─────────────┐      ┌─────────────┐
      │ Kubernetes  │      │ Kubernetes  │      │ Kubernetes  │
      │ us-west-1   │      │ eu-central-1│      │ ap-northeast│
      │             │      │             │      │             │
      │ ┌─────────┐ │      │ ┌─────────┐ │      │ ┌─────────┐ │
      │ │In-cluster│ │      │ │In-cluster│ │      │ │In-cluster│ │
      │ │Scheduler │ │      │ │Scheduler │ │      │ │Scheduler │ │
      │ │(Volcano) │ │      │ │(Volcano) │ │      │ │(Volcano) │ │
      │ └─────────┘ │      │ └─────────┘ │      │ └─────────┘ │
      │             │      │             │      │             │
      │ ┌─────────┐ │      │ ┌─────────┐ │      │ ┌─────────┐ │
      │ │Node 8GPU│ │      │ │Node 8GPU│ │      │ │Node 8GPU│ │
      │ ├─────────┤ │      │ ├─────────┤ │      │ ├─────────┤ │
      │ │Node 8GPU│ │      │ │Node 8GPU│ │      │ │Node 8GPU│ │
      │ ├─────────┤ │      │ ├─────────┤ │      │ ├─────────┤ │
      │ │   ...   │ │      │ │   ...   │ │      │ │   ...   │ │
      │ └─────────┘ │      │ └─────────┘ │      │ └─────────┘ │
      └─────────────┘      └─────────────┘      └─────────────┘
```

---

## 4. Component Descriptions

### 4.1 Event Ingestion Layer

Normalizes events from multiple sources into a unified stream for the Scheduler Core.

| Listener | Source | Events | Notes |
|----------|--------|--------|-------|
| **Traffic Event** | Traffic Gateway | `TrafficUpdate(app_id, qps, latency_p99, ...)` | Push via webhook/queue |
| **Cluster Event** | Cluster Aggregator | `NodeDown`, `NodeUp`, `PodTerminated`, `PodRunning` | Watch stream |
| **SLA Breach** | Monitoring | `SLABreach(app_id, metric, threshold, actual)` | Alert webhook |
| **Timer** | Internal | `PeriodicEval` | Every 60s |

**Debouncing:** Events are coalesced over a short window (1-2s) to prevent thrashing. Multiple `TrafficUpdate` events for the same app within the window are merged.

---

### 4.2 World State

In-memory representation of global state, continuously updated via background sync from external dependencies.

```
WorldState:
  clusters: Map<ClusterId, ClusterState>
  applications: Map<AppId, ApplicationConfig>
  replicas: Map<ReplicaId, ReplicaState>
  cache_locations: Map<ModelId, Set<(ClusterId, NodeId)>>
  performance_profiles: Map<AppId, PerformanceProfile>
  traffic: Map<AppId, TrafficMetrics>

ClusterState:
  cluster_id: string
  nodes: Map<NodeId, NodeState>
  fragmentation_score: float  # precomputed

NodeState:
  node_id: string
  total_gpus: 8  # fixed
  allocated_gpus: int
  free_gpus: int
  largest_contiguous_block: int  # e.g., 4 if GPUs 0-3 are free
  pods: List<PodId>
  cached_models: Set<ModelId>
  status: "ready" | "cordoned" | "draining" | "down"

ApplicationConfig:
  app_id: string
  model_id: string
  gpus_per_replica: 1 | 2 | 4 | 8
  priority: int  # lower = higher priority (P0 > P1 > P2)
  min_replicas: int
  failure_domain_rule: "spread_clusters" | "none"
  sla:
    max_p99_ttft_ms: int
    max_p99_tps: int

ReplicaState:
  replica_id: string
  app_id: string
  cluster_id: string
  node_id: string
  gpus: int
  status: "pending" | "running" | "draining" | "terminated"

PerformanceProfile:
  app_id: string
  tps_per_replica: float
  ttft_p99_ms: float
  cold_start_seconds: float
  warm_start_seconds: float
```

**Sync Strategy:**

| Data | Source | Method | Frequency |
|------|--------|--------|-----------|
| Clusters, Nodes, Pods | Cluster Aggregator | Watch stream + full reconcile | Continuous + every 5m |
| Cache locations | Cache Registry | Poll | Every 30s |
| Performance profiles | Performance Profiler | Poll | Every 5m |
| Application configs | Config store (API/GitOps) | Watch | Continuous |
| Traffic metrics | Event Ingestion | Push | Real-time |

---

### 4.3 Scaling Engine

Computes target replica count per application.

**Input:** `(ApplicationConfig, TrafficMetrics, PerformanceProfile, current_replica_count)`

**Output:** `target_replica_count: int`

```
function compute_target_replicas(app, traffic, profile, current_count):
    # Replicas needed to meet throughput demand
    required_for_tps = ceil(traffic.total_tps_demand / profile.tps_per_replica)
    
    # Replicas needed to keep latency under SLA
    # (Simplified: assumes linear scaling. Real impl may use queueing models.)
    required_for_latency = ceil(traffic.qps / profile.max_qps_per_replica_at_sla)
    
    target = max(required_for_tps, required_for_latency, app.min_replicas)
    
    # Dampening: don't scale down too aggressively
    if target < current_count:
        target = max(target, current_count - MAX_SCALE_DOWN_PER_CYCLE)
    
    return target
```

---

### 4.4 Placement Engine

Computes which clusters should host each application's replicas and generates scheduling constraints.

**Input:** `(WorldState, Map<AppId, target_replica_count>)`

**Output:** `PlacementDecisions`

```
PlacementDecisions:
  scale_ups: List<ScaleUpDecision>
  scale_downs: List<ScaleDownDecision>
  preemptions: List<PreemptionDecision>

ScaleUpDecision:
  app_id: string
  cluster_id: string
  scheduling_constraints: SchedulingConstraints
  
SchedulingConstraints:
  required_gpus: int
  preferred_nodes: List<NodeId>  # ordered by preference
  required_node_labels: Map<string, string>
  preferred_node_labels: Map<string, string>
  cache_affinity_model: ModelId | null

ScaleDownDecision:
  app_id: string
  replica_id: string

PreemptionDecision:
  victim_replica_id: string
  grace_period_seconds: int
  reason: string  # for observability
```

#### Algorithm

```
function compute_placement(world_state, scaling_decisions):
    plan = PlacementDecisions()
    
    # 1. Compute deltas
    for app_id, target in scaling_decisions:
        current = count(world_state.replicas where app_id)
        delta[app_id] = target - current
    
    # 2. Process scale-downs first (free resources)
    for app_id where delta[app_id] < 0:
        count_to_remove = -delta[app_id]
        victims = select_scale_down_victims(app_id, count_to_remove, world_state)
        for victim in victims:
            plan.scale_downs.append(ScaleDownDecision(app_id, victim.replica_id))
    
    # 3. Process scale-ups
    for app_id where delta[app_id] > 0, sorted by priority ASC (P0 first):
        app = world_state.applications[app_id]
        count_to_add = delta[app_id]
        
        for i in 1..count_to_add:
            cluster, constraints = find_best_cluster(app, world_state)
            
            if cluster is None:
                # No cluster can fit this replica; try preemption
                preemption_plan = find_preemption_opportunity(app, world_state)
                if preemption_plan is None:
                    log.warn("Cannot place replica for {app_id}, no capacity")
                    continue
                plan.preemptions.extend(preemption_plan.victims)
                cluster, constraints = preemption_plan.target_cluster, preemption_plan.constraints
            
            plan.scale_ups.append(ScaleUpDecision(app_id, cluster, constraints))
            # Update working state to reflect planned placement
            world_state.mark_gpus_pending(cluster, constraints.required_gpus)
    
    return plan
```

#### Cluster Selection Scoring

```
function find_best_cluster(app, world_state):
    candidates = []
    
    for cluster in world_state.clusters:
        # Hard constraints
        if not has_node_with_contiguous_gpus(cluster, app.gpus_per_replica):
            continue
        if app.failure_domain_rule == "spread_clusters":
            if exceeds_spread_limit(app, cluster, world_state):
                continue
        
        # Soft scoring
        score = 0
        
        # Cache affinity: prefer clusters where model is cached
        if model_cached_in_cluster(app.model_id, cluster, world_state):
            score += WEIGHT_CACHE * 1.0
            preferred_nodes = get_cached_nodes(app.model_id, cluster)
        else:
            preferred_nodes = []
        
        # Fragmentation: prefer clusters with better packing opportunity
        frag_score = compute_packing_score(cluster, app.gpus_per_replica)
        score += WEIGHT_FRAGMENTATION * frag_score
        
        # Balance: prefer less-loaded clusters
        utilization = cluster.allocated_gpus / cluster.total_gpus
        score += WEIGHT_BALANCE * (1 - utilization)
        
        candidates.append((cluster, score, preferred_nodes))
    
    if not candidates:
        return None, None
    
    best = max(candidates, key=lambda x: x[1])
    cluster, _, preferred_nodes = best
    
    constraints = SchedulingConstraints(
        required_gpus=app.gpus_per_replica,
        preferred_nodes=preferred_nodes,
        cache_affinity_model=app.model_id
    )
    
    return cluster, constraints
```

#### Preemption Logic

```
function find_preemption_opportunity(requesting_app, world_state):
    # Find replicas of lower-priority apps that could be preempted
    candidates = []
    
    for replica in world_state.replicas:
        victim_app = world_state.applications[replica.app_id]
        
        # Can only preempt lower priority
        if victim_app.priority <= requesting_app.priority:
            continue
        
        # Don't reduce below minimum (unless desperate)
        current_count = count_replicas(replica.app_id, world_state)
        if current_count <= victim_app.min_replicas:
            continue
        
        candidates.append(replica)
    
    # Sort by: priority (lowest first), then by packing benefit
    candidates.sort(key=lambda r: (
        world_state.applications[r.app_id].priority,  # preempt P2 before P1
        -packing_benefit(r, requesting_app.gpus_per_replica)  # prefer victims that free useful blocks
    ))
    
    # Find enough victims to free required GPUs
    victims = []
    freed = 0
    target_cluster = None
    
    for candidate in candidates:
        # Prefer to preempt from same cluster to avoid cross-cluster coordination
        if target_cluster and candidate.cluster_id != target_cluster:
            continue
        
        victims.append(PreemptionDecision(
            victim_replica_id=candidate.replica_id,
            grace_period_seconds=10,
            reason=f"Preempted for {requesting_app.app_id} (P{requesting_app.priority})"
        ))
        freed += candidate.gpus
        target_cluster = candidate.cluster_id
        
        if freed >= requesting_app.gpus_per_replica:
            break
    
    if freed < requesting_app.gpus_per_replica:
        return None  # Cannot free enough even with preemption
    
    return PreemptionPlan(
        victims=victims,
        target_cluster=target_cluster,
        constraints=SchedulingConstraints(
            required_gpus=requesting_app.gpus_per_replica,
            preferred_nodes=[v.node_id for v in victims]  # Prefer the just-freed nodes
        )
    )
```

---

### 4.5 Plan Generator

Transforms `PlacementDecisions` into an ordered execution plan.

```
ExecutionPlan:
  operations: List<Operation>  # ordered

Operation:
  type: "preempt" | "scale_down" | "scale_up"
  payload: PreemptionDecision | ScaleDownDecision | ScaleUpDecision
  depends_on: List<OperationId>  # for sequencing
```

**Ordering rules:**
1. Preemptions first (must free capacity before scale-ups can use it)
2. Scale-downs second (also frees capacity)
3. Scale-ups last

**Dependency tracking:**
- Scale-ups that target preempted capacity depend on those preemptions completing
- Otherwise, operations are independent and can execute in parallel per cluster

---

### 4.6 Plan Executor

Executes the plan by dispatching operations to the appropriate cluster.

#### Execution Coordinator

```
function execute_plan(plan):
    in_flight = {}
    completed = set()
    failed = []
    
    while not all_done(plan, completed, failed):
        for op in plan.operations:
            if op.id in completed or op.id in in_flight:
                continue
            if not dependencies_met(op, completed):
                continue
            
            cluster_client = get_client(op.cluster_id)
            future = dispatch_async(cluster_client, op)
            in_flight[op.id] = future
        
        # Wait for any operation to complete
        done_op_id, result = wait_any(in_flight)
        del in_flight[done_op_id]
        
        if result.success:
            completed.add(done_op_id)
        else:
            failed.append((done_op_id, result.error))
            # Abort dependent operations
            abort_dependents(done_op_id, plan)
    
    return ExecutionResult(completed=completed, failed=failed)
```

#### Cluster Client Operations

**Scale-up:**
```
function scale_up(cluster_client, decision):
    pod_spec = build_pod_spec(
        app_id=decision.app_id,
        resources={"nvidia.com/gpu": decision.scheduling_constraints.required_gpus},
        node_affinity=build_affinity(decision.scheduling_constraints),
        # ... other fields from app config
    )
    
    # Submit to cluster; in-cluster scheduler (Volcano) does final binding
    cluster_client.create_pod(pod_spec)
    
    # Wait for pod to be scheduled and running
    wait_for_condition(
        cluster_client,
        pod_spec.name,
        condition="Running",
        timeout=app_config.startup_timeout
    )
```

**Preemption:**
```
function preempt(cluster_client, decision):
    # Step 1: Mark pod as draining (optional: remove from service endpoints)
    cluster_client.label_pod(decision.victim_replica_id, {"draining": "true"})
    
    # Step 2: Delete with grace period
    cluster_client.delete_pod(
        decision.victim_replica_id,
        grace_period_seconds=decision.grace_period_seconds
    )
    
    # Step 3: Wait for termination
    wait_for_deletion(cluster_client, decision.victim_replica_id)
```

**Scale-down:**
```
function scale_down(cluster_client, decision):
    # Same as preemption but without the urgency
    cluster_client.delete_pod(
        decision.replica_id,
        grace_period_seconds=DEFAULT_GRACE_PERIOD
    )
    wait_for_deletion(cluster_client, decision.replica_id)
```

---

### 4.7 In-Cluster Scheduler (Volcano)

Each cluster runs an in-cluster scheduler (Volcano or kube-scheduler with GPU plugins) that:

1. **Receives pods** from the Global Scheduler (via Cluster Client)
2. **Respects constraints** specified in pod spec (nodeAffinity, resources)
3. **Binds to node** using its local bin-packing logic
4. **Reports status** back via standard k8s pod status (watched by Cluster Aggregator)

**Configuration:**
- Volcano configured with `binpack` scheduling plugin for GPU-aware packing
- Node labels for cache state (`cache.mycompany.io/model-xyz: "true"`) updated by Cache Registry agent
- GPU topology awareness enabled (nvidia device plugin)

**Why Volcano over kube-scheduler:**
- Native gang scheduling (if we later need multi-pod workloads)
- Better bin-packing plugins for GPU workloads
- Queue management (useful for batch, though not v1 scope)

---

## 5. Sequence Diagrams

### 5.1 Normal Scale-Up Flow

```
┌─────────┐┌────────────┐┌─────────────┐┌───────────┐┌───────────┐┌────────────┐┌─────────┐┌──────────┐
│ Traffic ││  Event     ││ Scheduler   ││  Scaling  ││ Placement ││   Plan     ││ Cluster ││ Volcano  │
│ Gateway ││ Ingestion  ││   Core      ││  Engine   ││  Engine   ││ Executor   ││ Client  ││(in-cluster)│
└────┬────┘└─────┬──────┘└──────┬──────┘└─────┬─────┘└─────┬─────┘└─────┬──────┘└────┬────┘└────┬─────┘
     │           │              │             │            │            │           │          │
     │TrafficUpdate             │             │            │            │           │          │
     │(app=X,qps=500)           │             │            │           │           │          │
     │──────────▶│              │             │            │            │           │          │
     │           │              │             │            │            │           │          │
     │           │ TriggerEval  │             │            │            │           │          │
     │           │─────────────▶│             │            │            │           │          │
     │           │              │             │            │            │           │          │
     │           │              │ snapshot    │            │            │           │          │
     │           │              │ WorldState  │            │            │           │          │
     │           │              │─────┐       │            │            │           │          │
     │           │              │     │       │            │            │           │          │
     │           │              │◀────┘       │            │            │           │          │
     │           │              │             │            │            │           │          │
     │           │              │ compute_target(app=X)    │            │           │          │
     │           │              │────────────▶│            │            │           │          │
     │           │              │             │            │            │           │          │
     │           │              │◀────────────│            │            │           │          │
     │           │              │  target=5 (was 3)        │            │           │          │
     │           │              │             │            │            │           │          │
     │           │              │ compute_placement(targets)            │           │          │
     │           │              │─────────────────────────▶│            │           │          │
     │           │              │             │            │            │           │          │
     │           │              │             │            │ ScaleUpDecisions:      │          │
     │           │              │             │            │ - app=X, cluster=us-west-1        │
     │           │              │             │            │   preferred_nodes=[node-3,node-7] │
     │           │              │             │            │ - app=X, cluster=eu-central-1     │
     │           │              │             │            │   preferred_nodes=[node-2]        │
     │           │              │◀─────────────────────────│            │           │          │
     │           │              │             │            │            │           │          │
     │           │              │ execute(plan)            │            │           │          │
     │           │              │─────────────────────────────────────▶│           │          │
     │           │              │             │            │            │           │          │
     │           │              │             │            │            │ create_pod│          │
     │           │              │             │            │            │ (X, affinity)        │
     │           │              │             │            │            │──────────▶│          │
     │           │              │             │            │            │           │          │
     │           │              │             │            │            │           │ bind_pod │
     │           │              │             │            │            │           │ (node-3) │
     │           │              │             │            │            │           │─────────▶│
     │           │              │             │            │            │           │          │
     │           │              │             │            │            │           │◀─────────│
     │           │              │             │            │            │           │  bound   │
     │           │              │             │            │            │           │          │
     │           │              │             │            │            │◀──────────│          │
     │           │              │             │            │            │  pod running         │
     │           │              │             │            │            │           │          │
     │           │              │◀─────────────────────────────────────│           │          │
     │           │              │  ExecutionResult: success            │           │          │
```

### 5.2 Preemption Flow

```
┌──────────┐┌───────────┐┌───────────┐┌───────────┐┌───────────┐┌─────────┐┌──────────┐
│  Event   ││ Scheduler ││ Placement ││   Plan    ││  Cluster  ││ Volcano ││   K8s    │
│ Trigger  ││   Core    ││  Engine   ││ Executor  ││  Client   ││         ││  Node    │
└────┬─────┘└─────┬─────┘└─────┬─────┘└─────┬─────┘└─────┬─────┘└────┬────┘└────┬─────┘
     │            │            │            │            │           │          │
     │ NewApp     │            │            │            │           │          │
     │ (app=Y,    │            │            │            │           │          │
     │  P0, 8GPU) │            │            │            │           │          │
     │───────────▶│            │            │            │           │          │
     │            │            │            │            │           │          │
     │            │ compute_placement       │            │           │          │
     │            │───────────▶│            │            │           │          │
     │            │            │            │            │           │          │
     │            │            │ No 8-GPU block free     │           │          │
     │            │            │ App Z (P2) on node-5    │           │          │
     │            │            │ can be preempted        │           │          │
     │            │            │            │            │           │          │
     │            │◀───────────│            │            │           │          │
     │            │ Plan:      │            │            │           │          │
     │            │  preempt Z │            │            │           │          │
     │            │  scale_up Y│            │            │           │          │
     │            │            │            │            │           │          │
     │            │ execute(plan)           │            │           │          │
     │            │────────────────────────▶│            │           │          │
     │            │            │            │            │           │          │
     │            │            │            │ [Phase 1: Preemption]  │          │
     │            │            │            │            │           │          │
     │            │            │            │ delete_pod │           │          │
     │            │            │            │ (Z, grace=10s)         │          │
     │            │            │            │───────────▶│           │          │
     │            │            │            │            │           │          │
     │            │            │            │            │ SIGTERM   │          │
     │            │            │            │            │──────────────────────▶
     │            │            │            │            │           │          │
     │            │            │            │            │ (drain 10s)          │
     │            │            │            │            │           │          │
     │            │            │            │            │ terminated│          │
     │            │            │            │            │◀──────────────────────
     │            │            │            │            │           │          │
     │            │            │            │◀───────────│           │          │
     │            │            │            │  deleted   │           │          │
     │            │            │            │            │           │          │
     │            │            │            │ [Phase 2: Scale-up]    │          │
     │            │            │            │            │           │          │
     │            │            │            │ create_pod │           │          │
     │            │            │            │ (Y, prefer │           │          │
     │            │            │            │  node-5)   │           │          │
     │            │            │            │───────────▶│           │          │
     │            │            │            │            │           │          │
     │            │            │            │            │ bind(node-5)         │
     │            │            │            │            │──────────▶│          │
     │            │            │            │            │           │          │
     │            │            │            │            │◀──────────│          │
     │            │            │            │            │  bound    │          │
     │            │            │            │            │           │          │
     │            │            │            │◀───────────│           │          │
     │            │            │            │  running   │           │          │
     │            │            │            │            │           │          │
     │            │◀───────────────────────│            │           │          │
     │            │  ExecutionResult: success           │           │          │
```

### 5.3 Background State Sync

```
┌───────────────┐┌───────────────┐┌───────────────┐┌─────────────┐
│   Cluster     ││    Cache      ││  State Sync   ││ World State │
│  Aggregator   ││   Registry    ││  (background) ││ (in-memory) │
└───────┬───────┘└───────┬───────┘└───────┬───────┘└──────┬──────┘
        │                │                │               │
        │ [Watch stream] │                │               │
        │◀───────────────────────────────│               │
        │                │                │               │
        │ PodEvent:      │                │               │
        │ pod-xyz now    │                │               │
        │ Running on     │                │               │
        │ node-5         │                │               │
        │───────────────────────────────▶│               │
        │                │                │               │
        │                │                │ upsert_replica│
        │                │                │ (pod-xyz,     │
        │                │                │  node-5)      │
        │                │                │──────────────▶│
        │                │                │               │
        │                │ [Poll: 30s]    │               │
        │                │◀───────────────│               │
        │                │                │               │
        │                │ CacheState:    │               │
        │                │ model-A cached │               │
        │                │ on [node-1,    │               │
        │                │     node-5]    │               │
        │                │───────────────▶│               │
        │                │                │               │
        │                │                │ update_cache  │
        │                │                │──────────────▶│
        │                │                │               │
        │ [Full sync: 5m]│                │               │
        │◀───────────────────────────────│               │
        │                │                │               │
        │ FullClusterState               │               │
        │───────────────────────────────▶│               │
        │                │                │               │
        │                │                │ reconcile_all │
        │                │                │──────────────▶│
        │                │                │               │
```

---

## 6. Key Design Decisions Summary

| Decision | Choice | Rationale |
|----------|--------|-----------|
| **Scheduling architecture** | Tiered: global cluster selection + local node binding | Leverages battle-tested in-cluster schedulers (Volcano); handles last-mile binding with fresher state; global scheduler still controls preemption and cluster-level constraints |
| **Global scheduler output** | Cluster + scheduling constraints (not exact node) | Allows local scheduler flexibility while ensuring cache affinity and packing preferences are respected |
| **Preemption coordination** | Global scheduler deletes pods directly, then schedules replacement | Ensures atomic "free capacity then use it" flow; preferred_nodes hint guides local scheduler to freed node |
| **State management** | In-memory WorldState with continuous sync | Low-latency reads; acceptable to rebuild on restart; Cluster Aggregator is source of truth |
| **In-cluster scheduler** | Volcano | GPU topology awareness, bin-packing plugins, gang scheduling (future), active community |

---

## 7. Failure Modes & Mitigations

| Failure | Impact | Mitigation |
|---------|--------|------------|
| **Global scheduler crash** | No new scheduling decisions | Leader election promotes standby; WorldState rebuilt from Cluster Aggregator |
| **Cluster Aggregator unavailable** | Stale WorldState | Continue with last-known state; mark affected clusters "degraded"; alert |
| **In-cluster scheduler fails to bind** | Pod stuck pending | Timeout in Plan Executor; re-evaluate with updated WorldState; possibly choose different cluster |
| **Preempted pod ignores SIGTERM** | Delays capacity release | Force-kill after grace period; this delay is factored into scale-up timeout |
| **Cache Registry stale** | Suboptimal placement (cold start instead of warm) | Acceptable degradation; cache affinity is a preference, not requirement |

---

## 8. Open Questions for Detailed Design

1. **Traffic signal schema:** Exact format from Traffic Gateway (QPS, latency histogram, queue depth?)
2. **Scoring weights:** Tuning WEIGHT_CACHE, WEIGHT_FRAGMENTATION, WEIGHT_BALANCE
3. **Volcano configuration:** Exact plugins and queue configuration
4. **Observability:** Metrics to export (scheduling latency, preemption rate, fragmentation score over time)
5. **Schedulator (simulator):** Design for testing algorithms before production

---

## 9. Next Steps

1. Define API contracts for Traffic Gateway and Cluster Aggregator
2. Prototype Scaling Engine with real traffic patterns
3. Implement Placement Engine scoring function; validate with simulation
4. Set up Volcano in test clusters with GPU bin-packing config
5. Build observability dashboards for scheduling decisions
