package plangen

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/hsubbaraj/schedulator/internal/observability"
	"github.com/hsubbaraj/schedulator/pkg/model"
)

// PlanGenerator transforms PlacementDecisions and MigrateDecisions into an
// ordered ExecutionPlan with dependency tracking.
type PlanGenerator struct {
	tracer trace.Tracer

	opsCounter       *prometheus.CounterVec
	generateDuration prometheus.Histogram
}

// NewPlanGenerator creates a PlanGenerator with the given tracer and Prometheus
// registerer. The generator is stateless aside from metrics.
func NewPlanGenerator(tracer trace.Tracer, reg prometheus.Registerer) *PlanGenerator {
	return &PlanGenerator{
		tracer: tracer,
		opsCounter: observability.MustNewCounterVec(reg, prometheus.CounterOpts{
			Name: "schedulator_plangen_operations_total",
			Help: "Total number of operations generated, by type.",
		}, []string{"type"}),
		generateDuration: observability.MustNewHistogram(reg, prometheus.HistogramOpts{
			Name:    "schedulator_plangen_generate_duration_seconds",
			Help:    "Time spent generating an execution plan.",
			Buckets: prometheus.DefBuckets,
		}),
	}
}

// GeneratePlan produces an ExecutionPlan from placement decisions and migration
// decisions. Operations are ordered: preemptions → scale-downs → scale-ups →
// migration pairs. Scale-ups depend on preemption ops in the same cluster.
// Migration scale-downs depend on their paired scale-up.
func (g *PlanGenerator) GeneratePlan(
	ctx context.Context,
	decisions model.PlacementDecisions,
	migrations []model.MigrateDecision,
) model.ExecutionPlan {
	ctx, span := g.tracer.Start(ctx, "plangen.generate")
	defer span.End()
	start := time.Now()

	// Track preemption op IDs per cluster for dependency linking.
	clusterPreemptionOps := make(map[model.ClusterID][]model.OperationID)

	// Phase 1: Preemptions.
	var ops []model.Operation
	for _, p := range decisions.Preemptions {
		id := model.OperationID(uuid.New().String())
		ops = append(ops, model.Operation{
			ID:      id,
			Type:    model.OperationPreempt,
			Payload: p,
		})
		clusterPreemptionOps[p.ClusterID] = append(clusterPreemptionOps[p.ClusterID], id)
	}

	// Phase 2: Scale-downs (no dependencies).
	for _, sd := range decisions.ScaleDowns {
		ops = append(ops, model.Operation{
			ID:      model.OperationID(uuid.New().String()),
			Type:    model.OperationScaleDown,
			Payload: sd,
		})
	}

	// Phase 3: Scale-ups (depend on preemptions in the same cluster).
	for _, su := range decisions.ScaleUps {
		op := model.Operation{
			ID:      model.OperationID(uuid.New().String()),
			Type:    model.OperationScaleUp,
			Payload: su,
		}
		if deps := clusterPreemptionOps[su.ClusterID]; len(deps) > 0 {
			op.DependsOn = make([]model.OperationID, len(deps))
			copy(op.DependsOn, deps)
		}
		ops = append(ops, op)
	}

	// Phase 4: Migrations — decompose each into scale-up + scale-down pair.
	// The scale-down depends on the scale-up completing first (blue-green).
	for _, m := range migrations {
		suID := model.OperationID(uuid.New().String())
		sdID := model.OperationID(uuid.New().String())

		suOp := model.Operation{
			ID:   suID,
			Type: model.OperationScaleUp,
			Payload: model.ScaleUpDecision{
				AppID:                 m.AppID,
				ClusterID:             m.TargetClusterID,
				SchedulingConstraints: m.SchedulingConstraints,
			},
		}
		if deps := clusterPreemptionOps[m.TargetClusterID]; len(deps) > 0 {
			suOp.DependsOn = make([]model.OperationID, len(deps))
			copy(suOp.DependsOn, deps)
		}

		sdOp := model.Operation{
			ID:   sdID,
			Type: model.OperationScaleDown,
			Payload: model.ScaleDownDecision{
				AppID:     m.AppID,
				ReplicaID: m.SourceReplicaID,
			},
			DependsOn: []model.OperationID{suID},
		}

		ops = append(ops, suOp, sdOp)
	}

	// Record metrics.
	preemptCount := len(decisions.Preemptions)
	scaleDownCount := len(decisions.ScaleDowns)
	scaleUpCount := len(decisions.ScaleUps)
	migrateCount := len(migrations)

	g.opsCounter.WithLabelValues(string(model.OperationPreempt)).Add(float64(preemptCount))
	g.opsCounter.WithLabelValues(string(model.OperationScaleDown)).Add(float64(scaleDownCount + migrateCount))
	g.opsCounter.WithLabelValues(string(model.OperationScaleUp)).Add(float64(scaleUpCount + migrateCount))

	span.SetAttributes(
		attribute.Int("operations_count", len(ops)),
		attribute.Int("preemptions", preemptCount),
		attribute.Int("scale_downs", scaleDownCount),
		attribute.Int("scale_ups", scaleUpCount),
		attribute.Int("migrations", migrateCount),
	)

	_ = ctx // span already uses ctx; suppress unused warning in future refactors.

	g.generateDuration.Observe(time.Since(start).Seconds())

	return model.ExecutionPlan{Operations: ops}
}
