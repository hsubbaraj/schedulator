# Problem Statement: Multi-Cluster GPU Scheduler for LLM Inference

## 1. Context

We operate a fleet of Kubernetes clusters across multiple regions (e.g., `us-west-1`, `eu-central-1`). Each cluster has a fixed number of nodes, and each node has 8 GPUs. These clusters serve LLM inference workloads for multiple applications with varying traffic patterns, latency requirements, and priorities.

## 2. Definitions

| Term | Definition |
|------|------------|
| **Application** | A logical deployment of a model, consisting of one or more replicas distributed across clusters. Each application has its own GPU requirement per replica, SLA targets, priority tier, and minimum replica constraints. |
| **Replica** | A single instance of an application running vLLM for inference. A replica runs on exactly one node and consumes 1, 2, 4, or 8 GPUs. |
| **SLA** | Per-application latency and throughput targets, defined as P99 time-to-first-token (TTFT) and P99 tokens-per-second (TPS). |
| **Priority Tier** | A static ranking (e.g., P0 > P1 > P2) determining preemption order when GPU capacity is insufficient. |

## 3. Problem Statement

**Design a global scheduler that continuously determines, for each application: (1) how many replicas are needed to meet SLA under current traffic, and (2) where those replicas should be placed across the cluster fleet—while maximizing GPU utilization, respecting application constraints, and preempting lower-priority workloads when necessary.**

## 4. Functional Requirements

### 4.1 Scaling Decisions

The scheduler must compute the target replica count for each application based on:

- Current traffic signals (provided by an external gateway)
- Per-application SLA definitions (P99 TTFT, P99 TPS)
- Per-replica performance characteristics (from an offline profiling service): tokens/sec, TTFT, E2E latency, startup time (warm and cold)

### 4.2 Placement Decisions

The scheduler must assign each replica to a specific cluster and node, respecting:

- **GPU topology:** A replica requiring N GPUs must land on a single node with N contiguous free GPUs
- **Application constraints:** Minimum replica counts (global), failure domain spreading rules (e.g., "at least 1 replica in 2 different clusters")
- **Cache affinity:** Prefer nodes/clusters where model weights are already cached (cache state provided by an external service)

### 4.3 Preemption

When GPU capacity is insufficient to satisfy higher-priority applications:

- The scheduler may preempt individual replicas of lower-priority applications
- Preempted replicas receive a graceful drain period (default: 10s, configurable) to complete in-flight requests
- The scheduler must not reduce any application below its defined minimum replica count, unless no other option exists

### 4.4 Rebalancing

The scheduler may proactively migrate replicas to improve packing efficiency, provided:

- The migration does not cause SLA breaches
- The improvement justifies the disruption (a configurable delta threshold)

## 5. Control Loop

The scheduler operates in a hybrid event-driven + polling mode:

**Events that trigger scheduling evaluation:**

- New application onboarded
- Application deleted or disabled
- Traffic signal indicates scaling is needed (up or down)
- SLA breach detected
- Node or cluster becomes unavailable
- Node or cluster recovers

**Scheduled evaluation:**

- Periodic re-evaluation (e.g., every 60s) even if no events arrive, to catch drift and optimize packing

## 6. External Dependencies

The scheduler assumes the existence of:

| Dependency | Provides |
|------------|----------|
| **Cluster Aggregator** | Unified view of pods, nodes, and GPU allocations across all clusters |
| **Cache Registry** | Current state of which models are cached on which clusters (shared storage) or nodes (local disk) |
| **Performance Profiler** | Offline-computed metrics per application: tokens/sec, TTFT, E2E latency, cold-start time, warm-start time |
| **Traffic Gateway** | Real-time traffic signals per application (format TBD by solution design) |

## 7. Constraints & Assumptions

- **Fixed infrastructure:** Number of clusters, nodes per cluster, and GPUs per node (8) are static
- **Single-node replicas only:** Multi-node / disaggregated serving is out of scope
- **No scale-to-zero:** Every application maintains at least its defined minimum replicas
- **No cost optimization (v1):** Cluster/region cost differences are not modeled
- **No traffic routing:** The scheduler does not control request routing; an external gateway handles this

## 8. Success Metrics

| Metric | Definition | Target |
|--------|------------|--------|
| **GPU Utilization** | (Allocated GPUs) / (Total GPUs) across fleet | Maximize |
| **Fragmentation Score** | 1 - (Largest schedulable block per node) / (Free GPUs per node), aggregated | Minimize |
| **SLA Compliance** | % of applications meeting P99 TTFT and TPS targets | > 99% |
| **Scheduling Latency** | P99 time to compute a placement decision | < 100ms |
| **Preemption Rate** | Replicas preempted per hour | Minimize (track, no hard target) |
| **Scale-up Latency** | Time from "need more replicas" signal to replica serving traffic | Track (depends on cold/warm start) |

## 9. Open Design Questions

1. **Traffic signal schema:** What format should the gateway provide—raw QPS, queue depth, observed latencies, or a normalized demand score?
2. **Rebalancing policy:** How do we quantify "worth the disruption"? What's the delta threshold formula?
3. **Preemption cascades:** If preempting a P2 app still isn't enough, do we preempt P1? What's the escalation policy?
4. **Startup time in SLA math:** When scaling up, how do we account for cold-start delay in SLA compliance calculations?
5. **Scheduling architecture:** Should the scheduler be fully global (decides both cluster and node placement) or tiered (global scheduler decides cluster placement and replica counts, then delegates node-level bin-packing to an in-cluster scheduler like Volcano or a custom kube-scheduler plugin)? Tradeoffs include:
   - *Global:* Full visibility enables optimal packing, but requires the scheduler to track node-level state across all clusters and increases decision complexity
   - *Tiered:* Simpler global logic, leverages existing in-cluster schedulers, but may result in suboptimal packing if the global scheduler lacks visibility into node-level fragmentation when making cluster assignments
