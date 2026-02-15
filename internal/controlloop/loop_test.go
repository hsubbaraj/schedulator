package controlloop

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hsubbaraj/schedulator/internal/engine/scaling"
	"github.com/hsubbaraj/schedulator/internal/ingestion"
	"github.com/hsubbaraj/schedulator/internal/observability"
	"github.com/hsubbaraj/schedulator/internal/worldstate"
	"github.com/hsubbaraj/schedulator/pkg/model"
)

// ---------------------------------------------------------------------------
// Hand-rolled mocks
// ---------------------------------------------------------------------------

// chanIngester wraps a manually controlled channel.
type chanIngester struct {
	ch  chan []ingestion.Event
	err error
}

func newChanIngester() *chanIngester {
	return &chanIngester{ch: make(chan []ingestion.Event, 10)}
}

func (ci *chanIngester) Start(_ context.Context) (<-chan []ingestion.Event, error) {
	if ci.err != nil {
		return nil, ci.err
	}
	return ci.ch, nil
}

// mockLeaderElector returns a configurable leadership state.
type mockLeaderElector struct {
	leader bool
}

func (m *mockLeaderElector) IsLeader() bool          { return m.leader }
func (m *mockLeaderElector) OnElected(callback func()) {}
func (m *mockLeaderElector) OnLost(callback func())    {}

// mockScalingEngine records calls and returns configured decisions.
type mockScalingEngine struct {
	mu        sync.Mutex
	calls     int
	decisions map[model.AppID]scaling.ScalingDecision
}

func (m *mockScalingEngine) ComputeTargets(_ context.Context, _ worldstate.WorldStateSnapshot) map[model.AppID]scaling.ScalingDecision {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls++
	if m.decisions != nil {
		return m.decisions
	}
	return map[model.AppID]scaling.ScalingDecision{}
}

func (m *mockScalingEngine) callCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.calls
}

// mockPlacementEngine records calls and returns configured decisions.
type mockPlacementEngine struct {
	mu        sync.Mutex
	calls     int
	decisions model.PlacementDecisions
}

func (m *mockPlacementEngine) ComputePlacement(_ context.Context, _ worldstate.WorldStateSnapshot, _ map[model.AppID]scaling.ScalingDecision) model.PlacementDecisions {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls++
	return m.decisions
}

func (m *mockPlacementEngine) callCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.calls
}

// mockRebalancer records calls.
type mockRebalancer struct {
	mu         sync.Mutex
	calls      int
	migrations []model.MigrateDecision
}

func (m *mockRebalancer) FindRebalancingOpportunities(_ context.Context, _ worldstate.WorldStateSnapshot) []model.MigrateDecision {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls++
	return m.migrations
}

func (m *mockRebalancer) callCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.calls
}

// mockPlanGenerator records calls and returns configured plan.
type mockPlanGenerator struct {
	mu   sync.Mutex
	calls int
	plan model.ExecutionPlan
}

func (m *mockPlanGenerator) GeneratePlan(_ context.Context, _ model.PlacementDecisions, _ []model.MigrateDecision) model.ExecutionPlan {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls++
	return m.plan
}

func (m *mockPlanGenerator) callCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.calls
}

// mockExecutor records calls and returns configured result.
type mockExecutor struct {
	mu     sync.Mutex
	calls  int
	result model.ExecutionResult
}

func (m *mockExecutor) Execute(_ context.Context, _ model.ExecutionPlan, _ worldstate.WorldStateSnapshot) model.ExecutionResult {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls++
	return m.result
}

func (m *mockExecutor) callCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.calls
}

// mockWorldState tracks all mutation calls.
type mockWorldState struct {
	mu                         sync.Mutex
	snap                       worldstate.WorldStateSnapshot
	createReservationCalls     []model.GPUReservation
	recordScaleEventCalls      []recordScaleEventCall
	incrementScaleDownCalls    []model.AppID
	expireStaleReservationsCalls int
}

type recordScaleEventCall struct {
	appID     model.AppID
	direction string
	at        time.Time
}

func (m *mockWorldState) Snapshot(_ context.Context) worldstate.WorldStateSnapshot {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.snap
}

func (m *mockWorldState) CreateReservation(r model.GPUReservation) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.createReservationCalls = append(m.createReservationCalls, r)
	return nil
}

