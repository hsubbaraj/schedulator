package shadow_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hsubbaraj/schedulator/internal/engine/scaling"
	"github.com/hsubbaraj/schedulator/internal/engine/shadow"
	"github.com/hsubbaraj/schedulator/internal/observability"
	"github.com/hsubbaraj/schedulator/internal/worldstate"
	"github.com/hsubbaraj/schedulator/pkg/model"
	"github.com/hsubbaraj/schedulator/pkg/ports"
)

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

// countingPlanner records call counts and returns a preset result.
type countingPlanner struct {
	name   string
	calls  atomic.Int64
	result ports.PlannerResult
	err    error
	delay  time.Duration // optional sleep before returning
}

func (p *countingPlanner) Plan(ctx context.Context, _ worldstate.WorldStateSnapshot, _ map[model.AppID]scaling.ScalingDecision) (ports.PlannerResult, error) {
	if p.delay > 0 {
		select {
		case <-time.After(p.delay):
		case <-ctx.Done():
			return ports.PlannerResult{}, ctx.Err()
		}
	}
	p.calls.Add(1)
	return p.result, p.err
}

func (p *countingPlanner) Name() string { return p.name }

// panicPlanner always panics in Plan.
type panicPlanner struct{ name string }

func (p *panicPlanner) Plan(_ context.Context, _ worldstate.WorldStateSnapshot, _ map[model.AppID]scaling.ScalingDecision) (ports.PlannerResult, error) {
	panic("intentional panic for test")
}
func (p *panicPlanner) Name() string { return p.name }

func newTestShadowPlanner(primary, shadowPlanner ports.Planner, timeoutMs int) *shadow.ShadowPlanner {
	return shadow.NewShadowPlanner(
		primary,
		shadowPlanner,
		timeoutMs,
		observability.NewNoopTracer(),
		prometheus.NewRegistry(),
	)
}

func emptySnap() worldstate.WorldStateSnapshot {
	return worldstate.WorldStateSnapshot{
		Clusters:     map[model.ClusterID]model.Cluster{},
		Applications: map[model.AppID]model.Application{},
		Replicas:     map[model.ReplicaID]model.Replica{},
	}
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestShadowPlanner_Name(t *testing.T) {
	pri := &countingPlanner{name: "heuristic"}
	sha := &countingPlanner{name: "solver"}
	p := newTestShadowPlanner(pri, sha, 500)
	assert.Equal(t, "shadow:heuristic+solver", p.Name())
}

func TestShadowPlanner_ReturnsPrimaryResult(t *testing.T) {
	primaryResult := ports.PlannerResult{
		Decisions: model.PlacementDecisions{
			ScaleUps: []model.ScaleUpDecision{{AppID: "app-A", ClusterID: "c1"}},
		},
	}
	shadowResult := ports.PlannerResult{
		Decisions: model.PlacementDecisions{
			ScaleUps: []model.ScaleUpDecision{{AppID: "app-B", ClusterID: "c2"}},
		},
	}

	pri := &countingPlanner{name: "heuristic", result: primaryResult}
	sha := &countingPlanner{name: "solver", result: shadowResult}
	p := newTestShadowPlanner(pri, sha, 500)

	got, err := p.Plan(context.Background(), emptySnap(), nil)
	require.NoError(t, err)
	assert.Equal(t, primaryResult.Decisions.ScaleUps, got.Decisions.ScaleUps,
		"Plan() should return the primary result, not the shadow result")
}

func TestShadowPlanner_ShadowPanic_Recovered(t *testing.T) {
	pri := &countingPlanner{name: "heuristic", result: ports.PlannerResult{}}
	sha := &panicPlanner{name: "solver"}
	p := newTestShadowPlanner(pri, sha, 500)

	// Should not panic — goroutine recover() handles it.
	got, err := p.Plan(context.Background(), emptySnap(), nil)
	require.NoError(t, err)
	assert.Empty(t, got.Decisions.ScaleUps, "primary result should be returned")
}

func TestShadowPlanner_BothPlannersCalled(t *testing.T) {
	pri := &countingPlanner{name: "heuristic"}
	sha := &countingPlanner{name: "solver"}
	p := newTestShadowPlanner(pri, sha, 500)

	_, err := p.Plan(context.Background(), emptySnap(), nil)
	require.NoError(t, err)

	// Primary is called synchronously; shadow is goroutine. Allow a short
	// time for the goroutine to complete.
	assert.Eventually(t, func() bool {
		return sha.calls.Load() == 1
	}, 2*time.Second, 10*time.Millisecond, "shadow planner should be called once")

	assert.Equal(t, int64(1), pri.calls.Load(), "primary should be called once")
}

func TestShadowPlanner_PrimaryError_Returned(t *testing.T) {
	pri := &countingPlanner{name: "heuristic", err: assert.AnError}
	sha := &countingPlanner{name: "solver"}
	p := newTestShadowPlanner(pri, sha, 500)

	_, err := p.Plan(context.Background(), emptySnap(), nil)
	assert.ErrorIs(t, err, assert.AnError, "primary error should propagate to caller")
}

func TestShadowPlanner_ShadowTimeout_Respected(t *testing.T) {
	pri := &countingPlanner{name: "heuristic"}
	// Shadow planner sleeps longer than the timeout.
	sha := &countingPlanner{name: "solver", delay: 2 * time.Second}

	// Short timeout: 50ms.
	p := newTestShadowPlanner(pri, sha, 50)

	start := time.Now()
	result, err := p.Plan(context.Background(), emptySnap(), nil)
	elapsed := time.Since(start)

	require.NoError(t, err, "primary error should not occur")
	_ = result

	// Primary returns quickly (no delay).
	assert.Less(t, elapsed, 500*time.Millisecond,
		"Plan() should return quickly — shadow timeout must not block hot path")
}

func TestShadowPlanner_MetricsRecorded(t *testing.T) {
	// ShadowMetrics.Record is called after both planners complete.
	// We verify indirectly: both planners are called and no panic occurs.
	pri := &countingPlanner{name: "heuristic", result: ports.PlannerResult{
		Decisions: model.PlacementDecisions{
			ScaleUps: []model.ScaleUpDecision{{AppID: "app-A"}},
		},
	}}
	sha := &countingPlanner{name: "solver", result: ports.PlannerResult{
		Decisions: model.PlacementDecisions{
			Preemptions: []model.PreemptionDecision{{VictimReplicaID: "r1"}},
		},
	}}

	p := newTestShadowPlanner(pri, sha, 500)

	_, err := p.Plan(context.Background(), emptySnap(), nil)
	require.NoError(t, err)

	// Wait for goroutine to finish.
	assert.Eventually(t, func() bool {
		return sha.calls.Load() == 1
	}, 2*time.Second, 10*time.Millisecond, "shadow must complete")
}
