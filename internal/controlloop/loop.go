package controlloop

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/hsubbaraj/schedulator/internal/engine/scaling"
	"github.com/hsubbaraj/schedulator/internal/eventlog"
	"github.com/hsubbaraj/schedulator/internal/ingestion"
	"github.com/hsubbaraj/schedulator/internal/observability"
	"github.com/hsubbaraj/schedulator/internal/worldstate"
	"github.com/hsubbaraj/schedulator/pkg/model"
	"github.com/hsubbaraj/schedulator/pkg/ports"
)

// ---------------------------------------------------------------------------
// Local interfaces — package-private, satisfied implicitly by concrete types.
// ---------------------------------------------------------------------------

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

	// Mutators for applying ingested events.
	UpdateVLLMMetrics(m model.VLLMMetrics)
	UpsertCluster(c model.Cluster)
	UpsertNode(n model.Node)
	RemoveNode(clusterID model.ClusterID, nodeID model.NodeID)
	UpsertApplication(app model.Application)
	UpsertReplica(r model.Replica)
	DeleteReplica(replicaID model.ReplicaID)
}

// ---------------------------------------------------------------------------
// Config & struct
// ---------------------------------------------------------------------------

// ControlLoopConfig holds tunable parameters for the control loop.
type ControlLoopConfig struct {
	ReservationExpiryScanInterval time.Duration
}

// ControlLoop orchestrates the full scheduling cycle.
type ControlLoop struct {
	cfg             ControlLoopConfig
	ingester        eventSource
	scalingEngine   scalingComputer
	placementEngine placementComputer
	rebalancer      rebalancer
	planGenerator   planGenerator
	executor        planExecutor
	worldState      worldStateManager
	leaderElector   ports.LeaderElector
	tracer          trace.Tracer
	publisher       cycleSummaryPublisher // optional; may be nil
	eventLogger     cycleEventLogger      // optional; may be nil

	cyclesTotal     *prometheus.CounterVec
	cycleDuration   prometheus.Histogram
	eventsProcessed prometheus.Counter
	isLeaderGauge   prometheus.Gauge
}

// NewControlLoop creates a ControlLoop with all dependencies injected.
// publisher and eventLogger are optional — pass nil to disable cycle summary
// publishing or event logging.
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
	publisher cycleSummaryPublisher,
	eventLogger cycleEventLogger,
	tracer trace.Tracer,
	reg prometheus.Registerer,
) *ControlLoop {
	if cfg.ReservationExpiryScanInterval == 0 {
		cfg.ReservationExpiryScanInterval = 30 * time.Second
	}

	return &ControlLoop{
		cfg:             cfg,
		ingester:        ingester,
		scalingEngine:   scalingEngine,
		placementEngine: placementEngine,
		rebalancer:      rebalancer,
		planGenerator:   planGenerator,
		executor:        executor,
		worldState:      worldState,
		leaderElector:   leaderElector,
		publisher:       publisher,
		eventLogger:     eventLogger,
		tracer:          tracer,

		cyclesTotal: observability.MustNewCounterVec(reg, prometheus.CounterOpts{
			Name: "schedulator_controlloop_cycles_total",
			Help: "Total number of scheduling cycles by outcome.",
		}, []string{"outcome"}),
		cycleDuration: observability.MustNewHistogram(reg, prometheus.HistogramOpts{
			Name:    "schedulator_controlloop_cycle_duration_seconds",
			Help:    "Duration of a single scheduling cycle.",
			Buckets: prometheus.DefBuckets,
		}),
		eventsProcessed: observability.MustNewCounter(reg, prometheus.CounterOpts{
			Name: "schedulator_controlloop_events_processed_total",
			Help: "Total number of events processed.",
		}),
		isLeaderGauge: observability.MustNewGauge(reg, prometheus.GaugeOpts{
			Name: "schedulator_controlloop_leader_is_leader",
			Help: "Whether this instance is the current leader (0 or 1).",
		}),
	}
}

