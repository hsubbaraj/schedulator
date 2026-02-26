package heuristic_test

import (
	"context"
	"sync"
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
	"github.com/hsubbaraj/schedulator/internal/observability"
	"github.com/hsubbaraj/schedulator/internal/worldstate"
	"github.com/hsubbaraj/schedulator/pkg/model"
)

func newTestPlanner(t *testing.T) *heuristic.HeuristicPlanner {
	t.Helper()
	tracer := observability.NewNoopTracer()
	reg := prometheus.NewRegistry()

	preemptionEngine := preemption.NewPreemptionEngine(preemption.DefaultPreemptionConfig(), tracer, reg)
	placementEngine := placement.NewPlacementEngine(placement.DefaultPlacementConfig(), preemptionEngine, tracer, reg)
	rebalancingEngine := rebalancing.NewRebalancingEngine(rebalancing.DefaultRebalancingConfig(), tracer, reg)

	return heuristic.NewHeuristicPlanner(placementEngine, rebalancingEngine, tracer, reg)
}

func baseSnap() worldstate.WorldStateSnapshot {
	return worldstate.WorldStateSnapshot{
		Clusters:            make(map[model.ClusterID]model.Cluster),
		Applications:        make(map[model.AppID]model.Application),
		Replicas:            make(map[model.ReplicaID]model.Replica),
		ReplicasByApp:       make(map[model.AppID][]model.Replica),
		CacheLocations:      make(map[model.ModelID][]model.CacheLocation),
		PerformanceProfiles: make(map[model.AppID]model.PerformanceProfile),
		VLLMMetrics:         make(map[model.AppID]model.VLLMMetrics),
		Reservations:        make(map[model.ReservationID]model.GPUReservation),
		ScalingHistory:      make(map[model.AppID]model.ScalingHistory),
		TakenAt:             time.Now(),
	}
}

func TestHeuristicPlanner_Name(t *testing.T) {
	p := newTestPlanner(t)
	assert.Equal(t, "heuristic", p.Name())
}

func TestHeuristicPlanner_DelegatesPlacement(t *testing.T) {
	p := newTestPlanner(t)

	snap := baseSnap()
	snap.Clusters["c1"] = model.Cluster{
		ClusterID: "c1",
		Nodes: map[model.NodeID]model.Node{
			"n1": {
				NodeID:    "n1",
				ClusterID: "c1",
				TotalGPUs: 8,
				FreeGPUs:  8,
				Status:    model.NodeStatusReady,
			},
		},
	}
	snap.Applications["app-A"] = model.Application{
		AppID:       "app-A",
		ModelID:     "model-X",
		Priority:    0,
		MinReplicas: 0,
	}
	snap.CacheLocations["model-X"] = []model.CacheLocation{
		{ClusterID: "c1", Tier: model.CacheTierGPUMemory},
	}

	targets := map[model.AppID]scaling.ScalingDecision{
		"app-A": {
			AppID:               "app-A",
			Direction:           scaling.ScaleDirectionUp,
			TargetCount:         2,
			CurrentCount:        0,
			ScaleActionApproved: true,
		},
	}

	result, err := p.Plan(context.Background(), snap, targets)
	require.NoError(t, err)
	assert.Len(t, result.Decisions.ScaleUps, 2, "should produce 2 scale-up decisions")
	for _, su := range result.Decisions.ScaleUps {
		assert.Equal(t, model.ClusterID("c1"), su.ClusterID)
	}
	assert.Nil(t, result.Decisions.ClaimedShadowSlots, "heuristic planner never sets ClaimedShadowSlots")
}

