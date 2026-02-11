package worldstate_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hsubbaraj/schedulator/internal/observability"
	"github.com/hsubbaraj/schedulator/internal/worldstate"
	"github.com/hsubbaraj/schedulator/pkg/model"
)

func newTestWorldState() *worldstate.WorldState {
	return worldstate.New(observability.NewNoopTracer(), prometheus.NewRegistry())
}

// seedClusterWithNodes creates a cluster with the given nodes in the WorldState.
func seedClusterWithNodes(ws *worldstate.WorldState, clusterID string, nodes []model.Node) {
	for _, n := range nodes {
		ws.UpsertNode(n)
	}
}

func TestSnapshot_IsImmutable(t *testing.T) {
	ws := newTestWorldState()
	ws.UpsertApplication(model.Application{AppID: "app-1", Priority: 1})
	ws.UpsertNode(model.Node{
		NodeID:    "node-1",
		ClusterID: "cluster-1",
		TotalGPUs: 8,
		FreeGPUs:  8,
		Status:    model.NodeStatusReady,
	})

	snap := ws.Snapshot(context.Background())

	// Mutate WorldState after snapshot.
	ws.UpsertApplication(model.Application{AppID: "app-2", Priority: 2})
	ws.UpsertNode(model.Node{
		NodeID:    "node-2",
		ClusterID: "cluster-1",
		TotalGPUs: 8,
		FreeGPUs:  8,
		Status:    model.NodeStatusReady,
	})

	// Snapshot should not see the mutations.
	assert.Len(t, snap.Applications, 1)
	assert.Contains(t, snap.Applications, "app-1")
	assert.NotContains(t, snap.Applications, "app-2")
	assert.Len(t, snap.Clusters["cluster-1"].Nodes, 1)

	// Mutate snapshot — WorldState should not be affected.
	snap.Applications["app-3"] = model.Application{AppID: "app-3"}
	snap2 := ws.Snapshot(context.Background())
	assert.NotContains(t, snap2.Applications, "app-3")
	assert.Len(t, snap2.Applications, 2) // app-1 and app-2
}

