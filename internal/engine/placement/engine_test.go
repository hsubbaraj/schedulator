package placement

import (
	"context"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hsubbaraj/schedulator/internal/engine/scaling"
	"github.com/hsubbaraj/schedulator/internal/observability"
	"github.com/hsubbaraj/schedulator/internal/worldstate"
	"github.com/hsubbaraj/schedulator/pkg/model"
)

// ---------- test helpers ----------

func newTestEngine() *PlacementEngine {
	return NewPlacementEngine(
		DefaultPlacementConfig(),
		nil,
		observability.NewNoopTracer(),
		prometheus.NewRegistry(),
	)
}

func newTestEngineWithPreemption(pf preemptionFinder) *PlacementEngine {
	return NewPlacementEngine(
		DefaultPlacementConfig(),
		pf,
		observability.NewNoopTracer(),
		prometheus.NewRegistry(),
	)
}

type mockPreemptionFinder struct {
	plan *model.PreemptionPlan
	err  error
}

func (m *mockPreemptionFinder) FindPreemptionOpportunity(
	_ context.Context,
	_ model.Application,
	_ worldstate.WorldStateSnapshot,
) (*model.PreemptionPlan, error) {
	return m.plan, m.err
}

func baseApp(appID string) model.Application {
	return model.Application{
		AppID:             appID,
		ModelID:           "model-1",
		GPUsPerReplica:    4,
		Priority:          1,
		MinReplicas:       1,
		FailureDomainRule: model.FailureDomainNone,
	}
}

func baseCluster(clusterID string, nodes map[model.NodeID]model.Node) model.Cluster {
	return model.Cluster{
		ClusterID: clusterID,
		Nodes:     nodes,
	}
}

func baseNode(nodeID, clusterID string, totalGPUs, freeGPUs int) model.Node {
	return model.Node{
		NodeID:        nodeID,
		ClusterID:     clusterID,
		TotalGPUs:     totalGPUs,
		AllocatedGPUs: totalGPUs - freeGPUs,
		FreeGPUs:      freeGPUs,
		Status:        model.NodeStatusReady,
	}
}

func baseSnap() worldstate.WorldStateSnapshot {
	return worldstate.WorldStateSnapshot{
		Clusters:            make(map[model.ClusterID]model.Cluster),
		Applications:        make(map[model.AppID]model.Application),
		Replicas:            make(map[model.ReplicaID]model.Replica),
		CacheLocations:      make(map[model.ModelID][]model.CacheLocation),
		PerformanceProfiles: make(map[model.AppID]model.PerformanceProfile),
		VLLMMetrics:         make(map[model.AppID]model.VLLMMetrics),
		Reservations:        make(map[model.ReservationID]model.GPUReservation),
		ScalingHistory:      make(map[model.AppID]model.ScalingHistory),
		TakenAt:             time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC),
	}
}

// ---------- computePackingScore ----------

func TestComputePackingScore_TightestFit(t *testing.T) {
	e := newTestEngine()

	tests := []struct {
		name      string
		freeGPUs  []int // free GPUs on each ready node
		needed    int
		wantScore float64
	}{
		{
			name:      "exact fit returns 1.0",
			freeGPUs:  []int{4},
			needed:    4,
			wantScore: 1.0, // waste=0, 1.0 - 0/8 = 1.0
		},
		{
			name:      "picks tightest among multiple",
			freeGPUs:  []int{8, 6, 4},
			needed:    4,
			wantScore: 1.0, // best_fit=4, waste=0
		},
		{
			name:      "some waste",
			freeGPUs:  []int{8, 6},
			needed:    4,
			wantScore: 0.75, // best_fit=6, waste=2, 1.0 - 2/8 = 0.75
		},
		{
			name:      "maximum waste",
			freeGPUs:  []int{8},
			needed:    1,
			wantScore: 0.125, // waste=7, 1.0 - 7/8 = 0.125
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			nodes := make(map[model.NodeID]model.Node)
			for i, free := range tt.freeGPUs {
				nid := model.NodeID("node-" + string(rune('a'+i)))
				nodes[nid] = baseNode(nid, "c1", 8, free)
			}
			cluster := baseCluster("c1", nodes)
			score := e.computePackingScore(cluster, tt.needed)
			assert.InDelta(t, tt.wantScore, score, 0.001)
		})
	}
}

