package executor

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/hsubbaraj/schedulator/internal/observability"
	"github.com/hsubbaraj/schedulator/internal/worldstate"
	"github.com/hsubbaraj/schedulator/pkg/model"
	"github.com/hsubbaraj/schedulator/pkg/ports"
	"github.com/hsubbaraj/schedulator/pkg/ports/mocks"
)

// --- test helpers ---

type mockWorldState struct {
	mu                sync.Mutex
	fulfilledIDs      []model.ReservationID
	expiredIDs        []model.ReservationID
	fulfillErr        error
	expireErr         error
}

func (m *mockWorldState) FulfillReservation(id model.ReservationID) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.fulfilledIDs = append(m.fulfilledIDs, id)
	return m.fulfillErr
}

func (m *mockWorldState) ExpireReservation(id model.ReservationID) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.expiredIDs = append(m.expiredIDs, id)
	return m.expireErr
}

func testConfig() ExecutorConfig {
	return ExecutorConfig{
		PollInterval:           1 * time.Millisecond,
		WaitForDeletionTimeout: 1 * time.Second,
		DefaultReservationTTL:  1 * time.Second,
	}
}

func newTestExecutor(t *testing.T, clients map[model.ClusterID]ports.ClusterClient) (*Executor, *mockWorldState) {
	t.Helper()
	ws := &mockWorldState{}
	e := NewExecutor(
		testConfig(),
		clients,
		ws,
		observability.NewNoopTracer(),
		prometheus.NewRegistry(),
	)
	return e, ws
}

