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

// ---------------------------------------------------------------------------
// Shadow slot integration tests (§9.5)
// ---------------------------------------------------------------------------

func TestGeneratePlan_NoShadowSlots_Unchanged(t *testing.T) {
	g := newTestGenerator()

	decisions := model.PlacementDecisions{
		ScaleUps: []model.ScaleUpDecision{
			{AppID: "a1", ClusterID: "c1"},
		},
	}

	// Without shadow slots, behaviour must be identical to pre-change.
	plan := g.GeneratePlan(context.Background(), decisions, nil)

	require.Len(t, plan.Operations, 1)
	assert.Equal(t, model.OperationScaleUp, plan.Operations[0].Type)
	assert.Empty(t, plan.Operations[0].DependsOn)
}

func TestGeneratePlan_ClaimedShadowSlot_EmitsMigrateOp(t *testing.T) {
	g := newTestGenerator()

	decisions := model.PlacementDecisions{
		ClaimedShadowSlots: []model.ShadowSlot{
			{
				SlotID:          "slot-1",
				SourceClusterID: "c1",
				ReleasableGPUs:  4,
				VictimReplicaID: "r-victim",
				VictimAppID:     "app-victim",
				DestClusterID:   "c2",
				DestConstraints: model.SchedulingConstraints{RequiredGPUs: 4},
				MigrationCost:   500.0,
			},
		},
	}

	plan := g.GeneratePlan(context.Background(), decisions, nil)

	// Should produce exactly 2 operations: scale-up (migrate to dest) + scale-down (old replica).
	require.Len(t, plan.Operations, 2, "one shadow slot → one scale-up + one scale-down")

	suOp := plan.Operations[0]
	require.Equal(t, model.OperationScaleUp, suOp.Type)
	suPayload, ok := suOp.Payload.(model.ScaleUpDecision)
	require.True(t, ok)
	assert.Equal(t, model.AppID("app-victim"), suPayload.AppID)
	assert.Equal(t, model.ClusterID("c2"), suPayload.ClusterID)
	assert.Equal(t, 4, suPayload.SchedulingConstraints.RequiredGPUs)

	sdOp := plan.Operations[1]
	require.Equal(t, model.OperationScaleDown, sdOp.Type)
	sdPayload, ok := sdOp.Payload.(model.ScaleDownDecision)
	require.True(t, ok)
	assert.Equal(t, model.ReplicaID("r-victim"), sdPayload.ReplicaID)
	assert.Equal(t, model.AppID("app-victim"), sdPayload.AppID)

	// Scale-down depends on scale-up.
	require.Len(t, sdOp.DependsOn, 1)
	assert.Equal(t, suOp.ID, sdOp.DependsOn[0])
}

func TestGeneratePlan_ScaleUpDependsOnShadowMigration(t *testing.T) {
	g := newTestGenerator()

	// Shadow slot frees capacity on c1. A scale-up also targets c1.
	decisions := model.PlacementDecisions{
		ScaleUps: []model.ScaleUpDecision{
			{AppID: "app-new", ClusterID: "c1"},
		},
		ClaimedShadowSlots: []model.ShadowSlot{
			{
				SlotID:          "slot-1",
				SourceClusterID: "c1", // frees GPUs on c1
				VictimReplicaID: "r-victim",
				VictimAppID:     "app-victim",
				DestClusterID:   "c2",
				DestConstraints: model.SchedulingConstraints{RequiredGPUs: 4},
			},
		},
	}

	plan := g.GeneratePlan(context.Background(), decisions, nil)

	// Expect: shadow scale-up (migrate victim to c2), regular scale-up (c1), shadow scale-down.
	// Total: 3 ops.
	require.Len(t, plan.Operations, 3)

	// Identify shadow migration scale-up (first, from Phase 4).
	// The ops order is: [Phase 3: regular SU] [Phase 4: shadow SU + shadow SD].
	// But shadow SU ID was pre-assigned and regular SU depends on it.
	// Find the shadow scale-up by payload (app-victim → c2).
	var shadowSU, regularSU model.Operation
	for _, op := range plan.Operations {
		if op.Type != model.OperationScaleUp {
			continue
		}
		su, ok := op.Payload.(model.ScaleUpDecision)
		if !ok {
			continue
		}
		if su.AppID == "app-victim" {
			shadowSU = op
		} else if su.AppID == "app-new" {
			regularSU = op
		}
	}

	require.NotEmpty(t, shadowSU.ID, "shadow migration scale-up must exist")
	require.NotEmpty(t, regularSU.ID, "regular scale-up for app-new must exist")

	// The regular scale-up must depend on the shadow migration's scale-up.
	assert.Contains(t, regularSU.DependsOn, shadowSU.ID,
		"scale-up targeting c1 must wait for shadow migration that frees c1 capacity")
}
