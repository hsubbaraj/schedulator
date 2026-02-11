# Section 07 — Scaling Engine (Pre-Implementation Doc)

## Overview

The scaling engine computes target replica counts per application using vLLM runtime metrics. It implements the algorithm from proposal Section 4.3.

## Types

### `ScaleDirection` (string)
- `"up"`, `"down"`, `"unchanged"`

### `ScaleSignal` (string)
- `"queue"`, `"kv_cache"`, `"sla"`, `"efficiency"`, `"none"`

### `ScalingConfig`
```go
type ScalingConfig struct {
    QueueHighWatermark   float64       // 10.0
    QueueTarget          float64       // 5.0
    QueueLowWatermark    float64       // 1.0
    KVCacheHighWatermark float64       // 0.85
    KVCacheTarget        float64       // 0.70
    KVCacheLowWatermark  float64       // 0.40
    BatchLowWatermark    float64       // 0.30
    MaxScaleDownPerCycle int           // 2
    MaxScaleUpPerCycle   int           // 5
    ScaleUpCooldown      time.Duration // 120s
    ScaleDownCooldown    time.Duration // 300s
    StabilizationCycles  int           // 3
}
```

### `ScalingDecision`
```go
type ScalingDecision struct {
    AppID                   model.AppID
    CurrentCount            int
    TargetCount             int
    Direction               ScaleDirection
    Signal                  ScaleSignal
    SLABreach               bool
    ScaleActionApproved     bool
    ActionAt                time.Time
    ScaleDownSignalObserved bool
}
```

## Function Signatures

```go
func NewScalingEngine(cfg ScalingConfig, tracer trace.Tracer, reg prometheus.Registerer) *ScalingEngine
func (e *ScalingEngine) ComputeTargets(ctx context.Context, snap worldstate.WorldStateSnapshot) map[model.AppID]ScalingDecision
func (e *ScalingEngine) computeTargetReplicas(app model.Application, metrics model.VLLMMetrics, profile model.PerformanceProfile, currentCount int) (int, ScaleSignal)
func (e *ScalingEngine) computeEffectiveTarget(ctx context.Context, app model.Application, rawTarget int, snap worldstate.WorldStateSnapshot) int
func (e *ScalingEngine) applyStabilityControls(ctx context.Context, appID model.AppID, app model.Application, target int, current int, snap worldstate.WorldStateSnapshot) ScalingDecision
```

## Key Design Decisions

1. **`snap.TakenAt` as canonical "now"** — deterministic, testable, no `time.Now()` in engine.
2. **`ScalingDecision` carries mutation intent** — `ScaleActionApproved` and `ScaleDownSignalObserved` tell the control loop what WorldState mutations to perform.
3. **`IncrementScaleDownSignal` on WorldState** — separates counter increment from timestamp update.
4. **`Replica.CreatedAt`** — needed for stale-pending detection. Zero value = non-stale.
5. **`currentCount` = running + pending** — pending replicas are expected capacity.

## Edge Cases

| Edge Case | Resolution |
|-----------|-----------|
| `SLA.MaxP99TTFTMs == 0` | SLA scaling disabled |
| `currentCount == 0` | Scale-up signals return 0; target = max(0, MinReplicas) |
| `QueueTarget == 0` | Skip queue scaling (guard) |
| `Replica.CreatedAt` is zero | Non-stale (never triggers override) |
| `PerformanceProfile` missing | Stale check skipped |
| `LastScaleUpAt` is zero | Cooldown auto-bypassed |
| Queue exactly 10.0 | `>` exclusive — does NOT trigger |
| Cooldown exactly 120s | `<` exclusive — exactly at boundary is NOT in cooldown |

## Test Cases

All implemented in `internal/engine/scaling/engine_test.go` with table-driven tests covering:
- Queue pressure scaling (boundary: exactly 10.0)
- KV cache pressure scaling (boundary: exactly 0.85)
- SLA breach scaling (SLA disabled case)
- Low utilization scale-down
- MinReplicas floor
- Per-cycle dampening caps
- Pending replica in-flight awareness
- Stale pending override (with/without SLA breach, zero CreatedAt)
- Scale-up cooldown (within, at boundary, past, zero time)
- Scale-down cooldown
- Stabilization window (0, 1, 2, 5 consecutive signals)
- SLA breach bypass (stabilization yes, cooldown no)
- Full integration test with realistic scenario
- QueueTarget=0 division-by-zero guard

## Traces

- `scaling.compute_targets` — parent span, attribute: `app_count`
- `scaling.compute_target` — per-app child, attributes: `app.id`, `current_count`, `target_count`, `signal`

## Metrics

- `schedulator_scaling_target_replicas` GaugeVec (`app_id`)
- `schedulator_scaling_decisions_total` CounterVec (`app_id`, `direction`)
- `schedulator_scaling_compute_duration_seconds` Histogram
- `schedulator_scaling_stability_suppressed_total` CounterVec (`app_id`, `reason`)
