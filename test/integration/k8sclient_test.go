//go:build integration

package integration

import (
	"context"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/hsubbaraj/schedulator/internal/k8sclient"
	"github.com/hsubbaraj/schedulator/internal/observability"
	"github.com/hsubbaraj/schedulator/pkg/model"
)

func TestKWOKNodes_ReportGPUCapacity(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	nodes, err := testClientset.CoreV1().Nodes().List(ctx, metav1.ListOptions{
		LabelSelector: "type=kwok",
	})
	require.NoError(t, err)
	require.NotEmpty(t, nodes.Items, "expected KWOK nodes to exist")

	for _, node := range nodes.Items {
		gpuCap := node.Status.Capacity["nvidia.com/gpu"]
		assert.Equal(t, int64(8), gpuCap.Value(), "node %s should have 8 GPUs", node.Name)

		gpuAlloc := node.Status.Allocatable["nvidia.com/gpu"]
		assert.Equal(t, int64(8), gpuAlloc.Value(), "node %s should have 8 allocatable GPUs", node.Name)
	}
}

func TestClusterClient_CreateAndDeleteReplica(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	reg := prometheus.NewRegistry()
	tracer := observability.NewNoopTracer()
	client := k8sclient.New(testClientset, "default", "integration-cluster", tracer, reg)

	// Create a replica.
	replicaID, err := client.CreateReplica(ctx, "integ-app", model.SchedulingConstraints{
		RequiredGPUs: 2,
	})
	require.NoError(t, err)
	assert.Contains(t, replicaID, "integ-app-replica-")

	// Verify it's pending (no scheduler will schedule it in kind without GPU support).
	status, err := client.GetReplicaStatus(ctx, replicaID)
	require.NoError(t, err)
	assert.Equal(t, model.ReplicaStatusPending, status)

	// Delete the replica.
	err = client.DeleteReplica(ctx, replicaID)
	require.NoError(t, err)

	// Verify it's terminated.
	status, err = client.GetReplicaStatus(ctx, replicaID)
	require.NoError(t, err)
	assert.Equal(t, model.ReplicaStatusTerminated, status)
}