func baseSnapshot() worldstate.WorldStateSnapshot {
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

// --- tests ---

func TestExecutor_EmptyPlan(t *testing.T) {
	e, _ := newTestExecutor(t, nil)
	result := e.Execute(context.Background(), model.ExecutionPlan{}, baseSnapshot())

	assert.Empty(t, result.Completed)
	assert.Empty(t, result.Failed)
	assert.Empty(t, result.Aborted)
}

func TestExecutor_HappyPath(t *testing.T) {
	client := mocks.NewMockClusterClient(t)
	clients := map[model.ClusterID]ports.ClusterClient{"cluster-a": client}
	e, ws := newTestExecutor(t, clients)

	snap := baseSnapshot()
	snap.Reservations["res-1"] = model.GPUReservation{
		ReservationID: "res-1",
		ClusterID:     "cluster-a",
		TTLSeconds:    10,
		Status:        model.ReservationStatusActive,
	}

	// Preemption: label → delete → getStatus(terminated).
	client.EXPECT().LabelReplica(mock.Anything, model.ReplicaID("victim-1"), map[string]string{"draining": "true"}).
		Return(nil)
	client.EXPECT().DeleteReplica(mock.Anything, model.ReplicaID("victim-1")).
		Return(nil)
	client.EXPECT().GetReplicaStatus(mock.Anything, model.ReplicaID("victim-1")).
		Return(model.ReplicaStatusTerminated, nil)

	// Scale-up: create → getStatus(running).
	client.EXPECT().CreateReplica(mock.Anything, model.AppID("app-1"), mock.Anything).
		Return(model.ReplicaID("new-1"), nil)
	client.EXPECT().GetReplicaStatus(mock.Anything, model.ReplicaID("new-1")).
		Return(model.ReplicaStatusRunning, nil)

	plan := model.ExecutionPlan{
		Operations: []model.Operation{
			{
				ID:   "op-preempt",
				Type: model.OperationPreempt,
				Payload: model.PreemptionDecision{
					VictimReplicaID:  "victim-1",
					VictimAppID:      "victim-app",
					ClusterID:        "cluster-a",
					NodeID:           "node-1",
					GracePeriodSeconds: 0,
				},
			},
			{
				ID:   "op-scaleup",
				Type: model.OperationScaleUp,
				Payload: model.ScaleUpDecision{
					AppID:         "app-1",
					ClusterID:     "cluster-a",
					ReservationID: "res-1",
				},
				DependsOn: []model.OperationID{"op-preempt"},
			},
		},
	}

	result := e.Execute(context.Background(), plan, snap)

	assert.Len(t, result.Completed, 2)
	assert.Empty(t, result.Failed)
	assert.Empty(t, result.Aborted)
	assert.Contains(t, result.Completed, model.OperationID("op-preempt"))
	assert.Contains(t, result.Completed, model.OperationID("op-scaleup"))
	assert.Contains(t, ws.fulfilledIDs, model.ReservationID("res-1"))
}

func TestExecutor_DependencyRespected(t *testing.T) {
	client := mocks.NewMockClusterClient(t)
	clients := map[model.ClusterID]ports.ClusterClient{"cluster-a": client}
	e, _ := newTestExecutor(t, clients)

	snap := baseSnapshot()

	// Track call order to verify dependency is respected.
	var orderMu sync.Mutex
	var callOrder []string

	// Preemption ops.
	client.EXPECT().LabelReplica(mock.Anything, model.ReplicaID("victim-1"), mock.Anything).
		Run(func(_ context.Context, _ model.ReplicaID, _ map[string]string) {
			orderMu.Lock()
			callOrder = append(callOrder, "preempt-label")
			orderMu.Unlock()
		}).Return(nil)
	client.EXPECT().DeleteReplica(mock.Anything, model.ReplicaID("victim-1")).
		Run(func(_ context.Context, _ model.ReplicaID) {
			orderMu.Lock()
			callOrder = append(callOrder, "preempt-delete")
			orderMu.Unlock()
		}).Return(nil)
	client.EXPECT().GetReplicaStatus(mock.Anything, model.ReplicaID("victim-1")).
		Return(model.ReplicaStatusTerminated, nil)

	// Scale-up ops.
	client.EXPECT().CreateReplica(mock.Anything, model.AppID("app-1"), mock.Anything).
		Run(func(_ context.Context, _ model.AppID, _ model.SchedulingConstraints) {
			orderMu.Lock()
			callOrder = append(callOrder, "scaleup-create")
			orderMu.Unlock()
		}).Return(model.ReplicaID("new-1"), nil)
	client.EXPECT().GetReplicaStatus(mock.Anything, model.ReplicaID("new-1")).
		Return(model.ReplicaStatusRunning, nil)

	plan := model.ExecutionPlan{
		Operations: []model.Operation{
			{
				ID:   "op-preempt",
				Type: model.OperationPreempt,
				Payload: model.PreemptionDecision{
					VictimReplicaID: "victim-1",
					VictimAppID:     "victim-app",
					ClusterID:       "cluster-a",
					NodeID:          "node-1",
				},
			},
			{
				ID:   "op-scaleup",
				Type: model.OperationScaleUp,
				Payload: model.ScaleUpDecision{
					AppID:     "app-1",
					ClusterID: "cluster-a",
				},
				DependsOn: []model.OperationID{"op-preempt"},
			},
		},
	}

	result := e.Execute(context.Background(), plan, snap)
	require.Len(t, result.Completed, 2)
	assert.Empty(t, result.Failed)

	// Verify preemption completed before scale-up started.
	orderMu.Lock()
	defer orderMu.Unlock()

	preemptIdx := -1
	scaleupIdx := -1
	for i, v := range callOrder {
		if v == "preempt-delete" {
			preemptIdx = i
		}
		if v == "scaleup-create" {
			scaleupIdx = i
		}
	}
	require.NotEqual(t, -1, preemptIdx, "preempt-delete not called")
	require.NotEqual(t, -1, scaleupIdx, "scaleup-create not called")
	assert.Less(t, preemptIdx, scaleupIdx, "preemption must complete before scale-up starts")
}

func TestExecutor_FailureAbortsDependents(t *testing.T) {
	client := mocks.NewMockClusterClient(t)
	clients := map[model.ClusterID]ports.ClusterClient{"cluster-a": client}
	e, _ := newTestExecutor(t, clients)

	snap := baseSnapshot()

	// Preemption fails at label step.
	client.EXPECT().LabelReplica(mock.Anything, model.ReplicaID("victim-1"), mock.Anything).
		Return(errors.New("label failed"))

	plan := model.ExecutionPlan{
		Operations: []model.Operation{
			{
				ID:   "op-preempt",
				Type: model.OperationPreempt,
				Payload: model.PreemptionDecision{
					VictimReplicaID: "victim-1",
					VictimAppID:     "victim-app",
					ClusterID:       "cluster-a",
					NodeID:          "node-1",
				},
			},
			{
				ID:   "op-scaleup",
				Type: model.OperationScaleUp,
				Payload: model.ScaleUpDecision{
					AppID:     "app-1",
					ClusterID: "cluster-a",
				},
				DependsOn: []model.OperationID{"op-preempt"},
			},
		},
	}

	result := e.Execute(context.Background(), plan, snap)

	assert.Empty(t, result.Completed)
	require.Len(t, result.Failed, 1)
	assert.Equal(t, model.OperationID("op-preempt"), result.Failed[0].OperationID)
	require.Len(t, result.Aborted, 1)
	assert.Equal(t, model.OperationID("op-scaleup"), result.Aborted[0])
}

func TestExecutor_ParallelIndependentOps(t *testing.T) {
	clientA := mocks.NewMockClusterClient(t)
	clientB := mocks.NewMockClusterClient(t)
	clients := map[model.ClusterID]ports.ClusterClient{
		"cluster-a": clientA,
		"cluster-b": clientB,
	}
	e, _ := newTestExecutor(t, clients)

	snap := baseSnapshot()
	snap.Replicas["r1"] = model.Replica{ReplicaID: "r1", AppID: "app-1", ClusterID: "cluster-a", Status: model.ReplicaStatusRunning}
	snap.Replicas["r2"] = model.Replica{ReplicaID: "r2", AppID: "app-2", ClusterID: "cluster-b", Status: model.ReplicaStatusRunning}

	// Track concurrent execution.
	var concurrency atomic.Int32
	var maxConcurrency atomic.Int32

	trackConcurrency := func() {
		cur := concurrency.Add(1)
		for {
			prev := maxConcurrency.Load()
			if cur <= prev || maxConcurrency.CompareAndSwap(prev, cur) {
				break
			}
		}
		// Small delay to allow overlap.
		time.Sleep(5 * time.Millisecond)
		concurrency.Add(-1)
	}

	clientA.EXPECT().DeleteReplica(mock.Anything, model.ReplicaID("r1")).
		Run(func(_ context.Context, _ model.ReplicaID) { trackConcurrency() }).
		Return(nil)
	clientA.EXPECT().GetReplicaStatus(mock.Anything, model.ReplicaID("r1")).
		Return(model.ReplicaStatusTerminated, nil)

	clientB.EXPECT().DeleteReplica(mock.Anything, model.ReplicaID("r2")).
		Run(func(_ context.Context, _ model.ReplicaID) { trackConcurrency() }).
		Return(nil)
	clientB.EXPECT().GetReplicaStatus(mock.Anything, model.ReplicaID("r2")).
		Return(model.ReplicaStatusTerminated, nil)

	plan := model.ExecutionPlan{
		Operations: []model.Operation{
			{
				ID:      "op-sd-1",
				Type:    model.OperationScaleDown,
				Payload: model.ScaleDownDecision{AppID: "app-1", ReplicaID: "r1"},
			},
			{
				ID:      "op-sd-2",
				Type:    model.OperationScaleDown,
				Payload: model.ScaleDownDecision{AppID: "app-2", ReplicaID: "r2"},
			},
		},
	}

	result := e.Execute(context.Background(), plan, snap)

	assert.Len(t, result.Completed, 2)
	assert.Empty(t, result.Failed)
	assert.GreaterOrEqual(t, maxConcurrency.Load(), int32(2), "independent ops should run concurrently")
}

func TestExecutor_ScaleUpTimeout(t *testing.T) {
	client := mocks.NewMockClusterClient(t)
	clients := map[model.ClusterID]ports.ClusterClient{"cluster-a": client}

	ws := &mockWorldState{}
	cfg := testConfig()
	cfg.DefaultReservationTTL = 20 * time.Millisecond // very short TTL for test
	e := NewExecutor(cfg, clients, ws, observability.NewNoopTracer(), prometheus.NewRegistry())

	snap := baseSnapshot()

	// CreateReplica succeeds.
	client.EXPECT().CreateReplica(mock.Anything, model.AppID("app-1"), mock.Anything).
		Return(model.ReplicaID("new-1"), nil)
	// GetReplicaStatus always returns Pending (never becomes Running).
	client.EXPECT().GetReplicaStatus(mock.Anything, model.ReplicaID("new-1")).
		Return(model.ReplicaStatusPending, nil).Maybe()
	// Cleanup: DeleteReplica called for orphan.
	client.EXPECT().DeleteReplica(mock.Anything, model.ReplicaID("new-1")).
		Return(nil).Maybe()

	plan := model.ExecutionPlan{
		Operations: []model.Operation{
			{
				ID:   "op-scaleup",
				Type: model.OperationScaleUp,
				Payload: model.ScaleUpDecision{
					AppID:         "app-1",
					ClusterID:     "cluster-a",
					ReservationID: "res-1",
				},
			},
		},
	}

	result := e.Execute(context.Background(), plan, snap)

	require.Len(t, result.Failed, 1)
	assert.Equal(t, model.OperationID("op-scaleup"), result.Failed[0].OperationID)
	assert.Empty(t, result.Completed)

	// Reservation should be expired.
	ws.mu.Lock()
	defer ws.mu.Unlock()
	assert.Contains(t, ws.expiredIDs, model.ReservationID("res-1"))
	assert.Empty(t, ws.fulfilledIDs)
}

func TestExecutor_ReservationFulfilled(t *testing.T) {
	client := mocks.NewMockClusterClient(t)
	clients := map[model.ClusterID]ports.ClusterClient{"cluster-a": client}
	e, ws := newTestExecutor(t, clients)

	snap := baseSnapshot()
	snap.Reservations["res-1"] = model.GPUReservation{
		ReservationID: "res-1",
		ClusterID:     "cluster-a",
		TTLSeconds:    60,
		Status:        model.ReservationStatusActive,
	}

	client.EXPECT().CreateReplica(mock.Anything, model.AppID("app-1"), mock.Anything).
		Return(model.ReplicaID("new-1"), nil)
	client.EXPECT().GetReplicaStatus(mock.Anything, model.ReplicaID("new-1")).
		Return(model.ReplicaStatusRunning, nil)

	plan := model.ExecutionPlan{
		Operations: []model.Operation{
			{
				ID:   "op-scaleup",
				Type: model.OperationScaleUp,
				Payload: model.ScaleUpDecision{
					AppID:         "app-1",
					ClusterID:     "cluster-a",
					ReservationID: "res-1",
				},
			},
		},
	}

	result := e.Execute(context.Background(), plan, snap)

	require.Len(t, result.Completed, 1)
	assert.Empty(t, result.Failed)

	ws.mu.Lock()
	defer ws.mu.Unlock()
	assert.Contains(t, ws.fulfilledIDs, model.ReservationID("res-1"))
}

func TestExecutor_ScaleDownNoClusterID(t *testing.T) {
	e, _ := newTestExecutor(t, nil)

	snap := baseSnapshot()
	// Intentionally do NOT add the replica to snapshot.

	plan := model.ExecutionPlan{
		Operations: []model.Operation{
			{
				ID:      "op-sd",
				Type:    model.OperationScaleDown,
				Payload: model.ScaleDownDecision{AppID: "app-1", ReplicaID: "r-missing"},
			},
		},
	}

	result := e.Execute(context.Background(), plan, snap)

	require.Len(t, result.Failed, 1)
	assert.Equal(t, model.OperationID("op-sd"), result.Failed[0].OperationID)
	assert.Contains(t, result.Failed[0].Err.Error(), "not found in snapshot")
}

func TestAbortTransitive(t *testing.T) {
	e, _ := newTestExecutor(t, nil)

	tests := []struct {
		name      string
		deps      map[model.OperationID][]model.OperationID // reverse dep map: dependents[X] = ops that depend on X
		status    map[model.OperationID]opStatus
		failedID  model.OperationID
		wantAbort []model.OperationID
	}{
		{
			name: "linear chain: A → B → C",
			deps: map[model.OperationID][]model.OperationID{
				"A": {"B"},
				"B": {"C"},
			},
			status: map[model.OperationID]opStatus{
				"A": opFailed,
				"B": opPending,
				"C": opPending,
			},
			failedID:  "A",
			wantAbort: []model.OperationID{"B", "C"},
		},
		{
			name: "fan-out: A → {B, C, D}",
			deps: map[model.OperationID][]model.OperationID{
				"A": {"B", "C", "D"},
			},
			status: map[model.OperationID]opStatus{
				"A": opFailed,
				"B": opPending,
				"C": opPending,
				"D": opPending,
			},
			failedID:  "A",
			wantAbort: []model.OperationID{"B", "C", "D"},
		},
		{
			name: "diamond: A → {B, C} → D",
			deps: map[model.OperationID][]model.OperationID{
				"A": {"B", "C"},
				"B": {"D"},
				"C": {"D"},
			},
			status: map[model.OperationID]opStatus{
				"A": opFailed,
				"B": opPending,
				"C": opPending,
				"D": opPending,
			},
			failedID:  "A",
			wantAbort: []model.OperationID{"B", "C", "D"},
		},
		{
			name: "no dependents",
			deps: map[model.OperationID][]model.OperationID{},
			status: map[model.OperationID]opStatus{
				"A": opFailed,
			},
			failedID:  "A",
			wantAbort: nil,
		},
		{
			name: "already inflight dependent not aborted",
			deps: map[model.OperationID][]model.OperationID{
				"A": {"B"},
			},
			status: map[model.OperationID]opStatus{
				"A": opFailed,
				"B": opInFlight,
			},
			failedID:  "A",
			wantAbort: nil, // inflight ops are not aborted by abortTransitive
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := e.abortTransitive(tt.failedID, tt.deps, tt.status)
			assert.ElementsMatch(t, tt.wantAbort, got)

			// Verify status was updated for aborted ops.
			for _, id := range tt.wantAbort {
				assert.Equal(t, opAborted, tt.status[id], "op %s should be aborted", id)
			}
		})
	}
}
