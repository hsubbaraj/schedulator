# Learnings from Dicer: Application to GPU Scheduling

This document summarizes architectural and algorithmic insights from [Dicer](https://github.com/databricks/dicer) (Databricks' Auto-Sharder) and evaluates how these patterns can be applied to the **Schedulator** GPU placement and scaling problem.

## 1. Core Algorithm Architecture

Dicer's Assigner uses a multi-phase control loop to maintain a "Global Assignment." Unlike a static scheduler, it treats placement as a **continuous optimization problem**.

### Part A: Dynamic Slicing (The "What")
Dicer partitions a key-space into **Slices** (contiguous ranges).
- **Split on Pressure:** When a slice's load (QPS/CPU) exceeds a threshold, it is split. If a single key is hot, it is isolated into a "point slice" for independent replication.
- **Merge on Cold:** Adjacent cold slices are merged to reduce management overhead.
- **Application to GPU Scheduling:** 
    - In LLM inference, the "Slice" is not just a replica, but the **KV Cache state** associated with a range of request keys (e.g., Session IDs or Model IDs).
    - If Schedulator becomes "session aware," it could use Dicer-like logic to split/merge KV-cache responsibilities across the fleet.

### Part B: Greedy Local Search Assignment (The "Where")
Dicer uses a **Penalty-Based Objective Function** rather than a single-pass scoring system.
- **The Objective Function:** It calculates a "Global Penalty Score" based on:
    - **Overload:** Penalizing pods operating above capacity.
    - **Churn:** High penalty for moving a slice (protects local cache).
    - **Fragmentation:** Penalizing uneven distribution.
- **Greedy Optimization:** It doesn't solve for "Perfect" in one go. It iteratively proposes operations (Move, Replicate, Dereplicate), calculates the delta to the Global Penalty, and executes the most beneficial one.

---

## 2. Applying Dicer Patterns to GPU Placement

While Schedulator is focused on hardware (GPUs) and Dicer is focused on software (Keys), the following Dicer patterns offer high value for our implementation:

### 1. Penalty-Based Rebalancing (from Assignment Algorithm)
Current Schedulator rebalancing uses a simple `MIN_FRAGMENTATION_DELTA`. We can improve this by adopting Dicer’s **Churn vs. Benefit** penalty model.
- **Schedulator Migration Penalty:** 
    - Moving an 8-GPU replica has a high "Churn Cost" (10-minute cold start).
    - If the "Fragmentation Benefit" (freeing a contiguous 8-GPU block) outweighs the "Churn Cost," the migration is triggered.
- **Implementation:** Use a local search in the `Rebalancing Engine` to evaluate if moving Replica A to Node B reduces the "Global Fragmentation Penalty" enough to justify the "Startup Time Penalty."

### 2. The "Deallocation Phase" Pattern
Dicer has a dedicated phase to move slices off resources marked for shutdown *before* considering load balance.
- **Schedulator Application:** When a K8s node enters `Cordoned` or `Draining` state, Schedulator should immediately trigger a "Migration Operation" that treats the node's capacity as negative-infinity. This ensures proactive migration before the K8s `SIGTERM` actually hits the pod.

### 3. Asymmetric Replication
Dicer allows "Asymmetric Replication" (replicating hot keys more than cold keys).
- **Schedulator Application:** For LLM workloads, we can asymmetrically scale applications based on their **SLA Tier**. A P0 application might have its "target_replica_count" computed not just on current QPS, but on a "Risk Penalty" where being under-provisioned is 100x more costly than being over-provisioned.

### 4. Convergence via Time-Boxing
Dicer’s `PLACEMENT_TIMEOUT` (30s) ensures the algorithm doesn't hang on complex optimization.
- **Schedulator Application:** Our Placement Engine should implement a similar timeout. If the "Optimal Bin-Packing" search takes too long, it should fall back to the "Next Best" valid placement to maintain the <100ms scheduling latency requirement.

## 3. The "State Transfer" Insight
Dicer is working on **State Transfer** (moving key values between pods during reassignments).
- **GPU Context:** This is the "Holy Grail" for LLM inference. If we can coordinate with the vLLM engine to "transfer" KV-cache blocks from an old replica to a new replica during a Schedulator Migration, we could eliminate the 10-minute "cold start" penalty that currently makes rebalancing dangerous.

## 4. Summary Recommendation

| Dicer Concept | Schedulator Application | Priority |
| :--- | :--- | :--- |
| **Penalty Function** | Use to quantify "Worth the disruption" for rebalancing. | **High** |
| **Greedy Local Search** | Use in the Rebalancing Engine to find better packing. | **Medium** |
| **Point Slicing** | Isolate "Hot Models" into dedicated clusters. | **Low** |
| **Deallocation Phase** | Proactive migration on `NodeCondition: DiskPressure` or `Cordoned`. | **High** |

**Conclusion:** We should not replace Schedulator's Go-based placement with Dicer's Scala-based sharding. Instead, we should **port the Penalty-based Optimization model** into our `rebalancing` and `placement` engines to handle the trade-offs between packing efficiency and migration disruption.

## 5. Future Research Reference

For future deep-dives into the Dicer (Slicer) assignment logic, the following paths in the Dicer repository are most critical:

### Core Algorithm Logic
- `dicer/assigner/algorithm/src/Algorithm.scala`: The main orchestrator of the multi-phase control loop. Start here to see the ordering of Split -> Merge -> Deallocate -> Place -> Constraints.
- `dicer/assigner/algorithm/src/PlacementPhase.scala`: The "heart" of the assignment logic. Implements the greedy local search and the delta calculation for the objective function.
- `dicer/assigner/algorithm/src/ConstraintPhase.scala`: Detailed implementation of hard-constraint enforcement (e.g., replica spreading, minimum counts).
- `dicer/assigner/algorithm/src/MutableAssignment.scala`: The primary data structure used during optimization; useful for understanding how Dicer represents the "World State" during a calculation.

### Slicing and Load Management
- `dicer/assigner/algorithm/src/Splitter.scala`: Logic for when and where to split a range based on load signals or hot-key detection.
- `dicer/assigner/algorithm/src/MergePhase.scala`: Logic for consolidating cold ranges to reduce metadata overhead.
- `dicer/assigner/algorithm/src/LoadMap.scala`: How Dicer aggregates and normalizes raw signals from pods into actionable load metrics for the algorithm.

### Testing and Validation
- `dicer/assigner/algorithm/test/`: Contains table-driven tests for the algorithm phases. These are excellent for understanding edge cases in rebalancing and shard movement.
- `dicer/demo/src/`: Sample application code showing how the "Slicelet" and "Clerk" libraries interact with the assignments generated by the algorithm.