// Run starts the control loop and blocks until the context is cancelled or the
// event channel is closed.
func (cl *ControlLoop) Run(ctx context.Context) error {
	eventCh, err := cl.ingester.Start(ctx)
	if err != nil {
		return err
	}

	go cl.runReservationExpiry(ctx)

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case events, ok := <-eventCh:
			if !ok {
				return nil
			}
			cl.eventsProcessed.Add(float64(len(events)))

			if !cl.leaderElector.IsLeader() {
				cl.isLeaderGauge.Set(0)
				continue
			}
			cl.isLeaderGauge.Set(1)

			if err := cl.runCycle(ctx, events); err != nil {
				slog.ErrorContext(ctx, "cycle failed", "error", err)
			}
		}
	}
}

// runCycle executes a single scheduling cycle.
func (cl *ControlLoop) runCycle(ctx context.Context, events []ingestion.Event) error {
	start := time.Now()
	ctx, span := cl.tracer.Start(ctx, "controlloop.cycle",
		trace.WithAttributes(attribute.Int("event_count", len(events))))
	defer span.End()

	// 0. Apply ingested events to world state before snapshotting.
	cl.applyEvents(ctx, events)

	// 1. Snapshot
	snap := cl.worldState.Snapshot(ctx)

	slog.InfoContext(ctx, "cycle started",
		"events", len(events),
		"clusters", len(snap.Clusters),
		"apps", len(snap.Applications),
		"replicas", len(snap.Replicas),
		"metrics", len(snap.VLLMMetrics),
	)

	// 2. Compute scaling targets
	scalingDecisions := cl.scalingEngine.ComputeTargets(ctx, snap)

	// Log scaling decisions.
	for appID, d := range scalingDecisions {
		if d.Direction != scaling.ScaleDirectionUnchanged {
			slog.InfoContext(ctx, "scaling decision",
				"app_id", appID,
				"current", d.CurrentCount,
				"target", d.TargetCount,
				"direction", d.Direction,
				"signal", d.Signal,
				"approved", d.ScaleActionApproved,
			)
		}
	}

	// 3. Post-process scaling decisions
	cl.applyScalingDecisions(scalingDecisions)

	// 4. Compute placement
	placements := cl.placementEngine.ComputePlacement(ctx, snap, scalingDecisions)

	// 5. Persist reservations
	for _, r := range placements.Reservations {
		if err := cl.worldState.CreateReservation(r); err != nil {
			slog.WarnContext(ctx, "failed to create reservation",
				"reservation_id", r.ReservationID,
				"error", err)
		}
	}

	// 6. Rebalancing
	migrations := cl.rebalancer.FindRebalancingOpportunities(ctx, snap)

	// 7. Generate plan
	plan := cl.planGenerator.GeneratePlan(ctx, placements, migrations)

	slog.InfoContext(ctx, "plan generated",
		"operations", len(plan.Operations),
		"scale_ups", len(placements.ScaleUps),
		"scale_downs", len(placements.ScaleDowns),
		"preemptions", len(placements.Preemptions),
		"migrations", len(migrations),
	)

	// 8. Validate
	if err := validatePlan(plan, snap); err != nil {
		outcome := "validation_failure"
		span.SetAttributes(attribute.String("outcome", outcome))
		cl.cyclesTotal.WithLabelValues(outcome).Inc()
		cl.cycleDuration.Observe(time.Since(start).Seconds())
		slog.ErrorContext(ctx, "plan validation failed", "error", err)
		return err
	}

	// 9. Execute
	result := cl.executor.Execute(ctx, plan, snap)

	// 10. Determine outcome
	outcome := "success"
	if len(result.Failed) > 0 {
		outcome = "execution_failure"
	}

	slog.InfoContext(ctx, "cycle complete",
		"outcome", outcome,
		"completed", len(result.Completed),
		"failed", len(result.Failed),
		"aborted", len(result.Aborted),
		"duration_ms", time.Since(start).Milliseconds(),
	)

	// 11. Record observability
	durationMs := time.Since(start).Milliseconds()
	span.SetAttributes(
		attribute.String("outcome", outcome),
		attribute.Int64("cycle_duration_ms", durationMs),
		attribute.Int("completed_ops", len(result.Completed)),
		attribute.Int("failed_ops", len(result.Failed)),
		attribute.Int("aborted_ops", len(result.Aborted)),
	)
	cl.cyclesTotal.WithLabelValues(outcome).Inc()
	cl.cycleDuration.Observe(time.Since(start).Seconds())

	// 12. Publish cycle summary and log events
	summary := cl.buildCycleSummary(events, scalingDecisions, placements, plan, result, durationMs)
	cl.publishAndLog(ctx, summary, plan, result, placements)

	return nil
}

