//go:build integration

package integration

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hsubbaraj/schedulator/internal/engine/scaling"
	"github.com/hsubbaraj/schedulator/pkg/model"
)

func TestEndToEnd_Rebalancing(t *testing.T) {
	t.Cleanup(func() {
		cleanupDeploymentsOnCluster(t, testClientset)
		cleanupDeploymentsOnCluster(t, testClientset2)
	})

	stack := newEngineStack(t, []clusterSetup{
		{ID: "c1", Clientset: testClientset},
		{ID: "c2", Clientset: testClientset2},
	})

	// c1: 2 nodes, node-0 has 2/8 GPUs allocated (1 replica), node-1 has 6/8 free.
	stack.WorldState.UpsertNode(model.Node{
		NodeID:        "c1-node-0",
		ClusterID:     "c1",
		TotalGPUs:     8,
		AllocatedGPUs: 2,
		FreeGPUs:      6,
		Status:        model.NodeStatusReady,
	})
	stack.WorldState.UpsertNode(model.Node{
		NodeID:        "c1-node-1",
		ClusterID:     "c1",
		TotalGPUs:     8,
		AllocatedGPUs: 0,
		FreeGPUs:      8,
		Status:        model.NodeStatusReady,
	})

	// c2: 2 nodes, node-0 has 2/8 GPUs allocated (1 replica), node-1 has 6/8 free.
	stack.WorldState.UpsertNode(model.Node{
		NodeID:        "c2-node-0",
		ClusterID:     "c2",
		TotalGPUs:     8,
		AllocatedGPUs: 2,
		FreeGPUs:      6,
		Status:        model.NodeStatusReady,
	})
	stack.WorldState.UpsertNode(model.Node{
		NodeID:        "c2-node-1",
		ClusterID:     "c2",
		TotalGPUs:     8,
		AllocatedGPUs: 0,
		FreeGPUs:      8,
		Status:        model.NodeStatusReady,
	})

	stack.WorldState.UpsertApplication(model.Application{
		AppID:          "app-r",
		ModelID:        "model-r",
		GPUsPerReplica: 2,
		Priority:       1,
		MinReplicas:    1,
	})

	// Cache on both clusters so rebalancing doesn't skip due to no_cache.
	stack.WorldState.UpdateCacheLocations("model-r", []model.CacheLocation{
		{ModelID: "model-r", ClusterID: "c1", Tier: model.CacheTierClusterStorage},
		{ModelID: "model-r", ClusterID: "c2", Tier: model.CacheTierClusterStorage},
	})

	// Pre-create Deployments: 1 replica on c1, 1 on c2.
	ctx := context.Background()
	r1ID, err := stack.Clients["c1"].CreateReplica(ctx, "app-r", model.SchedulingConstraints{RequiredGPUs: 2})
	require.NoError(t, err)
	r2ID, err := stack.Clients["c2"].CreateReplica(ctx, "app-r", model.SchedulingConstraints{RequiredGPUs: 2})
	require.NoError(t, err)

	stack.WorldState.UpsertReplica(model.Replica{
		ReplicaID: r1ID, AppID: "app-r", ClusterID: "c1", NodeID: "c1-node-0",
		GPUs: 2, Status: model.ReplicaStatusRunning, CreatedAt: time.Now().Add(-10 * time.Minute),
	})
	stack.WorldState.UpsertReplica(model.Replica{
		ReplicaID: r2ID, AppID: "app-r", ClusterID: "c2", NodeID: "c2-node-0",
		GPUs: 2, Status: model.ReplicaStatusRunning, CreatedAt: time.Now().Add(-10 * time.Minute),
	})

	// Stable metrics — no scaling needed.
	stack.WorldState.UpdateVLLMMetrics(model.VLLMMetrics{
		AppID:                 "app-r",
		AggregatedFrom:        2,
		AvgWaitingQueueDepth:  3,
		AvgBatchUtilization:   0.5,
		AvgKVCacheUtilization: 0.5,
		MaxKVCacheUtilization: 0.6,
	})

	result := stack.runCycle(t)

	if len(result.Completed) > 0 {
		// If rebalancing happened, verify total replica count is preserved
		// (blue-green: new created before old removed).
		totalDeploys := countDeploymentsOnCluster(t, testClientset, "app-r") +
			countDeploymentsOnCluster(t, testClientset2, "app-r")
		assert.GreaterOrEqual(t, totalDeploys, 2,
			"blue-green migration should preserve replica count")
	}
	assert.Empty(t, result.Failed, "expected no failed operations")
}

