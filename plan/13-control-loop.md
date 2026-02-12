# Section 13 — Control Loop (Pre-Implementation)

## Overview

The control loop is the top-level orchestrator that drives the full scheduling cycle:
receive events → snapshot → scale → post-process → place → persist reservations → rebalance → generate plan → validate → execute → log.

It implements the state machine from `docs/07-control-loop-state.mermaid`.

## Files

| File | Purpose |
|------|---------|
| `internal/controlloop/loop.go` | Local interfaces, `ControlLoop` struct, `NewControlLoop`, `Run`, `runCycle` |
| `internal/controlloop/validation.go` | `validatePlan` function |
| `internal/controlloop/loop_test.go` | Hand-rolled mocks + all tests |

## Local Interfaces

Package-private interfaces for testability (following existing pattern from placement/executor):

```go
type eventSource interface {
    Start(ctx context.Context) (<-chan []ingestion.Event, error)
}

type scalingComputer interface {
    ComputeTargets(ctx context.Context, snap worldstate.WorldStateSnapshot) map[model.AppID]scaling.ScalingDecision
}

type placementComputer interface {
    ComputePlacement(ctx context.Context, snap worldstate.WorldStateSnapshot, decisions map[model.AppID]scaling.ScalingDecision) model.PlacementDecisions
}

type rebalancer interface {
    FindRebalancingOpportunities(ctx context.Context, snap worldstate.WorldStateSnapshot) []model.MigrateDecision
}

type planGenerator interface {
    GeneratePlan(ctx context.Context, decisions model.PlacementDecisions, migrations []model.MigrateDecision) model.ExecutionPlan
}

type planExecutor interface {
    Execute(ctx context.Context, plan model.ExecutionPlan, snapshot worldstate.WorldStateSnapshot) model.ExecutionResult
}

type worldStateManager interface {
    Snapshot(ctx context.Context) worldstate.WorldStateSnapshot
    CreateReservation(r model.GPUReservation) error
    RecordScaleEvent(appID model.AppID, direction string, at time.Time)
    IncrementScaleDownSignal(appID model.AppID)
    ExpireStaleReservations(now time.Time) int
}
```

## Struct

```go
type ControlLoopConfig struct {
    ReservationExpiryScanInterval time.Duration // default 30s
}

type ControlLoop struct {
    cfg              ControlLoopConfig
    ingester         eventSource
    scalingEngine    scalingComputer
    placementEngine  placementComputer
    rebalancer       rebalancer
    planGenerator    planGenerator
    executor         planExecutor
    worldState       worldStateManager
    leaderElector    ports.LeaderElector
    tracer           trace.Tracer

    cyclesTotal     *prometheus.CounterVec   // label: outcome
    cycleDuration   prometheus.Histogram
    eventsProcessed prometheus.Counter
    isLeaderGauge   prometheus.Gauge
}
```

## Function Signatures

```go
func NewControlLoop(
    cfg ControlLoopConfig,
    ingester eventSource,
    scalingEngine scalingComputer,
    placementEngine placementComputer,
    rebalancer rebalancer,
    planGenerator planGenerator,
    executor planExecutor,
    worldState worldStateManager,
    leaderElector ports.LeaderElector,
    tracer trace.Tracer,
    reg prometheus.Registerer,
) *ControlLoop

func (cl *ControlLoop) Run(ctx context.Context) error
func (cl *ControlLoop) runCycle(ctx context.Context, events []ingestion.Event) error
func validatePlan(plan model.ExecutionPlan, snap worldstate.WorldStateSnapshot) error
```

## Run(ctx) Logic