func TestComputePackingScore_NoFittingNode(t *testing.T) {
	e := newTestEngine()

	tests := []struct {
		name     string
		freeGPUs []int
		needed   int
	}{
		{
			name:     "all nodes too small",
			freeGPUs: []int{2, 3},
			needed:   4,
		},
		{
			name:     "no nodes",
			freeGPUs: nil,
			needed:   4,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			nodes := make(map[model.NodeID]model.Node)
			for i, free := range tt.freeGPUs {
				nid := model.NodeID("node-" + string(rune('a'+i)))
				nodes[nid] = baseNode(nid, "c1", 8, free)
			}
			cluster := baseCluster("c1", nodes)
			score := e.computePackingScore(cluster, tt.needed)
			assert.Equal(t, 0.0, score)
		})
	}
}

// ---------- findBestCluster ----------

func TestFindBestCluster_PrefersWarmCache(t *testing.T) {
	e := newTestEngine()
	app := baseApp("app-1")
	snap := baseSnap()

	// Cluster A: has LOCAL_NVME cache but worse packing (8 free GPUs, waste=4).
	snap.Clusters["cluster-a"] = baseCluster("cluster-a", map[model.NodeID]model.Node{
		"node-a1": baseNode("node-a1", "cluster-a", 8, 8),
	})
	// Cluster B: no cache (REMOTE) but better packing (4 free GPUs, exact fit).
	snap.Clusters["cluster-b"] = baseCluster("cluster-b", map[model.NodeID]model.Node{
		"node-b1": baseNode("node-b1", "cluster-b", 8, 4),
	})

	snap.CacheLocations["model-1"] = []model.CacheLocation{
		{ModelID: "model-1", ClusterID: "cluster-a", NodeID: "node-a1", Tier: model.CacheTierLocalNVMe},
	}
	snap.Applications["app-1"] = app

	clusterID, constraints, ok := e.findBestCluster(context.Background(), app, snap, nil)
	require.True(t, ok)
	assert.Equal(t, "cluster-a", clusterID)
	assert.Equal(t, 4, constraints.RequiredGPUs)
	assert.Contains(t, constraints.PreferredNodes, model.NodeID("node-a1"))
	// Verify: cluster-a score = 500 (NVME) + 50*(1-4/8) + 30*(1-0/8) = 500+25+30 = 555
	// cluster-b score = 0 (REMOTE) + 50*1.0 + 30*(1-4/8) = 0+50+15 = 65
}

func TestFindBestCluster_GPUMemoryDominates(t *testing.T) {
	e := newTestEngine()
	app := baseApp("app-1")
	snap := baseSnap()

	// Cluster A: GPU_MEMORY cache, worst packing.
	snap.Clusters["cluster-a"] = baseCluster("cluster-a", map[model.NodeID]model.Node{
		"node-a1": baseNode("node-a1", "cluster-a", 8, 8),
	})
	// Cluster B: LOCAL_NVME cache, perfect packing.
	snap.Clusters["cluster-b"] = baseCluster("cluster-b", map[model.NodeID]model.Node{
		"node-b1": baseNode("node-b1", "cluster-b", 8, 4),
	})
	// Cluster C: CLUSTER_STORAGE, perfect packing, low utilization.
	snap.Clusters["cluster-c"] = baseCluster("cluster-c", map[model.NodeID]model.Node{
		"node-c1": baseNode("node-c1", "cluster-c", 8, 4),
	})

	snap.CacheLocations["model-1"] = []model.CacheLocation{
		{ModelID: "model-1", ClusterID: "cluster-a", NodeID: "node-a1", Tier: model.CacheTierGPUMemory},
		{ModelID: "model-1", ClusterID: "cluster-b", NodeID: "node-b1", Tier: model.CacheTierLocalNVMe},
		{ModelID: "model-1", ClusterID: "cluster-c", Tier: model.CacheTierClusterStorage},
	}
	snap.Applications["app-1"] = app

	clusterID, _, ok := e.findBestCluster(context.Background(), app, snap, nil)
	require.True(t, ok)
	assert.Equal(t, "cluster-a", clusterID)
	// cluster-a: 1000 + 50*0.5 + 30*1.0 = 1055
	// cluster-b: 500 + 50*1.0 + 30*0.5 = 565
	// cluster-c: 100 + 50*1.0 + 30*0.5 = 165
}

