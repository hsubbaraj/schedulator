# Section 11 — Plan Executor (Pre-Implementation Doc)

## Overview

The Plan Executor dispatches operations from an `ExecutionPlan` to cluster clients, respecting dependency ordering, handling failures with transitive abort, and managing GPU reservation lifecycle for scale-ups. It implements the Execution Coordinator from proposal Section 4.6.

## Dependencies

- **Consumes**: `model.ExecutionPlan` (from Section 10 — Plan Generator)
- **Uses**: `ports.ClusterClient` for K8s operations
- **Uses**: `worldstate.WorldState` (via `reservationStore` local interface) for reservation lifecycle
- **Uses**: `worldstate.WorldStateSnapshot` for routing data (replica→cluster, reservation TTLs)

## New Types

### `pkg/model/result.go`

```go
type FailedOp struct {
    OperationID OperationID
    Type        OperationType
    Err         error
}

type ExecutionResult struct {
    Completed []OperationID
    Failed    []FailedOp
    Aborted   []OperationID
}
```

## Config

### `internal/executor/config.go`

```go
type ExecutorConfig struct {
    PollInterval           time.Duration // default: 5s
    WaitForDeletionTimeout time.Duration // default: 120s
    DefaultReservationTTL  time.Duration // default: 300s
}
```

## Executor

### Local Interface

```go
type reservationStore interface {
    FulfillReservation(id model.ReservationID) error
    ExpireReservation(id model.ReservationID) error
}
```

This is satisfied by `*worldstate.WorldState`.

### Constructor

```go
func NewExecutor(
    cfg ExecutorConfig,
    clusterClients map[model.ClusterID]ports.ClusterClient,
    worldState reservationStore,
    tracer trace.Tracer,
    reg prometheus.Registerer,
) *Executor
```

### Execute Algorithm

```go
func (e *Executor) Execute(
    ctx context.Context,
    plan model.ExecutionPlan,
    snapshot worldstate.WorldStateSnapshot,
) model.ExecutionResult
```

1. Start span `executor.execute` with `plan_operations_count`.
2. Build `opByID` index and reverse dependency map.
3. Pre-extract routing data from snapshot.
4. Main loop:
   - Scan pending ops; dispatch ready ones (all DependsOn completed) as goroutines.
   - Collect results via buffered channel.
   - On success: mark completed.
   - On failure: mark failed, `abortTransitive` (BFS) marks all transitive dependents aborted.
   - Terminate when no pending or in-flight ops remain.
5. Return `ExecutionResult`.

### Operation Handlers

- `scaleUp`: CreateReplica → waitForRunning → FulfillReservation (or expire + cleanup on timeout)
- `preempt`: LabelReplica(draining) → DeleteReplica → waitForDeletion
- `scaleDown`: DeleteReplica → waitForDeletion
- `waitForRunning` / `waitForDeletion`: Poll with `time.NewTimer`, no `time.Sleep`

### Routing Edge Cases

- ScaleDown: look up `snapshot.Replicas[replicaID].ClusterID`
- Missing cluster client: fail op
- Missing reservation: fall back to `cfg.DefaultReservationTTL`
- FulfillReservation on expired: log warning, don't fail

## Observability

### Spans
- `executor.execute` — attrs: plan_operations_count, completed_count, failed_count, aborted_count
- `executor.dispatch_op` — attrs: op.id, op.type, cluster.id
- `executor.wait_for_running` / `executor.wait_for_deletion`

### Metrics
- `schedulator_executor_operation_duration_seconds` HistogramVec (type, status)
- `schedulator_executor_operations_inflight` Gauge
- `schedulator_executor_failures_total` CounterVec (type, reason)
- `schedulator_executor_reservations_expired_total` Counter

## Test Cases

| # | Test | Validates |
|---|------|-----------|
| 1 | TestExecutor_EmptyPlan | Empty plan → empty result, no panic |
| 2 | TestExecutor_HappyPath | Preempt + dependent scale-up both succeed |
| 3 | TestExecutor_DependencyRespected | Scale-up not dispatched until preemption completes |
| 4 | TestExecutor_FailureAbortsDependents | Preemption failure → dependent scale-up aborted |
| 5 | TestExecutor_ParallelIndependentOps | Independent ops dispatched concurrently |
| 6 | TestExecutor_ScaleUpTimeout | Reservation expires, orphan cleaned up |
| 7 | TestExecutor_ReservationFulfilled | Successful scale-up → FulfillReservation called |
| 8 | TestExecutor_ScaleDownNoClusterID | Replica not in snapshot → op fails gracefully |
| 9 | TestAbortTransitive | Table-driven: linear, fan-out, diamond graphs |
