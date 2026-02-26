# Feature: Unified Placement & Rebalancing Solver

## 1. Executive Summary
This document proposes transitioning Schedulator's placement, preemption, and rebalancing logic from a sequential, procedural heuristic model to a **Unified Optimization Solver** using Mixed-Integer Programming (MIP). This approach solves the "Where does this go?" problem holistically, ensuring mathematical optimality while maintaining sub-second control loop latency through time-boxed execution and incremental solving.

---

## 2. Analysis of Current Heuristic Components

### 2.1 Complexity & Scalability
The current implementation utilizes a procedural approach where engines run in sequence. Our analysis reveals that the primary bottleneck is the "un-indexed" nature of the `WorldStateSnapshot`, leading to redundant linear scans of the global replica list.

| Component | Time Complexity (Current) | Space Complexity | Primary Bottleneck |
| :--- | :--- | :--- | :--- |
| **Scaling** | $O(A 	imes R)$ | $O(A)$ | Redundant global replica scans ($5	imes$ per app). |
| **Placement** | $O(A 	imes (R + C 	imes N))$ | $O(A + C 	imes N)$ | Linear scan of $R$ for current count + Cluster/Node scoring. |
| **Rebalancing** | $O(C 	imes N 	imes (R + C 	imes N))$ | $O(C 	imes N)$ | Nested scans for fragmented nodes and destination clusters. |

*Notation: $A$ = Applications, $R$ = Replicas, $C$ = Clusters, $N$ = Nodes per Cluster.*

### 2.2 Functional Limitations
1.  **Greedy Myopia:** The placement engine makes the best decision for the *current* replica without considering if a slight shift in existing replicas would free up better capacity (e.g., an 8-GPU block).
2.  **Sequential Lag:** Because Rebalancing runs *after* Placement, the system may fail to place a P0 app in cycle $T$, only for the Rebalancer to free up that exact space in cycle $T+1$.
3.  **Complex Constraints:** As we add failure domains (power circuits, racks, regions), the procedural "if/else" logic becomes brittle and hard to verify.

---

## 3. Solver Exploration: GLOP vs. CP-SAT

We evaluated two primary approaches from the Google OR-Tools ecosystem:

### 3.1 Google GLOP (Linear Programming)
*   **Strengths:** Extremely fast; solves continuous problems ($x \in [0, 1]$).
*   **Weaknesses:** Cannot handle the "all-or-nothing" nature of GPU pods. You cannot place 0.5 of a pod on Node A and 0.5 on Node B.
*   **Use Case:** Ideal for high-level fleet distribution (e.g., "What percentage of traffic should go to Region X?").

### 3.2 Google CP-SAT / SCIP (Mixed-Integer Programming)
*   **Strengths:** Handles integer constraints ($x \in \{0, 1\}$). Perfectly models pod-to-node mapping.
*   **Weaknesses:** NP-Hard. Execution time can grow exponentially with problem size.
*   **Use Case:** **Recommended for Schedulator.** By applying time-boxing (e.g., 200ms limit), CP-SAT returns the "Best Feasible" solution, which is almost always superior to a greedy heuristic.

---

## 4. Formal Optimization Problem Formulation

We define a unified problem that combines **Placement**, **Rebalancing**, and **Preemption** into a single objective.

### 4.1 Decision Variables
*   $x_{r,n} \in \{0, 1\}$ : 1 if replica $r$ is assigned to node $n$, else 0.
*   $u_a \in \mathbb{Z}^+$ : Unmet demand for application $a$ (Target - Allocated).
*   $p_r \in \{0, 1\}$ : 1 if existing replica $r$ is preempted.
*   $m_r \in \{0, 1\}$ : 1 if existing replica $r$ is migrated (moved to a different node).

### 4.2 Objective Function
Minimize the total cost $J$:

$$J = 	ext{Cost}_{	ext{Unmet}} + 	ext{Cost}_{	ext{Preemption}} + 	ext{Cost}_{	ext{Churn}} + 	ext{Cost}_{	ext{ColdStart}} - 	ext{Benefit}_{	ext{Packing}}$$

$$J = \sum_{a \in A} (W_{	ext{unmet}, a} \cdot u_a) + \sum_{r \in R_{	ext{exist}}} (W_{	ext{preempt}, a} \cdot p_r) + \sum_{r \in R_{	ext{exist}}} (W_{	ext{churn}} \cdot m_r) + \sum_{r \in R, n \in N} (W_{	ext{cold}, r, n} \cdot x_{r, n}) - \dots$$

### 4.3 Proposed Weights (Constants)
To align with the project's priorities (SLA > Cache > Packing), we propose the following default weights:

| Weight Name | Default Value | Rationale |
| :--- | :--- | :--- |
| $W_{	ext{unmet}, P0}$ | $1,000,000$ | P0 apps must be satisfied at almost any cost. |
| $W_{	ext{unmet}, P1}$ | $100,000$ | P1 apps are secondary to P0. |
| $W_{	ext{preempt}, P2}$ | $50,000$ | Cost of killing a P2 app to make room. |
| $W_{	ext{churn}}$ | $5,000$ | Penalty for moving an existing replica (Migration). |
| $W_{	ext{cold}}$ | $0 	o 10,000$ | Variable based on cache tier: GPU_MEM=0, NVME=2k, REMOTE=10k. |
| $W_{	ext{packing}}$ | $100$ | Small reward for using nodes with fewer free GPUs (Bin-packing). |

---

## 5. Constraints

