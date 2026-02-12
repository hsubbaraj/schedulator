package executor

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/hsubbaraj/schedulator/internal/worldstate"
	"github.com/hsubbaraj/schedulator/pkg/model"
	"github.com/hsubbaraj/schedulator/pkg/ports"
)

// dispatchOp routes an operation to the appropriate handler based on its payload type.
func (e *Executor) dispatchOp(ctx context.Context, op model.Operation, snapshot worldstate.WorldStateSnapshot) error {
	start := time.Now()
	opType := string(op.Type)

	ctx, span := e.tracer.Start(ctx, "executor.dispatch_op",
		trace.WithAttributes(
			attribute.String("op.id", string(op.ID)),
			attribute.String("op.type", opType),
		))
	defer span.End()

	var err error
	switch d := op.Payload.(type) {
	case model.ScaleUpDecision:
		span.SetAttributes(attribute.String("cluster.id", string(d.ClusterID)))
		client, cerr := e.clusterClientFor(d.ClusterID)
		if cerr != nil {
			err = cerr
		} else {
			ttl := e.reservationTTL(d.ReservationID, snapshot)
			err = e.scaleUp(ctx, client, d, ttl)
		}

	case model.PreemptionDecision:
		span.SetAttributes(attribute.String("cluster.id", string(d.ClusterID)))
		client, cerr := e.clusterClientFor(d.ClusterID)
		if cerr != nil {
			err = cerr
		} else {
			err = e.preempt(ctx, client, d)
		}

	case model.ScaleDownDecision:
		replica, ok := snapshot.Replicas[d.ReplicaID]
		if !ok {
			err = fmt.Errorf("replica %s not found in snapshot for scale-down", d.ReplicaID)
		} else {
			span.SetAttributes(attribute.String("cluster.id", string(replica.ClusterID)))
			client, cerr := e.clusterClientFor(replica.ClusterID)
			if cerr != nil {
				err = cerr
			} else {
				err = e.scaleDown(ctx, client, d)
			}
		}

	default:
		err = fmt.Errorf("unsupported operation payload type %T", op.Payload)
	}

	status := "success"
	if err != nil {
		status = "error"
		span.RecordError(err)
	}
	e.opDuration.WithLabelValues(opType, status).Observe(time.Since(start).Seconds())
	return err
}

// scaleUp creates a new replica, waits for it to become Running, and fulfills
// the associated GPU reservation. On timeout, the reservation is expired and
// the orphan replica is cleaned up.
func (e *Executor) scaleUp(ctx context.Context, client ports.ClusterClient, decision model.ScaleUpDecision, ttl time.Duration) error {
	replicaID, err := client.CreateReplica(ctx, decision.AppID, decision.SchedulingConstraints)
	if err != nil {
		return fmt.Errorf("create replica for %s: %w", decision.AppID, err)
	}

	waitCtx, cancel := context.WithTimeout(ctx, ttl)
	defer cancel()

	err = e.waitForRunning(waitCtx, client, replicaID)
	if err != nil {
		// Timeout or other failure — expire reservation and clean up orphan.
		slog.WarnContext(ctx, "scale-up wait failed, expiring reservation",
			"replica_id", replicaID, "reservation_id", decision.ReservationID, "error", err)

		e.reservationsExpired.Inc()
		if expErr := e.worldState.ExpireReservation(decision.ReservationID); expErr != nil {
			slog.WarnContext(ctx, "expire reservation failed", "reservation_id", decision.ReservationID, "error", expErr)
		}
		if delErr := client.DeleteReplica(ctx, replicaID); delErr != nil {
			slog.WarnContext(ctx, "cleanup orphan replica failed", "replica_id", replicaID, "error", delErr)
		}
		return fmt.Errorf("wait for running replica %s: %w", replicaID, err)
	}

	// Replica is Running — fulfill the reservation.
	if fulfillErr := e.worldState.FulfillReservation(decision.ReservationID); fulfillErr != nil {
		// Log but don't fail — the replica is already running.
		slog.WarnContext(ctx, "fulfill reservation failed (replica is running)",
			"reservation_id", decision.ReservationID, "error", fulfillErr)
	}
	return nil
}

// preempt labels a victim replica as draining, deletes it, and waits for
// deletion to complete.
func (e *Executor) preempt(ctx context.Context, client ports.ClusterClient, decision model.PreemptionDecision) error {
	if err := client.LabelReplica(ctx, decision.VictimReplicaID, map[string]string{"draining": "true"}); err != nil {
		return fmt.Errorf("label replica %s as draining: %w", decision.VictimReplicaID, err)
	}

	if err := client.DeleteReplica(ctx, decision.VictimReplicaID); err != nil {
		return fmt.Errorf("delete preempted replica %s: %w", decision.VictimReplicaID, err)
	}

	timeout := e.cfg.WaitForDeletionTimeout + time.Duration(decision.GracePeriodSeconds)*time.Second
	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	if err := e.waitForDeletion(waitCtx, client, decision.VictimReplicaID); err != nil {
		return fmt.Errorf("wait for deletion of preempted replica %s: %w", decision.VictimReplicaID, err)
	}
	return nil
}

// scaleDown deletes a replica and waits for deletion to complete.
func (e *Executor) scaleDown(ctx context.Context, client ports.ClusterClient, decision model.ScaleDownDecision) error {
	if err := client.DeleteReplica(ctx, decision.ReplicaID); err != nil {
		return fmt.Errorf("delete replica %s: %w", decision.ReplicaID, err)
	}

	waitCtx, cancel := context.WithTimeout(ctx, e.cfg.WaitForDeletionTimeout)
	defer cancel()

	if err := e.waitForDeletion(waitCtx, client, decision.ReplicaID); err != nil {
		return fmt.Errorf("wait for deletion of replica %s: %w", decision.ReplicaID, err)
	}
	return nil
}

// waitForRunning polls GetReplicaStatus until the replica is Running or the
// context is cancelled. Uses time.NewTimer for testability — no time.Sleep.
func (e *Executor) waitForRunning(ctx context.Context, client ports.ClusterClient, replicaID model.ReplicaID) error {
	_, span := e.tracer.Start(ctx, "executor.wait_for_running",
		trace.WithAttributes(attribute.String("replica.id", string(replicaID))))
	defer span.End()

	for {
		status, err := client.GetReplicaStatus(ctx, replicaID)
		if err != nil {
			return fmt.Errorf("get status of replica %s: %w", replicaID, err)
		}
		if status == model.ReplicaStatusRunning {
			return nil
		}
		if status == model.ReplicaStatusTerminated {
			return fmt.Errorf("replica %s terminated unexpectedly", replicaID)
		}

		timer := time.NewTimer(e.cfg.PollInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

// waitForDeletion polls GetReplicaStatus until the replica is Terminated or the
// status check returns an error (indicating the replica no longer exists), or
// the context is cancelled.
func (e *Executor) waitForDeletion(ctx context.Context, client ports.ClusterClient, replicaID model.ReplicaID) error {
	_, span := e.tracer.Start(ctx, "executor.wait_for_deletion",
		trace.WithAttributes(attribute.String("replica.id", string(replicaID))))
	defer span.End()

	for {
		status, err := client.GetReplicaStatus(ctx, replicaID)
		if err != nil {
			// Replica no longer exists — deletion complete.
			return nil
		}
		if status == model.ReplicaStatusTerminated {
			return nil
		}

		timer := time.NewTimer(e.cfg.PollInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}
