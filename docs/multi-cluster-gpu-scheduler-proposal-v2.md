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
├───────────────────────────────────┬─────────────────────┬───────────────────────────────┤
│         Cluster Aggregator        │   Cache Registry    │     Performance Profiler      │
│  (pods/nodes/vLLM metrics across  │  (tiered model      │     (offline metrics)         │
│          all clusters)            │   locations)        │                               │
└─────────────────┬─────────────────┴──────────┬──────────┴───────────────┬───────────────┘
                  │                            │                          │
                  │ cluster state + vLLM       │ cache state (tiered)     │ app profiles
                  │ (watch + poll 10s)         │ (poll 30s)               │ (poll 5m)
                  ▼                            ▼                          ▼
┌─────────────────────────────────────────────────────────────────────────────────────────┐
│                                                                                         │
│                              GLOBAL SCHEDULER                                           │
│                                                                                         │
│  ┌───────────────────────────────────────────────────────────────────────────────────┐  │
│  │                            Event Ingestion Layer                                  │  │
│  │  ┌─────────────┐ ┌─────────────┐ ┌─────────────┐ ┌─────────────┐                 │  │
│  │  │vLLM Metrics │ │Cluster Event│ │ SLA Breach  │ │   Timer     │                 │  │
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
│  │   │  │  Cluster   │ │Application │ │Cache State │ │   vLLM     │            │   │  │
│  │   │  │  Topology  │ │  Configs   │ │  (tiered)  │ │  Metrics   │            │   │  │
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
│  │   │ (uses vLLM      │         │ (cache-tier     │    │                 │        │  │
│  │   │  metrics)       │         │  weighted)      │    │                 │        │  │
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
| **vLLM Metrics** | Cluster Aggregator | `MetricsUpdate(app_id, queue_depth, kv_cache_util, ...)` | Polled every 10s |
| **Cluster Event** | Cluster Aggregator | `NodeDown`, `NodeUp`, `PodTerminated`, `PodRunning` | Watch stream |
| **SLA Breach** | Monitoring | `SLABreach(app_id, metric, threshold, actual)` | Alert webhook |
| **Timer** | Internal | `PeriodicEval` | Every 60s |

**Debouncing:** Events are coalesced over a short window (1-2s) to prevent thrashing. Multiple `MetricsUpdate` events for the same app within the window are merged, keeping the most recent values.

---

### 4.2 World State

In-memory representation of global state, continuously updated via background sync from external dependencies.

```
WorldState:
  clusters: Map<ClusterId, ClusterState>
  applications: Map<AppId, ApplicationConfig>
  replicas: Map<ReplicaId, ReplicaState>
  cache_locations: Map<ModelId, List<CacheLocation>>
  performance_profiles: Map<AppId, PerformanceProfile>
  vllm_metrics: Map<AppId, vLLMMetrics>
  reservations: Map<ReservationId, GpuReservation>
  scaling_history: Map<AppId, ScalingHistory>

ClusterState:
  cluster_id: string
  nodes: Map<NodeId, NodeState>
  fragmentation_score: float  # precomputed

NodeState:
  node_id: string
  total_gpus: 8  # fixed
  allocated_gpus: int
  free_gpus: int
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

CacheLocation:
  model_id: string
  cluster_id: string
  node_id: string | null       # null for CLUSTER_STORAGE tier
  tier: "GPU_MEMORY" | "LOCAL_NVME" | "CLUSTER_STORAGE"
  cached_at: timestamp

vLLMMetrics:
  app_id: string
  aggregated_from: int         # number of replicas reporting

  # Queue pressure
  avg_waiting_queue_depth: float
  max_waiting_queue_depth: float
  avg_running_queue_depth: float

  # Batch utilization
  avg_batch_utilization: float  # 0.0-1.0

  # KV cache pressure
  avg_kv_cache_utilization: float
  max_kv_cache_utilization: float

  # Throughput
  total_tokens_per_second: float
  avg_time_to_first_token_ms: float
  p99_time_to_first_token_ms: float

  measured_at: timestamp

GpuReservation:
  reservation_id: string
  cluster_id: string
  node_id: string | null        # null = cluster-level reservation
  gpus: int
  for_app_id: string
  created_at: timestamp
  ttl_seconds: int              # 2x expected startup time
  status: "active" | "fulfilled" | "expired"

ScalingHistory:
  app_id: string
  last_scale_up_at: timestamp
  last_scale_down_at: timestamp
  consecutive_scale_down_signals: int   # reset on scale-up or no signal
```

**Fragmentation Metrics:**

```
# Per-cluster fragmentation: fraction of total GPUs that are free
cluster_fragmentation = 1 - (sum(node.free_gpus) / sum(node.total_gpus))
  # 0.0 = all GPUs free
  # 1.0 = all GPUs allocated
```

**GPU Reservations:**