func TestUpsertReplica_UpdatesNodeAllocations(t *testing.T) {
	tests := []struct {
		name         string
		setup        func(*worldstate.WorldState)
		action       func(*worldstate.WorldState)
		wantFreeGPUs int
	}{
		{
			name: "add 2-GPU replica",
			setup: func(ws *worldstate.WorldState) {
				ws.UpsertNode(model.Node{
					NodeID: "n1", ClusterID: "c1",
					TotalGPUs: 8, FreeGPUs: 8,
					Status: model.NodeStatusReady,
				})
			},
			action: func(ws *worldstate.WorldState) {
				ws.UpsertReplica(model.Replica{
					ReplicaID: "r1", AppID: "a1",
					ClusterID: "c1", NodeID: "n1",
					GPUs: 2, Status: model.ReplicaStatusRunning,
				})
			},
			wantFreeGPUs: 6,
		},
		{
			name: "add second 4-GPU replica",
			setup: func(ws *worldstate.WorldState) {
				ws.UpsertNode(model.Node{
					NodeID: "n1", ClusterID: "c1",
					TotalGPUs: 8, FreeGPUs: 8,
					Status: model.NodeStatusReady,
				})
				ws.UpsertReplica(model.Replica{
					ReplicaID: "r1", AppID: "a1",
					ClusterID: "c1", NodeID: "n1",
					GPUs: 2, Status: model.ReplicaStatusRunning,
				})
			},
			action: func(ws *worldstate.WorldState) {
				ws.UpsertReplica(model.Replica{
					ReplicaID: "r2", AppID: "a1",
					ClusterID: "c1", NodeID: "n1",
					GPUs: 4, Status: model.ReplicaStatusRunning,
				})
			},
			wantFreeGPUs: 2,
		},
		{
			name: "delete first replica restores GPUs",
			setup: func(ws *worldstate.WorldState) {
				ws.UpsertNode(model.Node{
					NodeID: "n1", ClusterID: "c1",
					TotalGPUs: 8, FreeGPUs: 8,
					Status: model.NodeStatusReady,
				})
				ws.UpsertReplica(model.Replica{
					ReplicaID: "r1", AppID: "a1",
					ClusterID: "c1", NodeID: "n1",
					GPUs: 2, Status: model.ReplicaStatusRunning,
				})
				ws.UpsertReplica(model.Replica{
					ReplicaID: "r2", AppID: "a1",
					ClusterID: "c1", NodeID: "n1",
					GPUs: 4, Status: model.ReplicaStatusRunning,
				})
			},
			action: func(ws *worldstate.WorldState) {
				ws.DeleteReplica("r1")
			},
			wantFreeGPUs: 4, // r2 still holds 4 GPUs: 8 - 4 = 4
		},
		{
			name: "status-only update same node no GPU change",
			setup: func(ws *worldstate.WorldState) {
				ws.UpsertNode(model.Node{
					NodeID: "n1", ClusterID: "c1",
					TotalGPUs: 8, FreeGPUs: 8,
					Status: model.NodeStatusReady,
				})
				ws.UpsertReplica(model.Replica{
					ReplicaID: "r1", AppID: "a1",
					ClusterID: "c1", NodeID: "n1",
					GPUs: 2, Status: model.ReplicaStatusPending,
				})
			},
			action: func(ws *worldstate.WorldState) {
				ws.UpsertReplica(model.Replica{
					ReplicaID: "r1", AppID: "a1",
					ClusterID: "c1", NodeID: "n1",
					GPUs: 2, Status: model.ReplicaStatusRunning,
				})
			},
			wantFreeGPUs: 6,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ws := newTestWorldState()
			tt.setup(ws)
			tt.action(ws)

			snap := ws.Snapshot(context.Background())
			node := snap.Clusters["c1"].Nodes["n1"]
			assert.Equal(t, tt.wantFreeGPUs, node.FreeGPUs)
		})
	}
}

