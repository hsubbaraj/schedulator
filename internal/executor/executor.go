package executor

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/hsubbaraj/schedulator/internal/observability"
	"github.com/hsubbaraj/schedulator/internal/worldstate"
	"github.com/hsubbaraj/schedulator/pkg/model"
	"github.com/hsubbaraj/schedulator/pkg/ports"
)

// reservationStore is a local interface satisfied by *worldstate.WorldState.
type reservationStore interface {
	FulfillReservation(id model.ReservationID) error
	ExpireReservation(id model.ReservationID) error
}

// Executor dispatches operations from an ExecutionPlan to cluster clients,
// respecting dependency ordering and handling failures with transitive abort.
type Executor struct {
	cfg            ExecutorConfig
	clusterClients map[model.ClusterID]ports.ClusterClient
	worldState     reservationStore
	tracer         trace.Tracer

	// metrics
	opDuration          *prometheus.HistogramVec
	opsInflight         prometheus.Gauge
	failuresTotal       *prometheus.CounterVec
	reservationsExpired prometheus.Counter
}

// NewExecutor creates an Executor with the given dependencies.
func NewExecutor(
	cfg ExecutorConfig,
	clusterClients map[model.ClusterID]ports.ClusterClient,
	worldState reservationStore,
	tracer trace.Tracer,
	reg prometheus.Registerer,
) *Executor {
	return &Executor{
		cfg:            cfg,
		clusterClients: clusterClients,
		worldState:     worldState,
		tracer:         tracer,
		opDuration: observability.MustNewHistogramVec(reg, prometheus.HistogramOpts{
			Name:    "schedulator_executor_operation_duration_seconds",
			Help:    "Duration of individual operation execution.",
			Buckets: prometheus.DefBuckets,
		}, []string{"type", "status"}),
		opsInflight: observability.MustNewGauge(reg, prometheus.GaugeOpts{
			Name: "schedulator_executor_operations_inflight",
			Help: "Number of operations currently being executed.",
		}),
		failuresTotal: observability.MustNewCounterVec(reg, prometheus.CounterOpts{
			Name: "schedulator_executor_failures_total",
			Help: "Total number of failed operations.",
		}, []string{"type", "reason"}),
		reservationsExpired: observability.MustNewCounter(reg, prometheus.CounterOpts{
			Name: "schedulator_executor_reservations_expired_total",
			Help: "Total number of reservations that expired during scale-up.",
		}),
	}
}

// opStatus tracks an operation's lifecycle in the executor loop.
type opStatus int

const (
	opPending opStatus = iota
	opInFlight
	opCompleted
	opFailed
	opAborted
)

// opResult carries the outcome of a dispatched operation back to the main loop.
type opResult struct {
	id  model.OperationID
	err error
}

