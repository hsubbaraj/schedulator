# Section 8 — Placement Engine (Pre-Implementation)

## Overview

The placement engine takes scaling targets from the Scaling Engine and decides **where** to place replicas across the cluster fleet. It implements cache-aware cluster scoring, scale-down victim selection, GPU reservation tracking, and failure domain enforcement.

**Proposal reference:** Section 4.4 — Placement Engine

## Types

### `pkg/model/decisions.go` — Shared Decision Types

```go
type PlacementDecisions struct {
    ScaleUps     []ScaleUpDecision
    ScaleDowns   []ScaleDownDecision
    Preemptions  []PreemptionDecision
    Reservations []GPUReservation
}

type ScaleUpDecision struct {
    AppID                 AppID
    ClusterID             ClusterID
    ReservationID         ReservationID
    SchedulingConstraints SchedulingConstraints
}

type ScaleDownDecision struct {
    AppID     AppID
    ReplicaID ReplicaID
}

type PreemptionDecision struct {
    VictimReplicaID    ReplicaID
    VictimAppID        AppID
    NodeID             NodeID
    ClusterID          ClusterID
    GracePeriodSeconds int
    Reason             string
}

type PreemptionPlan struct {
    Victims       []PreemptionDecision
    TargetCluster ClusterID
    Constraints   SchedulingConstraints
}
```

### `internal/engine/placement/config.go`

```go
type PlacementConfig struct {
    WeightCacheGPUMemory         float64 // 1000
    WeightCacheLocalNVMe         float64 // 500
    WeightCacheClusterStorage    float64 // 100
    WeightFragmentation          float64 // 50
    WeightBalance                float64 // 30
    MaxGPUsPerNode               int     // 8
    ReservationTTLMultiplier     int     // 2
    DefaultReservationTTLSeconds int     // 300
}
```

## Function Signatures

### `engine.go`

```go
func NewPlacementEngine(cfg PlacementConfig, preemption preemptionFinder, tracer trace.Tracer, reg prometheus.Registerer) *PlacementEngine
func (e *PlacementEngine) ComputePlacement(ctx context.Context, snap worldstate.WorldStateSnapshot, scalingDecisions map[model.AppID]scaling.ScalingDecision) model.PlacementDecisions
func (e *PlacementEngine) findBestCluster(ctx context.Context, app model.Application, snap worldstate.WorldStateSnapshot, localReservations map[model.ClusterID]int) (model.ClusterID, model.SchedulingConstraints, bool)
```

### `scoring.go`

```go
func (e *PlacementEngine) computeCacheScore(modelID model.ModelID, clusterID model.ClusterID, snap worldstate.WorldStateSnapshot) (float64, []model.NodeID, string)
func (e *PlacementEngine) computePackingScore(cluster model.Cluster, gpusNeeded int) float64
func (e *PlacementEngine) selectScaleDownVictims(appID model.AppID, count int, snap worldstate.WorldStateSnapshot) []model.ReplicaID
```

### Local preemption interface (unexported)

```go
type preemptionFinder interface {
    FindPreemptionOpportunity(ctx context.Context, app model.Application, snap worldstate.WorldStateSnapshot) (*model.PreemptionPlan, error)
}
```

## Algorithm: `ComputePlacement`

1. Start span `placement.compute`
2. Compute deltas: for each scaling target where `ScaleActionApproved`, `delta = TargetCount - currentCount` (Running + Pending replicas)
3. Process scale-downs first (delta < 0): `selectScaleDownVictims(appID, -delta, snap)`
4. Process scale-ups (delta > 0), sorted by app priority ASC (P0 first):
   - For each replica to add: `findBestCluster(app, snap, localReservations)`
   - If no cluster: `preemptionFinder.FindPreemptionOpportunity`
   - Create GPUReservation with TTL = `2 * coldStartSeconds` (or default 300s)
   - Update localReservations
5. Return PlacementDecisions

## Algorithm: `findBestCluster`

For each cluster:
- **Hard constraints:**
  - Any ready node has `FreeGPUs >= gpusNeeded`
  - `clusterAvailableGPUs >= gpusNeeded` (snap reservations + local reservations)
  - If spread_clusters: `!exceedsSpreadLimit`
- **Soft scoring:**
  - `cacheScore` (tier-weighted)
  - `WeightFragmentation * packingScore`
  - `WeightBalance * (1 - utilization)`
- Return best + SchedulingConstraints

## Algorithm: `selectScaleDownVictims`

Sort running/pending replicas by removal preference:
1. Higher node fragmentation (1 - free/total) → remove first
2. Non-cached replicas → remove first
3. Over-represented clusters → remove first

## Observability

- Spans: `placement.compute`, `placement.find_best_cluster`, `placement.score_cluster`
- Metrics: `schedulator_placement_score`, `schedulator_placement_scaleups_total`, `schedulator_placement_scaledowns_total`, `schedulator_placement_no_capacity_total`, `schedulator_placement_compute_duration_seconds`

## Test Cases

| # | Test | Validates |
|---|------|-----------|
| 1 | `TestFindBestCluster_PrefersWarmCache` | LOCAL_NVME cluster beats better-packed REMOTE cluster |
| 2 | `TestFindBestCluster_GPUMemoryDominates` | GPU_MEMORY always wins regardless of secondary scores |
| 3 | `TestComputePackingScore_TightestFit` | Exact-fit node → score 1.0 |
| 4 | `TestComputePackingScore_NoFittingNode` | No node with enough GPUs → score 0.0 |
| 5 | `TestComputePlacement_ScaleDownFirst` | Scale-downs appear when delta < 0 |
| 6 | `TestComputePlacement_FailureDomainSpread` | spread_clusters prevents over-concentration |
| 7 | `TestComputePlacement_CreatesReservations` | Each scale-up produces a GPUReservation |
| 8 | `TestSelectScaleDownVictims_PrefersHighFragmentation` | Victims from fragmented nodes first |
| 9 | `TestComputePlacement_DelegatesToPreemption` | No capacity → calls preemption engine |
| 10 | `TestComputePlacement_MultipleScaleUps_ReservationTracking` | Second scale-up sees reduced capacity |

## Edge Cases

- Zero eligible clusters → returns empty PlacementDecisions with no_capacity metric
- App with no performance profile → uses DefaultReservationTTLSeconds
- Cluster has fitting node but insufficient cluster-level capacity after reservations → skip
- All clusters at spread limit → falls through to preemption
- localReservations must be tracked per-cluster within a single ComputePlacement call
