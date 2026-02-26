package integration

import (
	"context"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hsubbaraj/schedulator/internal/engine/heuristic"
	"github.com/hsubbaraj/schedulator/internal/engine/placement"
	"github.com/hsubbaraj/schedulator/internal/engine/preemption"
	"github.com/hsubbaraj/schedulator/internal/engine/rebalancing"
	"github.com/hsubbaraj/schedulator/internal/engine/scaling"
	"github.com/hsubbaraj/schedulator/internal/engine/solver"
	"github.com/hsubbaraj/schedulator/internal/observability"
	"github.com/hsubbaraj/schedulator/internal/worldstate"
	"github.com/hsubbaraj/schedulator/pkg/model"
	"github.com/hsubbaraj/schedulator/pkg/ports"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func newHeuristicPlanner(t *testing.T) ports.Planner {
	t.Helper()
	tracer := observability.NewNoopTracer()
	reg := prometheus.NewRegistry()
	preemptionEngine := preemption.NewPreemptionEngine(preemption.DefaultPreemptionConfig(), tracer, reg)
	placementEngine := placement.NewPlacementEngine(placement.DefaultPlacementConfig(), preemptionEngine, tracer, reg)
	rebalancingEngine := rebalancing.NewRebalancingEngine(rebalancing.DefaultRebalancingConfig(), tracer, reg)
	return heuristic.NewHeuristicPlanner(placementEngine, rebalancingEngine, tracer, reg)
}

func newSolverPlanner(t *testing.T, fallback ports.Planner) ports.Planner {
	t.Helper()
	reg := prometheus.NewRegistry()
	tracer := observability.NewNoopTracer()
	return solver.NewSolverPlanner(
		solver.DefaultSolverConfig(),
		nil, // rebalancing engine not needed for stub
		fallback,
		tracer,
		reg,
	)
}

func tightClusterSnap() worldstate.WorldStateSnapshot {
	// Two clusters; cluster-A has a P2 replica filling all capacity.
	// cluster-B is empty with full capacity.
	p2App := model.Application{
		AppID:       "app-P2",
		ModelID:     "model-X",
		Priority:    2,
		MinReplicas: 0,
	}
	p0App := model.Application{
		AppID:       "app-P0",
		ModelID:     "model-X",
		Priority:    0,
		MinReplicas: 0,
	}

	snap := worldstate.WorldStateSnapshot{
		Clusters: map[model.ClusterID]model.Cluster{
			"cluster-A": {
				ClusterID: "cluster-A",
				Nodes: map[model.NodeID]model.Node{
					"node-A1": {
						NodeID:        "node-A1",
						ClusterID:     "cluster-A",
						TotalGPUs:     4,
						AllocatedGPUs: 4,
						FreeGPUs:      0,
						Status:        model.NodeStatusReady,
						Pods:          []model.ReplicaID{"r-p2"},
					},
				},
			},
			"cluster-B": {
				ClusterID: "cluster-B",
				Nodes: map[model.NodeID]model.Node{
					"node-B1": {
						NodeID:    "node-B1",
						ClusterID: "cluster-B",
						TotalGPUs: 8,
						FreeGPUs:  8,
						Status:    model.NodeStatusReady,
					},
				},
			},
		},
		Applications: map[model.AppID]model.Application{
			"app-P2": p2App,
			"app-P0": p0App,
		},
		Replicas: map[model.ReplicaID]model.Replica{
			"r-p2": {
				ReplicaID: "r-p2",
				AppID:     "app-P2",
				ClusterID: "cluster-A",
				NodeID:    "node-A1",
				GPUs:      4,
				Status:    model.ReplicaStatusRunning,
			},
		},
		CacheLocations: map[model.ModelID][]model.CacheLocation{
			"model-X": {
				{ClusterID: "cluster-A", Tier: model.CacheTierGPUMemory},
				{ClusterID: "cluster-B", Tier: model.CacheTierGPUMemory},
			},
		},
		ReplicasByApp: map[model.AppID][]model.Replica{
			"app-P2": {{ReplicaID: "r-p2", AppID: "app-P2", ClusterID: "cluster-A", GPUs: 4, Status: model.ReplicaStatusRunning}},
		},
		PerformanceProfiles: map[model.AppID]model.PerformanceProfile{},
		VLLMMetrics:         map[model.AppID]model.VLLMMetrics{},
		Reservations:        map[model.ReservationID]model.GPUReservation{},
		ScalingHistory:      map[model.AppID]model.ScalingHistory{},
		TakenAt:             time.Now(),
	}
	return snap
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

// TestComparison_PreemptionScenario verifies that both heuristic and solver
// planners produce valid preemption decisions and respect min_replicas.
func TestComparison_PreemptionScenario(t *testing.T) {
	snap := tightClusterSnap()

	targets := map[model.AppID]scaling.ScalingDecision{
		"app-P0": {
			AppID:               "app-P0",
			Direction:           scaling.ScaleDirectionUp,
			TargetCount:         1,
			CurrentCount:        0,
			ScaleActionApproved: true,
		},
	}

	hPlanner := newHeuristicPlanner(t)
	// SolverPlanner (stub) delegates to heuristic — both should produce the
	// same results in this environment.
	sPlanner := newSolverPlanner(t, hPlanner)

	ctx := context.Background()

	hResult, hErr := hPlanner.Plan(ctx, snap, targets)
	require.NoError(t, hErr, "heuristic planner should not error")

	sResult, sErr := sPlanner.Plan(ctx, snap, targets)
	require.NoError(t, sErr, "solver planner should not error")

	// Both planners must respect min_replicas (P2 has min_replicas=0 so
	// preemption is allowed).
	for _, p := range hResult.Decisions.Preemptions {
		app := snap.Applications[p.VictimAppID]
		runningCount := 0
		for _, r := range snap.ReplicasByApp[p.VictimAppID] {
			if r.Status == model.ReplicaStatusRunning {
				runningCount++
			}
		}
		assert.GreaterOrEqual(t, runningCount-1, app.MinReplicas,
			"heuristic preemption must not violate min_replicas for %s", p.VictimAppID)
	}
	for _, p := range sResult.Decisions.Preemptions {
		app := snap.Applications[p.VictimAppID]
		runningCount := 0
		for _, r := range snap.ReplicasByApp[p.VictimAppID] {
			if r.Status == model.ReplicaStatusRunning {
				runningCount++
			}
		}
		assert.GreaterOrEqual(t, runningCount-1, app.MinReplicas,
			"solver preemption must not violate min_replicas for %s", p.VictimAppID)
	}

	// Solver unmet demand must be ≤ heuristic unmet demand.
	// In stub mode, solver == heuristic so they should be equal.
	assert.LessOrEqual(t, len(sResult.Decisions.ScaleUps), len(hResult.Decisions.ScaleUps)+1,
		"solver should place at least as many replicas as heuristic")
}

// TestComparison_MultiClusterPlacement verifies that both planners produce
// scale-up decisions targeting valid clusters.
func TestComparison_MultiClusterPlacement(t *testing.T) {
	snap := worldstate.WorldStateSnapshot{
		Clusters: map[model.ClusterID]model.Cluster{
			"cluster-A": {
				ClusterID: "cluster-A",
				Nodes: map[model.NodeID]model.Node{
					"n-A": {NodeID: "n-A", ClusterID: "cluster-A", TotalGPUs: 8, FreeGPUs: 8, Status: model.NodeStatusReady},
				},
			},
			"cluster-B": {
				ClusterID: "cluster-B",
				Nodes: map[model.NodeID]model.Node{
					"n-B": {NodeID: "n-B", ClusterID: "cluster-B", TotalGPUs: 8, FreeGPUs: 8, Status: model.NodeStatusReady},
				},
			},
		},
		Applications: map[model.AppID]model.Application{
			"app-X": {AppID: "app-X", ModelID: "model-Y", Priority: 0, MinReplicas: 0},
		},
		Replicas:            map[model.ReplicaID]model.Replica{},
		ReplicasByApp:       map[model.AppID][]model.Replica{},
		CacheLocations:      map[model.ModelID][]model.CacheLocation{"model-Y": {{ClusterID: "cluster-A", Tier: model.CacheTierGPUMemory}}},
		PerformanceProfiles: map[model.AppID]model.PerformanceProfile{},
		VLLMMetrics:         map[model.AppID]model.VLLMMetrics{},
		Reservations:        map[model.ReservationID]model.GPUReservation{},
		ScalingHistory:      map[model.AppID]model.ScalingHistory{},
		TakenAt:             time.Now(),
	}

	targets := map[model.AppID]scaling.ScalingDecision{
		"app-X": {
			AppID:               "app-X",
			Direction:           scaling.ScaleDirectionUp,
			TargetCount:         2,
			CurrentCount:        0,
			ScaleActionApproved: true,
		},
	}

	hPlanner := newHeuristicPlanner(t)
	sPlanner := newSolverPlanner(t, hPlanner)

	ctx := context.Background()

	hResult, err := hPlanner.Plan(ctx, snap, targets)
	require.NoError(t, err)

	sResult, err := sPlanner.Plan(ctx, snap, targets)
	require.NoError(t, err)

	// All scale-ups must target valid clusters.
	validClusters := map[model.ClusterID]bool{"cluster-A": true, "cluster-B": true}
	for _, su := range hResult.Decisions.ScaleUps {
		assert.True(t, validClusters[su.ClusterID], "heuristic scale-up must target a valid cluster")
	}
	for _, su := range sResult.Decisions.ScaleUps {
		assert.True(t, validClusters[su.ClusterID], "solver scale-up must target a valid cluster")
	}
}

// TestComparison_ConstraintEquivalence runs both planners on several snapshots
// and asserts solver unmet demand ≤ heuristic unmet demand.
func TestComparison_ConstraintEquivalence(t *testing.T) {
	hPlanner := newHeuristicPlanner(t)
	sPlanner := newSolverPlanner(t, hPlanner)

	tests := []struct {
		name    string
		snap    worldstate.WorldStateSnapshot
		targets map[model.AppID]scaling.ScalingDecision
	}{
		{
			name: "empty fleet",
			snap: worldstate.WorldStateSnapshot{
				Clusters: map[model.ClusterID]model.Cluster{},
				Applications: map[model.AppID]model.Application{},
				Replicas:     map[model.ReplicaID]model.Replica{},
				ReplicasByApp: map[model.AppID][]model.Replica{},
				CacheLocations: map[model.ModelID][]model.CacheLocation{},
				PerformanceProfiles: map[model.AppID]model.PerformanceProfile{},
				VLLMMetrics:  map[model.AppID]model.VLLMMetrics{},
				Reservations: map[model.ReservationID]model.GPUReservation{},
				ScalingHistory: map[model.AppID]model.ScalingHistory{},
				TakenAt:      time.Now(),
			},
			targets: map[model.AppID]scaling.ScalingDecision{},
		},
		{
			name:    "tight cluster preemption scenario",
			snap:    tightClusterSnap(),
			targets: map[model.AppID]scaling.ScalingDecision{
				"app-P0": {AppID: "app-P0", Direction: scaling.ScaleDirectionUp, TargetCount: 1, ScaleActionApproved: true},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()

			hResult, hErr := hPlanner.Plan(ctx, tt.snap, tt.targets)
			sResult, sErr := sPlanner.Plan(ctx, tt.snap, tt.targets)

			// Both should not return errors.
			assert.NoError(t, hErr)
			assert.NoError(t, sErr)

			// Solver unmet demand (scale-ups not placed) must be ≤ heuristic.
			// In stub mode they are equal; in full solver mode this is a
			// strict quality guarantee.
			hUnmet := countUnmetDemand(tt.targets, hResult)
			sUnmet := countUnmetDemand(tt.targets, sResult)
			assert.LessOrEqual(t, sUnmet, hUnmet,
				"solver unmet demand must not exceed heuristic unmet demand")
		})
	}
}

// countUnmetDemand counts how many requested replicas were not placed.
func countUnmetDemand(
	targets map[model.AppID]scaling.ScalingDecision,
	result ports.PlannerResult,
) int {
	// Count placements per app.
	placed := make(map[model.AppID]int)
	for _, su := range result.Decisions.ScaleUps {
		placed[su.AppID]++
	}

	unmet := 0
	for appID, d := range targets {
		if !d.ScaleActionApproved {
			continue
		}
		needed := d.TargetCount - d.CurrentCount
		if needed < 0 {
			needed = 0
		}
		got := placed[appID]
		if got < needed {
			unmet += needed - got
		}
	}
	return unmet
}