func TestAvailableGPUs_SubtractsReservations(t *testing.T) {
	tests := []struct {
		name         string
		reservations []model.GPUReservation
		queryCluster string
		want         int
	}{
		{
			name:         "no reservations",
			reservations: nil,
			queryCluster: "c1",
			want:         16, // 2 nodes × 8 GPUs
		},
		{
			name: "active reservation subtracted",
			reservations: []model.GPUReservation{
				{ReservationID: "res1", ClusterID: "c1", GPUs: 4, Status: model.ReservationStatusActive, ForAppID: "a1", CreatedAt: time.Now(), TTLSeconds: 60},
			},
			queryCluster: "c1",
			want:         12,
		},
		{
			name: "fulfilled reservation not counted",
			reservations: []model.GPUReservation{
				{ReservationID: "res1", ClusterID: "c1", GPUs: 4, Status: model.ReservationStatusFulfilled, ForAppID: "a1", CreatedAt: time.Now(), TTLSeconds: 60},
			},
			queryCluster: "c1",
			want:         16,
		},
		{
			name: "expired reservation not counted",
			reservations: []model.GPUReservation{
				{ReservationID: "res1", ClusterID: "c1", GPUs: 4, Status: model.ReservationStatusExpired, ForAppID: "a1", CreatedAt: time.Now(), TTLSeconds: 60},
			},
			queryCluster: "c1",
			want:         16,
		},
		{
			name: "different cluster not counted",
			reservations: []model.GPUReservation{
				{ReservationID: "res1", ClusterID: "c2", GPUs: 4, Status: model.ReservationStatusActive, ForAppID: "a1", CreatedAt: time.Now(), TTLSeconds: 60},
			},
			queryCluster: "c1",
			want:         16,
		},
		{
			name: "exceeds free floors at 0",
			reservations: []model.GPUReservation{
				{ReservationID: "res1", ClusterID: "c1", GPUs: 20, Status: model.ReservationStatusActive, ForAppID: "a1", CreatedAt: time.Now(), TTLSeconds: 60},
			},
			queryCluster: "c1",
			want:         0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ws := newTestWorldState()
			seedClusterWithNodes(ws, "c1", []model.Node{
				{NodeID: "n1", ClusterID: "c1", TotalGPUs: 8, FreeGPUs: 8, Status: model.NodeStatusReady},
				{NodeID: "n2", ClusterID: "c1", TotalGPUs: 8, FreeGPUs: 8, Status: model.NodeStatusReady},
			})
			for _, r := range tt.reservations {
				require.NoError(t, ws.CreateReservation(r))
			}

			got := ws.AvailableGPUs(tt.queryCluster)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestHasContiguousGPUs_VariousNodeLayouts(t *testing.T) {
	tests := []struct {
		name         string
		nodes        []model.Node
		reservations []model.GPUReservation
		needed       int
		want         bool
	}{
		{
			name: "single node fits",
			nodes: []model.Node{
				{NodeID: "n1", ClusterID: "c1", TotalGPUs: 8, FreeGPUs: 8, Status: model.NodeStatusReady},
			},
			needed: 4,
			want:   true,
		},
		{
			name: "single node does not fit",
			nodes: []model.Node{
				{NodeID: "n1", ClusterID: "c1", TotalGPUs: 8, FreeGPUs: 2, Status: model.NodeStatusReady},
			},
			needed: 4,
			want:   false,
		},
		{
			name: "two nodes one fits",
			nodes: []model.Node{
				{NodeID: "n1", ClusterID: "c1", TotalGPUs: 8, FreeGPUs: 2, Status: model.NodeStatusReady},
				{NodeID: "n2", ClusterID: "c1", TotalGPUs: 8, FreeGPUs: 4, Status: model.NodeStatusReady},
			},
			needed: 4,
			want:   true,
		},
		{
			name: "node-scoped reservation reduces availability",
			nodes: []model.Node{
				{NodeID: "n1", ClusterID: "c1", TotalGPUs: 8, FreeGPUs: 4, Status: model.NodeStatusReady},
			},
			reservations: []model.GPUReservation{
				{ReservationID: "res1", ClusterID: "c1", NodeID: "n1", GPUs: 2, Status: model.ReservationStatusActive, ForAppID: "a1", CreatedAt: time.Now(), TTLSeconds: 60},
			},
			needed: 4,
			want:   false,
		},
		{
			name: "cordoned node excluded",
			nodes: []model.Node{
				{NodeID: "n1", ClusterID: "c1", TotalGPUs: 8, FreeGPUs: 8, Status: model.NodeStatusCordoned},
			},
			needed: 4,
			want:   false,
		},
		{
			name: "down node excluded",
			nodes: []model.Node{
				{NodeID: "n1", ClusterID: "c1", TotalGPUs: 8, FreeGPUs: 8, Status: model.NodeStatusDown},
			},
			needed: 4,
			want:   false,
		},
		{
			name:   "empty cluster",
			nodes:  nil,
			needed: 1,
			want:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ws := newTestWorldState()
			if len(tt.nodes) > 0 {
				seedClusterWithNodes(ws, "c1", tt.nodes)
			}
			for _, r := range tt.reservations {
				require.NoError(t, ws.CreateReservation(r))
			}

			got := ws.HasContiguousGPUs("c1", tt.needed)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestReservation_Lifecycle(t *testing.T) {
	t.Run("create and fulfill", func(t *testing.T) {
		ws := newTestWorldState()
		r := model.GPUReservation{
			ReservationID: "res1", ClusterID: "c1", GPUs: 4,
			ForAppID: "a1", Status: model.ReservationStatusActive,
			CreatedAt: time.Now(), TTLSeconds: 60,
		}
		require.NoError(t, ws.CreateReservation(r))
		require.NoError(t, ws.FulfillReservation("res1"))

		snap := ws.Snapshot(context.Background())
		assert.Equal(t, model.ReservationStatusFulfilled, snap.Reservations["res1"].Status)
	})

	t.Run("create and expire", func(t *testing.T) {
		ws := newTestWorldState()
		r := model.GPUReservation{
			ReservationID: "res1", ClusterID: "c1", GPUs: 4,
			ForAppID: "a1", Status: model.ReservationStatusActive,
			CreatedAt: time.Now(), TTLSeconds: 60,
		}
		require.NoError(t, ws.CreateReservation(r))
		require.NoError(t, ws.ExpireReservation("res1"))

		snap := ws.Snapshot(context.Background())
		assert.Equal(t, model.ReservationStatusExpired, snap.Reservations["res1"].Status)
	})

	t.Run("TTL-based expiry via ExpireStaleReservations", func(t *testing.T) {
		ws := newTestWorldState()
		r := model.GPUReservation{
			ReservationID: "res1", ClusterID: "c1", GPUs: 4,
			ForAppID: "a1", Status: model.ReservationStatusActive,
			CreatedAt: time.Now().Add(-2 * time.Minute), TTLSeconds: 60,
		}
		require.NoError(t, ws.CreateReservation(r))

		expired := ws.ExpireStaleReservations(time.Now())
		assert.Equal(t, 1, expired)

		snap := ws.Snapshot(context.Background())
		assert.Equal(t, model.ReservationStatusExpired, snap.Reservations["res1"].Status)
	})

	t.Run("duplicate create error", func(t *testing.T) {
		ws := newTestWorldState()
		r := model.GPUReservation{
			ReservationID: "res1", ClusterID: "c1", GPUs: 4,
			ForAppID: "a1", Status: model.ReservationStatusActive,
			CreatedAt: time.Now(), TTLSeconds: 60,
		}
		require.NoError(t, ws.CreateReservation(r))
		err := ws.CreateReservation(r)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "already exists")
	})

	t.Run("double fulfill error", func(t *testing.T) {
		ws := newTestWorldState()
		r := model.GPUReservation{
			ReservationID: "res1", ClusterID: "c1", GPUs: 4,
			ForAppID: "a1", Status: model.ReservationStatusActive,
			CreatedAt: time.Now(), TTLSeconds: 60,
		}
		require.NoError(t, ws.CreateReservation(r))
		require.NoError(t, ws.FulfillReservation("res1"))
		err := ws.FulfillReservation("res1")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "not active")
	})
}

func TestConcurrentAccess(t *testing.T) {
	ws := newTestWorldState()
	seedClusterWithNodes(ws, "c1", []model.Node{
		{NodeID: "n1", ClusterID: "c1", TotalGPUs: 8, FreeGPUs: 8, Status: model.NodeStatusReady},
	})

	var wg sync.WaitGroup
	ctx := context.Background()

	// 5 writers doing UpsertReplica/DeleteReplica.
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			rid := model.ReplicaID("r-" + string(rune('a'+i)))
			for j := 0; j < 100; j++ {
				ws.UpsertReplica(model.Replica{
					ReplicaID: rid, AppID: "a1",
					ClusterID: "c1", NodeID: "n1",
					GPUs: 1, Status: model.ReplicaStatusRunning,
				})
				ws.DeleteReplica(rid)
			}
		}(i)
	}

	// 5 readers doing Snapshot + AvailableGPUs.
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				ws.Snapshot(ctx)
				ws.AvailableGPUs("c1")
			}
		}()
	}

	wg.Wait()
	// If we get here without -race failure, the test passes.
}