Reservations reduce `free_gpus` in NodeState calculations. When computing available capacity, active reservations are subtracted from physical free GPUs. Reservations are cluster-scoped (not node-scoped) when using tiered scheduling, since the in-cluster scheduler picks the final node.

**Sync Strategy:**

| Data | Source | Method | Frequency |
|------|--------|--------|-----------|
| Clusters, Nodes, Pods | Cluster Aggregator | Watch stream + full reconcile | Continuous + every 5m |
| Cache locations (tiered) | Cache Registry | Poll | Every 30s |
| Performance profiles | Performance Profiler | Poll | Every 5m |
| Application configs | Config store (API/GitOps) | Watch | Continuous |
| vLLM metrics | Cluster Aggregator (scrapes vLLM /metrics) | Poll | Every 10s |

---

### 4.3 Scaling Engine

Computes target replica count per application using vLLM runtime metrics.

**Input:** `(ApplicationConfig, vLLMMetrics, PerformanceProfile, current_replica_count)`

**Output:** `target_replica_count: int`

#### vLLM Metrics

The Scaling Engine consumes metrics directly from vLLM replicas (aggregated by the Cluster Aggregator):

```
vLLMMetrics:
  app_id: string
  aggregated_from: int  # number of replicas reporting

  # Queue pressure (primary scaling signal)
  avg_waiting_queue_depth: float      # requests waiting to be processed
  max_waiting_queue_depth: float      # worst replica's queue
  avg_running_queue_depth: float      # requests currently being processed

  # Batch utilization
  avg_batch_utilization: float        # 0.0-1.0, actual batch size / max batch size

  # KV cache pressure
  avg_kv_cache_utilization: float     # 0.0-1.0, used blocks / total blocks
  max_kv_cache_utilization: float     # worst replica's cache pressure

  # Throughput (trailing window)
  total_tokens_per_second: float      # sum across all replicas
  avg_time_to_first_token_ms: float   # P50 TTFT
  p99_time_to_first_token_ms: float   # P99 TTFT

  # Timing
  measured_at: timestamp
```

#### Scaling Algorithm

```
function compute_target_replicas(app, metrics, profile, current_count):
    # Primary signal: queue depth indicates demand exceeding capacity
    # Target: keep avg queue depth below threshold (e.g., 5 requests)
    if metrics.avg_waiting_queue_depth > QUEUE_HIGH_WATERMARK:
        # Scale up: estimate replicas needed to drain queue
        queue_pressure_factor = metrics.avg_waiting_queue_depth / QUEUE_TARGET
        required_for_queue = ceil(current_count * queue_pressure_factor)
    else:
        required_for_queue = current_count

    # Secondary signal: KV cache pressure indicates memory exhaustion
    # High cache utilization causes evictions → latency spikes
    if metrics.max_kv_cache_utilization > KV_CACHE_HIGH_WATERMARK:
        # Need more replicas to spread request load
        cache_pressure_factor = metrics.max_kv_cache_utilization / KV_CACHE_TARGET
        required_for_cache = ceil(current_count * cache_pressure_factor)
    else:
        required_for_cache = current_count

    # Tertiary signal: SLA compliance
    # If P99 TTFT exceeds SLA despite low queue depth, model is overloaded
    if metrics.p99_time_to_first_token_ms > app.sla.max_p99_ttft_ms:
        sla_breach_factor = metrics.p99_time_to_first_token_ms / app.sla.max_p99_ttft_ms
        required_for_sla = ceil(current_count * sla_breach_factor)
    else:
        required_for_sla = current_count

    # Scale-down signal: low utilization across all metrics
    if (metrics.avg_waiting_queue_depth < QUEUE_LOW_WATERMARK and
        metrics.avg_batch_utilization < BATCH_LOW_WATERMARK and
        metrics.avg_kv_cache_utilization < KV_CACHE_LOW_WATERMARK and
        current_count > app.min_replicas):
        # Can potentially scale down
        utilization = max(metrics.avg_batch_utilization,
                          metrics.avg_kv_cache_utilization,
                          metrics.avg_waiting_queue_depth / QUEUE_TARGET)
        desired_for_efficiency = max(ceil(current_count * utilization), app.min_replicas)
    else:
        desired_for_efficiency = current_count

    # Compute target: max of scale-up signals, but respect scale-down if no pressure
    scale_up_target = max(required_for_queue, required_for_cache, required_for_sla)

    if scale_up_target > current_count:
        target = scale_up_target
    else:
        target = desired_for_efficiency

    # Always respect minimum
    target = max(target, app.min_replicas)

    # Dampening: don't scale down too aggressively
    if target < current_count:
        target = max(target, current_count - MAX_SCALE_DOWN_PER_CYCLE)

    # Dampening: don't scale up too aggressively (avoid thrashing)
    if target > current_count:
        target = min(target, current_count + MAX_SCALE_UP_PER_CYCLE)

    return target

# Configurable thresholds
QUEUE_HIGH_WATERMARK = 10      # requests waiting → scale up
QUEUE_TARGET = 5               # ideal queue depth
QUEUE_LOW_WATERMARK = 1        # requests waiting → consider scale down
KV_CACHE_HIGH_WATERMARK = 0.85 # 85% cache utilization → scale up
KV_CACHE_TARGET = 0.70         # ideal cache utilization
KV_CACHE_LOW_WATERMARK = 0.40  # 40% cache utilization → consider scale down
BATCH_LOW_WATERMARK = 0.30     # 30% batch utilization → consider scale down
MAX_SCALE_DOWN_PER_CYCLE = 2   # max replicas to remove per evaluation
MAX_SCALE_UP_PER_CYCLE = 5     # max replicas to add per evaluation
```

