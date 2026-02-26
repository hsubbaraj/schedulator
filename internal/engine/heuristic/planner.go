package heuristic

import (
	"context"

	"github.com/prometheus/client_golang/prometheus"
	"go.opentelemetry.io/otel/trace"

	"github.com/hsubbaraj/schedulator/internal/engine/placement"
	"github.com/hsubbaraj/schedulator/internal/engine/rebalancing"
	"github.com/hsubbaraj/schedulator/internal/engine/scaling"
	"github.com/hsubbaraj/schedulator/internal/worldstate"
	"github.com/hsubbaraj/schedulator/pkg/model"
	"github.com/hsubbaraj/schedulator/pkg/ports"
)

// HeuristicPlanner wraps PlacementEngine, PreemptionEngine (embedded in
// PlacementEngine), and RebalancingEngine. It delegates to each in turn and
// bundles results into a PlannerResult. This is a thin adapter — no logic
// changes to any underlying engine.
type HeuristicPlanner struct {
	placement   *placement.PlacementEngine
	rebalancing *rebalancing.RebalancingEngine
	tracer      trace.Tracer
}

// NewHeuristicPlanner returns a HeuristicPlanner. The reg parameter is
// accepted for interface compatibility but currently unused — latency is
// tracked via OTel spans; planner-level metrics are emitted by ShadowMetrics
// when running in shadow mode.
func NewHeuristicPlanner(
	placementEngine *placement.PlacementEngine,
	rebalancingEngine *rebalancing.RebalancingEngine,
	tracer trace.Tracer,
	_ prometheus.Registerer,
) *HeuristicPlanner {
	return &HeuristicPlanner{
		placement:   placementEngine,
		rebalancing: rebalancingEngine,
		tracer:      tracer,
	}
}

// Plan delegates to PlacementEngine.ComputePlacement (which internally calls
// PreemptionEngine on capacity failure) and then to
// RebalancingEngine.FindRebalancingOpportunities. ClaimedShadowSlots is always
// nil for the heuristic planner.
func (p *HeuristicPlanner) Plan(
	ctx context.Context,
	snap worldstate.WorldStateSnapshot,
	targets map[model.AppID]scaling.ScalingDecision,
) (ports.PlannerResult, error) {
	_, span := p.tracer.Start(ctx, "heuristic.plan")
	defer span.End()

	decisions := p.placement.ComputePlacement(ctx, snap, targets)
	migrations := p.rebalancing.FindRebalancingOpportunities(ctx, snap)

	return ports.PlannerResult{
		Decisions:  decisions,
		Migrations: migrations,
	}, nil
}

// Name returns the planner identifier.
func (p *HeuristicPlanner) Name() string { return "heuristic" }
