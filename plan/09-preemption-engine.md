# Section 9 — Preemption Engine Pre-Implementation Doc

## Overview

The preemption engine is invoked by the placement engine when no cluster has sufficient capacity for a scale-up. It implements the proposal's strict priority cascade to free GPUs by evicting lower-priority replicas.

## Interface

Satisfies the `preemptionFinder` interface defined in `internal/engine/placement/engine.go`:

```go
type preemptionFinder interface {
    FindPreemptionOpportunity(
        ctx context.Context,
        app model.Application,
        snap worldstate.WorldStateSnapshot,
    ) (*model.PreemptionPlan, error)
}
```

## Types (all pre-existing in `pkg/model/decisions.go`)

- `PreemptionDecision` — per-victim: `VictimReplicaID`, `VictimAppID`, `NodeID`, `ClusterID`, `GracePeriodSeconds`, `Reason`
- `PreemptionPlan` — `Victims []PreemptionDecision`, `TargetCluster ClusterID`, `Constraints SchedulingConstraints`

## Config — `internal/engine/preemption/config.go`

```go
type PreemptionConfig struct {
    DefaultGracePeriodSeconds int // default: 10
}

func DefaultPreemptionConfig() PreemptionConfig
```

## Constructor

```go
func NewPreemptionEngine(
    cfg PreemptionConfig,
    tracer trace.Tracer,
    reg prometheus.Registerer,
) *PreemptionEngine
```

## Algorithm — `FindPreemptionOpportunity`

1. Start span `preemption.find_opportunity` with `requesting_app.id`, `requesting_app.priority`.
2. If `app.Priority == 2` → return `nil, nil` (P2 cannot preempt).
3. Build candidate pools by iterating all replicas in snapshot:
   - Skip replicas not running/pending.
   - Skip replicas of same app or same/higher priority (lower or equal Priority int).
   - Categorize into `p2Candidates` and `p1Candidates`.
4. Sort each pool by `preemptionSortKey` (above min_replicas first, then packing benefit descending).
5. Call `selectVictims(p2Candidates, requiredGPUs, snap, "")`.
6. If freed < required AND requester is P0 → call `selectVictims(p1Candidates, remaining, snap, targetCluster)`.
7. If still insufficient → increment `insufficient_capacity_total`, return `nil, nil`.
8. Record metrics, set span attributes, return `PreemptionPlan`.

## Algorithm — `selectVictims`

```go
func (e *PreemptionEngine) selectVictims(
    candidates []victimCandidate,
    requiredGPUs int,
    snap worldstate.WorldStateSnapshot,
    preferCluster model.ClusterID,
) ([]model.PreemptionDecision, int, model.ClusterID)
```

1. Track `preemptingPerApp map[AppID]int` for min_replicas protection.
2. For each candidate:
   - Count active replicas for candidate's app. If `activeCount - preemptingPerApp[appID] <= app.MinReplicas` → skip.
   - If `preferCluster != ""` and `candidate.ClusterID != preferCluster` → skip.
   - First victim sets `preferCluster`.
   - Append `PreemptionDecision`, increment `preemptingPerApp`, accumulate freed GPUs.
   - Break when freed >= required.
3. Return victims, freed GPUs, target cluster.

## Packing Benefit

```go
func packingBenefit(replicaGPUs, gpusNeeded int) float64
```

- If `replicaGPUs == gpusNeeded` → return `1.0` (perfect match).
- Otherwise → `1.0 / (1.0 + math.Abs(float64(replicaGPUs - gpusNeeded)))`.

## Observability

### Spans
- `preemption.find_opportunity` — attrs: `requesting_app.id`, `requesting_app.priority`, `victims_count`, `freed_gpus`

### Metrics
- `schedulator_preemption_events_total` — CounterVec, labels: `requesting_priority`, `victim_priority`
- `schedulator_preemption_victims_total` — Counter
- `schedulator_preemption_insufficient_capacity_total` — Counter

## Priority Cascade Rules

| Requester | Can preempt P2 | Can preempt P1 | Can preempt P0 |
|-----------|---------------|---------------|---------------|
| P0        | Yes           | Yes (escalate)| No            |
| P1        | Yes           | No            | No            |
| P2        | No            | No            | No            |

## Test Cases

| # | Test Name | Validates |
|---|-----------|-----------|
| 1 | `TestPreemption_P0CanPreemptP2` | P0 app preempts P2 replicas |
| 2 | `TestPreemption_P0EscalatesToP1` | P2 insufficient → cascades to P1 |
| 3 | `TestPreemption_P1CannotEscalateToP0` | P1 only preempts P2, never P0 |
| 4 | `TestPreemption_P2CannotPreempt` | P2 returns nil |
| 5 | `TestPreemption_RespectsMinReplicas` | Skips victims at min_replicas |
| 6 | `TestPreemption_SameClusterPreference` | All victims from same cluster |
| 7 | `TestPreemption_PackingBenefit` | Prefers replicas with matching GPU count |
| 8 | `TestPreemption_InsufficientCapacity` | Not enough preemptable → returns nil |

## Edge Cases

- App with 0 MinReplicas: all replicas are preemptable.
- Mixed clusters: first victim pins the cluster; other-cluster candidates are skipped.
- No candidates at all: return nil immediately.
- Requester needs more GPUs than any single victim provides: accumulate across multiple victims.