func TestHeuristicPlanner_DelegatesPreemption(t *testing.T) {
	p := newTestPlanner(t)

	snap := baseSnap()
	// Cluster full with a P2 replica.
	snap.Clusters["c1"] = model.Cluster{
		ClusterID: "c1",
		Nodes: map[model.NodeID]model.Node{
			"n1": {
				NodeID:        "n1",
				ClusterID:     "c1",
				TotalGPUs:     4,
				AllocatedGPUs: 4,
				FreeGPUs:      0,
				Status:        model.NodeStatusReady,
				Pods:          []model.ReplicaID{"r-p2"},
			},
		},
	}
	snap.Applications["app-P2"] = model.Application{
		AppID:       "app-P2",
		ModelID:     "model-X",
		Priority:    2,
		MinReplicas: 0,
	}
	snap.Applications["app-P0"] = model.Application{
		AppID:          "app-P0",
		ModelID:        "model-X",
		Priority:       0,
		MinReplicas:    0,
		GPUsPerReplica: 4,
	}
	snap.Replicas["r-p2"] = model.Replica{
		ReplicaID: "r-p2",
		AppID:     "app-P2",
		ClusterID: "c1",
		NodeID:    "n1",
		GPUs:      4,
		Status:    model.ReplicaStatusRunning,
	}
	snap.ReplicasByApp["app-P2"] = []model.Replica{snap.Replicas["r-p2"]}
	snap.CacheLocations["model-X"] = []model.CacheLocation{
		{ClusterID: "c1", Tier: model.CacheTierGPUMemory},
	}

	targets := map[model.AppID]scaling.ScalingDecision{
		"app-P0": {
			AppID:               "app-P0",
			Direction:           scaling.ScaleDirectionUp,
			TargetCount:         1,
			CurrentCount:        0,
			ScaleActionApproved: true,
		},
	}

	result, err := p.Plan(context.Background(), snap, targets)
	require.NoError(t, err)
	assert.NotEmpty(t, result.Decisions.Preemptions, "P0 should preempt P2 to gain capacity")
	assert.Equal(t, model.ReplicaID("r-p2"), result.Decisions.Preemptions[0].VictimReplicaID)
}

func TestHeuristicPlanner_IncludesRebalancingMigrations(t *testing.T) {
	p := newTestPlanner(t)

	snap := baseSnap()
	// Source cluster (c1): 1 small node, 8 GPUs, 4 free — frag = 0.5.
	// Two replicas of 2 GPUs each sit on it (above min_replicas=1).
	snap.Clusters["c1"] = model.Cluster{
		ClusterID:          "c1",
		FragmentationScore: 0.5,
		Nodes: map[model.NodeID]model.Node{
			"n1": {
				NodeID:        "n1",
				ClusterID:     "c1",
				TotalGPUs:     8,
				AllocatedGPUs: 4,
				FreeGPUs:      4,
				Status:        model.NodeStatusReady,
				Pods:          []model.ReplicaID{"r1", "r2"},
			},
		},
	}
	// Destination cluster (c2): 3 large nodes of 8 GPUs each (24 total),
	// 22 allocated, 2 free (on n3). Frag ≈ 0.917.
	// This asymmetry makes the fragmentation improvement > MinFragmentationDelta (0.15).
	snap.Clusters["c2"] = model.Cluster{
		ClusterID:          "c2",
		FragmentationScore: float64(22) / float64(24),
		Nodes: map[model.NodeID]model.Node{
			"n3": {NodeID: "n3", ClusterID: "c2", TotalGPUs: 8, AllocatedGPUs: 6, FreeGPUs: 2, Status: model.NodeStatusReady},
			"n4": {NodeID: "n4", ClusterID: "c2", TotalGPUs: 8, AllocatedGPUs: 8, FreeGPUs: 0, Status: model.NodeStatusReady},
			"n5": {NodeID: "n5", ClusterID: "c2", TotalGPUs: 8, AllocatedGPUs: 8, FreeGPUs: 0, Status: model.NodeStatusReady},
		},
	}
	snap.Applications["app-A"] = model.Application{
		AppID:          "app-A",
		ModelID:        "model-X",
		Priority:       1,
		MinReplicas:    1,
		GPUsPerReplica: 2,
	}
	snap.Replicas["r1"] = model.Replica{
		ReplicaID: "r1",
		AppID:     "app-A",
		ClusterID: "c1",
		NodeID:    "n1",
		GPUs:      2,
		Status:    model.ReplicaStatusRunning,
	}
	snap.Replicas["r2"] = model.Replica{
		ReplicaID: "r2",
		AppID:     "app-A",
		ClusterID: "c1",
		NodeID:    "n1",
		GPUs:      2,
		Status:    model.ReplicaStatusRunning,
	}
	snap.ReplicasByApp["app-A"] = []model.Replica{snap.Replicas["r1"], snap.Replicas["r2"]}
	snap.CacheLocations["model-X"] = []model.CacheLocation{
		{ClusterID: "c2", Tier: model.CacheTierGPUMemory},
	}

	result, err := p.Plan(context.Background(), snap, map[model.AppID]scaling.ScalingDecision{})
	require.NoError(t, err)
	assert.NotEmpty(t, result.Migrations, "should include rebalancing migrations")
}

func TestHeuristicPlanner_RaceCondition(t *testing.T) {
	p := newTestPlanner(t)
	snap := baseSnap()

	const goroutines = 10
	var wg sync.WaitGroup
	wg.Add(goroutines)

	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			_, _ = p.Plan(context.Background(), snap, map[model.AppID]scaling.ScalingDecision{})
		}()
	}

	wg.Wait()
}
