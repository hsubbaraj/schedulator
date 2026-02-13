# Section 14 — Integration Tests (Pre-Impl Doc)

## Overview

End-to-end integration tests that wire up real engines and execute against kind+KWOK clusters, verifying the full scheduling pipeline produces correct K8s state.

## Key Design Decision: statusOverrideClient

KWOK nodes have `NoSchedule` taint and no KWOK controller, so pods stay `Pending` forever. The `statusOverrideClient` wraps the real `k8sclient.Client` and overrides `GetReplicaStatus` to return `Running` for created replicas, allowing the executor's `waitForRunning` to complete immediately.

## Types

### statusOverrideClient

```go
type statusOverrideClient struct {
    inner   ports.ClusterClient
    mu      sync.Mutex
    created map[model.ReplicaID]bool
}
```

Delegates `CreateReplica`, `DeleteReplica`, `LabelReplica` to inner. Overrides `GetReplicaStatus`.

### engineStack

```go
type clusterSetup struct {
    ID        string
    Clientset kubernetes.Interface
}

type engineStack struct {
    WorldState  *worldstate.WorldState
    Scaling     *scaling.ScalingEngine
    Placement   *placement.PlacementEngine
    Preemption  *preemption.PreemptionEngine
    Rebalancing *rebalancing.RebalancingEngine
    PlanGen     *plangen.PlanGenerator
    Executor    *executor.Executor
    Clients     map[string]*statusOverrideClient
}
```

### Functions

- `newEngineStack(t *testing.T, clusters []clusterSetup) *engineStack`
- `(s *engineStack) runCycle(t *testing.T) model.ExecutionResult`
- `countDeploymentsOnCluster(t *testing.T, clientset kubernetes.Interface, appID string) int`
- `cleanupDeploymentsOnCluster(t *testing.T, clientset kubernetes.Interface)`

## Test Cases

1. **TestEndToEnd_ScaleUp** — high queue pressure triggers scale-up, verify K8s Deployments created
2. **TestEndToEnd_ScaleDown** — low utilization with stabilization met triggers scale-down, verify Deployments removed
3. **TestEndToEnd_Preemption** — P0 app preempts P2 when cluster full, verify victim removed and new app placed
4. **TestEndToEnd_Rebalancing** — fragmented replicas consolidated via migration
5. **TestEndToEnd_CacheAffinity** — 2 clusters, cache on c1, verify placement to c1
6. **TestEndToEnd_FailureDomainSpread** — 2 clusters, spread policy, verify 1 replica per cluster

## Multi-Cluster Setup

`testmain_test.go` creates 2 kind clusters: `schedulator-test` and `schedulator-test-2`. Each gets 3 KWOK nodes with 8 GPUs. Both are torn down in TestMain cleanup.

## Build Tag

All test files use `//go:build integration`.
