# Project Status & Next Steps

## User Intent & Vision

The primary goal is to move beyond unit tests and mocked interactions to a **"Live Experience"** of the Schedulator. The user wants to run the system against a real (or simulated) Kubernetes environment and manually trigger scenarios to observe the scheduler's behavior in real-time.

Specifically, the user wants to:
1.  **Visualize the Fleet:** Have a dashboard that shows the current state of all clusters, nodes, and pods, along with the scheduler's decisions (scaling, placement, preemption).
2.  **Manually Interact:** Be able to use standard tools like `kubectl` to modify the environment (e.g., delete a node, scale a deployment) or inject new workloads and see the scheduler react instantly.
3.  **Verify Behavior:** Confirm that the system correctly handles edge cases—like a cluster going down or a sudden traffic spike—by observing the dashboard updates and the resulting cluster state changes.

This is distinct from the **Simulator**, which is an offline tool for running high-speed algorithmic tests. The user's focus is on the **Live Schedulator Daemon** running against a Kind/KWOK cluster.

## Current Component Status

The system's "Brain" (Engines) and "Arms" (Executor) are built and tested, but its "Eyes" (Aggregator) are currently mocked.

| Component | Status | Notes |
| :--- | :--- | :--- |
| **Scaling Engine** | ✅ Ready | Logic for queue/KV cache scaling is implemented and tested. |
| **Placement Engine** | ✅ Ready | Logic for bin-packing and cache affinity is implemented and tested. |
| **Preemption Engine** | ✅ Ready | Logic for priority-based eviction is implemented and tested. |
| **Executor** | ✅ Ready | Real `K8sClient` implementation exists to create/delete Deployments in Kind. |
| **Cluster Client** | ✅ Ready | Wraps `client-go` to interact with K8s API. |
| **Cluster Aggregator** | ❌ **Mocked** | **Critical Missing Piece.** No real implementation to watch K8s events (Nodes/Pods) or fetch vLLM metrics. The system cannot "see" the cluster state automatically. |
| **Config Store** | ❌ **Mocked** | No real implementation to load Application configs (e.g., from CRDs or YAML). |
| **API / Dashboard** | ❌ **Missing** | No HTTP API to expose `WorldState` to a frontend. No frontend application exists. |
| **Simulator** | ❌ **Missing** | Planned but not yet implemented. Useful for offline testing, but distinct from the "live" manual experience. |

## Gap Analysis: The "Live Experience" Missing Pieces

To achieve the vision of a system that reacts to `kubectl delete node`, we must bridge the gap between the mocked interfaces and the real world.

### 1. The Missing "Eyes" (Cluster Aggregator)
Currently, the `ClusterAggregator` is only defined as an interface with a mock implementation. This means the Schedulator has no way of knowing when a node is added, removed, or changes status in the Kubernetes cluster. It also cannot see the vLLM metrics (queue depth, etc.) required for scaling decisions.

In the current state, running the Schedulator binary would result in an empty World State, as it has no mechanism to ingest data from the Kind/KWOK clusters. To fix this, we need a real `K8sAggregator` that uses Kubernetes Informers to watch for `Node` and `Pod` events and feed them into the ingestion pipeline.

### 2. The Missing "Interface" (Dashboard & API)
Even if the system were running and making decisions, there is currently no way for the user to *see* what it is doing. The `WorldState` is locked inside the process memory.
*   **API:** We need to expose the internal `WorldState` via a read-only HTTP API (e.g., `GET /api/v1/clusters`) so external tools can query it.
*   **Dashboard:** We need a frontend (React/Next.js) that consumes this API to render the "Fleet Overview" and "Node Tetris" views described in the design doc.

### 3. The Missing "Configuration" (Config Store)
The system currently mocks the `ConfigStore`, which holds the definitions of Applications (SLA, priority, model ID). To support "Injecting a New Application" dynamically, we need a real implementation—likely reading from a local YAML file or watching Custom Resource Definitions (CRDs) in the cluster.

## Recommended Next Steps

### Phase 1: Build the "Eyes" (Aggregator)
The immediate priority is to let the Schedulator see the cluster.
*   **Implement `internal/k8saggregator`:** Create a concrete implementation of `ports.ClusterAggregator`.
*   **Watch Logic:** Use `client-go` Informers to watch `Nodes` and `Pods` and convert them into `ClusterEvents`.
*   **Metrics Fetching:** Implement a simple poller that can fetch (or mock) vLLM metrics for running apps.

### Phase 2: Build the "Configuration" (Config Store)
*   **Implement `internal/configstore`:** Create a file-based or CRD-based store to load `Application` structs.
*   **Hot Reload:** Ensure that modifying the config file (or CRD) triggers an update event in the Ingester.

### Phase 3: Build the "Interface" (API & Dashboard)
*   **Expose API:** Add HTTP handlers in `cmd/schedulator/main.go` to serve JSON snapshots of the `WorldState`.
*   **Develop Frontend:** Build the React/Next.js dashboard to visualize the fleet and operations in real-time.

### Phase 4: Persistent History (Auditability)
*   **Structured Logging:** Implement the SQLite-based event logger to track *why* decisions were made (e.g., "Scaled up due to queue depth > 10").
*   **History View:** Add a "History" tab to the dashboard to replay past events.