#### In-Flight Awareness (Startup Time)

Before computing the final delta, the Scaling Engine accounts for replicas that are already pending (requested but not yet running). This prevents redundant scale-ups during the startup window (30s to 10min depending on cache tier).

```
function compute_effective_target(app, target, world_state):
    current_running = count(world_state.replicas
                            where app_id == app.app_id and status == "running")
    already_scaling = count(world_state.replicas
                            where app_id == app.app_id and status == "pending")

    # Don't count pending replicas as capacity (PENDING_DISCOUNT = 0.0)
    # But DO count them toward "already scaling" to prevent re-firing
    additional_needed = max(0, target - current_running - already_scaling)

    # SLA breach override: if pending replicas have been pending too long,
    # treat them as failed and request fresh scale-ups
    profile = world_state.performance_profiles[app.app_id]
    expected_startup = profile.cold_start_seconds * 2  # generous margin
    stale_pending = count(world_state.replicas
                          where app_id == app.app_id
                          and status == "pending"
                          and age > expected_startup)

    if stale_pending > 0 and is_sla_breach(app, world_state.vllm_metrics[app.app_id]):
        # These pending replicas are likely stuck; request fresh ones
        additional_needed += stale_pending

    return current_running + already_scaling + additional_needed
```

#### Scaling Stability Controls

Per-cycle dampening prevents large jumps in a single evaluation, but doesn't prevent oscillation across cycles. The following controls add cross-cycle stability:

**Cooldown periods:**
```
SCALE_UP_COOLDOWN = 120s    # after scale-up, wait before allowing scale-down
SCALE_DOWN_COOLDOWN = 300s  # after scale-down, wait before further scale-down
```

**Stabilization window:** Require metrics to exceed thresholds for `STABILIZATION_CYCLES` (e.g., 3 consecutive evaluations = ~3 min) before acting on scale-down. Scale-up has no stabilization delay (responsiveness matters more than stability for scaling up). Exception: SLA breaches bypass stabilization entirely.

**Scaling history tracking:** Each application maintains a `ScalingHistory` record in WorldState (see section 4.2).

**Stability checks applied after computing target:**
```
function apply_stability_controls(app_id, target, current, world_state):
    history = world_state.scaling_history[app_id]
    now = current_time()

    # Cooldown: don't scale down too soon after scaling up
    if target > current:
        if now - history.last_scale_up_at < SCALE_UP_COOLDOWN:
            return current  # still in cooldown
        history.last_scale_up_at = now
        history.consecutive_scale_down_signals = 0  # reset

    if target < current:
        # Cooldown: don't scale down too soon after last scale-down
        if now - history.last_scale_down_at < SCALE_DOWN_COOLDOWN:
            return current

        # Stabilization: require N consecutive signals before scale-down
        history.consecutive_scale_down_signals += 1
        if history.consecutive_scale_down_signals < STABILIZATION_CYCLES:
            return current  # not enough consecutive signals yet

        # SLA breach override: bypass stabilization
        metrics = world_state.vllm_metrics[app_id]
        app = world_state.applications[app_id]
        if is_sla_breach(app, metrics):
            pass  # allow immediate action

        history.last_scale_down_at = now
        history.consecutive_scale_down_signals = 0

    return target

STABILIZATION_CYCLES = 3  # ~3 min at 60s eval interval
```

#### Why These Signals

| Signal | Why It Matters | Scaling Action |
|--------|----------------|----------------|
| **Queue depth** | Direct measure of demand exceeding capacity | High → add replicas to process backlog |
| **KV cache utilization** | Memory pressure causes evictions → latency spikes | High → spread load across more replicas |
| **Batch utilization** | Low utilization = wasted GPUs | Low + low queue → consolidate replicas |
| **P99 TTFT** | SLA compliance | Breach → add capacity regardless of other signals |

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
            # Create GPU reservation to prevent double-booking
            reservation = GpuReservation(
                cluster_id=cluster.cluster_id,
                node_id=None,  # cluster-scoped; in-cluster scheduler picks node
                gpus=constraints.required_gpus,
                for_app_id=app_id,
                ttl_seconds=2 * world_state.performance_profiles[app_id].cold_start_seconds
            )
            world_state.reservations[reservation.reservation_id] = reservation
            world_state.update_available_capacity(cluster, reservation)
    
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
        preferred_nodes = []

        # Cache affinity: tiered scoring based on cache warmth
        # GPU memory (hot) >> Local NVMe >> Remote storage (cold)
        cache_score, preferred_nodes = compute_cache_score(app.model_id, cluster, world_state)
        score += cache_score  # Already weighted by tier

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


