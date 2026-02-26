//go:build solver

package solver

// translate.go — Converts a CPSATSolution into a ports.PlannerResult.
// This is the bridge between the OR-Tools solver output and the scheduler's
// decision types consumed by PlanGenerator.

import (
	"context"
	"log/slog"

	"github.com/google/uuid"

	"github.com/hsubbaraj/schedulator/internal/worldstate"
	"github.com/hsubbaraj/schedulator/pkg/model"
	"github.com/hsubbaraj/schedulator/pkg/ports"
)

// TranslateResult converts a CPSATSolution into a PlannerResult.
//
// Translation rules (spec §5.6):
//   - x[r][n] == 1 (direct placement)    → ScaleUpDecision with PreferredNodes=[n]
//   - y[r][k] == 1 (shadow placement)    → ScaleUpDecision with PreferredNodes=[] (scheduler picks node)
//   - p[r] == 1                           → PreemptionDecision
//   - s[k] == 1                           → append slot to ClaimedShadowSlots
//   - u[a] > 0                            → slog.Warn + unmet demand logged
func TranslateResult(
	ctx context.Context,
	solution CPSATSolution,
	newReplicas []newReplicaDef,
	preemptVars []preemptVarDef,
	shadowSlots []model.ShadowSlot,
	snap worldstate.WorldStateSnapshot,
	_ SolverConfig,
) ports.PlannerResult {
	var result ports.PlannerResult

	// --- ScaleUpDecisions ---
	for ri, assign := range solution.Assignments {
		rep := newReplicas[ri]
		app := snap.Applications[rep.appID]

		var preferred []model.NodeID
		if assign.nodeID != "" {
			// Direct node placement (x[r][n] == 1): hint the scheduler.
			preferred = []model.NodeID{assign.nodeID}
		}
		// Shadow placement (y[r][k] == 1): no node hint — the K8s scheduler
		// selects a node after the migration has freed capacity.

		result.Decisions.ScaleUps = append(result.Decisions.ScaleUps, model.ScaleUpDecision{
			AppID:         rep.appID,
			ClusterID:     assign.clusterID,
			ReservationID: uuid.New().String(),
			SchedulingConstraints: model.SchedulingConstraints{
				RequiredGPUs:       rep.reqGPUs,
				PreferredNodes:     preferred,
				CacheAffinityModel: app.ModelID,
			},
		})
	}

	// --- PreemptionDecisions ---
	for _, vc := range preemptVars {
		if !solution.Preemptions[vc.replicaID] {
			continue
		}
		result.Decisions.Preemptions = append(result.Decisions.Preemptions, model.PreemptionDecision{
			VictimReplicaID:    vc.replicaID,
			VictimAppID:        vc.appID,
			NodeID:             vc.nodeID,
			ClusterID:          vc.clusterID,
			GracePeriodSeconds: 30,
			Reason:             "solver: capacity reclaimed for higher-priority workload",
		})
	}

	// --- ClaimedShadowSlots ---
	for _, slot := range shadowSlots {
		if solution.ClaimedSlots[slot.SlotID] {
			result.Decisions.ClaimedShadowSlots = append(result.Decisions.ClaimedShadowSlots, slot)
		}
	}

	// --- Unmet demand warnings ---
	for appID, count := range solution.UnmetDemand {
		if count > 0 {
			slog.WarnContext(ctx, "solver: unmet demand",
				"app_id", string(appID),
				"unmet_count", count,
			)
		}
	}

	return result
}
