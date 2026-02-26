//go:build !solver

package solver_test

import (
	"context"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hsubbaraj/schedulator/internal/engine/scaling"
	"github.com/hsubbaraj/schedulator/internal/engine/solver"
	"github.com/hsubbaraj/schedulator/internal/observability"
	"github.com/hsubbaraj/schedulator/internal/worldstate"
	"github.com/hsubbaraj/schedulator/pkg/model"
	"github.com/hsubbaraj/schedulator/pkg/ports"
)

// stubFallback is a minimal ports.Planner for use in tests.
type stubFallback struct {
	calls  int
	result ports.PlannerResult
	err    error
}

func (f *stubFallback) Plan(_ context.Context, _ worldstate.WorldStateSnapshot, _ map[model.AppID]scaling.ScalingDecision) (ports.PlannerResult, error) {
	f.calls++
	return f.result, f.err
}

func (f *stubFallback) Name() string { return "stub-fallback" }

func newTestSolverPlanner(fallback ports.Planner) *solver.SolverPlanner {
	return solver.NewSolverPlanner(
		solver.DefaultSolverConfig(),
		nil, // rebalancing engine — unused in stub
		fallback,
		observability.NewNoopTracer(),
		prometheus.NewRegistry(),
	)
}

func TestSolverPlanner_Name(t *testing.T) {
	p := newTestSolverPlanner(&stubFallback{})
	assert.Equal(t, "solver", p.Name())
}

func TestSolverPlanner_StubDelegatesToFallback(t *testing.T) {
	expected := ports.PlannerResult{
		Decisions: model.PlacementDecisions{
			ScaleUps: []model.ScaleUpDecision{{AppID: "app-A", ClusterID: "c1"}},
		},
	}
	fb := &stubFallback{result: expected}

	p := newTestSolverPlanner(fb)

	snap := worldstate.WorldStateSnapshot{
		Clusters:     map[model.ClusterID]model.Cluster{},
		Applications: map[model.AppID]model.Application{},
		Replicas:     map[model.ReplicaID]model.Replica{},
	}

	result, err := p.Plan(context.Background(), snap, nil)
	require.NoError(t, err)
	assert.Equal(t, 1, fb.calls, "fallback should be called once")
	assert.Equal(t, expected.Decisions.ScaleUps, result.Decisions.ScaleUps)
}

func TestSolverPlanner_StubFallbackError_Propagated(t *testing.T) {
	fb := &stubFallback{err: assert.AnError}
	p := newTestSolverPlanner(fb)

	snap := worldstate.WorldStateSnapshot{
		Clusters:     map[model.ClusterID]model.Cluster{},
		Applications: map[model.AppID]model.Application{},
		Replicas:     map[model.ReplicaID]model.Replica{},
	}

	_, err := p.Plan(context.Background(), snap, nil)
	assert.ErrorIs(t, err, assert.AnError)
}