1.  **GPU Capacity:** For every node $n$, the sum of GPUs used by assigned replicas must not exceed node capacity.
    $$\sum_{r} (x_{r,n} \cdot 	ext{GPUs}_r) \leq 	ext{Capacity}_n$$
2.  **Contiguity:** Each replica $r$ must be assigned to exactly one node $n$.
    $$\sum_{n} x_{r,n} = 1 - p_r$$
3.  **Minimum Replicas:** Preemption cannot reduce an application below its `min_replicas`.
    $$\sum_{r \in R_a} (1 - p_r) \geq 	ext{MinReplicas}_a$$
4.  **Failure Domains:** Spread constraints enforced via sums across cluster/rack labels.

---

## 6. Practical Implementation Guidelines

### 6.1 Incremental Solving & Stability
To avoid "Fleet Jitter," we use **Warm Starting**. The solver is provided with the current placement as a starting point. The $W_{	ext{churn}}$ penalty ensures the solver only moves a replica if the benefit (e.g., satisfying a P0 app or improving cache warmth) exceeds the cost of migration.

### 6.2 The Hybrid Control Loop
1.  **Scaling Engine (Procedural):** Remains as is. It computes the `TargetCount` for each app.
2.  **Optimizer Engine (Solver):**
    *   Takes `TargetCounts` and `WorldState`.
    *   Constructs the MIP model.
    *   Sets a **200ms time limit**.
    *   Translates the result into `ScaleUp`, `ScaleDown`, `Preempt`, and `Migrate` decisions.
3.  **Plan Executor:** Sequences the decisions (Preempt $	o$ Migrate $	o$ ScaleUp).

---

## 7. Performance Expectation
For a fleet of 100 apps and 500 nodes, the search space is large but highly constrained. Modern solvers like CP-SAT can find a solution within 10% of global optimality in **< 300ms** for problems of this scale, which is well within our control loop requirements.

---

## 8. Open Concerns & Prerequisites

This section documents unresolved concerns that must be addressed before implementation begins.

### 8.1 Latency Bound is Unsubstantiated

Section 7 asserts "< 300ms for 100 apps and 500 nodes" without a benchmark. The claim depends heavily on problem structure. CP-SAT is NP-Hard; the 200ms time-box returns "best found so far," which under high constraint pressure may be the trivially feasible initial solution with no improvement over the current heuristic. The failure scenario — a full cluster going down, triggering mass preemption — is exactly when fast decisions are most critical and exactly when the MIP problem is most constrained. **A benchmark on realistic problem instances (including a simulated cluster failure) must be run before this design is committed to.**

### 8.2 Migration is Modeled Incorrectly

The decision variable $m_r \in \{0, 1\}$ treats migration as instantaneous. In reality, migration is blue-green: a new replica is started, confirmed running, and only then is the old replica deleted. During the intermediate state, both replicas consume GPU capacity simultaneously. The solver's capacity accounting does not model this, so it may approve migrations that are individually feasible but collectively exhaust cluster capacity when inflight. The existing `RebalancingEngine` handles this correctly by treating migration as two separate ordered operations. **The formulation must either model two-phase migration correctly or restrict $m_r$ to be handled outside the solver, as it is today.**

### 8.3 Weight Calibration Has No Feedback Loop

The proposed weights span six orders of magnitude ($1{,}000{,}000$ to $100$). These values encode priorities but are not derived from measurement. As fleet composition changes — adding apps, changing GPU block sizes, adjusting `min_replicas` — the correct weights may shift. There is no proposed mechanism for detecting that weights are producing suboptimal outcomes, nor a process for tuning them. With the heuristic, a bad placement decision is traceable to a scoring function (e.g., "cache score of cluster-A was 1000 vs. cluster-B's 500"). With MIP, the explanation is "the solver found this globally optimal assignment given these weights," which is correct but operationally undebuggable. **A weight validation strategy — ideally using the simulator's scorecard as a feedback signal — should be defined before weights are treated as production constants.**

### 8.4 Test Precision is Weakened

The current heuristic engines are fully deterministic: given a fixed `WorldStateSnapshot`, they produce the same placement every time. This enables precise table-driven tests asserting exact replica-to-cluster assignments. CP-SAT output depends on the solver's internal search state; while it can be made deterministic via a fixed `random_seed`, test assertions must shift from "replica R goes to cluster C" to "all constraints are satisfied." This is a weaker guarantee and makes behavioral regressions harder to detect. **Test strategy for the solver — including what a meaningful constraint-satisfaction test looks like for preemption and spread cases — should be designed before implementation.**

### 8.5 The O(R) Scan Problem Should Be Fixed First, Independently

Section 2.1 correctly identifies redundant linear replica scans as the primary performance bottleneck. This problem exists regardless of whether the solver is adopted; in fact, building an efficient MIP model requires the same indexed data structures. Indexing replicas by `AppID` in `WorldStateSnapshot` is a self-contained change that eliminates the bottleneck, improves the heuristic path, and is a prerequisite for efficient solver model construction. **This should be implemented as a standalone change before any solver work begins.**

### 8.6 Recommended Sequencing

1. Add `ReplicasByApp map[model.AppID][]model.Replica` index to `WorldStateSnapshot` and eliminate all `O(R)` scans.
2. Fix sequential lag by computing rebalancing candidates before placement and exposing releasable capacity to `findBestCluster`.
3. Benchmark CP-SAT on the simulator's `cluster_failure` and `preemption` scenarios to validate the 200ms latency claim.
4. Resolve the blue-green migration modeling gap.
5. Define a weight validation strategy using the simulator scorecard.