function compute_cache_score(model_id, cluster, world_state):
    """
    Compute cache affinity score based on tiered cache warmth.

    Cache tiers (from fastest to slowest):
    - GPU_MEMORY: Model weights already loaded in GPU VRAM (near-instant start)
    - LOCAL_NVME: Model weights on node's local NVMe storage (~30-60s load)
    - CLUSTER_STORAGE: Model in cluster's shared storage (~2-5min load)
    - REMOTE: Model must be pulled from remote (S3/GCS) (~5-10min load)

    Returns (score, preferred_nodes) where score heavily favors warmer tiers.
    """

    cache_locations = world_state.cache_locations.get(model_id, [])
    cluster_locations = [loc for loc in cache_locations if loc.cluster_id == cluster.cluster_id]

    # Categorize nodes by cache tier
    gpu_memory_nodes = []   # Tier 0: model in GPU VRAM (replica already running)
    local_nvme_nodes = []   # Tier 1: model on local disk
    cluster_storage = False # Tier 2: model in shared cluster storage

    for loc in cluster_locations:
        if loc.tier == "GPU_MEMORY":
            gpu_memory_nodes.append(loc.node_id)
        elif loc.tier == "LOCAL_NVME":
            local_nvme_nodes.append(loc.node_id)
        elif loc.tier == "CLUSTER_STORAGE":
            cluster_storage = True

    # Score by best available tier (higher = better)
    # These weights are intentionally large to dominate other factors
    # because cache warmth has 100x impact on startup time
    if gpu_memory_nodes:
        # Best case: can scale by adding to existing replica's node or nearby
        # (Note: GPU_MEMORY usually means we already have a replica there,
        #  so this primarily helps with identifying "warm" clusters)
        score = WEIGHT_CACHE_GPU_MEMORY  # 1000 points
        preferred_nodes = gpu_memory_nodes + local_nvme_nodes
    elif local_nvme_nodes:
        # Good case: fast local load from NVMe
        score = WEIGHT_CACHE_LOCAL_NVME  # 500 points
        preferred_nodes = local_nvme_nodes
    elif cluster_storage:
        # Acceptable: shared storage within cluster
        score = WEIGHT_CACHE_CLUSTER_STORAGE  # 100 points
        preferred_nodes = []  # Any node in cluster can access
    else:
        # Cold start: must pull from remote
        score = WEIGHT_CACHE_REMOTE  # 0 points
        preferred_nodes = []

    return score, preferred_nodes


# Cache tier weights (dominate other scoring factors)
WEIGHT_CACHE_GPU_MEMORY = 1000      # ~instant start
WEIGHT_CACHE_LOCAL_NVME = 500       # ~30-60s start
WEIGHT_CACHE_CLUSTER_STORAGE = 100  # ~2-5min start
WEIGHT_CACHE_REMOTE = 0             # ~5-10min start (baseline)

# Other scoring weights (secondary to cache)
WEIGHT_FRAGMENTATION = 50   # Packing efficiency
WEIGHT_BALANCE = 30         # Load distribution


function compute_packing_score(cluster, gpus_needed):
    """
    Score how well a cluster can pack the requested GPUs.
    Prefers tightest-fitting node (fewest free GPUs >= gpus_needed)
    to minimize stranded capacity.

    Returns 0.0-1.0 where 1.0 = perfect fit (no waste).
    """
    fitting_nodes = [n for n in cluster.nodes
                     if n.free_gpus >= gpus_needed and n.status == "ready"]
    if not fitting_nodes:
        return 0.0
    best_fit = min(fitting_nodes, key=lambda n: n.free_gpus)
    waste = best_fit.free_gpus - gpus_needed
    return 1.0 - (waste / 8)  # 0-1 score; 8 = max GPUs per node
