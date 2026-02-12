# Section 10 — Plan Generator (Pre-Implementation Doc)

## Overview

The Plan Generator transforms `PlacementDecisions` (from the placement engine) and `[]MigrateDecision` (from the rebalancing engine) into an ordered `ExecutionPlan` with dependency tracking. The proposal (Section 4.5) specifies strict operation ordering: **preemptions → scale-downs → scale-ups → migrations**.

## New Types — `pkg/model/plan.go`

```go
type OperationType string

const (
    OperationPreempt   OperationType = "preempt"
    OperationScaleDown OperationType = "scale_down"
    OperationScaleUp   OperationType = "scale_up"
    OperationMigrate   OperationType = "migrate" // decomposed into scale_up + scale_down
)

type Operation struct {
    ID        OperationID
    Type      OperationType
    Payload   any
    DependsOn []OperationID
}

type ExecutionPlan struct {
    Operations []Operation
}

type MigrateDecision struct {
    AppID                 AppID
    SourceReplicaID       ReplicaID
    TargetClusterID       ClusterID
    SchedulingConstraints SchedulingConstraints
}
```

## Constructor

```go
func NewPlanGenerator(tracer trace.Tracer, reg prometheus.Registerer) *PlanGenerator
```

Stateless — only tracer and metrics.

## Function Signature

```go
func (g *PlanGenerator) GeneratePlan(
    ctx context.Context,
    decisions model.PlacementDecisions,
    migrations []model.MigrateDecision,
) model.ExecutionPlan
```

## Algorithm

1. Start span `plangen.generate`.
2. **Preemptions**: For each `PreemptionDecision`, create `Operation{Type: OperationPreempt}` with a new UUID. Track `clusterID → []OperationID` for dependency linking.
3. **Scale-downs**: For each `ScaleDownDecision`, create `Operation{Type: OperationScaleDown}`. No dependencies.
4. **Scale-ups**: For each `ScaleUpDecision`, create `Operation{Type: OperationScaleUp}`. If preemption ops exist for the same `ClusterID`, set `DependsOn` to those op IDs.
5. **Migrations**: For each `MigrateDecision`, decompose into:
   - `scale_up` operation (new replica in target cluster, DependsOn any preemptions in that cluster)
   - `scale_down` operation (old replica) with `DependsOn = [scale_up.ID]`
6. Append in order: preemptions, scale-downs, scale-ups, migration pairs.
7. Record metrics, set span attributes, return plan.

## Observability

### Spans
- `plangen.generate` — attrs: `operations_count`, `preemptions`, `scale_downs`, `scale_ups`, `migrations`

### Metrics
- `schedulator_plangen_operations_total` CounterVec (label: `type`)
- `schedulator_plangen_generate_duration_seconds` Histogram

## Test Cases

| # | Test | Validates |
|---|------|-----------|
| 1 | `TestGeneratePlan_OperationOrdering` | Preemptions before scale-downs before scale-ups |
| 2 | `TestGeneratePlan_PreemptionDependency` | Scale-up in cluster X depends on preemption ops in cluster X |
| 3 | `TestGeneratePlan_MigrationDecomposition` | MigrateDecision → scale_up + scale_down pair; scale_down.DependsOn = [scale_up.ID] |
| 4 | `TestGeneratePlan_IndependentOpsNoDependency` | Scale-ups without preemptions have empty DependsOn |
| 5 | `TestGeneratePlan_EmptyDecisions` | Returns empty plan, no panic |

## Edge Cases

- Empty decisions + empty migrations → empty plan, no panic.
- Multiple preemptions in same cluster → scale-up depends on all of them.
- Migration target cluster has preemptions → migration scale-up also depends on those preemptions.
- Payload carries the original decision struct for executor consumption.
