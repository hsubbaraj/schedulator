package ports

import (
	"context"

	"github.com/hsubbaraj/schedulator/internal/engine/scaling"
	"github.com/hsubbaraj/schedulator/internal/worldstate"
	"github.com/hsubbaraj/schedulator/pkg/model"
)

// PlannerResult is the unified output of any Planner implementation.
// It contains all information needed by PlanGenerator.GeneratePlan.
type PlannerResult struct {
	Decisions  model.PlacementDecisions
	Migrations []model.MigrateDecision
}

// Planner is the interface satisfied by HeuristicPlanner, SolverPlanner,
// and ShadowPlanner. The control loop calls Plan once per tick.
//
// Plan must be safe to call concurrently from multiple goroutines.
// All state must come from snap; implementations must not hold mutable state
// that is written during Plan.
type Planner interface {
	Plan(
		ctx context.Context,
		snap worldstate.WorldStateSnapshot,
		targets map[model.AppID]scaling.ScalingDecision,
	) (PlannerResult, error)

	// Name returns a short identifier used in metrics labels and logs.
	// Examples: "heuristic", "solver", "shadow:heuristic+solver".
	Name() string
}