```

#### Cache Tier Definitions

| Tier | Location | Startup Time | Score | Use Case |
|------|----------|--------------|-------|----------|
| `GPU_MEMORY` | Weights loaded in VRAM | ~5s | 1000 | Existing replica on node; can potentially share |
| `LOCAL_NVME` | Node's local SSD/NVMe | 30-60s | 500 | Previously ran on this node; weights cached |
| `CLUSTER_STORAGE` | Shared PVC/NFS in cluster | 2-5min | 100 | Model pre-staged to cluster storage |
| `REMOTE` | S3/GCS/remote registry | 5-10min | 0 | Cold start; must download model |

The scoring weights ensure that a "worse packed" cluster with warm cache almost always beats a "better packed" cluster requiring cold start. For a 70B model, the difference between LOCAL_NVME (60s) and REMOTE (10min) is 9 minutes of SLA exposure.

#### Preemption Logic

Preemption follows a strict priority cascade: P2 is preempted first, then P1 only if P2 capacity is insufficient and the requesting app is P0.

```
function find_preemption_opportunity(requesting_app, world_state):
    """
    Find replicas to preempt to make room for requesting_app.

    Preemption policy:
    - P0 can preempt: P2 first, then P1 if P2 is insufficient
    - P1 can preempt: P2 only
    - P2 cannot preempt anyone

    Within each priority tier, we prefer to preempt:
    1. Replicas above min_replicas (preserve minimum guarantees)
    2. Replicas that free useful GPU blocks (packing benefit)
    3. Replicas in the same cluster (avoid cross-cluster coordination)
    """

    if requesting_app.priority == 2:
        # P2 cannot preempt anyone
        return None

    # Build candidate pool by priority tier
    p2_candidates = []
    p1_candidates = []

    for replica in world_state.replicas:
        victim_app = world_state.applications[replica.app_id]

        # Skip same or higher priority
        if victim_app.priority <= requesting_app.priority:
            continue

        # Categorize by priority tier
        if victim_app.priority == 2:
            p2_candidates.append(replica)
        elif victim_app.priority == 1:
            p1_candidates.append(replica)

    # Sort each tier by preemption preference
    def preemption_sort_key(replica):
        victim_app = world_state.applications[replica.app_id]
        current_count = count_replicas(replica.app_id, world_state)
        above_minimum = current_count > victim_app.min_replicas

        return (
            0 if above_minimum else 1,  # Prefer replicas above min_replicas
            -packing_benefit(replica, requesting_app.gpus_per_replica),  # Prefer better packing
        )

    p2_candidates.sort(key=preemption_sort_key)
    p1_candidates.sort(key=preemption_sort_key)

    # Try to satisfy request with P2 preemptions first
    victims, freed, target_cluster = select_victims(
        p2_candidates,
        requesting_app.gpus_per_replica,
        world_state
    )

    # If P2 is insufficient and requester is P0, escalate to P1
    if freed < requesting_app.gpus_per_replica and requesting_app.priority == 0:
        # Continue selecting from P1, respecting existing target_cluster if set
        additional_victims, additional_freed, target_cluster = select_victims(
            p1_candidates,
            requesting_app.gpus_per_replica - freed,
            world_state,
            prefer_cluster=target_cluster
        )
        victims.extend(additional_victims)
        freed += additional_freed

    if freed < requesting_app.gpus_per_replica:
        return None  # Cannot free enough even with full preemption cascade

    return PreemptionPlan(
        victims=victims,
        target_cluster=target_cluster,
        constraints=SchedulingConstraints(
            required_gpus=requesting_app.gpus_per_replica,
            preferred_nodes=[v.node_id for v in victims]  # Prefer the just-freed nodes
        )
    )


function select_victims(candidates, required_gpus, world_state, prefer_cluster=None):
    """
    Select victims from candidates to free at least required_gpus.
    Returns (victims, freed_gpus, target_cluster).
    """
    victims = []
    freed = 0
    target_cluster = prefer_cluster

    for candidate in candidates:
        victim_app = world_state.applications[candidate.app_id]
        current_count = count_replicas(candidate.app_id, world_state)

        # Skip if this would reduce below minimum
        # (We already sorted to prefer above-minimum, but double-check)
        already_preempting = sum(1 for v in victims if v.app_id == candidate.app_id)
        if current_count - already_preempting <= victim_app.min_replicas:
            continue

        # Prefer same cluster to avoid cross-cluster coordination
        if target_cluster and candidate.cluster_id != target_cluster:
            continue

        # First victim sets the target cluster
        if not target_cluster:
            target_cluster = candidate.cluster_id

        victims.append(PreemptionDecision(
            victim_replica_id=candidate.replica_id,
            victim_app_id=candidate.app_id,
            node_id=candidate.node_id,
            grace_period_seconds=DEFAULT_PREEMPTION_GRACE_PERIOD,
            reason=f"Preempted for higher priority workload"
        ))
        freed += candidate.gpus

        if freed >= required_gpus:
            break

    return victims, freed, target_cluster


# Preemption constants
DEFAULT_PREEMPTION_GRACE_PERIOD = 10  # seconds to drain in-flight requests
```

#### Preemption Policy Summary

| Requester | Can Preempt | Escalation |
|-----------|-------------|------------|
| P0 (highest) | P2 first, then P1 | Full cascade allowed |
| P1 (medium) | P2 only | No escalation to P0 |
| P2 (lowest) | None | Cannot preempt |

#### Preemption Safeguards

1. **min_replicas protection**: A victim app is only preempted down to its `min_replicas` threshold. Below that, it is protected (except in the extreme case where the system cannot satisfy any P0 request—this is an operational emergency).

2. **Same-cluster preference**: All preemption victims are selected from the same cluster to avoid coordinating cross-cluster operations and to ensure the freed capacity is usable by the requester.

3. **Packing benefit**: Among equally-prioritized victims, prefer those that free contiguous GPU blocks matching the requester's needs (e.g., if requester needs 8 GPUs, prefer preempting an 8-GPU replica over two 4-GPU replicas).

---

### 4.5 Plan Generator

Transforms `PlacementDecisions` into an ordered execution plan.

```
ExecutionPlan:
  operations: List<Operation>  # ordered

