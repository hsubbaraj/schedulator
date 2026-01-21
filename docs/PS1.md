# Problem Statement: Multi-Cluster GPU Orchestration for Tiered AI Services

## 1. Context: The Provider-Scale Infrastructure Challenge

In a high-scale AI Platform-as-a-Service (PaaS) environment (e.g., Google Vertex AI, Baseten), infrastructure must manage a global fleet of heterogeneous GPU clusters (NVIDIA H100, A100, L40). Unlike single-purpose clusters, this environment must reconcile four fundamentally different classes of compute demand:

### A. Dedicated Real-Time Inference
*   **Nature:** Single-tenant, always-on instances for specific enterprise customers.
*   **SLA:** Zero-tolerance for preemption; strict latency guarantees.
*   **Challenge:** These create "capacity islands" that often sit idle, requiring the scheduler to manage reserved but under-utilized blocks.

### B. Serverless Real-Time Inference 
*   **Nature:** Multi-tenant, bursty API traffic (e.g., public Llama-3 endpoints).
*   **SLA:** High availability with volatile demand.
*   **Challenge:** Requires aggressive Autoscaling. The scheduler must decide where to scale out based on which clusters have the model weights pre-loaded ("Warm Pools") to avoid catastrophic cold-start latencies.

### C. Batch Inference Jobs
*   **Nature:** Offline, high-throughput processing (e.g., bulk document summarization).
*   **SLA:** Flexible completion windows; high tolerance for preemption.
*   **Challenge:** These serve as the system "slack," backfilling idle GPUs to maximize ROI, but they must be evicted instantly when real-time traffic spikes.

### D. Training & Fine-Tuning 
*   **Nature:** Multi-node, gang-scheduled, long-running jobs.
*   **Challenge:** High topology sensitivity; often treated as a background load that requires contiguous blocks of GPUs.

## 2. The Core Problem

How do we build a global orchestration layer that dynamically autoscales volatile serverless traffic and honors dedicated reservations, while using batch workloads to maximize fleet-wide "Goodput" without violating real-time SLAs?

Standard Kubernetes scheduling fails at this scale because it lacks a **Multi-Class Priority Model** and a global view of **Weight Locality**. It cannot intelligently "preempt" a batch job in Region A to make room for a serverless burst in Region B based on where model weights are already cached.

## 3. Specific Challenges (The "Staff" Perspective)

### A. The "Goodput" vs. Utilization Trap
Raw GPU utilization is a vanity metric. A GPU "reserved" by a container but waiting 10 minutes for a 150GB model weight pull is 100% utilized but provides zero Goodput.
*   **Requirement:** The scheduler must prioritize **Weight-Aware Placement** to minimize "Cold Starts."

### B. State Staleness
In a multi-region deployment, the Global Scheduler’s view of cluster capacity is always slightly stale (1s–10s lag).
*   **The Risk:** "Scheduling Collisions," where multiple placement decisions target the same "free" GPU simultaneously, leading to high tail latency and failed pod starts.
* Is a global scheduler or a tiered scheduler better here? Also have to consider the startup times of the models vs the scheduling dicision time.

### C. Strategic Fragmentation
A naive "Bin-Packing" algorithm might fill an 8-GPU node with eight 1-GPU batch jobs. If a Dedicated customer suddenly requests an 8-GPU instance, the system must either wait for eight evictions or fail the request.
*   **Requirement:** The scheduler must perform "Strategic Fragmentation," intentionally leaving high-topology nodes open for multi-GPU workloads.

### D. The "Fast-Evict" Preemption Storm
When Serverless traffic spikes, the scheduler must evict Batch jobs. If not managed carefully, this triggers a "re-scheduling storm" where evicted batch jobs constantly hunt for new holes, creating massive control-plane overhead.

## 4. The Required Solution: "The Schedulator"

To solve this without risking production stability, we require a **Simulation & Replay Framework** that allows for "Plug & Play" algorithm testing.

### Simulator Capabilities:
*   **Workload Injection:** Realistic simulation of "Burst" profiles (Serverless), "Sticky" reservations (Dedicated), and "Deep Queues" (Batch).
*   **State Latency Modeling:** Injecting variable delays in cluster state updates to test algorithm robustness against staleness.
*   **Weight Awareness:** Tracking the "warmth" (cache status) of model weights across the global fleet.
*   **Chaos Injection:** Simulating node failures and network partitions to measure auto-recovery speed.

## 5. Success Metrics

Any proposed scheduling solution must be evaluated against this balanced scorecard:

| Metric | Definition | Importance |
| :--- | :--- | :--- |
| **SLA Attainment** | % of Dedicated/Serverless requests meeting latency targets. | **Critical (The Floor)** |
| **System Goodput** | Total tokens generated / Total GPU hours reserved. | **Economic (The Ceiling)** |
| **Scaling Lag** | P99 time from "Autoscale Trigger" to "First Token." | Measures Weight-Awareness. |
| **Preemption Regret** | Total compute hours lost due to evicted batch jobs. | Measures Stability/Jitter. |
| **Conflict Rate** | % of placement decisions rejected due to stale state. | Measures Control Plane Health. |

---

**Next Step:** Would you like me to begin defining the technical API interfaces for the simulator, specifically how we define the `ResourceSnapshot` and the `PlacementRequest` objects for these four tiers?