// ---------- selectScaleDownVictims ----------

func TestSelectScaleDownVictims_PrefersHighFragmentation(t *testing.T) {
	e := newTestEngine()
	snap := baseSnap()
	app := baseApp("app-1")
	snap.Applications["app-1"] = app

	// Two clusters with different fragmentation levels.
	snap.Clusters["cluster-a"] = baseCluster("cluster-a", map[model.NodeID]model.Node{
		"node-a1": baseNode("node-a1", "cluster-a", 8, 2), // frag = 1 - 2/8 = 0.75
	})
	snap.Clusters["cluster-b"] = baseCluster("cluster-b", map[model.NodeID]model.Node{
		"node-b1": baseNode("node-b1", "cluster-b", 8, 6), // frag = 1 - 6/8 = 0.25
	})

	snap.Replicas["r1"] = model.Replica{ReplicaID: "r1", AppID: "app-1", ClusterID: "cluster-a", NodeID: "node-a1", Status: model.ReplicaStatusRunning}
	snap.Replicas["r2"] = model.Replica{ReplicaID: "r2", AppID: "app-1", ClusterID: "cluster-b", NodeID: "node-b1", Status: model.ReplicaStatusRunning}

	victims := e.selectScaleDownVictims("app-1", 1, snap)
	require.Len(t, victims, 1)
	assert.Equal(t, model.ReplicaID("r1"), victims[0]) // higher fragmentation removed first
}

// ---------- ComputePlacement ----------

func TestComputePlacement_ScaleDownFirst(t *testing.T) {
	e := newTestEngine()
	snap := baseSnap()
	app := baseApp("app-1")
	snap.Applications["app-1"] = app

	snap.Clusters["cluster-a"] = baseCluster("cluster-a", map[model.NodeID]model.Node{
		"node-a1": baseNode("node-a1", "cluster-a", 8, 4),
	})
	snap.Replicas["r1"] = model.Replica{ReplicaID: "r1", AppID: "app-1", ClusterID: "cluster-a", NodeID: "node-a1", Status: model.ReplicaStatusRunning}
	snap.Replicas["r2"] = model.Replica{ReplicaID: "r2", AppID: "app-1", ClusterID: "cluster-a", NodeID: "node-a1", Status: model.ReplicaStatusRunning}
	snap.Replicas["r3"] = model.Replica{ReplicaID: "r3", AppID: "app-1", ClusterID: "cluster-a", NodeID: "node-a1", Status: model.ReplicaStatusRunning}

	decisions := map[model.AppID]scaling.ScalingDecision{
		"app-1": {
			AppID:               "app-1",
			CurrentCount:        3,
			TargetCount:         1,
			Direction:           scaling.ScaleDirectionDown,
			ScaleActionApproved: true,
		},
	}

	result := e.ComputePlacement(context.Background(), snap, decisions)
	assert.Len(t, result.ScaleDowns, 2)   // 3 - 1 = 2 scale-downs
	assert.Empty(t, result.ScaleUps)       // no scale-ups
	assert.Empty(t, result.Reservations)   // no reservations
}