Operation:
  type: "preempt" | "scale_down" | "scale_up" | "migrate"
  payload: PreemptionDecision | ScaleDownDecision | ScaleUpDecision | MigrateDecision
  depends_on: List<OperationId>  # for sequencing

MigrateDecision:
  app_id: string
  source_replica_id: string       # replica to remove after new one is running
  target_cluster_id: string
  scheduling_constraints: SchedulingConstraints
  # Internally decomposed into a dependent pair:
  #   scale_up_op (new replica) → scale_down_op (old replica)
  #   scale_down_op.depends_on = [scale_up_op.id]
```

**Ordering rules:**
1. Preemptions first (must free capacity before scale-ups can use it)
2. Scale-downs second (also frees capacity)
3. Scale-ups third
4. Migrations last (blue-green: scale-up new, then scale-down old)

**Dependency tracking:**
- Scale-ups that target preempted capacity depend on those preemptions completing
- Migrations are decomposed into a scale-up + scale-down pair, where the scale-down depends on the scale-up completing successfully (new replica must be running before old is removed)
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

#### Cluster Client Interface

GPU inference replicas are NOT fungible — each has unique cache state, scheduling constraints, and preemption value. A single multi-replica Deployment won't work because: (a) all pods share one pod template (can't have different preferred_nodes per replica), and (b) K8s chooses which pod to remove on scale-down (the global scheduler needs that control for preemption/packing).

**V1 approach: Single-replica Deployments.** Each replica is a separate Deployment with `replicas: 1` and a unique name (e.g., `app-X-replica-a1b2`). Each gets its own pod template with per-replica scheduling constraints. K8s Deployment controller handles crash recovery automatically. The global scheduler creates/deletes Deployments (not raw pods). Scale-down/preemption = delete the specific Deployment.

```
# Cluster Client interface (implementation-agnostic)
create_replica(app_id, scheduling_constraints) → replica_id
delete_replica(replica_id)
get_replica_status(replica_id) → status
```

**Future migration path:** The Cluster Client interface is the abstraction boundary. A future version can swap to a custom `InferenceReplica` CRD + operator (cleaner K8s-native pattern) by changing only the Cluster Client implementation — no changes needed in the Scaling Engine, Placement Engine, or Plan Executor.

#### Cluster Client Operations

**Scale-up (with reservation lifecycle):**
```
function scale_up(cluster_client, decision, reservation):
    # Step 1: Reservation already created during placement (see 4.4)
    assert reservation.status == "active"

    # Step 2: Create single-replica Deployment
    replica_id = cluster_client.create_replica(
        app_id=decision.app_id,
        scheduling_constraints=decision.scheduling_constraints
    )

    # Step 3: Wait for pod to be scheduled and running
    try:
        wait_for_condition(
            cluster_client,
            replica_id,
            condition="Running",
            timeout=reservation.ttl_seconds
        )
        # Success: reservation fulfilled
        reservation.status = "fulfilled"
    except Timeout:
        # Failure: reservation expired, freed for next cycle
        reservation.status = "expired"
        cluster_client.delete_replica(replica_id)
        raise ScaleUpFailed(decision)
```

**Preemption:**
```
function preempt(cluster_client, decision):
    # Step 1: Mark replica as draining (remove from service endpoints)
    cluster_client.label_replica(decision.victim_replica_id, {"draining": "true"})

    # Step 2: Delete the Deployment
    cluster_client.delete_replica(decision.victim_replica_id)

    # Step 3: Wait for termination
    wait_for_deletion(cluster_client, decision.victim_replica_id)
```

**Scale-down:**
```
function scale_down(cluster_client, decision):
    # Same as preemption but without the urgency
    cluster_client.delete_replica(decision.replica_id)
    wait_for_deletion(cluster_client, decision.replica_id)