func (m *mockWorldState) RecordScaleEvent(appID model.AppID, direction string, at time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.recordScaleEventCalls = append(m.recordScaleEventCalls, recordScaleEventCall{
		appID: appID, direction: direction, at: at,
	})
}

func (m *mockWorldState) IncrementScaleDownSignal(appID model.AppID) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.incrementScaleDownCalls = append(m.incrementScaleDownCalls, appID)
}

func (m *mockWorldState) ExpireStaleReservations(_ time.Time) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.expireStaleReservationsCalls++
	return 0
}

func (m *mockWorldState) UpdateVLLMMetrics(_ model.VLLMMetrics)                        {}
func (m *mockWorldState) UpsertCluster(_ model.Cluster)                                {}
func (m *mockWorldState) UpsertNode(_ model.Node)                                      {}
func (m *mockWorldState) RemoveNode(_ model.ClusterID, _ model.NodeID)                 {}
func (m *mockWorldState) UpsertApplication(_ model.Application)                        {}
func (m *mockWorldState) UpsertReplica(_ model.Replica)                                {}
func (m *mockWorldState) DeleteReplica(_ model.ReplicaID)                              {}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

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

type testHarness struct {
	ingester  *chanIngester
	leader    *mockLeaderElector
	scaling   *mockScalingEngine
	placement *mockPlacementEngine
	rebal     *mockRebalancer
	plangen   *mockPlanGenerator
	executor  *mockExecutor
	ws        *mockWorldState
	loop      *ControlLoop
}