func TestComputePlacement_FailureDomainSpread(t *testing.T) {
	e := newTestEngine()
	snap := baseSnap()

	app := baseApp("app-1")
	app.FailureDomainRule = model.FailureDomainSpreadClusters
	snap.Applications["app-1"] = app

	// Two clusters, each can hold replicas.
	snap.Clusters["cluster-a"] = baseCluster("cluster-a", map[model.NodeID]model.Node{
		"node-a1": baseNode("node-a1", "cluster-a", 8, 8),
	})
	snap.Clusters["cluster-b"] = baseCluster("cluster-b", map[model.NodeID]model.Node{
		"node-b1": baseNode("node-b1", "cluster-b", 8, 8),
	})

	// Already have 1 replica in cluster-a.
	snap.Replicas["r1"] = model.Replica{
		ReplicaID: "r1", AppID: "app-1", ClusterID: "cluster-a",
		NodeID: "node-a1", Status: model.ReplicaStatusRunning, GPUs: 4,
	}

	decisions := map[model.AppID]scaling.ScalingDecision{
		"app-1": {
			AppID:               "app-1",
			CurrentCount:        1,
			TargetCount:         2,
			Direction:           scaling.ScaleDirectionUp,
			ScaleActionApproved: true,
		},
	}

	result := e.ComputePlacement(context.Background(), snap, decisions)
	require.Len(t, result.ScaleUps, 1)
	// With spread_clusters, 2 replicas across 2 clusters → limit=1 per cluster.
	// cluster-a already has 1, so the new replica goes to cluster-b.
	assert.Equal(t, "cluster-b", result.ScaleUps[0].ClusterID)
}

func TestComputePlacement_CreatesReservations(t *testing.T) {
	e := newTestEngine()
	snap := baseSnap()

	app := baseApp("app-1")
	snap.Applications["app-1"] = app
	snap.PerformanceProfiles["app-1"] = model.PerformanceProfile{
		AppID:            "app-1",
		ColdStartSeconds: 60,
	}

	snap.Clusters["cluster-a"] = baseCluster("cluster-a", map[model.NodeID]model.Node{
		"node-a1": baseNode("node-a1", "cluster-a", 8, 8),
	})

	decisions := map[model.AppID]scaling.ScalingDecision{
		"app-1": {
			AppID:               "app-1",
			CurrentCount:        0,
			TargetCount:         1,
			Direction:           scaling.ScaleDirectionUp,
			ScaleActionApproved: true,
		},
	}

	result := e.ComputePlacement(context.Background(), snap, decisions)
	require.Len(t, result.ScaleUps, 1)
	require.Len(t, result.Reservations, 1)

	res := result.Reservations[0]
	assert.Equal(t, "cluster-a", res.ClusterID)
	assert.Equal(t, 4, res.GPUs)
	assert.Equal(t, "app-1", res.ForAppID)
	assert.Equal(t, 120, res.TTLSeconds) // 2 × 60s cold start
	assert.Equal(t, model.ReservationStatusActive, res.Status)
	assert.Equal(t, result.ScaleUps[0].ReservationID, res.ReservationID)
}

func TestComputePlacement_DelegatesToPreemption(t *testing.T) {
	preemptionPlan := &model.PreemptionPlan{
		Victims: []model.PreemptionDecision{
			{
				VictimReplicaID:    "victim-1",
				VictimAppID:        "low-pri-app",
				NodeID:             "node-a1",
				ClusterID:          "cluster-a",
				GracePeriodSeconds: 10,
				Reason:             "Preempted for higher priority workload",
			},
		},
		TargetCluster: "cluster-a",
		Constraints: model.SchedulingConstraints{
			RequiredGPUs:   4,
			PreferredNodes: []model.NodeID{"node-a1"},
		},
	}
	mock := &mockPreemptionFinder{plan: preemptionPlan}
	e := newTestEngineWithPreemption(mock)
	snap := baseSnap()

	app := baseApp("app-1")
	app.Priority = 0
	snap.Applications["app-1"] = app

	// No clusters with capacity — all nodes full.
	snap.Clusters["cluster-a"] = baseCluster("cluster-a", map[model.NodeID]model.Node{
		"node-a1": baseNode("node-a1", "cluster-a", 8, 0), // fully allocated
	})

	decisions := map[model.AppID]scaling.ScalingDecision{
		"app-1": {
			AppID:               "app-1",
			CurrentCount:        0,
			TargetCount:         1,
			Direction:           scaling.ScaleDirectionUp,
			ScaleActionApproved: true,
		},
	}

	result := e.ComputePlacement(context.Background(), snap, decisions)
	require.Len(t, result.Preemptions, 1)
	assert.Equal(t, "victim-1", result.Preemptions[0].VictimReplicaID)
	require.Len(t, result.ScaleUps, 1)
	assert.Equal(t, "cluster-a", result.ScaleUps[0].ClusterID)
}