func TestRecordScaleEvent(t *testing.T) {
	ws := newTestWorldState()
	now := time.Now()

	// Scale down twice — consecutive counter should increment.
	ws.RecordScaleEvent("app-1", "down", now)
	ws.RecordScaleEvent("app-1", "down", now.Add(time.Minute))

	snap := ws.Snapshot(context.Background())
	h := snap.ScalingHistory["app-1"]
	assert.Equal(t, 2, h.ConsecutiveScaleDownSignals)
	assert.Equal(t, now.Add(time.Minute), h.LastScaleDownAt)

	// Scale up — should reset consecutive counter.
	ws.RecordScaleEvent("app-1", "up", now.Add(2*time.Minute))

	snap = ws.Snapshot(context.Background())
	h = snap.ScalingHistory["app-1"]
	assert.Equal(t, 0, h.ConsecutiveScaleDownSignals)
	assert.Equal(t, now.Add(2*time.Minute), h.LastScaleUpAt)
}

func TestUpsertNode_RecomputesFragmentation(t *testing.T) {
	ws := newTestWorldState()

	// Single node, all free → fragmentation = 0.0
	ws.UpsertNode(model.Node{
		NodeID: "n1", ClusterID: "c1",
		TotalGPUs: 8, FreeGPUs: 8,
		Status: model.NodeStatusReady,
	})
	snap := ws.Snapshot(context.Background())
	assert.InDelta(t, 0.0, snap.Clusters["c1"].FragmentationScore, 0.001)

	// Add node with 4/8 allocated → total: 16 capacity, 12 free → frag = 1 - 12/16 = 0.25
	ws.UpsertNode(model.Node{
		NodeID: "n2", ClusterID: "c1",
		TotalGPUs: 8, FreeGPUs: 4,
		Status: model.NodeStatusReady,
	})
	snap = ws.Snapshot(context.Background())
	assert.InDelta(t, 0.25, snap.Clusters["c1"].FragmentationScore, 0.001)
}

