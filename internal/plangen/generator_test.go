package plangen

import (
	"context"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hsubbaraj/schedulator/internal/observability"
	"github.com/hsubbaraj/schedulator/pkg/model"
)

func newTestGenerator() *PlanGenerator {
	return NewPlanGenerator(observability.NewNoopTracer(), prometheus.NewRegistry())
}

func TestGeneratePlan_OperationOrdering(t *testing.T) {
	g := newTestGenerator()

	decisions := model.PlacementDecisions{
		Preemptions: []model.PreemptionDecision{
			{VictimReplicaID: "r1", VictimAppID: "a1", ClusterID: "c1"},
		},
		ScaleDowns: []model.ScaleDownDecision{
			{AppID: "a2", ReplicaID: "r2"},
		},
		ScaleUps: []model.ScaleUpDecision{
			{AppID: "a3", ClusterID: "c1"},
		},
	}

	plan := g.GeneratePlan(context.Background(), decisions, nil)

	require.Len(t, plan.Operations, 3)

	// Verify ordering: preempt → scale_down → scale_up.
	assert.Equal(t, model.OperationPreempt, plan.Operations[0].Type)
	assert.Equal(t, model.OperationScaleDown, plan.Operations[1].Type)
	assert.Equal(t, model.OperationScaleUp, plan.Operations[2].Type)
}

func TestGeneratePlan_PreemptionDependency(t *testing.T) {
	g := newTestGenerator()

	decisions := model.PlacementDecisions{
		Preemptions: []model.PreemptionDecision{
			{VictimReplicaID: "r1", VictimAppID: "a1", ClusterID: "c1"},
			{VictimReplicaID: "r2", VictimAppID: "a2", ClusterID: "c1"},
			{VictimReplicaID: "r3", VictimAppID: "a3", ClusterID: "c2"},
		},
		ScaleUps: []model.ScaleUpDecision{
			{AppID: "a4", ClusterID: "c1"},
			{AppID: "a5", ClusterID: "c2"},
			{AppID: "a6", ClusterID: "c3"}, // no preemptions in c3
		},
	}

	plan := g.GeneratePlan(context.Background(), decisions, nil)

	require.Len(t, plan.Operations, 6)

	// Collect preemption op IDs by cluster.
	preemptC1 := []model.OperationID{plan.Operations[0].ID, plan.Operations[1].ID}
	preemptC2 := []model.OperationID{plan.Operations[2].ID}

	// Scale-up for c1 should depend on both c1 preemptions.
	suC1 := plan.Operations[3]
	assert.Equal(t, model.OperationScaleUp, suC1.Type)
	assert.ElementsMatch(t, preemptC1, suC1.DependsOn)

	// Scale-up for c2 should depend on c2 preemption.
	suC2 := plan.Operations[4]
	assert.Equal(t, model.OperationScaleUp, suC2.Type)
	assert.ElementsMatch(t, preemptC2, suC2.DependsOn)

	// Scale-up for c3 should have no dependencies.
	suC3 := plan.Operations[5]
	assert.Equal(t, model.OperationScaleUp, suC3.Type)
	assert.Empty(t, suC3.DependsOn)
}

func TestGeneratePlan_MigrationDecomposition(t *testing.T) {
	g := newTestGenerator()

	migrations := []model.MigrateDecision{
		{
			AppID:           "a1",
			SourceReplicaID: "r1",
			TargetClusterID: "c2",
			SchedulingConstraints: model.SchedulingConstraints{
				RequiredGPUs: 4,
			},
		},
	}

	plan := g.GeneratePlan(context.Background(), model.PlacementDecisions{}, migrations)

	require.Len(t, plan.Operations, 2)

	// First op: scale_up for the new replica.
	suOp := plan.Operations[0]
	assert.Equal(t, model.OperationScaleUp, suOp.Type)
	assert.Empty(t, suOp.DependsOn)

	suPayload, ok := suOp.Payload.(model.ScaleUpDecision)
	require.True(t, ok)
	assert.Equal(t, model.AppID("a1"), suPayload.AppID)
	assert.Equal(t, model.ClusterID("c2"), suPayload.ClusterID)
	assert.Equal(t, 4, suPayload.SchedulingConstraints.RequiredGPUs)

	// Second op: scale_down for the old replica, depends on scale_up.
	sdOp := plan.Operations[1]
	assert.Equal(t, model.OperationScaleDown, sdOp.Type)
	require.Len(t, sdOp.DependsOn, 1)
	assert.Equal(t, suOp.ID, sdOp.DependsOn[0])

	sdPayload, ok := sdOp.Payload.(model.ScaleDownDecision)
	require.True(t, ok)
	assert.Equal(t, model.AppID("a1"), sdPayload.AppID)
	assert.Equal(t, model.ReplicaID("r1"), sdPayload.ReplicaID)
}

func TestGeneratePlan_IndependentOpsNoDependency(t *testing.T) {
	g := newTestGenerator()

	decisions := model.PlacementDecisions{
		ScaleUps: []model.ScaleUpDecision{
			{AppID: "a1", ClusterID: "c1"},
			{AppID: "a2", ClusterID: "c2"},
		},
		ScaleDowns: []model.ScaleDownDecision{
			{AppID: "a3", ReplicaID: "r3"},
		},
	}

	plan := g.GeneratePlan(context.Background(), decisions, nil)

	require.Len(t, plan.Operations, 3)

	for _, op := range plan.Operations {
		assert.Empty(t, op.DependsOn, "op %s (%s) should have no dependencies", op.ID, op.Type)
	}
}

func TestGeneratePlan_EmptyDecisions(t *testing.T) {
	g := newTestGenerator()

	plan := g.GeneratePlan(context.Background(), model.PlacementDecisions{}, nil)

	assert.Empty(t, plan.Operations)
}
