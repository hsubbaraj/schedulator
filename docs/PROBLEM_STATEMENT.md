# Problem Statement: Multi-Cluster GPU Scheduling for LLM Workloads

## 1. Context: The LLM Infrastructure Challenge
As organizations scale Large Language Model (LLM) inference and training, they increasingly operate across **multiple heterogeneous Kubernetes clusters** spread across different regions (e.g., `us-west-1`, `eu-central-1`) and cloud providers. These clusters contain expensive, scarce resources—specifically **GPUs** (NVIDIA H100s, A100s, L40s)—organized into specific node topologies (e.g., 1x, 4x, or 8x GPUs per node).

## 2. The Core Problem
**How do we optimally schedule incoming LLM workloads across a global fleet of clusters to maximize GPU utilization and minimize fragmentation, without risking production stability?**

Standard Kubernetes scheduling (`kube-scheduler`) operates at a **single-cluster level** and uses a "filter-then-score" approach that is often insufficient for:
1.  **Global Optimization:** It cannot see capacity across multiple clusters to make global load-balancing decisions.
2.  **Efficient Bin-Packing:** It often fragments resources (e.g., scheduling a 1-GPU workload on an 8-GPU node), preventing larger models (requiring 8 GPUs) from scheduling later, even if total free capacity exists.
3.  **Complex Constraints:** LLMs have strict topology requirements (e.g., "Need 8x H100s with NVLink").

## 3. Specific Challenges

### A. GPU Fragmentation (The "Tetris" Problem)
*   **Scenario:** A cluster has 16 free GPUs.
*   **Issue:** If those 16 GPUs are spread across 16 different nodes (1 free per node), a single 70B parameter model requiring 8 GPUs on a *single node* cannot be scheduled.
*   **Impact:** Low effective utilization despite high nominal availability. Expensive hardware sits idle.

### B. Added Complexity
In the future, there can be additional restraints on how to schedule the workloads:
*   **Model Preloading/Caching:** The model weights may already be preloaded on certain nodes or shared storage on a cluster, reduces latency for startup.
*   **Model startup time:** Each model will have a different startup time based on configuration, which will affect the scheduling decisions.
*   **Autoscaling:** As certain models receive more traffic, we will want to scale replicas of the model. This should be scheduled to take into account latency as well as redundancy in case a cluster goes down.
*   **Hardware Diversity:** Clusters contain a mix of generations (A100 vs H100) and interconnects (NVLink vs PCIe).
*   **Topology Constraints:** A workload might require `nvidia.com/gpu: 8` but also implicitly require them to be on the same physical host for bandwidth reasons. A scheduler must respect these constraints strictly.

### C. Multi-Objective Optimization
A scheduling algorithm must balance conflicting goals:
*   **Maximize Utilization:** Pack workloads tightly to free up space.
*   **Minimize Latency:** Place workloads near users or data.
*   **Maximize Reliability:** Spread replicas across failure domains if possible (ie spread replicas across multiple clusters if possible).
*   **Minimize Cost:** Prefer owned hardware over spot instances or expensive regions.

### D. The "Testing Gap"
*   **Production Risk:** You cannot test a new experimental scheduler (e.g., one based on Constraint Programming or Reinforcement Learning) on a live production cluster without risking downtime for critical services.
*   **Cost:** Spinning up hundreds of real GPU nodes for testing is prohibitively expensive.
*   **Time:** Real-time workloads take hours to run; waiting for real-world results is too slow for iteration.

## 4. The Required Solution
We need a **simulation framework ("Schedulator")** that allows us to:
1.  **Simulate** multiple clusters with realistic GPU node topologies (using fake nodes to save cost).
2.  **Replay** realistic workload scenarios (bursts, steady streams, heterogeneous model sizes).
3.  **Plug & Play** different scheduling algorithms (Random, Greedy/Best-Fit, CP-SAT Solver).
4.  **Measure** the performance of these algorithms apples-to-apples based on utilization, fragmentation, and decision latency.

The simulation framework needs clean APIs so that different solutions can be compared apples to apples. For example, it should be able to support an external controller scheduler that picks which cluster/node to schedule the workload on, as well as in-cluster schedulers such as Volcano or some custome scheduling logic that plays with kubernetes. 

## 5. Success Metrics for a Solution
Any proposed scheduling solution must be evaluated against:
*   **Global GPU Utilization:** Total allocated / Total capacity.
*   **Fragmentation Score:** Percentage of "wasted" free GPUs (free but unusable for N-GPU workloads).
*   **Scheduling Success Rate:** % of pods successfully placed within a deadline.
*   **Decision Latency:** Time taken to calculate a placement (must be < 100ms for online systems).

---
**Input for LLM:**
Use this problem statement to design specific scheduling algorithms (e.g., "Design a Greedy Best-Fit scheduler that minimizes fragmentation for 8-GPU blocks") or to refine the simulator's capabilities to better model these challenges.