func TestEndToEnd_CacheAffinity(t *testing.T) {
	t.Cleanup(func() {
		cleanupDeploymentsOnCluster(t, testClientset)
		cleanupDeploymentsOnCluster(t, testClientset2)
	})

	stack := newEngineStack(t, []clusterSetup{
		{ID: "c1", Clientset: testClientset},
		{ID: "c2", Clientset: testClientset2},
	})

	// Both clusters have 3 nodes × 8 GPUs.
	for i := 0; i < 3; i++ {
		stack.WorldState.UpsertNode(model.Node{
			NodeID: "c1-node-" + string(rune('0'+i)), ClusterID: "c1",
			TotalGPUs: 8, FreeGPUs: 8, Status: model.NodeStatusReady,
		})
		stack.WorldState.UpsertNode(model.Node{
			NodeID: "c2-node-" + string(rune('0'+i)), ClusterID: "c2",
			TotalGPUs: 8, FreeGPUs: 8, Status: model.NodeStatusReady,
		})
	}

	// Model cached at GPU_MEMORY tier on c1 only.
	stack.WorldState.UpdateCacheLocations("model-ca", []model.CacheLocation{
		{
			ModelID:   "model-ca",
			ClusterID: "c1",
			NodeID:    "c1-node-0",
			Tier:      model.CacheTierGPUMemory,
		},
	})

	stack.WorldState.UpsertApplication(model.Application{
		AppID:          "app-ca",
		ModelID:        "model-ca",
		GPUsPerReplica: 2,
		Priority:       1,
		MinReplicas:    1,
	})

	// High queue pressure → scale up from 0 to 1.
	stack.WorldState.UpdateVLLMMetrics(model.VLLMMetrics{
		AppID:                 "app-ca",
		AggregatedFrom:        0,
		AvgWaitingQueueDepth:  20,
		MaxWaitingQueueDepth:  25,
		AvgBatchUtilization:   0.0,
		AvgKVCacheUtilization: 0.0,
		MaxKVCacheUtilization: 0.0,
	})

	result := stack.runCycle(t)

	assert.NotEmpty(t, result.Completed, "expected completed scale-up ops")
	assert.Empty(t, result.Failed, "expected no failed operations")

	// Deployment should land on cluster 1 (cache affinity).
	c1Count := countDeploymentsOnCluster(t, testClientset, "app-ca")
	c2Count := countDeploymentsOnCluster(t, testClientset2, "app-ca")
	assert.Equal(t, 1, c1Count, "expected 1 Deployment on c1 (cached cluster)")
	assert.Equal(t, 0, c2Count, "expected 0 Deployments on c2 (no cache)")
}

func TestEndToEnd_FailureDomainSpread(t *testing.T) {
	t.Cleanup(func() {
		cleanupDeploymentsOnCluster(t, testClientset)
		cleanupDeploymentsOnCluster(t, testClientset2)
	})

	stack := newEngineStack(t, []clusterSetup{
		{ID: "c1", Clientset: testClientset},
		{ID: "c2", Clientset: testClientset2},
	})

	// Both clusters have 3 nodes × 8 GPUs.
	for i := 0; i < 3; i++ {
		stack.WorldState.UpsertNode(model.Node{
			NodeID: "c1-node-" + string(rune('0'+i)), ClusterID: "c1",
			TotalGPUs: 8, FreeGPUs: 8, Status: model.NodeStatusReady,
		})
		stack.WorldState.UpsertNode(model.Node{
			NodeID: "c2-node-" + string(rune('0'+i)), ClusterID: "c2",
			TotalGPUs: 8, FreeGPUs: 8, Status: model.NodeStatusReady,
		})
	}

	stack.WorldState.UpsertApplication(model.Application{
		AppID:             "app-fd",
		ModelID:           "model-fd",
		GPUsPerReplica:    2,
		Priority:          1,
		MinReplicas:       1,
		FailureDomainRule: model.FailureDomainSpreadClusters,
	})

	// 1 running replica already on c1.
	ctx := context.Background()
	seedID, err := stack.Clients["c1"].CreateReplica(ctx, "app-fd", model.SchedulingConstraints{RequiredGPUs: 2})
	require.NoError(t, err)
	stack.WorldState.UpsertReplica(model.Replica{
		ReplicaID: seedID, AppID: "app-fd", ClusterID: "c1", NodeID: "c1-node-0",
		GPUs: 2, Status: model.ReplicaStatusRunning, CreatedAt: time.Now().Add(-5 * time.Minute),
	})

	// Provide an explicit scaling decision to scale up by exactly 1 replica,
	// so we can verify the spread logic places it on c2 (not c1 which is at limit).
	overrides := map[model.AppID]scaling.ScalingDecision{
		"app-fd": {
			AppID:               "app-fd",
			CurrentCount:        1,
			TargetCount:         2,
			Direction:           scaling.ScaleDirectionUp,
			Signal:              scaling.ScaleSignalQueue,
			ScaleActionApproved: true,
			ActionAt:            time.Now(),
		},
	}

	result := stack.runCycleWithOverrides(t, overrides)

	assert.NotEmpty(t, result.Completed, "expected completed operations")
	assert.Empty(t, result.Failed, "expected no failed operations")

	// With FailureDomainSpreadClusters and 1 replica already on c1, the new
	// replica should be placed on c2 to spread across failure domains.
	c1Count := countDeploymentsOnCluster(t, testClientset, "app-fd")
	c2Count := countDeploymentsOnCluster(t, testClientset2, "app-fd")
	assert.Equal(t, 1, c1Count, "expected 1 Deployment on c1 (existing)")
	assert.Equal(t, 1, c2Count, "expected 1 Deployment on c2 (spread policy)")
}