// buildCycleSummary constructs a CycleSummary from the cycle's computed data.
func (cl *ControlLoop) buildCycleSummary(
	events []ingestion.Event,
	scalingDecisions map[model.AppID]scaling.ScalingDecision,
	placements model.PlacementDecisions,
	plan model.ExecutionPlan,
	result model.ExecutionResult,
	durationMs int64,
) CycleSummary {
	// Deduplicate trigger kinds.
	kindSet := make(map[ingestion.EventKind]struct{})
	for _, ev := range events {
		kindSet[ev.Kind] = struct{}{}
	}
	triggerKinds := make([]ingestion.EventKind, 0, len(kindSet))
	for k := range kindSet {
		triggerKinds = append(triggerKinds, k)
	}

	// Build failed ops with string errors.
	failedOps := make([]failedOpJSON, 0, len(result.Failed))
	for _, f := range result.Failed {
		errStr := ""
		if f.Err != nil {
			errStr = f.Err.Error()
		}
		failedOps = append(failedOps, failedOpJSON{
			OperationID: f.OperationID,
			Type:        string(f.Type),
			Err:         errStr,
		})
	}

	return CycleSummary{
		TriggerKinds:     triggerKinds,
		ScalingDecisions: scalingDecisions,
		Placement: PlacementSummary{
			ScaleUpCount:    len(placements.ScaleUps),
			ScaleDownCount:  len(placements.ScaleDowns),
			PreemptionCount: len(placements.Preemptions),
			ScaleUps:        placements.ScaleUps,
			ScaleDowns:      placements.ScaleDowns,
			Preemptions:     placements.Preemptions,
		},
		Execution: ExecutionSummary{
			CompletedCount: len(result.Completed),
			FailedCount:    len(result.Failed),
			AbortedCount:   len(result.Aborted),
			Completed:      result.Completed,
			Failed:         failedOps,
			Aborted:        result.Aborted,
		},
		CycleDurationMs: durationMs,
	}
}

// publishAndLog publishes the cycle summary via SSE and logs individual
// operations to the event store.
func (cl *ControlLoop) publishAndLog(
	ctx context.Context,
	summary CycleSummary,
	plan model.ExecutionPlan,
	result model.ExecutionResult,
	placements model.PlacementDecisions,
) {
	if cl.publisher != nil {
		cl.publisher.PublishCycleSummary(summary)
	}

	if cl.eventLogger == nil {
		return
	}

	// Build a set of completed operation IDs for lookup.
	completedSet := make(map[model.OperationID]struct{}, len(result.Completed))
	for _, id := range result.Completed {
		completedSet[id] = struct{}{}
	}

	// Log individual completed operations.
	now := time.Now()
	for _, op := range plan.Operations {
		if _, ok := completedSet[op.ID]; !ok {
			continue
		}
		var rec eventlog.EventRecord
		rec.Timestamp = now
		switch op.Type {
		case model.OperationScaleUp:
			if su, ok := op.Payload.(model.ScaleUpDecision); ok {
				rec.Type = "scale_up"
				rec.AppID = su.AppID
				rec.ClusterID = su.ClusterID
				rec.Summary = fmt.Sprintf("Scaled up %s in %s", su.AppID, su.ClusterID)
			}
		case model.OperationScaleDown:
			if sd, ok := op.Payload.(model.ScaleDownDecision); ok {
				rec.Type = "scale_down"
				rec.AppID = sd.AppID
				rec.Summary = fmt.Sprintf("Scaled down replica %s of %s", sd.ReplicaID, sd.AppID)
			}
		case model.OperationPreempt:
			if p, ok := op.Payload.(model.PreemptionDecision); ok {
				rec.Type = "preempt"
				rec.AppID = p.VictimAppID
				rec.ClusterID = p.ClusterID
				rec.Summary = fmt.Sprintf("Preempted %s on %s: %s", p.VictimReplicaID, p.ClusterID, p.Reason)
			}
		default:
			continue
		}
		if rec.Type == "" {
			continue
		}
		if err := cl.eventLogger.LogEvent(ctx, rec); err != nil {
			slog.WarnContext(ctx, "failed to log event", "type", rec.Type, "error", err)
		}
	}

	// Log a cycle_complete event.
	detailJSON, _ := json.Marshal(summary)
	if err := cl.eventLogger.LogEvent(ctx, eventlog.EventRecord{
		Timestamp:  now,
		Type:       "cycle_complete",
		Summary:    fmt.Sprintf("+%d scale-up, -%d scale-down, %d preempt", len(placements.ScaleUps), len(placements.ScaleDowns), len(placements.Preemptions)),
		DetailJSON: string(detailJSON),
	}); err != nil {
		slog.WarnContext(ctx, "failed to log cycle_complete", "error", err)
	}
}