// Execute dispatches every operation in plan, respecting DependsOn ordering.
// Operations whose dependencies all completed successfully are dispatched
// concurrently. On failure, all transitive dependents are aborted.
func (e *Executor) Execute(
	ctx context.Context,
	plan model.ExecutionPlan,
	snapshot worldstate.WorldStateSnapshot,
) model.ExecutionResult {
	ctx, span := e.tracer.Start(ctx, "executor.execute",
		trace.WithAttributes(attribute.Int("plan_operations_count", len(plan.Operations))))
	defer span.End()

	if len(plan.Operations) == 0 {
		return model.ExecutionResult{}
	}

	// Build indices.
	opByID := make(map[model.OperationID]model.Operation, len(plan.Operations))
	status := make(map[model.OperationID]opStatus, len(plan.Operations))
	// dependents[X] = list of ops that depend on X.
	dependents := make(map[model.OperationID][]model.OperationID)

	for _, op := range plan.Operations {
		opByID[op.ID] = op
		status[op.ID] = opPending
		for _, dep := range op.DependsOn {
			dependents[dep] = append(dependents[dep], op.ID)
		}
	}

	results := make(chan opResult, len(plan.Operations))
	var wg sync.WaitGroup

	var completed []model.OperationID
	var failed []model.FailedOp
	var aborted []model.OperationID

	pending := len(plan.Operations)
	inflight := 0

	for pending+inflight > 0 {
		// Dispatch ready ops.
		for id, st := range status {
			if st != opPending {
				continue
			}
			if !e.depsCompleted(opByID[id], status) {
				continue
			}
			status[id] = opInFlight
			pending--
			inflight++
			e.opsInflight.Inc()
			wg.Add(1)

			op := opByID[id]
			go func() {
				defer wg.Done()
				err := e.dispatchOp(ctx, op, snapshot)
				results <- opResult{id: op.ID, err: err}
			}()
		}

		if inflight == 0 {
			// All remaining ops must be blocked by failures — they should
			// already be aborted, but break to avoid infinite loop.
			break
		}

		// Wait for one result.
		select {
		case r := <-results:
			inflight--
			e.opsInflight.Dec()

			if r.err != nil {
				op := opByID[r.id]
				status[r.id] = opFailed
				failed = append(failed, model.FailedOp{
					OperationID: r.id,
					Type:        op.Type,
					Err:         r.err,
				})
				e.failuresTotal.WithLabelValues(string(op.Type), "dispatch_error").Inc()

				// Abort all transitive dependents.
				abortedIDs := e.abortTransitive(r.id, dependents, status)
				aborted = append(aborted, abortedIDs...)
				pending -= len(abortedIDs)

				slog.WarnContext(ctx, "operation failed",
					"op_id", r.id, "op_type", op.Type, "error", r.err,
					"aborted_dependents", len(abortedIDs))
			} else {
				status[r.id] = opCompleted
				completed = append(completed, r.id)
			}

		case <-ctx.Done():
			// Context cancelled — abort everything still pending/inflight.
			for id, st := range status {
				if st == opPending {
					status[id] = opAborted
					aborted = append(aborted, id)
					pending--
				}
			}
			// Drain inflight results.
			for inflight > 0 {
				r := <-results
				inflight--
				e.opsInflight.Dec()
				if r.err != nil {
					op := opByID[r.id]
					status[r.id] = opFailed
					failed = append(failed, model.FailedOp{
						OperationID: r.id,
						Type:        op.Type,
						Err:         r.err,
					})
				} else {
					status[r.id] = opCompleted
					completed = append(completed, r.id)
				}
			}
		}
	}

	span.SetAttributes(
		attribute.Int("completed_count", len(completed)),
		attribute.Int("failed_count", len(failed)),
		attribute.Int("aborted_count", len(aborted)),
	)

	return model.ExecutionResult{
		Completed: completed,
		Failed:    failed,
		Aborted:   aborted,
	}
}

// depsCompleted returns true if all of op's DependsOn are in the completed state.
func (e *Executor) depsCompleted(op model.Operation, status map[model.OperationID]opStatus) bool {
	for _, dep := range op.DependsOn {
		if status[dep] != opCompleted {
			return false
		}
	}
	return true
}

// abortTransitive performs BFS over the reverse dependency graph starting from
// failedID, marking all transitive dependents as aborted.
func (e *Executor) abortTransitive(
	failedID model.OperationID,
	dependents map[model.OperationID][]model.OperationID,
	status map[model.OperationID]opStatus,
) []model.OperationID {
	var abortedIDs []model.OperationID
	queue := dependents[failedID]
	visited := map[model.OperationID]bool{failedID: true}

	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		if visited[id] {
			continue
		}
		visited[id] = true

		if status[id] == opPending {
			status[id] = opAborted
			abortedIDs = append(abortedIDs, id)
		}
		queue = append(queue, dependents[id]...)
	}
	return abortedIDs
}

// clusterClientFor returns the ClusterClient for the given cluster, or an error
// if none is configured.
func (e *Executor) clusterClientFor(clusterID model.ClusterID) (ports.ClusterClient, error) {
	client, ok := e.clusterClients[clusterID]
	if !ok {
		return nil, fmt.Errorf("no cluster client for %s", clusterID)
	}
	return client, nil
}

// reservationTTL extracts the TTL for a reservation from the snapshot, falling
// back to DefaultReservationTTL if the reservation is not found.
func (e *Executor) reservationTTL(resID model.ReservationID, snapshot worldstate.WorldStateSnapshot) time.Duration {
	if res, ok := snapshot.Reservations[resID]; ok && res.TTLSeconds > 0 {
		return time.Duration(res.TTLSeconds) * time.Second
	}
	return e.cfg.DefaultReservationTTL
}
