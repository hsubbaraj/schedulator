# Schedulator

A global multi-cluster GPU scheduler for LLM inference workloads.

## Overview

Schedulator manages a fleet of Kubernetes clusters across multiple regions to optimize placement and scaling of LLM inference replicas. It balances SLA compliance (latency/throughput) with high GPU utilization and handles priority-based preemption.

## Development

### Prerequisites

- Go 1.22+
- Docker
- Kind (Kubernetes in Docker)
- Make

### Building

```bash
make build
```

### Running Unit Tests

```bash
make test
```

### Running Integration Tests

Integration tests spin up a local Kind cluster and use KWOK (Kubernetes WithOut Kubelet) to simulate GPU nodes.

```bash
make test-integration
```

## Integration Test Scenarios

The integration suite (`test/integration/`) verifies the end-to-end behavior of the scheduler against a real Kubernetes API.

### 1. Scale-Up (`TestEndToEnd_ScaleUp`)
**Scenario:** A high-priority application is under heavy load.
- **Setup:** A single cluster with 3 nodes (24 GPUs total). One seed replica is running.
- **Trigger:** vLLM metrics report high queue depth (20 requests waiting).
- **Behavior:** The Scaling Engine computes a new target (e.g., 5 replicas). The Placement Engine finds space on available nodes. The Executor creates new Deployments.
- **Verification:** Assert that the number of Deployments for the app increases.

### 2. Scale-Down (`TestEndToEnd_ScaleDown`)
**Scenario:** Traffic drops for an application.
- **Setup:** An application has 3 replicas running, but metrics show low utilization (queue=0, batch=0.1).
- **Trigger:** The Scaling Engine detects sustained low utilization and recommends scaling down to 1 replica.
- **Behavior:** The Placement Engine selects victims (preferring fragmented nodes). The Executor deletes the specific Deployments.
- **Verification:** Assert that the number of Deployments decreases but stays at `min_replicas`.

### 3. Preemption (`TestEndToEnd_Preemption`)
**Scenario:** A P0 (highest priority) app needs capacity, but the cluster is full of P2 (lowest priority) workloads.
- **Setup:** Cluster is fully allocated to P2 replicas. A new P0 app is submitted.
- **Trigger:** Placement Engine fails to find free space.
- **Behavior:** The Preemption Engine identifies P2 victims. The Executor terminates them (with a grace period) and schedules the P0 replicas in their place.
- **Verification:** Assert P2 replicas are deleted and P0 replicas are running.

### 4. Rebalancing (`TestEndToEnd_Rebalancing`)
**Scenario:** Cluster is fragmented (many nodes with few replicas), preventing large models from scheduling.
- **Setup:** Replicas are scattered across multiple nodes.
- **Trigger:** The Rebalancing Engine detects an opportunity to consolidate replicas onto fewer nodes.
- **Behavior:** A migration plan is generated: create a new replica on a packed node, wait for it to be ready, then terminate the old replica (Blue-Green).
- **Verification:** Assert that the fragmentation score improves and total replica count remains constant during the transition.

### 5. Cache Affinity (`TestEndToEnd_CacheAffinity`)
**Scenario:** Two clusters are available; one has the model weights cached on NVMe.
- **Setup:** Cluster A has cached model; Cluster B does not.
- **Trigger:** Scale-up requested.
- **Behavior:** The Placement Engine scores Cluster A higher due to cache affinity.
- **Verification:** Assert that new replicas are scheduled on Cluster A.