// applyEvents processes ingested events and applies them to world state so that
// the subsequent snapshot reflects the latest cluster, metrics, and config state.
func (cl *ControlLoop) applyEvents(ctx context.Context, events []ingestion.Event) {
	for _, ev := range events {
		switch ev.Kind {
		case ingestion.EventMetricsUpdate:
			if ev.VLLMMetrics != nil {
				cl.worldState.UpdateVLLMMetrics(*ev.VLLMMetrics)
			}
		case ingestion.EventClusterEvent:
			if ev.ClusterEvent != nil {
				cl.applyClusterEvent(ctx, *ev.ClusterEvent)
			}
		case ingestion.EventAppConfigUpdate:
			if ev.Application != nil {
				cl.worldState.UpsertApplication(*ev.Application)
				slog.InfoContext(ctx, "applied app config update", "app_id", ev.Application.AppID)
			}
		case ingestion.EventPeriodicEval, ingestion.EventSLABreach:
			// These are trigger events only — no state mutation needed.
		}
	}
}

// applyClusterEvent translates a ClusterEvent into world state mutations.
func (cl *ControlLoop) applyClusterEvent(_ context.Context, ce model.ClusterEvent) {
	switch ce.Kind {
	case model.ClusterEventNodeUp:
		cl.worldState.UpsertNode(model.Node{
			NodeID:       ce.NodeID,
			ClusterID:    ce.ClusterID,
			TotalGPUs:    ce.TotalGPUs,
			FreeGPUs:     ce.TotalGPUs,
			CachedModels: make(map[model.ModelID]struct{}),
			Status:       model.NodeStatusReady,
		})
	case model.ClusterEventNodeDown:
		cl.worldState.RemoveNode(ce.ClusterID, ce.NodeID)
	case model.ClusterEventPodRunning:
		if ce.ReplicaID != "" {
			cl.worldState.UpsertReplica(model.Replica{
				ReplicaID: ce.ReplicaID,
				AppID:     ce.AppID,
				ClusterID: ce.ClusterID,
				NodeID:    ce.NodeID,
				GPUs:      ce.GPUs,
				Status:    model.ReplicaStatusRunning,
				CreatedAt: ce.Timestamp,
			})
		}
	case model.ClusterEventPodTerminated:
		if ce.ReplicaID != "" {
			cl.worldState.DeleteReplica(ce.ReplicaID)
		}
	}
}

// applyScalingDecisions updates world state based on scaling decisions.
func (cl *ControlLoop) applyScalingDecisions(decisions map[model.AppID]scaling.ScalingDecision) {
	for appID, d := range decisions {
		if d.ScaleActionApproved {
			cl.worldState.RecordScaleEvent(appID, string(d.Direction), d.ActionAt)
		} else if d.ScaleDownSignalObserved {
			slog.Info("incrementing scale-down signal", "app_id", appID, "current", d.CurrentCount, "target", d.TargetCount)
			cl.worldState.IncrementScaleDownSignal(appID)
		}
	}
}

// runReservationExpiry periodically sweeps stale reservations.
func (cl *ControlLoop) runReservationExpiry(ctx context.Context) {
	ticker := time.NewTicker(cl.cfg.ReservationExpiryScanInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			expired := cl.worldState.ExpireStaleReservations(time.Now())
			if expired > 0 {
				slog.InfoContext(ctx, "expired stale reservations", "count", expired)
			}
		}
	}
}
