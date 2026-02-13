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

func TestEndToEnd_ScaleUp(t *testing.T) {
	t.Cleanup(func() { cleanupDeploymentsOnCluster(t, testClientset) })

	stack := newEngineStack(t, []clusterSetup{
		{ID: "c1", Clientset: testClientset},
	})

	// Set up world state: 1 cluster with 3 nodes × 8 GPUs.
	for i := 0; i < 3; i++ {
		stack.WorldState.UpsertNode(model.Node{
			NodeID:    nodeID("c1", i),
			ClusterID: "c1",
			TotalGPUs: 8,
			FreeGPUs:  8,
			Status:    model.NodeStatusReady,
		})
	}

	// 1 app: 2 GPUs/replica, min 1.
	stack.WorldState.UpsertApplication(model.Application{
		AppID:          "app-a",
		ModelID:        "model-a",
		GPUsPerReplica: 2,
		Priority:       1,
		MinReplicas:    1,
	})

	// 1 running replica in the world state.
	stack.WorldState.UpsertReplica(model.Replica{
		ReplicaID: "app-a-replica-seed",
		AppID:     "app-a",
		ClusterID: "c1",
		NodeID:    nodeID("c1", 0),
		GPUs:      2,
		Status:    model.ReplicaStatusRunning,
		CreatedAt: time.Now().Add(-5 * time.Minute),
	})

	// Pre-create the seed Deployment on K8s so it matches world state.
	ctx := context.Background()
	_, err := stack.Clients["c1"].CreateReplica(ctx, "app-a", model.SchedulingConstraints{RequiredGPUs: 2})
	require.NoError(t, err)

	// High queue pressure → should trigger scale-up.
	stack.WorldState.UpdateVLLMMetrics(model.VLLMMetrics{
		AppID:                "app-a",
		AggregatedFrom:       1,
		AvgWaitingQueueDepth: 20,
		MaxWaitingQueueDepth: 25,
		AvgBatchUtilization:  0.8,
		AvgKVCacheUtilization: 0.5,
		MaxKVCacheUtilization: 0.6,
	})

	result := stack.runCycle(t)

	// Expect completed scale-up ops and no failures.
	assert.NotEmpty(t, result.Completed, "expected completed operations")
	assert.Empty(t, result.Failed, "expected no failed operations")

	// Verify K8s has more than 1 Deployment for app-a (seed + new).
	count := countDeploymentsOnCluster(t, testClientset, "app-a")
	assert.Greater(t, count, 1, "expected more than 1 Deployment after scale-up")
}

func TestEndToEnd_ScaleDown(t *testing.T) {
	t.Cleanup(func() { cleanupDeploymentsOnCluster(t, testClientset) })

	stack := newEngineStack(t, []clusterSetup{
		{ID: "c1", Clientset: testClientset},
	})

	// Set up world state: 1 cluster with 3 nodes × 8 GPUs.
	for i := 0; i < 3; i++ {
		stack.WorldState.UpsertNode(model.Node{
			NodeID:    nodeID("c1", i),
			ClusterID: "c1",
			TotalGPUs: 8,
			FreeGPUs:  8,
			Status:    model.NodeStatusReady,
		})
	}

	stack.WorldState.UpsertApplication(model.Application{
		AppID:          "app-b",
		ModelID:        "model-b",
		GPUsPerReplica: 2,
		Priority:       1,
		MinReplicas:    1,
	})

	// 3 running replicas — pre-create Deployments on K8s.
	ctx := context.Background()
	replicaIDs := make([]string, 3)
	for i := 0; i < 3; i++ {
		id, err := stack.Clients["c1"].CreateReplica(ctx, "app-b", model.SchedulingConstraints{RequiredGPUs: 2})
		require.NoError(t, err)
		replicaIDs[i] = id

		stack.WorldState.UpsertReplica(model.Replica{
			ReplicaID: id,
			AppID:     "app-b",
			ClusterID: "c1",
			NodeID:    nodeID("c1", i%3),
			GPUs:      2,
			Status:    model.ReplicaStatusRunning,
			CreatedAt: time.Now().Add(-10 * time.Minute),
		})
	}

	initialCount := countDeploymentsOnCluster(t, testClientset, "app-b")
	require.Equal(t, 3, initialCount, "expected 3 Deployments before scale-down")

	// The scaling engine's computeEffectiveTarget caps the target at
	// running+pending, so it cannot produce a scale-down through ComputeTargets.
	// Provide an explicit scaling decision to test the placement → executor
	// scale-down pipeline end-to-end.
	overrides := map[model.AppID]scaling.ScalingDecision{
		"app-b": {
			AppID:               "app-b",
			CurrentCount:        3,
			TargetCount:         1,
			Direction:           scaling.ScaleDirectionDown,
			Signal:              scaling.ScaleSignalEfficiency,
			ScaleActionApproved: true,
			ActionAt:            time.Now(),
		},
	}

	result := stack.runCycleWithOverrides(t, overrides)

	assert.NotEmpty(t, result.Completed, "expected completed operations")
	assert.Empty(t, result.Failed, "expected no failed operations")

	// Verify K8s has fewer Deployments for app-b.
	count := countDeploymentsOnCluster(t, testClientset, "app-b")
	assert.Less(t, count, initialCount, "expected fewer Deployments after scale-down")
	assert.GreaterOrEqual(t, count, 1, "should not scale below min_replicas")
}

// nodeID returns a deterministic node ID for test setup.
func nodeID(clusterID string, idx int) string {
	return clusterID + "-node-" + string(rune('0'+idx))
}