func TestComputePlacement_MultipleScaleUps_ReservationTracking(t *testing.T) {
	e := newTestEngine()
	snap := baseSnap()

	app := baseApp("app-1")
	snap.Applications["app-1"] = app

	// Cluster with exactly 8 free GPUs. App needs 4 GPUs each.
	// First scale-up takes 4, second should still fit (8-4=4).
	// A third would not fit.
	snap.Clusters["cluster-a"] = baseCluster("cluster-a", map[model.NodeID]model.Node{
		"node-a1": baseNode("node-a1", "cluster-a", 8, 8),
	})

	decisions := map[model.AppID]scaling.ScalingDecision{
		"app-1": {
			AppID:               "app-1",
			CurrentCount:        0,
			TargetCount:         3,
			Direction:           scaling.ScaleDirectionUp,
			ScaleActionApproved: true,
		},
	}

	result := e.ComputePlacement(context.Background(), snap, decisions)
	// First two replicas fit (4+4=8), third does not (no preemption configured).
	assert.Len(t, result.ScaleUps, 2)
	assert.Len(t, result.Reservations, 2)

	// All placed in cluster-a.
	for _, su := range result.ScaleUps {
		assert.Equal(t, "cluster-a", su.ClusterID)
	}

	// Each reservation has unique ID.
	assert.NotEqual(t, result.Reservations[0].ReservationID, result.Reservations[1].ReservationID)
}

func TestComputePlacement_SkipsUnapprovedDecisions(t *testing.T) {
	e := newTestEngine()
	snap := baseSnap()

	app := baseApp("app-1")
	snap.Applications["app-1"] = app
	snap.Clusters["cluster-a"] = baseCluster("cluster-a", map[model.NodeID]model.Node{
		"node-a1": baseNode("node-a1", "cluster-a", 8, 8),
	})

	decisions := map[model.AppID]scaling.ScalingDecision{
		"app-1": {
			AppID:               "app-1",
			CurrentCount:        1,
			TargetCount:         3,
			Direction:           scaling.ScaleDirectionUp,
			ScaleActionApproved: false, // not approved
		},
	}

	result := e.ComputePlacement(context.Background(), snap, decisions)
	assert.Empty(t, result.ScaleUps)
	assert.Empty(t, result.ScaleDowns)
}

// ---------- computeCacheScore ----------

func TestComputeCacheScore_Tiers(t *testing.T) {
	e := newTestEngine()

	tests := []struct {
		name          string
		locations     []model.CacheLocation
		wantScore     float64
		wantTier      string
		wantPrefNodes int
	}{
		{
			name: "GPU_MEMORY tier",
			locations: []model.CacheLocation{
				{ModelID: "m1", ClusterID: "c1", NodeID: "n1", Tier: model.CacheTierGPUMemory},
				{ModelID: "m1", ClusterID: "c1", NodeID: "n2", Tier: model.CacheTierLocalNVMe},
			},
			wantScore:     1000,
			wantTier:      "GPU_MEMORY",
			wantPrefNodes: 2, // gpu_memory + local_nvme
		},
		{
			name: "LOCAL_NVME tier",
			locations: []model.CacheLocation{
				{ModelID: "m1", ClusterID: "c1", NodeID: "n1", Tier: model.CacheTierLocalNVMe},
			},
			wantScore:     500,
			wantTier:      "LOCAL_NVME",
			wantPrefNodes: 1,
		},
		{
			name: "CLUSTER_STORAGE tier",
			locations: []model.CacheLocation{
				{ModelID: "m1", ClusterID: "c1", Tier: model.CacheTierClusterStorage},
			},
			wantScore:     100,
			wantTier:      "CLUSTER_STORAGE",
			wantPrefNodes: 0,
		},
		{
			name:          "REMOTE (no cache)",
			locations:     nil,
			wantScore:     0,
			wantTier:      "REMOTE",
			wantPrefNodes: 0,
		},
		{
			name: "different cluster ignored",
			locations: []model.CacheLocation{
				{ModelID: "m1", ClusterID: "other-cluster", NodeID: "n1", Tier: model.CacheTierGPUMemory},
			},
			wantScore:     0,
			wantTier:      "REMOTE",
			wantPrefNodes: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			snap := baseSnap()
			snap.CacheLocations["m1"] = tt.locations

			score, preferred, tier := e.computeCacheScore("m1", "c1", snap)
			assert.Equal(t, tt.wantScore, score)
			assert.Equal(t, tt.wantTier, tier)
			assert.Len(t, preferred, tt.wantPrefNodes)
		})
	}
}