func TestDeleteReplica_NonExistent(t *testing.T) {
	ws := newTestWorldState()
	// Should not panic.
	ws.DeleteReplica("does-not-exist")
}

func TestUpsertReplica_MoveAcrossNodes(t *testing.T) {
	ws := newTestWorldState()
	seedClusterWithNodes(ws, "c1", []model.Node{
		{NodeID: "n1", ClusterID: "c1", TotalGPUs: 8, FreeGPUs: 8, Status: model.NodeStatusReady},
		{NodeID: "n2", ClusterID: "c1", TotalGPUs: 8, FreeGPUs: 8, Status: model.NodeStatusReady},
	})

	// Place replica on n1.
	ws.UpsertReplica(model.Replica{
		ReplicaID: "r1", AppID: "a1",
		ClusterID: "c1", NodeID: "n1",
		GPUs: 4, Status: model.ReplicaStatusRunning,
	})

	snap := ws.Snapshot(context.Background())
	assert.Equal(t, 4, snap.Clusters["c1"].Nodes["n1"].FreeGPUs)
	assert.Equal(t, 8, snap.Clusters["c1"].Nodes["n2"].FreeGPUs)

	// Move replica to n2.
	ws.UpsertReplica(model.Replica{
		ReplicaID: "r1", AppID: "a1",
		ClusterID: "c1", NodeID: "n2",
		GPUs: 4, Status: model.ReplicaStatusRunning,
	})

	snap = ws.Snapshot(context.Background())
	assert.Equal(t, 8, snap.Clusters["c1"].Nodes["n1"].FreeGPUs, "old node should have GPUs restored")
	assert.Equal(t, 4, snap.Clusters["c1"].Nodes["n2"].FreeGPUs, "new node should have GPUs allocated")

	// Verify Pods lists.
	assert.NotContains(t, snap.Clusters["c1"].Nodes["n1"].Pods, "r1")
	assert.Contains(t, snap.Clusters["c1"].Nodes["n2"].Pods, "r1")
}
