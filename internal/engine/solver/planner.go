//go:build solver

package solver

// planner.go — SolverPlanner: CP-SAT-based implementation of ports.Planner.
//
// Plan() runs a four-phase pipeline:
//   1. Build newReplicaDef list from scaling targets (ScaleActionApproved only).
//   2. Build preemptVarDef list respecting C4 priority cascade.
//   3. Compute shadow slots (ShadowCapComputer pre-pass).
//   4. Call BuildAndSolve; translate result or fall back to heuristic on failure.

import (
	"context"
	"log/slog"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"go.opentelemetry.io/otel/trace"

	"github.com/hsubbaraj/schedulator/internal/engine/engineutil"
	"github.com/hsubbaraj/schedulator/internal/engine/rebalancing"
	"github.com/hsubbaraj/schedulator/internal/engine/scaling"
	"github.com/hsubbaraj/schedulator/internal/observability"
	"github.com/hsubbaraj/schedulator/internal/worldstate"
	"github.com/hsubbaraj/schedulator/pkg/model"
	"github.com/hsubbaraj/schedulator/pkg/ports"
)

// SolverPlanner uses Google OR-Tools CP-SAT to jointly optimize placement
// and preemption in a single pass. Falls back to the heuristic planner if
// the solver cannot find a feasible solution within the time budget.
type SolverPlanner struct {
	cfg       SolverConfig
	shadowcap *ShadowCapComputer
	heuristic ports.Planner
	tracer    trace.Tracer

	solveLatency    prometheus.Histogram
	fallbackCounter prometheus.Counter
	unmetDemand     *prometheus.CounterVec
	solutionStatus  *prometheus.CounterVec
}

// NewSolverPlanner creates a SolverPlanner with the given configuration.
// The rebalancing engine parameter is accepted for API symmetry with the
// stub but is unused — shadow slots are computed via ShadowCapComputer.
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
		heuristic: fallback,
		tracer:    tracer,
		solveLatency: observability.MustNewHistogram(reg, prometheus.HistogramOpts{
			Name:    "schedulator_solver_solve_duration_seconds",
			Help:    "Time spent in the CP-SAT solver per planning cycle.",
			Buckets: []float64{0.005, 0.01, 0.05, 0.1, 0.2, 0.5, 1.0},
		}),
		fallbackCounter: observability.MustNewCounter(reg, prometheus.CounterOpts{
			Name: "schedulator_solver_fallback_total",
			Help: "Total number of solver cycles that fell back to the heuristic planner.",
		}),
		unmetDemand: observability.MustNewCounterVec(reg, prometheus.CounterOpts{
			Name: "schedulator_solver_unmet_demand_total",
			Help: "Total replicas that could not be placed by the solver, by app.",
		}, []string{"app_id"}),
		solutionStatus: observability.MustNewCounterVec(reg, prometheus.CounterOpts{
			Name: "schedulator_solver_solution_status_total",
			Help: "Number of solver solutions by status (optimal, feasible, fallback).",
		}, []string{"status"}),
	}
}

// Plan runs the CP-SAT solver to jointly optimize placement and preemption.
// Falls back to the heuristic planner if no feasible solution is found.
//
// Plan is safe for concurrent use: all state derives from the immutable snap.
func (p *SolverPlanner) Plan(
	ctx context.Context,
	snap worldstate.WorldStateSnapshot,
	targets map[model.AppID]scaling.ScalingDecision,
) (ports.PlannerResult, error) {
	ctx, span := p.tracer.Start(ctx, "solver.plan")
	defer span.End()

	start := time.Now()

	// --- Phase 1: Build new-replica list ---
	// Only process apps with ScaleActionApproved and positive demand delta.
	var newReplicas []newReplicaDef
	targetsNeeded := make(map[model.AppID]int)
	minRequestPriority := 3 // sentinel: no requesters yet (priorities are 0–2)

	for appID, sd := range targets {
		if !sd.ScaleActionApproved {
			continue
		}
		current := engineutil.CountActiveReplicas(snap, appID)
		needed := sd.TargetCount - current
		if needed <= 0 {
			continue
		}
		app, ok := snap.Applications[appID]
		if !ok {
			continue
		}
		targetsNeeded[appID] = needed
		for i := 0; i < needed; i++ {
			newReplicas = append(newReplicas, newReplicaDef{
				idx:     len(newReplicas),
				appID:   appID,
				reqGPUs: app.GPUsPerReplica,
			})
		}
		if app.Priority < minRequestPriority {
			minRequestPriority = app.Priority
		}
	}

	// --- Phase 2: Build preemption candidates ---
	// C4: only create p[v] for replicas where victimApp.Priority > minRequestPriority.
	// This encodes the priority cascade: P0 can preempt P1/P2, P1 can preempt P2,
	// P2 cannot preempt anyone.
	var preemptCandidates []preemptVarDef
	if minRequestPriority < 3 { // at least one requester exists
		for _, replicas := range snap.ReplicasByApp {
			for _, r := range replicas {
				if r.Status != model.ReplicaStatusRunning && r.Status != model.ReplicaStatusPending {
					continue
				}
				app, ok := snap.Applications[r.AppID]
				if !ok {
					continue
				}
				// Victim priority must be strictly greater (lower importance) than requester.
				if app.Priority <= minRequestPriority {
					continue
				}
				preemptCandidates = append(preemptCandidates, preemptVarDef{
					replicaID: r.ReplicaID,
					appID:     r.AppID,
					clusterID: r.ClusterID,
					nodeID:    r.NodeID,
					gpus:      r.GPUs,
					priority:  app.Priority,
				})
			}
		}
	}

	// --- Phase 3: Shadow capacity pre-pass ---
	shadowSlots := p.shadowcap.Compute(ctx, snap, p.cfg)

	// --- Phase 4: Solve ---
	var solution CPSATSolution
	if len(newReplicas) == 0 {
		// Nothing to place: return empty optimal result immediately without
		// calling the solver (avoids unnecessary CP-SAT overhead).
		solution = CPSATSolution{
			Assignments:  make(map[int]*nodeAssignment),
			Preemptions:  make(map[model.ReplicaID]bool),
			ClaimedSlots: make(map[string]bool),
			UnmetDemand:  make(map[model.AppID]int),
			Status:       "optimal",
		}
	} else {
		solution = BuildAndSolve(
			ctx,
			newReplicas,
			preemptCandidates,
			shadowSlots,
			targetsNeeded,
			snap,
			p.cfg,
		)
	}

	p.solveLatency.Observe(time.Since(start).Seconds())
	p.solutionStatus.WithLabelValues(solution.Status).Inc()

	// --- Phase 5: Fallback on solver failure ---
	if solution.Status == "fallback" {
		p.fallbackCounter.Inc()
		slog.WarnContext(ctx, "solver: no feasible solution within time budget; falling back to heuristic planner")
		return p.heuristic.Plan(ctx, snap, targets)
	}

	// --- Phase 6: Record unmet demand metrics ---
	for appID, count := range solution.UnmetDemand {
		if count > 0 {
			p.unmetDemand.WithLabelValues(string(appID)).Add(float64(count))
		}
	}

	return TranslateResult(ctx, solution, newReplicas, preemptCandidates, shadowSlots, snap, p.cfg), nil
}

// Name returns the planner identifier.
func (p *SolverPlanner) Name() string { return "solver" }
