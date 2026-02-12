package rebalancing

import (
	"context"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hsubbaraj/schedulator/internal/observability"
	"github.com/hsubbaraj/schedulator/internal/worldstate"
	"github.com/hsubbaraj/schedulator/pkg/model"
)

// ---------- test helpers ----------

func newTestEngine(t *testing.T) *RebalancingEngine {
	t.Helper()
	return NewRebalancingEngine(
		DefaultRebalancingConfig(),
		observability.NewNoopTracer(),
		prometheus.NewRegistry(),
	)
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

func baseApp(id string, priority, gpus, minReplicas int) model.Application {
	return model.Application{
		AppID:          id,
		ModelID:        "model-" + id,
		GPUsPerReplica: gpus,
		Priority:       priority,
		MinReplicas:    minReplicas,
	}
}

func baseCluster(id string, nodes map[model.NodeID]model.Node) model.Cluster {
	// Compute FragmentationScore the same way worldstate does.
	totalCapacity := 0
	totalFree := 0
	for _, n := range nodes {
		totalCapacity += n.TotalGPUs
		totalFree += n.FreeGPUs
	}
	frag := 0.0
	if totalCapacity > 0 {
		frag = 1.0 - float64(totalFree)/float64(totalCapacity)
	}
	return model.Cluster{
		ClusterID:          id,
		Nodes:              nodes,
		FragmentationScore: frag,
	}
}

func baseNode(id, clusterID string, totalGPUs, freeGPUs int) model.Node {
	return model.Node{
		NodeID:        id,
		ClusterID:     clusterID,
		TotalGPUs:     totalGPUs,
		AllocatedGPUs: totalGPUs - freeGPUs,
		FreeGPUs:      freeGPUs,
		Status:        model.NodeStatusReady,
	}
}

func addReplica(snap *worldstate.WorldStateSnapshot, id, appID, clusterID, nodeID string, gpus int) {
	snap.Replicas[id] = model.Replica{
		ReplicaID: id,
		AppID:     appID,
		ClusterID: clusterID,
		NodeID:    nodeID,
		GPUs:      gpus,
		Status:    model.ReplicaStatusRunning,
		CreatedAt: snap.TakenAt.Add(-time.Minute),
	}
	// Add replica to node's Pods list.
	if cluster, ok := snap.Clusters[clusterID]; ok {
		if node, ok := cluster.Nodes[nodeID]; ok {
			node.Pods = append(node.Pods, id)
			cluster.Nodes[nodeID] = node
			snap.Clusters[clusterID] = cluster
		}
	}
}

// ---------- FindRebalancingOpportunities ----------

func TestRebalancing_FindsConsolidation(t *testing.T) {
	e := newTestEngine(t)
	snap := baseSnap()

	// Cluster-A (source): 1 node, 8 total, 4 free (4 allocated). Frag = 0.5.
	// Two replicas of 2 GPUs each.
	snap.Clusters["cluster-a"] = baseCluster("cluster-a", map[model.NodeID]model.Node{
		"node-1": baseNode("node-1", "cluster-a", 8, 4),
	})

	// Cluster-B (dest): 3 nodes of 8 GPUs each (24 total), 22 allocated, 2 free.
	// One node has 2 free GPUs → room for the replica.
	// Frag = 1 - 2/24 ≈ 0.917.
	// Delta = (0.5 + 0.917) - (newSrcFrag + newDestFrag) should exceed 0.15
	// because srcCap(8) < destCap(24).
	snap.Clusters["cluster-b"] = baseCluster("cluster-b", map[model.NodeID]model.Node{
		"node-3": baseNode("node-3", "cluster-b", 8, 2),
		"node-4": baseNode("node-4", "cluster-b", 8, 0),
		"node-5": baseNode("node-5", "cluster-b", 8, 0),
	})

	app := baseApp("app-1", 1, 2, 1)
	app.ModelID = "model-1"
	snap.Applications["app-1"] = app

	// Two running replicas (above min=1).
	addReplica(&snap, "r1", "app-1", "cluster-a", "node-1", 2)
	addReplica(&snap, "r2", "app-1", "cluster-a", "node-1", 2)

	// Cache present in cluster-B.
	snap.CacheLocations["model-1"] = []model.CacheLocation{
		{ModelID: "model-1", ClusterID: "cluster-b", Tier: model.CacheTierLocalNVMe},
	}

	result := e.FindRebalancingOpportunities(context.Background(), snap)
	require.NotEmpty(t, result)
	assert.Equal(t, "cluster-b", result[0].TargetClusterID)
	assert.Equal(t, "app-1", result[0].AppID)
}

func TestRebalancing_SkipsWellPackedClusters(t *testing.T) {
	e := newTestEngine(t)
	snap := baseSnap()

	// All clusters have FragmentationScore < 0.15.
	// Cluster with all GPUs free: frag = 1 - 8/8 = 0.0
	snap.Clusters["cluster-a"] = baseCluster("cluster-a", map[model.NodeID]model.Node{
		"node-1": baseNode("node-1", "cluster-a", 8, 8),
	})
	// Cluster with 1/8 allocated: frag = 1 - 7/8 = 0.125
	snap.Clusters["cluster-b"] = baseCluster("cluster-b", map[model.NodeID]model.Node{
		"node-2": baseNode("node-2", "cluster-b", 8, 7),
	})

	app := baseApp("app-1", 1, 1, 1)
	snap.Applications["app-1"] = app
	addReplica(&snap, "r1", "app-1", "cluster-b", "node-2", 1)

	result := e.FindRebalancingOpportunities(context.Background(), snap)
	assert.Empty(t, result)
}

func TestRebalancing_RespectsMinReplicas(t *testing.T) {
	e := newTestEngine(t)
	snap := baseSnap()

	// Fragmented cluster.
	snap.Clusters["cluster-a"] = baseCluster("cluster-a", map[model.NodeID]model.Node{
		"node-1": baseNode("node-1", "cluster-a", 8, 4),
	})
	snap.Clusters["cluster-b"] = baseCluster("cluster-b", map[model.NodeID]model.Node{
		"node-2": baseNode("node-2", "cluster-b", 8, 4),
	})

	// App with MinReplicas=1 and only 1 running replica.
	app := baseApp("app-1", 1, 4, 1)
	app.ModelID = "model-1"
	snap.Applications["app-1"] = app
	addReplica(&snap, "r1", "app-1", "cluster-a", "node-1", 4)

	snap.CacheLocations["model-1"] = []model.CacheLocation{
		{ModelID: "model-1", ClusterID: "cluster-b", Tier: model.CacheTierLocalNVMe},
	}

	result := e.FindRebalancingOpportunities(context.Background(), snap)
	assert.Empty(t, result)
}

func TestRebalancing_RequiresCachedModel(t *testing.T) {
	e := newTestEngine(t)
	snap := baseSnap()

	// Fragmented source cluster.
	snap.Clusters["cluster-a"] = baseCluster("cluster-a", map[model.NodeID]model.Node{
		"node-1": baseNode("node-1", "cluster-a", 8, 6),
		"node-2": baseNode("node-2", "cluster-a", 8, 6),
	})
	// Destination with room.
	snap.Clusters["cluster-b"] = baseCluster("cluster-b", map[model.NodeID]model.Node{
		"node-3": baseNode("node-3", "cluster-b", 8, 2),
	})

	app := baseApp("app-1", 1, 2, 1)
	app.ModelID = "model-1"
	snap.Applications["app-1"] = app

	addReplica(&snap, "r1", "app-1", "cluster-a", "node-1", 2)
	addReplica(&snap, "r2", "app-1", "cluster-a", "node-2", 2)

	// No cache in cluster-b — cache only in cluster-a.
	snap.CacheLocations["model-1"] = []model.CacheLocation{
		{ModelID: "model-1", ClusterID: "cluster-a", Tier: model.CacheTierLocalNVMe},
	}

	result := e.FindRebalancingOpportunities(context.Background(), snap)
	assert.Empty(t, result)
}

func TestRebalancing_MinFragDelta(t *testing.T) {
	e := newTestEngine(t)
	snap := baseSnap()

	// Source: 1 node, 4/8 allocated → frag = 0.5. Fragmented node: 4 allocated, 4 free.
	snap.Clusters["cluster-a"] = baseCluster("cluster-a", map[model.NodeID]model.Node{
		"node-1": baseNode("node-1", "cluster-a", 8, 4),
	})
	// Dest: 1 node, 4/8 allocated → frag = 0.5. Moving 2 GPUs: dest becomes 6/8 → new frag 0.75.
	// Source becomes 2/8 allocated → new frag 0.25.
	// Delta = (0.5 + 0.5) - (0.25 + 0.75) = 0.0, which is < 0.15.
	snap.Clusters["cluster-b"] = baseCluster("cluster-b", map[model.NodeID]model.Node{
		"node-2": baseNode("node-2", "cluster-b", 8, 4),
	})

	app := baseApp("app-1", 1, 2, 1)
	app.ModelID = "model-1"
	snap.Applications["app-1"] = app

	addReplica(&snap, "r1", "app-1", "cluster-a", "node-1", 2)
	addReplica(&snap, "r2", "app-1", "cluster-a", "node-1", 2)

	snap.CacheLocations["model-1"] = []model.CacheLocation{
		{ModelID: "model-1", ClusterID: "cluster-b", Tier: model.CacheTierLocalNVMe},
	}

	result := e.FindRebalancingOpportunities(context.Background(), snap)
	assert.Empty(t, result)
}

func TestRebalancing_MaxMigrationsPerCycle(t *testing.T) {
	e := newTestEngine(t)
	snap := baseSnap()

	// Source cluster: small (4 GPU), partially allocated → frag = 0.75.
	// delta per 1-GPU migration = 1*(1/4 - 1/40) = 0.225 ≥ 0.15 ✓
	snap.Clusters["cluster-a"] = baseCluster("cluster-a", map[model.NodeID]model.Node{
		"node-1": baseNode("node-1", "cluster-a", 4, 1),
	})

	// Large destination cluster.
	snap.Clusters["cluster-b"] = baseCluster("cluster-b", map[model.NodeID]model.Node{
		"node-dest": baseNode("node-dest", "cluster-b", 40, 3),
	})

	app := baseApp("app-1", 1, 1, 1)
	app.ModelID = "model-1"
	snap.Applications["app-1"] = app

	// 3 running replicas of 1 GPU each (3 allocated, 1 free on 4-GPU node).
	addReplica(&snap, "r1", "app-1", "cluster-a", "node-1", 1)
	addReplica(&snap, "r2", "app-1", "cluster-a", "node-1", 1)
	addReplica(&snap, "r3", "app-1", "cluster-a", "node-1", 1)

	snap.CacheLocations["model-1"] = []model.CacheLocation{
		{ModelID: "model-1", ClusterID: "cluster-b", Tier: model.CacheTierLocalNVMe},
	}

	result := e.FindRebalancingOpportunities(context.Background(), snap)
	// 3 candidates but capped at MaxMigrationsPerCycle=2.
	assert.Len(t, result, 2)
}

func TestRebalancing_PrefersLowPriority(t *testing.T) {
	e := newTestEngine(t)
	snap := baseSnap()

	// Source cluster: small (6 total), fragmented. Frag = 1 - 2/6 ≈ 0.667.
	// delta per 1-GPU migration = 1*(1/6 - 1/100) ≈ 0.157 ≥ 0.15 ✓
	snap.Clusters["cluster-a"] = baseCluster("cluster-a", map[model.NodeID]model.Node{
		"node-1": baseNode("node-1", "cluster-a", 6, 2),
	})

	// Large destination cluster.
	snap.Clusters["cluster-b"] = baseCluster("cluster-b", map[model.NodeID]model.Node{
		"node-3": baseNode("node-3", "cluster-b", 100, 4),
	})

	// P0 app (highest priority).
	appP0 := baseApp("app-p0", 0, 1, 1)
	appP0.ModelID = "model-shared"
	snap.Applications["app-p0"] = appP0

	// P2 app (lower priority, higher int).
	appP2 := baseApp("app-p2", 2, 1, 1)
	appP2.ModelID = "model-shared"
	snap.Applications["app-p2"] = appP2

	// Two running replicas per app on node-1 (above min=1).
	// 4 GPUs allocated on a 6-GPU node (2 free).
	addReplica(&snap, "r-p0-1", "app-p0", "cluster-a", "node-1", 1)
	addReplica(&snap, "r-p0-2", "app-p0", "cluster-a", "node-1", 1)
	addReplica(&snap, "r-p2-1", "app-p2", "cluster-a", "node-1", 1)
	addReplica(&snap, "r-p2-2", "app-p2", "cluster-a", "node-1", 1)

	snap.CacheLocations["model-shared"] = []model.CacheLocation{
		{ModelID: "model-shared", ClusterID: "cluster-b", Tier: model.CacheTierLocalNVMe},
	}

	// Allow enough migrations so we don't early-return and the sort runs.
	e.cfg.MaxMigrationsPerCycle = 10

	result := e.FindRebalancingOpportunities(context.Background(), snap)
	require.NotEmpty(t, result)
	// P2 (priority=2) should come first since higher int = lower priority = migrated first.
	assert.Equal(t, "app-p2", result[0].AppID)
}

// ---------- scoring helpers ----------

func TestCountRunningReplicas(t *testing.T) {
	snap := baseSnap()
	snap.Replicas["r1"] = model.Replica{ReplicaID: "r1", AppID: "app-1", Status: model.ReplicaStatusRunning}
	snap.Replicas["r2"] = model.Replica{ReplicaID: "r2", AppID: "app-1", Status: model.ReplicaStatusPending}
	snap.Replicas["r3"] = model.Replica{ReplicaID: "r3", AppID: "app-1", Status: model.ReplicaStatusTerminated}
	snap.Replicas["r4"] = model.Replica{ReplicaID: "r4", AppID: "app-2", Status: model.ReplicaStatusRunning}

	assert.Equal(t, 1, countRunningReplicas("app-1", snap)) // only running, not pending
	assert.Equal(t, 1, countRunningReplicas("app-2", snap))
	assert.Equal(t, 0, countRunningReplicas("app-3", snap))
}

func TestHasCachedModel(t *testing.T) {
	snap := baseSnap()
	snap.CacheLocations["model-1"] = []model.CacheLocation{
		{ModelID: "model-1", ClusterID: "cluster-a", Tier: model.CacheTierGPUMemory},
		{ModelID: "model-1", ClusterID: "cluster-b", Tier: model.CacheTierClusterStorage},
	}

	assert.True(t, hasCachedModel("cluster-a", "model-1", snap))
	assert.True(t, hasCachedModel("cluster-b", "model-1", snap))
	assert.False(t, hasCachedModel("cluster-c", "model-1", snap))
	assert.False(t, hasCachedModel("cluster-a", "model-2", snap))
}

func TestEstimateFragmentationDelta(t *testing.T) {
	snap := baseSnap()

	// Source cluster: 2 nodes, each 8 GPUs, 6 free → frag = 1 - 12/16 = 0.25.
	snap.Clusters["cluster-a"] = baseCluster("cluster-a", map[model.NodeID]model.Node{
		"node-1": baseNode("node-1", "cluster-a", 8, 6),
		"node-2": baseNode("node-2", "cluster-a", 8, 6),
	})
	// Dest cluster: 1 node, 8 GPUs, 2 free → frag = 1 - 2/8 = 0.75.
	snap.Clusters["cluster-b"] = baseCluster("cluster-b", map[model.NodeID]model.Node{
		"node-3": baseNode("node-3", "cluster-b", 8, 2),
	})

	sourceNode := snap.Clusters["cluster-a"].Nodes["node-1"]
	destCluster := snap.Clusters["cluster-b"]

	// Moving 2 GPUs from node-1 (cluster-a) to node-3 (cluster-b).
	// New source: node-1 has 8 free, node-2 has 6 free → frag = 1 - 14/16 = 0.125
	// New dest: node-3 has 0 free → frag = 1 - 0/8 = 1.0
	// Delta = (0.25 + 0.75) - (0.125 + 1.0) = 1.0 - 1.125 = -0.125
	delta := estimateFragmentationDelta(sourceNode, destCluster, 2, snap)
	assert.InDelta(t, -0.125, delta, 0.001)
}

func TestFindConsolidationTarget_ExcludesSourceCluster(t *testing.T) {
	e := newTestEngine(t)
	snap := baseSnap()

	snap.Clusters["cluster-a"] = baseCluster("cluster-a", map[model.NodeID]model.Node{
		"node-1": baseNode("node-1", "cluster-a", 8, 4),
	})

	// No other cluster available.
	replica := model.Replica{ReplicaID: "r1", AppID: "app-1", ClusterID: "cluster-a", GPUs: 2}
	app := baseApp("app-1", 1, 2, 1)

	_, found := e.findConsolidationTarget(replica, app, snap)
	assert.False(t, found)
}

func TestFindConsolidationTarget_PicksTightestFit(t *testing.T) {
	e := newTestEngine(t)
	snap := baseSnap()

	snap.Clusters["cluster-a"] = baseCluster("cluster-a", map[model.NodeID]model.Node{
		"node-1": baseNode("node-1", "cluster-a", 8, 4),
	})
	// cluster-b: node with 4 free → waste=2 for 2 GPU replica.
	snap.Clusters["cluster-b"] = baseCluster("cluster-b", map[model.NodeID]model.Node{
		"node-2": baseNode("node-2", "cluster-b", 8, 4),
	})
	// cluster-c: node with 2 free → waste=0 (exact fit).
	snap.Clusters["cluster-c"] = baseCluster("cluster-c", map[model.NodeID]model.Node{
		"node-3": baseNode("node-3", "cluster-c", 8, 2),
	})

	replica := model.Replica{ReplicaID: "r1", AppID: "app-1", ClusterID: "cluster-a", GPUs: 2}
	app := baseApp("app-1", 1, 2, 1)

	target, found := e.findConsolidationTarget(replica, app, snap)
	require.True(t, found)
	assert.Equal(t, "cluster-c", target.clusterID) // tighter fit
	assert.Equal(t, 2, target.constraints.RequiredGPUs)
	assert.Equal(t, []model.NodeID{"node-3"}, target.constraints.PreferredNodes)
}
