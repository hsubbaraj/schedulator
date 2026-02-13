//go:build integration

package integration

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hsubbaraj/schedulator/pkg/model"
)

func TestEndToEnd_Preemption(t *testing.T) {
	t.Cleanup(func() { cleanupDeploymentsOnCluster(t, testClientset) })

	stack := newEngineStack(t, []clusterSetup{
		{ID: "c1", Clientset: testClientset},
	})

	// 1 cluster, 1 node × 8 GPUs — fully occupied by app-p2.
	stack.WorldState.UpsertNode(model.Node{
		NodeID:    "c1-node-0",
		ClusterID: "c1",
		TotalGPUs: 8,
		FreeGPUs:  0,
		AllocatedGPUs: 8,
		Status:    model.NodeStatusReady,
	})

	// app-p2: priority 2 (lowest), 4 replicas × 2 GPUs, min 0.
	stack.WorldState.UpsertApplication(model.Application{
		AppID:          "app-p2",
		ModelID:        "model-p2",
		GPUsPerReplica: 2,
		Priority:       2,
		MinReplicas:    0,
	})

	// Pre-create 4 Deployments for app-p2.
	ctx := context.Background()
	p2ReplicaIDs := make([]string, 4)
	for i := 0; i < 4; i++ {
		id, err := stack.Clients["c1"].CreateReplica(ctx, "app-p2", model.SchedulingConstraints{RequiredGPUs: 2})
		require.NoError(t, err)
		p2ReplicaIDs[i] = id

		stack.WorldState.UpsertReplica(model.Replica{
			ReplicaID: id,
			AppID:     "app-p2",
			ClusterID: "c1",
			NodeID:    "c1-node-0",
			GPUs:      2,
			Status:    model.ReplicaStatusRunning,
			CreatedAt: time.Now().Add(-10 * time.Minute),
		})
	}

	// app-p2 has stable metrics (not triggering its own scaling).
	stack.WorldState.UpdateVLLMMetrics(model.VLLMMetrics{
		AppID:                 "app-p2",
		AggregatedFrom:        4,
		AvgWaitingQueueDepth:  2,
		AvgBatchUtilization:   0.5,
		AvgKVCacheUtilization: 0.5,
		MaxKVCacheUtilization: 0.6,
	})

	// app-p0: priority 0 (highest), 2 GPUs/replica, min 1.
	stack.WorldState.UpsertApplication(model.Application{
		AppID:          "app-p0",
		ModelID:        "model-p0",
		GPUsPerReplica: 2,
		Priority:       0,
		MinReplicas:    1,
	})

	// High queue metrics for P0 → needs scale-up.
	stack.WorldState.UpdateVLLMMetrics(model.VLLMMetrics{
		AppID:                 "app-p0",
		AggregatedFrom:        0,
		AvgWaitingQueueDepth:  20,
		MaxWaitingQueueDepth:  30,
		AvgBatchUtilization:   0.0,
		AvgKVCacheUtilization: 0.0,
		MaxKVCacheUtilization: 0.0,
	})

	p2Before := countDeploymentsOnCluster(t, testClientset, "app-p2")
	require.Equal(t, 4, p2Before, "expected 4 Deployments for app-p2 before preemption")

	result := stack.runCycle(t)

	assert.NotEmpty(t, result.Completed, "expected completed operations")
	assert.Empty(t, result.Failed, "expected no failed operations")

	// app-p0 should have at least 1 Deployment.
	p0Count := countDeploymentsOnCluster(t, testClientset, "app-p0")
	assert.GreaterOrEqual(t, p0Count, 1, "expected at least 1 Deployment for app-p0")

	// app-p2 should have fewer Deployments.
	p2After := countDeploymentsOnCluster(t, testClientset, "app-p2")
	assert.Less(t, p2After, p2Before, "expected fewer Deployments for app-p2 after preemption")
}