// ---------- helpers ----------

func TestCountActiveReplicas(t *testing.T) {
	snap := baseSnap()
	snap.Replicas["r1"] = model.Replica{ReplicaID: "r1", AppID: "app-1", Status: model.ReplicaStatusRunning}
	snap.Replicas["r2"] = model.Replica{ReplicaID: "r2", AppID: "app-1", Status: model.ReplicaStatusPending}
	snap.Replicas["r3"] = model.Replica{ReplicaID: "r3", AppID: "app-1", Status: model.ReplicaStatusTerminated}
	snap.Replicas["r4"] = model.Replica{ReplicaID: "r4", AppID: "app-2", Status: model.ReplicaStatusRunning}

	assert.Equal(t, 2, countActiveReplicas("app-1", snap))
	assert.Equal(t, 1, countActiveReplicas("app-2", snap))
	assert.Equal(t, 0, countActiveReplicas("app-3", snap))
}

func TestHasFittingNode(t *testing.T) {
	tests := []struct {
		name   string
		nodes  map[model.NodeID]model.Node
		needed int
		want   bool
	}{
		{
			name: "has fitting node",
			nodes: map[model.NodeID]model.Node{
				"n1": baseNode("n1", "c1", 8, 4),
			},
			needed: 4,
			want:   true,
		},
		{
			name: "no fitting node",
			nodes: map[model.NodeID]model.Node{
				"n1": baseNode("n1", "c1", 8, 3),
			},
			needed: 4,
			want:   false,
		},
		{
			name: "node not ready",
			nodes: map[model.NodeID]model.Node{
				"n1": {NodeID: "n1", TotalGPUs: 8, FreeGPUs: 8, Status: model.NodeStatusDraining},
			},
			needed: 4,
			want:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cluster := baseCluster("c1", tt.nodes)
			assert.Equal(t, tt.want, hasFittingNode(cluster, tt.needed))
		})
	}
}

func TestClusterAvailableGPUs(t *testing.T) {
	snap := baseSnap()
	snap.Clusters["c1"] = baseCluster("c1", map[model.NodeID]model.Node{
		"n1": baseNode("n1", "c1", 8, 6),
		"n2": baseNode("n2", "c1", 8, 4),
	})
	snap.Reservations["res-1"] = model.GPUReservation{
		ReservationID: "res-1",
		ClusterID:     "c1",
		GPUs:          2,
		Status:        model.ReservationStatusActive,
	}

	// 6+4=10 free, minus 2 reserved = 8.
	local := map[model.ClusterID]int{"c1": 3}
	// 8 - 3 local = 5.
	assert.Equal(t, 5, clusterAvailableGPUs("c1", snap, local))
}

func TestComputeReservationTTL(t *testing.T) {
	e := newTestEngine()

	t.Run("uses cold start seconds", func(t *testing.T) {
		snap := baseSnap()
		snap.PerformanceProfiles["app-1"] = model.PerformanceProfile{
			AppID:            "app-1",
			ColdStartSeconds: 60,
		}
		assert.Equal(t, 120, e.computeReservationTTL("app-1", snap)) // 2 × 60
	})

	t.Run("uses default when no profile", func(t *testing.T) {
		snap := baseSnap()
		assert.Equal(t, 300, e.computeReservationTTL("app-1", snap))
	})

	t.Run("uses default when cold start is zero", func(t *testing.T) {
		snap := baseSnap()
		snap.PerformanceProfiles["app-1"] = model.PerformanceProfile{
			AppID:            "app-1",
			ColdStartSeconds: 0,
		}
		assert.Equal(t, 300, e.computeReservationTTL("app-1", snap))
	})
}
