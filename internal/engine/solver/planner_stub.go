//go:build !solver

package solver

import (
	"context"
	"errors"
	"log/slog"

	"github.com/prometheus/client_golang/prometheus"
	"go.opentelemetry.io/otel/trace"

	"github.com/hsubbaraj/schedulator/internal/engine/rebalancing"
	"github.com/hsubbaraj/schedulator/internal/engine/scaling"
	"github.com/hsubbaraj/schedulator/internal/observability"
	"github.com/hsubbaraj/schedulator/internal/worldstate"
	"github.com/hsubbaraj/schedulator/pkg/model"
	"github.com/hsubbaraj/schedulator/pkg/ports"
)

// ErrSolverNotAvailable is returned when the solver binary was built without
// the "solver" build tag (i.e. CGO_ENABLED=0 or OR-Tools not installed).
var ErrSolverNotAvailable = errors.New("solver not available: rebuild with CGO_ENABLED=1 and -tags solver")

// SolverPlanner is the stub implementation used when OR-Tools is unavailable.
// It immediately delegates every Plan() call to the fallback planner and
// increments the fallback counter.
type SolverPlanner struct {
	cfg             SolverConfig
	shadowcap       *ShadowCapComputer
	fallback        ports.Planner
	tracer          trace.Tracer
	fallbackCounter prometheus.Counter
}

// NewSolverPlanner returns a SolverPlanner that delegates all planning to the
// provided fallback. The solver is unavailable in this build.
func NewSolverPlanner(
	cfg SolverConfig,
	_ *rebalancing.RebalancingEngine,
	fallback ports.Planner,
	tracer trace.Tracer,
	reg prometheus.Registerer,
) *SolverPlanner {
	return &SolverPlanner{
		cfg:       cfg,
		shadowcap: NewShadowCapComputer(),
		fallback:  fallback,
		tracer:    tracer,
		fallbackCounter: observability.MustNewCounter(reg, prometheus.CounterOpts{
			Name: "schedulator_solver_fallback_total",
			Help: "Total number of solver cycles that fell back to the heuristic planner.",
		}),
	}
}

// Plan delegates to the fallback planner because the solver is not available
// in this build. A warning is logged on each call.
func (p *SolverPlanner) Plan(
	ctx context.Context,
	snap worldstate.WorldStateSnapshot,
	targets map[model.AppID]scaling.ScalingDecision,
) (ports.PlannerResult, error) {
	slog.WarnContext(ctx, "solver not available in this build; falling back to heuristic",
		"reason", ErrSolverNotAvailable.Error())
	p.fallbackCounter.Inc()
	return p.fallback.Plan(ctx, snap, targets)
}

// Name returns the planner identifier.
func (p *SolverPlanner) Name() string { return "solver" }