func newTestHarness() *testHarness {
	ing := newChanIngester()
	leader := &mockLeaderElector{leader: true}
	sc := &mockScalingEngine{}
	pl := &mockPlacementEngine{}
	rb := &mockRebalancer{}
	pg := &mockPlanGenerator{}
	ex := &mockExecutor{}
	ws := &mockWorldState{snap: baseSnap()}

	reg := prometheus.NewRegistry()
	tracer := observability.NewNoopTracer()

	cl := NewControlLoop(
		ControlLoopConfig{ReservationExpiryScanInterval: time.Hour}, // long interval to avoid interference
		ing, sc, pl, rb, pg, ex, ws, leader,
		nil, nil, // publisher, eventLogger
		tracer, reg,
	)

	return &testHarness{
		ingester: ing, leader: leader, scaling: sc, placement: pl,
		rebal: rb, plangen: pg, executor: ex, ws: ws, loop: cl,
	}
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestControlLoop_FullCycle(t *testing.T) {
	h := newTestHarness()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() { errCh <- h.loop.Run(ctx) }()

	// Send one batch of events then close the channel.
	h.ingester.ch <- []ingestion.Event{{Kind: ingestion.EventPeriodicEval}}
	close(h.ingester.ch)

	err := <-errCh
	require.NoError(t, err)

	assert.Equal(t, 1, h.scaling.callCount(), "scaling engine should be called once")
	assert.Equal(t, 1, h.placement.callCount(), "placement engine should be called once")
	assert.Equal(t, 1, h.rebal.callCount(), "rebalancer should be called once")
	assert.Equal(t, 1, h.plangen.callCount(), "plan generator should be called once")
	assert.Equal(t, 1, h.executor.callCount(), "executor should be called once")
}

func TestControlLoop_LeaderElection(t *testing.T) {
	h := newTestHarness()
	h.leader.leader = false

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() { errCh <- h.loop.Run(ctx) }()

	// Send events — should be drained without engine calls.
	h.ingester.ch <- []ingestion.Event{{Kind: ingestion.EventPeriodicEval}}
	close(h.ingester.ch)

	err := <-errCh
	require.NoError(t, err)

	assert.Equal(t, 0, h.scaling.callCount(), "scaling engine should not be called")
	assert.Equal(t, 0, h.placement.callCount(), "placement engine should not be called")
	assert.Equal(t, 0, h.executor.callCount(), "executor should not be called")
}

func TestControlLoop_ValidationRejects(t *testing.T) {
	h := newTestHarness()

	// Plan with self-preemption: app-A scaled up AND preempted.
	h.plangen.plan = model.ExecutionPlan{
		Operations: []model.Operation{
			{ID: "op1", Type: model.OperationScaleUp, Payload: model.ScaleUpDecision{AppID: "app-A"}},
			{ID: "op2", Type: model.OperationPreempt, Payload: model.PreemptionDecision{VictimAppID: "app-A"}},
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() { errCh <- h.loop.Run(ctx) }()

	h.ingester.ch <- []ingestion.Event{{Kind: ingestion.EventPeriodicEval}}
	close(h.ingester.ch)

	err := <-errCh
	require.NoError(t, err) // Run returns nil (channel closed)

	// Executor should NOT have been called because validation failed.
	assert.Equal(t, 0, h.executor.callCount(), "executor must not be called on validation failure")
	// But plan generator was called.
	assert.Equal(t, 1, h.plangen.callCount(), "plan generator should be called")
}

func TestControlLoop_ContextCancellation(t *testing.T) {
	h := newTestHarness()

	ctx, cancel := context.WithCancel(context.Background())

	errCh := make(chan error, 1)
	go func() { errCh <- h.loop.Run(ctx) }()

	cancel()

	err := <-errCh
	assert.ErrorIs(t, err, context.Canceled)
}

func TestControlLoop_ScalingPostProcessing_ApprovedScaleUp(t *testing.T) {
	h := newTestHarness()
	actionAt := time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC)

	h.scaling.decisions = map[model.AppID]scaling.ScalingDecision{
		"app-A": {
			AppID:               "app-A",
			Direction:           scaling.ScaleDirectionUp,
			ScaleActionApproved: true,
			ActionAt:            actionAt,
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() { errCh <- h.loop.Run(ctx) }()

	h.ingester.ch <- []ingestion.Event{{Kind: ingestion.EventPeriodicEval}}
	close(h.ingester.ch)

	<-errCh

	h.ws.mu.Lock()
	defer h.ws.mu.Unlock()
	require.Len(t, h.ws.recordScaleEventCalls, 1)
	assert.Equal(t, "app-A", h.ws.recordScaleEventCalls[0].appID)
	assert.Equal(t, "up", h.ws.recordScaleEventCalls[0].direction)
	assert.Equal(t, actionAt, h.ws.recordScaleEventCalls[0].at)
	assert.Empty(t, h.ws.incrementScaleDownCalls)
}

func TestControlLoop_ScalingPostProcessing_SuppressedScaleDown(t *testing.T) {
	h := newTestHarness()

	h.scaling.decisions = map[model.AppID]scaling.ScalingDecision{
		"app-B": {
			AppID:                   "app-B",
			Direction:               scaling.ScaleDirectionDown,
			ScaleActionApproved:     false,
			ScaleDownSignalObserved: true,
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() { errCh <- h.loop.Run(ctx) }()

	h.ingester.ch <- []ingestion.Event{{Kind: ingestion.EventPeriodicEval}}
	close(h.ingester.ch)

	<-errCh

	h.ws.mu.Lock()
	defer h.ws.mu.Unlock()
	assert.Empty(t, h.ws.recordScaleEventCalls, "RecordScaleEvent should NOT be called")
	require.Len(t, h.ws.incrementScaleDownCalls, 1)
	assert.Equal(t, "app-B", h.ws.incrementScaleDownCalls[0])
}

// ---------------------------------------------------------------------------
// Validation tests
// ---------------------------------------------------------------------------

func TestValidatePlan_NoSelfPreemption(t *testing.T) {
	tests := []struct {
		name    string
		plan    model.ExecutionPlan
		wantErr bool
	}{
		{
			name: "valid: different apps",
			plan: model.ExecutionPlan{
				Operations: []model.Operation{
					{ID: "op1", Type: model.OperationScaleUp, Payload: model.ScaleUpDecision{AppID: "app-A"}},
					{ID: "op2", Type: model.OperationPreempt, Payload: model.PreemptionDecision{VictimAppID: "app-B"}},
				},
			},
			wantErr: false,
		},
		{
			name: "invalid: same app scaled up and preempted",
			plan: model.ExecutionPlan{
				Operations: []model.Operation{
					{ID: "op1", Type: model.OperationScaleUp, Payload: model.ScaleUpDecision{AppID: "app-A"}},
					{ID: "op2", Type: model.OperationPreempt, Payload: model.PreemptionDecision{VictimAppID: "app-A"}},
				},
			},
			wantErr: true,
		},
		{
			name:    "valid: empty plan",
			plan:    model.ExecutionPlan{},
			wantErr: false,
		},
	}

	snap := baseSnap()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validatePlan(tt.plan, snap)
			if tt.wantErr {
				assert.Error(t, err)
				assert.Contains(t, err.Error(), "self-preemption")
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestValidatePlan_ReservationCapacity(t *testing.T) {
	tests := []struct {
		name    string
		snap    worldstate.WorldStateSnapshot
		plan    model.ExecutionPlan
		wantErr bool
	}{
		{
			name: "within capacity",
			snap: func() worldstate.WorldStateSnapshot {
				s := baseSnap()
				s.Clusters["c1"] = model.Cluster{
					ClusterID: "c1",
					Nodes: map[model.NodeID]model.Node{
						"n1": {NodeID: "n1", TotalGPUs: 8},
					},
				}
				s.Reservations["r1"] = model.GPUReservation{
					ReservationID: "r1", ClusterID: "c1", GPUs: 4,
				}
				return s
			}(),
			plan: model.ExecutionPlan{
				Operations: []model.Operation{
					{ID: "op1", Type: model.OperationScaleUp, Payload: model.ScaleUpDecision{
						AppID: "app-A", ReservationID: "r1",
					}},
				},
			},
			wantErr: false,
		},
		{
			name: "exceeds capacity",
			snap: func() worldstate.WorldStateSnapshot {
				s := baseSnap()
				s.Clusters["c1"] = model.Cluster{
					ClusterID: "c1",
					Nodes: map[model.NodeID]model.Node{
						"n1": {NodeID: "n1", TotalGPUs: 4},
					},
				}
				s.Reservations["r1"] = model.GPUReservation{
					ReservationID: "r1", ClusterID: "c1", GPUs: 4,
				}
				s.Reservations["r2"] = model.GPUReservation{
					ReservationID: "r2", ClusterID: "c1", GPUs: 4,
				}
				return s
			}(),
			plan: model.ExecutionPlan{
				Operations: []model.Operation{
					{ID: "op1", Type: model.OperationScaleUp, Payload: model.ScaleUpDecision{
						AppID: "app-A", ReservationID: "r1",
					}},
					{ID: "op2", Type: model.OperationScaleUp, Payload: model.ScaleUpDecision{
						AppID: "app-B", ReservationID: "r2",
					}},
				},
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validatePlan(tt.plan, tt.snap)
			if tt.wantErr {
				assert.Error(t, err)
				assert.Contains(t, err.Error(), "exceed")
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestValidatePlan_NoTerminatedReplicaScaleDown(t *testing.T) {
	tests := []struct {
		name    string
		snap    worldstate.WorldStateSnapshot
		plan    model.ExecutionPlan
		wantErr bool
	}{
		{
			name: "running replica OK",
			snap: func() worldstate.WorldStateSnapshot {
				s := baseSnap()
				s.Replicas["r1"] = model.Replica{
					ReplicaID: "r1", AppID: "app-A", Status: model.ReplicaStatusRunning,
				}
				return s
			}(),
			plan: model.ExecutionPlan{
				Operations: []model.Operation{
					{ID: "op1", Type: model.OperationScaleDown, Payload: model.ScaleDownDecision{
						AppID: "app-A", ReplicaID: "r1",
					}},
				},
			},
			wantErr: false,
		},
		{
			name: "terminated replica rejected",
			snap: func() worldstate.WorldStateSnapshot {
				s := baseSnap()
				s.Replicas["r1"] = model.Replica{
					ReplicaID: "r1", AppID: "app-A", Status: model.ReplicaStatusTerminated,
				}
				return s
			}(),
			plan: model.ExecutionPlan{
				Operations: []model.Operation{
					{ID: "op1", Type: model.OperationScaleDown, Payload: model.ScaleDownDecision{
						AppID: "app-A", ReplicaID: "r1",
					}},
				},
			},
			wantErr: true,
		},
		{
			name: "unknown replica OK",
			snap: baseSnap(),
			plan: model.ExecutionPlan{
				Operations: []model.Operation{
					{ID: "op1", Type: model.OperationScaleDown, Payload: model.ScaleDownDecision{
						AppID: "app-A", ReplicaID: "r-unknown",
					}},
				},
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validatePlan(tt.plan, tt.snap)
			if tt.wantErr {
				assert.Error(t, err)
				assert.Contains(t, err.Error(), "terminated")
			} else {
				assert.NoError(t, err)
			}
		})
	}
}
