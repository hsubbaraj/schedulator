# Section 12 — Rebalancing Engine (Pre-Impl Doc)

## Overview

The Rebalancing Engine proactively consolidates fragmented GPU allocations via blue-green migration. It implements proposal Section 4.8. After all scale-ups/downs are computed, the rebalancing engine scans for replicas on partially-filled nodes that could be moved to tighter-packed destinations, producing `[]model.MigrateDecision` that the Plan Generator (Section 10) decomposes into paired scale-up + scale-down operations.

## Package

`internal/engine/rebalancing/`

## Types

```go
// config.go
type RebalancingConfig struct {
    MaxMigrationsPerCycle int     // default: 2
    MinFragmentationDelta float64 // default: 0.15
}

func DefaultRebalancingConfig() RebalancingConfig

// engine.go
type RebalancingEngine struct {
    cfg    RebalancingConfig
    tracer trace.Tracer

    migrationsCounter prometheus.Counter
    improvementHist   prometheus.Histogram
    skippedCounter    *prometheus.CounterVec // labels: reason
}

// scoring.go
type consolidationTarget struct {
    clusterID   model.ClusterID
    constraints model.SchedulingConstraints
}
```

## Function Signatures

```go
func NewRebalancingEngine(cfg RebalancingConfig, tracer trace.Tracer, reg prometheus.Registerer) *RebalancingEngine

func (e *RebalancingEngine) FindRebalancingOpportunities(ctx context.Context, snap worldstate.WorldStateSnapshot) []model.MigrateDecision

func (e *RebalancingEngine) findConsolidationTarget(replica model.Replica, app model.Application, snap worldstate.WorldStateSnapshot) (*consolidationTarget, bool)

func estimateFragmentationDelta(sourceNode model.Node, destCluster model.Cluster, gpus int, snap worldstate.WorldStateSnapshot) float64

func hasCachedModel(clusterID model.ClusterID, modelID model.ModelID, snap worldstate.WorldStateSnapshot) bool

func countRunningReplicas(appID model.AppID, snap worldstate.WorldStateSnapshot) int
```

## Algorithm (Proposal Section 4.8)

1. Start span `rebalancing.find_opportunities`.
2. Iterate `snap.Clusters`. Skip cluster if `FragmentationScore < cfg.MinFragmentationDelta`. Increment `skippedCounter("well_packed")`.
3. For each cluster, find fragmented nodes: `0 < node.AllocatedGPUs < node.TotalGPUs` and `node.Status == NodeStatusReady`.
4. For each replica on a fragmented node:
   a. Look up app. Count running replicas. If `runningCount <= app.MinReplicas`, skip (`min_replicas`).
   b. Call `findConsolidationTarget`. If nil, skip.
   c. Check `hasCachedModel`. If false, skip (`no_cache`).
   d. Call `estimateFragmentationDelta`. If `< cfg.MinFragmentationDelta`, skip (`low_delta`).
   e. Append `MigrateDecision`. If `len >= MaxMigrationsPerCycle`, return early.
5. Sort: higher Priority int first (lower business priority = safer to migrate). Tiebreaker by ReplicaID.
6. Truncate to `MaxMigrationsPerCycle`.

## Fragmentation Computation

`computeFragmentation(nodes) = 1.0 - totalFree/totalCapacity` (same as worldstate).

`estimateFragmentationDelta`:
- currentSourceFrag = source cluster's fragmentation score
- newSourceFrag = fragmentation after removing `gpus` from sourceNode
- currentDestFrag = dest cluster's fragmentation score
- newDestFrag = fragmentation after adding `gpus` to best-fit node in dest
- delta = (currentSourceFrag + currentDestFrag) - (newSourceFrag + newDestFrag)

## Observability

- Span: `rebalancing.find_opportunities` with attrs `migrations_proposed` (int), `clusters_evaluated` (int)
- Counter: `schedulator_rebalancing_migrations_proposed_total`
- Histogram: `schedulator_rebalancing_fragmentation_improvement`
- CounterVec: `schedulator_rebalancing_skipped_total` labels: `reason` (well_packed, min_replicas, no_cache, low_delta)

## Test Cases

| # | Test | Validates |
|---|------|-----------|
| 1 | TestRebalancing_FindsConsolidation | Migration proposed from fragmented to well-packed cluster |
| 2 | TestRebalancing_SkipsWellPackedClusters | Empty result when all clusters well-packed |
| 3 | TestRebalancing_RespectsMinReplicas | No migration when at min replicas |
| 4 | TestRebalancing_RequiresCachedModel | No migration without cached model |
| 5 | TestRebalancing_MinFragDelta | No migration when improvement too small |
| 6 | TestRebalancing_MaxMigrationsPerCycle | Only 2 returned from 5 candidates |
| 7 | TestRebalancing_PrefersLowPriority | Higher Priority int migrated first |

## Edge Cases

- Empty clusters map → no migrations
- Node with 0 allocated GPUs → not fragmented, skipped
- Node with all GPUs allocated → not fragmented, skipped
- Multiple replicas on same node → each evaluated independently
- Source replica cluster == candidate dest cluster → skip (findConsolidationTarget excludes source)
