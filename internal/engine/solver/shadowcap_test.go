package solver

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hsubbaraj/schedulator/internal/worldstate"
	"github.com/hsubbaraj/schedulator/pkg/model"
)

func defaultTestConfig() SolverConfig {
	cfg := DefaultSolverConfig()
	cfg.MaxShadowSlots = 3
	return cfg
}

// buildSnapWithFragment creates a snapshot with one fragmented node in cluster-A
// (partially allocated) and a destination cluster-B with free GPUs and a cached
// model. The replica count for the app exceeds MinReplicas so migration is
// allowed.
func buildSnapWithFragment(replicaCount int, minReplicas int) worldstate.WorldStateSnapshot {
	app := model.Application{
		AppID:       "app-A",
		ModelID:     "model-X",
		MinReplicas: minReplicas,
		Priority:    1,
	}

	snap := worldstate.WorldStateSnapshot{
		Clusters: map[model.ClusterID]model.Cluster{
			"cluster-A": {
				ClusterID:          "cluster-A",
				FragmentationScore: 0.5, // above threshold
				Nodes: map[model.NodeID]model.Node{
					"node-A1": {
						NodeID:        "node-A1",
						ClusterID:     "cluster-A",
						TotalGPUs:     8,
						AllocatedGPUs: 4, // partially allocated — fragmented
						FreeGPUs:      4,
						Status:        model.NodeStatusReady,
						Pods:          []model.ReplicaID{"replica-1"},
					},
				},
			},
			"cluster-B": {
				ClusterID:          "cluster-B",
				FragmentationScore: 0.1, // well-packed destination
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
			"app-A": app,
		},
		Replicas: map[model.ReplicaID]model.Replica{
			"replica-1": {
				ReplicaID: "replica-1",
				AppID:     "app-A",
				ClusterID: "cluster-A",
				NodeID:    "node-A1",
				GPUs:      4,
				Status:    model.ReplicaStatusRunning,
			},
		},
		CacheLocations: map[model.ModelID][]model.CacheLocation{
			"model-X": {
				{ClusterID: "cluster-B", Tier: model.CacheTierGPUMemory},
			},
		},
	}

	// Build ReplicasByApp index.
	snap.ReplicasByApp = map[model.AppID][]model.Replica{}
	for _, r := range snap.Replicas {
		snap.ReplicasByApp[r.AppID] = append(snap.ReplicasByApp[r.AppID], r)
	}

	// Add extra replicas so count exceeds minReplicas.
	for i := 1; i < replicaCount; i++ {
		extraID := model.ReplicaID("replica-extra-" + string(rune('0'+i)))
		snap.Replicas[extraID] = model.Replica{
			ReplicaID: extraID,
			AppID:     "app-A",
			ClusterID: "cluster-A",
			GPUs:      4,
			Status:    model.ReplicaStatusRunning,
		}
		snap.ReplicasByApp["app-A"] = append(snap.ReplicasByApp["app-A"], snap.Replicas[extraID])
	}

	return snap
}

func TestComputeShadowSlots_Empty(t *testing.T) {
	snap := worldstate.WorldStateSnapshot{
		Clusters:     map[model.ClusterID]model.Cluster{},
		Applications: map[model.AppID]model.Application{},
		Replicas:     map[model.ReplicaID]model.Replica{},
		CacheLocations: map[model.ModelID][]model.CacheLocation{},
	}
	cfg := defaultTestConfig()

	slots := ComputeShadowSlots(context.Background(), snap, cfg)
	assert.Empty(t, slots)
}

func TestComputeShadowSlots_FragmentedNode_OneCandidate(t *testing.T) {
	snap := buildSnapWithFragment(2, 1)
	cfg := defaultTestConfig()

	slots := ComputeShadowSlots(context.Background(), snap, cfg)
	require.Len(t, slots, 1)

	slot := slots[0]
	assert.NotEmpty(t, slot.SlotID)
	assert.Equal(t, model.ClusterID("cluster-A"), slot.SourceClusterID)
	assert.Equal(t, 4, slot.ReleasableGPUs)
	assert.Equal(t, model.ReplicaID("replica-1"), slot.VictimReplicaID)
	assert.Equal(t, model.AppID("app-A"), slot.VictimAppID)
	assert.Equal(t, model.ClusterID("cluster-B"), slot.DestClusterID)
	assert.Equal(t, 4, slot.DestConstraints.RequiredGPUs)
}

func TestComputeShadowSlots_MinReplicasProtected(t *testing.T) {
	// Only 1 replica, minReplicas=1 — migration would drop below min.
	snap := buildSnapWithFragment(1, 1)
	cfg := defaultTestConfig()

	slots := ComputeShadowSlots(context.Background(), snap, cfg)
	assert.Empty(t, slots, "should not offer slot when migration would violate min_replicas")
}

func TestComputeShadowSlots_BudgetCapped(t *testing.T) {
	// Build a snapshot with many fragmented nodes.
	cfg := defaultTestConfig()
	cfg.MaxShadowSlots = 3

	snap := worldstate.WorldStateSnapshot{
		Clusters: map[model.ClusterID]model.Cluster{
			"cluster-A": {
				ClusterID:          "cluster-A",
				FragmentationScore: 0.5,
				Nodes:              map[model.NodeID]model.Node{},
			},
			"cluster-B": {
				ClusterID:          "cluster-B",
				FragmentationScore: 0.0,
				Nodes: map[model.NodeID]model.Node{
					"node-B1": {
						NodeID:    "node-B1",
						ClusterID: "cluster-B",
						TotalGPUs: 80,
						FreeGPUs:  80,
						Status:    model.NodeStatusReady,
					},
				},
			},
		},
		Applications: map[model.AppID]model.Application{},
		Replicas:     map[model.ReplicaID]model.Replica{},
		CacheLocations: map[model.ModelID][]model.CacheLocation{
			"model-X": {{ClusterID: "cluster-B", Tier: model.CacheTierGPUMemory}},
		},
		ReplicasByApp: map[model.AppID][]model.Replica{},
	}

	// Add 10 apps/replicas on fragmented nodes.
	for i := 0; i < 10; i++ {
		appID := model.AppID("app-" + string(rune('A'+i)))
		replicaID := model.ReplicaID("replica-" + string(rune('A'+i)))
		nodeID := model.NodeID("node-A-" + string(rune('A'+i)))

		snap.Applications[appID] = model.Application{
			AppID:       appID,
			ModelID:     "model-X",
			MinReplicas: 1,
			Priority:    1,
		}

		snap.Clusters["cluster-A"].Nodes[nodeID] = model.Node{
			NodeID:        nodeID,
			ClusterID:     "cluster-A",
			TotalGPUs:     8,
			AllocatedGPUs: 4,
			FreeGPUs:      4,
			Status:        model.NodeStatusReady,
			Pods:          []model.ReplicaID{replicaID},
		}

		snap.Replicas[replicaID] = model.Replica{
			ReplicaID: replicaID,
			AppID:     appID,
			ClusterID: "cluster-A",
			NodeID:    nodeID,
			GPUs:      4,
			Status:    model.ReplicaStatusRunning,
		}
		// Two replicas so count > minReplicas.
		extra := model.ReplicaID("replica-extra-" + string(rune('A'+i)))
		snap.Replicas[extra] = model.Replica{
			ReplicaID: extra,
			AppID:     appID,
			ClusterID: "cluster-A",
			GPUs:      4,
			Status:    model.ReplicaStatusRunning,
		}
		snap.ReplicasByApp[appID] = []model.Replica{snap.Replicas[replicaID], snap.Replicas[extra]}
	}

	slots := ComputeShadowSlots(context.Background(), snap, cfg)
	assert.LessOrEqual(t, len(slots), cfg.MaxShadowSlots, "result must not exceed MaxShadowSlots")
	assert.Equal(t, cfg.MaxShadowSlots, len(slots), "should return exactly MaxShadowSlots when enough candidates")
}

func TestComputeShadowSlots_NoDestination_Skipped(t *testing.T) {
	// cluster-B has no nodes with free GPUs — no consolidation target.
	snap := worldstate.WorldStateSnapshot{
		Clusters: map[model.ClusterID]model.Cluster{
			"cluster-A": {
				ClusterID:          "cluster-A",
				FragmentationScore: 0.5,
				Nodes: map[model.NodeID]model.Node{
					"node-A1": {
						NodeID:        "node-A1",
						ClusterID:     "cluster-A",
						TotalGPUs:     8,
						AllocatedGPUs: 4,
						FreeGPUs:      4,
						Status:        model.NodeStatusReady,
						Pods:          []model.ReplicaID{"replica-1"},
					},
				},
			},
			"cluster-B": {
				ClusterID:          "cluster-B",
				FragmentationScore: 1.0,
				Nodes: map[model.NodeID]model.Node{
					"node-B1": {
						NodeID:    "node-B1",
						ClusterID: "cluster-B",
						TotalGPUs: 8,
						FreeGPUs:  0, // no capacity
						Status:    model.NodeStatusReady,
					},
				},
			},
		},
		Applications: map[model.AppID]model.Application{
			"app-A": {AppID: "app-A", ModelID: "model-X", MinReplicas: 1},
		},
		Replicas: map[model.ReplicaID]model.Replica{
			"replica-1": {ReplicaID: "replica-1", AppID: "app-A", ClusterID: "cluster-A", NodeID: "node-A1", GPUs: 4, Status: model.ReplicaStatusRunning},
			"replica-2": {ReplicaID: "replica-2", AppID: "app-A", ClusterID: "cluster-A", GPUs: 4, Status: model.ReplicaStatusRunning},
		},
		CacheLocations: map[model.ModelID][]model.CacheLocation{
			"model-X": {{ClusterID: "cluster-B"}},
		},
		ReplicasByApp: map[model.AppID][]model.Replica{
			"app-A": {
				{ReplicaID: "replica-1", AppID: "app-A", ClusterID: "cluster-A", GPUs: 4, Status: model.ReplicaStatusRunning},
				{ReplicaID: "replica-2", AppID: "app-A", ClusterID: "cluster-A", GPUs: 4, Status: model.ReplicaStatusRunning},
			},
		},
	}

	cfg := defaultTestConfig()
	slots := ComputeShadowSlots(context.Background(), snap, cfg)
	assert.Empty(t, slots, "should return empty when no consolidation target exists")
}

func TestComputeShadowSlots_NoCachedModel_Skipped(t *testing.T) {
	snap := buildSnapWithFragment(2, 1)
	// Remove cache location from cluster-B.
	snap.CacheLocations = map[model.ModelID][]model.CacheLocation{}

	cfg := defaultTestConfig()
	slots := ComputeShadowSlots(context.Background(), snap, cfg)
	assert.Empty(t, slots, "should skip when destination has no cached model")
}

func TestComputeShadowSlots_CostAssignment(t *testing.T) {
	snap := buildSnapWithFragment(2, 1)
	cfg := defaultTestConfig()
	cfg.WShadow = 500

	slots := ComputeShadowSlots(context.Background(), snap, cfg)
	require.Len(t, slots, 1)
	assert.Equal(t, 500.0, slots[0].MigrationCost)
}

func TestComputeShadowSlots_ClusterBelowFragThreshold(t *testing.T) {
	snap := buildSnapWithFragment(2, 1)
	// Set fragmentation score below threshold.
	c := snap.Clusters["cluster-A"]
	c.FragmentationScore = 0.05 // below shadowMinFragmentationDelta (0.15)
	snap.Clusters["cluster-A"] = c

	cfg := defaultTestConfig()
	slots := ComputeShadowSlots(context.Background(), snap, cfg)
	assert.Empty(t, slots, "should skip clusters below fragmentation threshold")
}