1. `eventCh, err := ingester.Start(ctx)` — return error on failure
2. Start background goroutine: `runReservationExpiry(ctx)` — sweeps stale reservations every `cfg.ReservationExpiryScanInterval`
3. Main loop:
   - `select` on `eventCh` (with nil-check for channel close) and `ctx.Done()`
   - On events: increment `eventsProcessed`, check `leaderElector.IsLeader()`, set `isLeaderGauge`, skip if not leader, otherwise `runCycle(ctx, events)`
   - On `ctx.Done()`: return `ctx.Err()`
   - On channel close: return nil

## runCycle(ctx, events) Logic

1. Start span `controlloop.cycle` with `event_count` attribute
2. `snap := worldState.Snapshot(ctx)`
3. `scalingDecisions := scalingEngine.ComputeTargets(ctx, snap)`
4. Post-process scaling decisions:
   - If `ScaleActionApproved == true`: call `worldState.RecordScaleEvent(appID, direction, ActionAt)`
   - If `ScaleDownSignalObserved == true && !ScaleActionApproved`: call `worldState.IncrementScaleDownSignal(appID)`
5. `placements := placementEngine.ComputePlacement(ctx, snap, scalingDecisions)`
6. For each reservation in `placements.Reservations`: call `worldState.CreateReservation(r)` (log error, non-fatal)
7. `migrations := rebalancer.FindRebalancingOpportunities(ctx, snap)`
8. `plan := planGenerator.GeneratePlan(ctx, placements, migrations)`
9. `err := validatePlan(plan, snap)` — if error: set outcome="validation_failure", return error
10. `result := executor.Execute(ctx, plan, snap)`
11. Determine outcome: if `len(result.Failed) > 0` → "execution_failure", else "success"
12. Set span attributes, observe metrics
13. Return nil (even on execution failure — partial success is expected)

## validatePlan(plan, snap) Logic

Three checks, all violations collected via `errors.Join`:

1. **No self-preemption**: Build set of app IDs from `OperationScaleUp` ops (payload is `ScaleUpDecision`). For each `OperationPreempt` (payload is `PreemptionDecision`), if `VictimAppID` is in scale-up set → error.
2. **Reservations within capacity**: Sum GPUs reserved per cluster from `OperationScaleUp` by looking up `ReservationID` in `snap.Reservations`. If sum > cluster total GPUs → error.
3. **No terminated replica scale-down**: For each `OperationScaleDown` (payload is `ScaleDownDecision`), if `snap.Replicas[ReplicaID].Status == Terminated` → error.

## Observability

### Span
- `controlloop.cycle` — attrs: `event_count`, `outcome`, `cycle_duration_ms`, `completed_ops`, `failed_ops`, `aborted_ops`

### Metrics
- `schedulator_controlloop_cycles_total` CounterVec (label: `outcome`)
- `schedulator_controlloop_cycle_duration_seconds` Histogram
- `schedulator_controlloop_events_processed_total` Counter
- `schedulator_controlloop_leader_is_leader` Gauge

## Test Cases

| # | Test | Validates |
|---|------|-----------|
| 1 | `TestControlLoop_FullCycle` | All engines called exactly once when leader; channel closes cleanly |
| 2 | `TestControlLoop_LeaderElection` | Non-leader: zero engine calls, events drained |
| 3 | `TestControlLoop_ValidationRejects` | Self-preemption plan → executor NOT called |
| 4 | `TestControlLoop_ContextCancellation` | Cancel ctx → Run returns `context.Canceled` |
| 5 | `TestControlLoop_ScalingPostProcessing_ApprovedScaleUp` | `RecordScaleEvent("up", ...)` called |
| 6 | `TestControlLoop_ScalingPostProcessing_SuppressedScaleDown` | `IncrementScaleDownSignal` called, NOT `RecordScaleEvent` |
| 7 | `TestValidatePlan_NoSelfPreemption` | Table-driven |
| 8 | `TestValidatePlan_ReservationCapacity` | Table-driven |
| 9 | `TestValidatePlan_NoTerminatedReplicaScaleDown` | Table-driven |

All mocks are hand-rolled (package-private interfaces can't use mockery).