```

---

### 4.7 In-Cluster Scheduler (Volcano)

Each cluster runs an in-cluster scheduler (Volcano or kube-scheduler with GPU plugins) that:

1. **Receives Deployments** created by the Global Scheduler (via Cluster Client). Each Deployment has `replicas: 1` with per-replica scheduling constraints
2. **Schedules the pod** created by the Deployment controller, respecting constraints (nodeAffinity, resources)
3. **Binds to node** using its local bin-packing logic
4. **Reports status** back via standard k8s pod status (watched by Cluster Aggregator)

The Deployment controller handles crash recovery (restarts). Volcano handles initial scheduling and rescheduling after node failures.

**Configuration:**
- Volcano configured with `binpack` scheduling plugin for GPU-aware packing
- Node labels for cache state (`cache.mycompany.io/model-xyz: "true"`) updated by Cache Registry agent
- GPU topology awareness enabled (nvidia device plugin)

**Why Volcano over kube-scheduler:**
- Native gang scheduling (if we later need multi-pod workloads)
- Better bin-packing plugins for GPU workloads
- Queue management (useful for batch, though not v1 scope)

---

### 4.8 Rebalancing Engine

Proactively consolidates fragmented GPU allocations by migrating replicas to better-packed locations. Runs as the final phase of `compute_placement()`, after all scale-ups have been computed.

**Triggers:**
- Periodic evaluation (every 5-10 min), or
- After scale-downs leave fragmented capacity (detected when `cluster_fragmentation` exceeds threshold)

**Algorithm:**

```
function find_rebalancing_opportunities(world_state):
    """
    Identify replicas that could consolidate onto better-packed nodes,
    freeing full nodes for larger workloads.

    Example: two nodes each with 2 GPUs used could consolidate onto one,
    freeing a full 8-GPU node.
    """
    migrations = []

    for cluster in world_state.clusters:
        if cluster.fragmentation_score < MIN_FRAGMENTATION_DELTA:
            continue  # cluster is well-packed already

        # Find nodes with partial allocations that could move
        fragmented_nodes = [n for n in cluster.nodes
                            if 0 < n.allocated_gpus < n.total_gpus
                            and n.status == "ready"]

        for source_node in fragmented_nodes:
            for replica in source_node.pods:
                app = world_state.applications[replica.app_id]

                # Prefer migrating lower-priority apps first
                # Skip if migration would drop below min_replicas during transition
                running_count = count_running_replicas(replica.app_id, world_state)
                if running_count <= app.min_replicas:
                    continue

                # Find a better destination (tighter packing)
                dest_cluster, dest_constraints = find_consolidation_target(
                    replica, app, world_state
                )
                if dest_cluster is None:
                    continue

                # Skip if destination has no cached model (would cause cold start)
                if not has_cached_model(dest_cluster, app.model_id, world_state):
                    continue

                # Check if fragmentation improvement is worthwhile
                improvement = estimate_fragmentation_delta(
                    source_node, dest_cluster, replica.gpus, world_state
                )
                if improvement < MIN_FRAGMENTATION_DELTA:
                    continue

                migrations.append(MigrateDecision(
                    app_id=replica.app_id,
                    source_replica_id=replica.replica_id,
                    target_cluster_id=dest_cluster.cluster_id,
                    scheduling_constraints=dest_constraints
                ))

                if len(migrations) >= MAX_MIGRATIONS_PER_CYCLE:
                    return migrations

    # Sort: lower priority apps first (safer to migrate)
    migrations.sort(key=lambda m: world_state.applications[m.app_id].priority,
                    reverse=True)
    return migrations[:MAX_MIGRATIONS_PER_CYCLE]


