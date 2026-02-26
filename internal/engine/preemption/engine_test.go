package preemption

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

func newTestEngine() *PreemptionEngine {
	return NewPreemptionEngine(DefaultPreemptionConfig(), observability.NewNoopTracer(), prometheus.NewRegistry())
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

func addReplica(snap *worldstate.WorldStateSnapshot, id, appID, clusterID, nodeID string, gpus int) {
	r := model.Replica{
		ReplicaID: id,
		AppID:     appID,
		ClusterID: clusterID,
		NodeID:    nodeID,
		GPUs:      gpus,
		Status:    model.ReplicaStatusRunning,
		CreatedAt: snap.TakenAt.Add(-time.Minute),
	}
	snap.Replicas[id] = r
}

func TestPreemption_P0CanPreemptP2(t *testing.T) {
	e := newTestEngine()
	snap := baseSnap()

	requester := baseApp("p0-app", 0, 4, 1)
	victim := baseApp("p2-app", 2, 4, 0)
	snap.Applications["p0-app"] = requester
	snap.Applications["p2-app"] = victim

	addReplica(&snap, "r1", "p2-app", "cluster-a", "node-1", 4)
	addReplica(&snap, "r2", "p2-app", "cluster-a", "node-2", 4)

	plan, err := e.FindPreemptionOpportunity(context.Background(), requester, snap)
	require.NoError(t, err)
	require.NotNil(t, plan)
	assert.Len(t, plan.Victims, 1)
	assert.Equal(t, "p2-app", plan.Victims[0].VictimAppID)
	assert.Equal(t, "cluster-a", plan.TargetCluster)
	assert.Equal(t, 4, plan.Constraints.RequiredGPUs)
}

func TestPreemption_P0EscalatesToP1(t *testing.T) {
	e := newTestEngine()
	snap := baseSnap()

	requester := baseApp("p0-app", 0, 8, 1)
	p2App := baseApp("p2-app", 2, 4, 0)
	p1App := baseApp("p1-app", 1, 4, 0)
	snap.Applications["p0-app"] = requester
	snap.Applications["p2-app"] = p2App
	snap.Applications["p1-app"] = p1App

	// Only 4 GPUs from P2, need 8 total → must escalate to P1.
	addReplica(&snap, "r1", "p2-app", "cluster-a", "node-1", 4)
	addReplica(&snap, "r2", "p1-app", "cluster-a", "node-2", 4)

	plan, err := e.FindPreemptionOpportunity(context.Background(), requester, snap)
	require.NoError(t, err)
	require.NotNil(t, plan)
	assert.Len(t, plan.Victims, 2)

	victimApps := map[string]bool{}
	for _, v := range plan.Victims {
		victimApps[v.VictimAppID] = true
	}
	assert.True(t, victimApps["p2-app"])
	assert.True(t, victimApps["p1-app"])
}

func TestPreemption_P1CannotEscalateToP0(t *testing.T) {
	e := newTestEngine()
	snap := baseSnap()

	requester := baseApp("p1-app", 1, 4, 1)
	p0App := baseApp("p0-app", 0, 4, 0)
	snap.Applications["p1-app"] = requester
	snap.Applications["p0-app"] = p0App

	// P0 replicas exist, but P1 cannot preempt them.
	addReplica(&snap, "r1", "p0-app", "cluster-a", "node-1", 4)

	plan, err := e.FindPreemptionOpportunity(context.Background(), requester, snap)
	require.NoError(t, err)
	assert.Nil(t, plan)
}

func TestPreemption_P2CannotPreempt(t *testing.T) {
	e := newTestEngine()
	snap := baseSnap()

	requester := baseApp("p2-app", 2, 4, 1)
	snap.Applications["p2-app"] = requester

	plan, err := e.FindPreemptionOpportunity(context.Background(), requester, snap)
	require.NoError(t, err)
	assert.Nil(t, plan)
}

func TestPreemption_RespectsMinReplicas(t *testing.T) {
	e := newTestEngine()
	snap := baseSnap()

	requester := baseApp("p0-app", 0, 4, 1)
	victim := baseApp("p2-app", 2, 4, 2) // minReplicas=2
	snap.Applications["p0-app"] = requester
	snap.Applications["p2-app"] = victim

	// p2-app has exactly 2 replicas = min_replicas → all protected.
	addReplica(&snap, "r1", "p2-app", "cluster-a", "node-1", 4)
	addReplica(&snap, "r2", "p2-app", "cluster-a", "node-2", 4)

	plan, err := e.FindPreemptionOpportunity(context.Background(), requester, snap)
	require.NoError(t, err)
	assert.Nil(t, plan)
}

func TestPreemption_RespectsMinReplicas_PartialProtection(t *testing.T) {
	e := newTestEngine()
	snap := baseSnap()

	requester := baseApp("p0-app", 0, 8, 1)
	victim := baseApp("p2-app", 2, 4, 1) // minReplicas=1, has 3 replicas → 2 preemptable
	snap.Applications["p0-app"] = requester
	snap.Applications["p2-app"] = victim

	addReplica(&snap, "r1", "p2-app", "cluster-a", "node-1", 4)
	addReplica(&snap, "r2", "p2-app", "cluster-a", "node-2", 4)
	addReplica(&snap, "r3", "p2-app", "cluster-a", "node-3", 4)

	plan, err := e.FindPreemptionOpportunity(context.Background(), requester, snap)
	require.NoError(t, err)
	require.NotNil(t, plan)
	// Can preempt 2 out of 3 (must keep 1 for min_replicas).
	assert.Len(t, plan.Victims, 2)
}

func TestPreemption_SameClusterPreference(t *testing.T) {
	e := newTestEngine()
	snap := baseSnap()

	requester := baseApp("p0-app", 0, 8, 1)
	victim := baseApp("p2-app", 2, 4, 0)
	snap.Applications["p0-app"] = requester
	snap.Applications["p2-app"] = victim

	// Replicas in two different clusters.
	addReplica(&snap, "r1", "p2-app", "cluster-a", "node-1", 4)
	addReplica(&snap, "r2", "p2-app", "cluster-a", "node-2", 4)
	addReplica(&snap, "r3", "p2-app", "cluster-b", "node-3", 4)

	plan, err := e.FindPreemptionOpportunity(context.Background(), requester, snap)
	require.NoError(t, err)
	require.NotNil(t, plan)

	// All victims must be from the same cluster.
	for _, v := range plan.Victims {
		assert.Equal(t, plan.TargetCluster, v.ClusterID)
	}
}

func TestPreemption_PackingBenefit(t *testing.T) {
	e := newTestEngine()
	snap := baseSnap()

	requester := baseApp("p0-app", 0, 4, 1)
	victim := baseApp("p2-app", 2, 4, 0) // has replicas with different GPU counts
	snap.Applications["p0-app"] = requester
	snap.Applications["p2-app"] = victim

	// r1 has 8 GPUs (poor match), r2 has 4 GPUs (perfect match).
	addReplica(&snap, "r1", "p2-app", "cluster-a", "node-1", 8)
	addReplica(&snap, "r2", "p2-app", "cluster-a", "node-2", 4)

	plan, err := e.FindPreemptionOpportunity(context.Background(), requester, snap)
	require.NoError(t, err)
	require.NotNil(t, plan)
	assert.Len(t, plan.Victims, 1)
	// Should prefer r2 (4 GPUs = perfect match) over r1 (8 GPUs).
	assert.Equal(t, "r2", plan.Victims[0].VictimReplicaID)
}

func TestPreemption_InsufficientCapacity(t *testing.T) {
	e := newTestEngine()
	snap := baseSnap()

	requester := baseApp("p0-app", 0, 16, 1)
	victim := baseApp("p2-app", 2, 4, 0)
	snap.Applications["p0-app"] = requester
	snap.Applications["p2-app"] = victim

	// Only 4 GPUs available, need 16.
	addReplica(&snap, "r1", "p2-app", "cluster-a", "node-1", 4)

	plan, err := e.FindPreemptionOpportunity(context.Background(), requester, snap)
	require.NoError(t, err)
	assert.Nil(t, plan)
}

func TestPackingBenefit(t *testing.T) {
	tests := []struct {
		name        string
		replicaGPUs int
		gpusNeeded  int
		want        float64
	}{
		{"perfect match", 4, 4, 1.0},
		{"off by 1", 3, 4, 0.5},
		{"off by 4", 8, 4, 0.2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := packingBenefit(tt.replicaGPUs, tt.gpusNeeded)
			assert.InDelta(t, tt.want, got, 0.001)
		})
	}
}