MAX_MIGRATIONS_PER_CYCLE = 2    # max migrations per evaluation
MIN_FRAGMENTATION_DELTA = 0.15  # minimum improvement to justify migration
```

**Migration Strategy: Blue-Green**

Migrations are never in-place. Each migration is a dependent scale-up → scale-down pair:

1. Scale up a new replica in the better location
2. Wait for the new replica to be running and serving traffic
3. Only then scale down the old replica

This ensures the application never temporarily drops below its desired replica count.

**Disruption Budget:**
- Max `MAX_MIGRATIONS_PER_CYCLE` (e.g., 2) migrations per evaluation
- Only migrate if fragmentation improvement exceeds `MIN_FRAGMENTATION_DELTA` (e.g., 0.15)
- Never migrate if it would cause the app to temporarily drop below `min_replicas` (the new replica must be running before the old one is removed)
- Prefer migrating replicas of lower-priority apps first
- Skip migration if the destination cluster has no cached model (would cause cold start)

**Interaction with Plan Generator:** Migrations appear in the execution plan as `MigrateDecision` operations, which the Plan Generator decomposes into paired scale-up + scale-down operations with a dependency (scale-up must complete before scale-down begins). See section 4.5.

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
| **Preemption cascade policy** | P0 can preempt P2 then P1; P1 can preempt P2 only; P2 cannot preempt | Strict priority ordering prevents lower-priority workloads from disrupting higher tiers; allows P0 to reclaim resources in emergencies |
| **Scaling signals** | vLLM runtime metrics (queue depth, KV cache utilization, batch utilization) | Directly measures inference capacity pressure rather than proxy metrics (QPS); captures batching and memory dynamics |
| **Cache affinity** | Tiered scoring: GPU_MEMORY (1000) > LOCAL_NVME (500) > CLUSTER_STORAGE (100) > REMOTE (0) | Cache warmth dominates startup time (5s vs 10min); weights ensure warm cluster beats "optimal" cold cluster |
| **State management** | In-memory WorldState with continuous sync | Low-latency reads; acceptable to rebuild on restart; Cluster Aggregator is source of truth |
| **In-cluster scheduler** | Volcano | GPU topology awareness, bin-packing plugins, gang scheduling (future), active community |
| **Rebalancing** | Blue-green migration with disruption budget | Avoids in-place migration risk; ensures min_replicas during migration; max 2 migrations per cycle |
| **Replica lifecycle** | Single-replica Deployments (v1); Cluster Client interface abstraction allows future CRD migration | Replicas are not fungible (different cache/constraints per replica); K8s handles crash recovery; Cluster Client hides implementation |
| **Race condition prevention** | GPU reservations with TTL | Prevents double-booking; TTL prevents permanent resource leaks; cluster-scoped for tiered scheduling |

---

## 7. Failure Modes & Mitigations

| Failure | Impact | Mitigation |
|---------|--------|------------|
| **Global scheduler crash** | No new scheduling decisions | Leader election promotes standby; WorldState rebuilt from Cluster Aggregator |
| **Cluster Aggregator unavailable** | Stale WorldState | Continue with last-known state; mark affected clusters "degraded"; alert |
| **In-cluster scheduler fails to bind** | Pod stuck pending | Timeout in Plan Executor; re-evaluate with updated WorldState; possibly choose different cluster |
| **Preempted pod ignores SIGTERM** | Delays capacity release | Force-kill after grace period; this delay is factored into scale-up timeout |
| **Cache Registry stale** | Suboptimal placement (cold start instead of warm) | Acceptable degradation; cache affinity is a preference, not requirement |
| **Reservation leak (TTL expiry)** | GPUs appear reserved but no pod arrives | TTL auto-expires; freed for next scheduling cycle; no permanent resource lock |
| **Migration failure (new replica doesn't start)** | Old replica not removed; fragmentation unchanged | Migration is atomic: old replica only removed after new one is confirmed running; on failure, old replica continues serving |

---

## 8. Performance Characteristics

WorldState is in-memory — all reads are O(1) map lookups, ensuring the scheduling loop is not bottlenecked by data access.

| Component | Complexity | Notes |
|-----------|-----------|-------|
| **Scaling Engine** | O(A) where A = applications | Each app's computation is O(1) arithmetic |
| **Placement Engine** | O(A × C × N) where C = clusters, N = nodes/cluster | For typical fleets (50 apps, 10 clusters, 100 nodes/cluster) = 50K scoring operations, each O(1) |
| **Plan Generator** | O(ops) | Simple sorting of operations |
| **Rebalancing Engine** | O(C × N × R) where R = replicas per node | Bounded by MAX_MIGRATIONS_PER_CYCLE (early exit) |
| **Total per cycle** | < 100ms | Comfortably handles fleets up to hundreds of apps and thousands of nodes |

**If fleet grows beyond these bounds:**
- Incremental scoring: only re-score apps with changed metrics since last cycle
- Parallel scoring: score apps independently (embarrassingly parallel)
- Pre-computed cluster summaries: refresh on state sync rather than per-cycle

---

## 9. Open Questions for Detailed Design

1. ~~**Traffic signal schema:**~~ → Resolved: Using vLLM metrics (queue depth, KV cache utilization, batch utilization, TTFT) scraped via Cluster Aggregator
2. **Scoring weights:** Tuning thresholds for scaling (QUEUE_HIGH_WATERMARK, KV_CACHE_HIGH_WATERMARK) and placement weights—requires simulation/production tuning
3. **Volcano configuration:** Exact plugins and queue configuration
4. ~~**Startup time handling:**~~ → Resolved: In-flight awareness with PENDING_DISCOUNT; SLA breach override for stale pending replicas; cooldown and stabilization windows prevent oscillation (see section 4.3)
5. **Observability:** Metrics to export (scheduling latency, preemption rate, fragmentation score over time)
6. **Schedulator (simulator):** Design for testing algorithms before production
7. ~~**Preemption cascades:**~~ → Resolved: P0 can preempt P1 after exhausting P2; P1 can only preempt P2; P2 cannot preempt
8. ~~**Race conditions:**~~ → Resolved: GPU reservations with TTL prevent double-booking; reservations are cluster-scoped for tiered scheduling; TTL auto-expires to prevent permanent resource leaks (see sections 4.2, 4.4, 4.6)
9. **Rebalancing tuning:** Rebalancing frequency and disruption budget tuning (MAX_MIGRATIONS_PER_CYCLE, MIN_FRAGMENTATION_DELTA) — requires simulation data to validate

---

## 10. Next Steps

1. Define API contracts for Traffic Gateway and Cluster Aggregator
2. Prototype Scaling Engine with real traffic patterns
3. Implement Placement Engine scoring function; validate with simulation
4. Set up Volcano in test clusters with GPU bin-packing config
5. Build observability dashboards for scheduling decisions
