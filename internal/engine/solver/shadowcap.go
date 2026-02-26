package solver

import (
	"context"

	"github.com/google/uuid"

	"github.com/hsubbaraj/schedulator/internal/engine/engineutil"
	"github.com/hsubbaraj/schedulator/internal/worldstate"
	"github.com/hsubbaraj/schedulator/pkg/model"
)

// ShadowCapComputer wraps the shadow capacity pre-pass computation.
// It enumerates migration candidates from the world state and exposes them
// as costed capacity resources for the solver.
type ShadowCapComputer struct{}

// NewShadowCapComputer returns a new ShadowCapComputer.
func NewShadowCapComputer() *ShadowCapComputer {
	return &ShadowCapComputer{}
}

// Compute returns up to cfg.MaxShadowSlots migration opportunities that the
// solver may claim as costed capacity resources.
func (s *ShadowCapComputer) Compute(
	ctx context.Context,
	snap worldstate.WorldStateSnapshot,
	cfg SolverConfig,
) []model.ShadowSlot {
	return ComputeShadowSlots(ctx, snap, cfg)
}

// ComputeShadowSlots returns up to cfg.MaxShadowSlots migration opportunities
// that the solver may claim as costed capacity resources.
// It mirrors the RebalancingEngine's candidate-enumeration logic but produces
// ShadowSlot values instead of MigrateDecisions.
func ComputeShadowSlots(
	_ context.Context,
	snap worldstate.WorldStateSnapshot,
	cfg SolverConfig,
) []model.ShadowSlot {
	var slots []model.ShadowSlot

	for _, cluster := range snap.Clusters {
		if cluster.FragmentationScore < shadowMinFragmentationDelta {
			continue
		}

		for _, node := range cluster.Nodes {
			if node.Status != model.NodeStatusReady {
				continue
			}
			// Fragmented node: partially allocated.
			if node.AllocatedGPUs <= 0 || node.AllocatedGPUs >= node.TotalGPUs {
				continue
			}

			for _, replicaID := range node.Pods {
				replica, ok := snap.Replicas[replicaID]
				if !ok {
					continue
				}

				app, ok := snap.Applications[replica.AppID]
				if !ok {
					continue
				}

				// min_replicas guard.
				if shadowCountRunning(replica.AppID, snap) <= app.MinReplicas {
					continue
				}

				// Find consolidation target.
				dest, found := shadowFindConsolidationTarget(replica, snap)
				if !found {
					continue
				}

				// Destination must have the model cached.
				if !shadowHasCachedModel(dest.clusterID, app.ModelID, snap) {
					continue
				}

				slots = append(slots, model.ShadowSlot{
					SlotID:          uuid.New().String(),
					SourceClusterID: cluster.ClusterID,
					ReleasableGPUs:  replica.GPUs,
					VictimReplicaID: replica.ReplicaID,
					VictimAppID:     replica.AppID,
					DestClusterID:   dest.clusterID,
					DestConstraints: dest.constraints,
					MigrationCost:   float64(cfg.WShadow),
				})

				if len(slots) >= cfg.MaxShadowSlots {
					return slots
				}
			}
		}
	}

	return slots
}

// shadowMinFragmentationDelta mirrors RebalancingConfig.MinFragmentationDelta.
// It is a package-level constant to avoid importing rebalancing.
const shadowMinFragmentationDelta = 0.15

// shadowConsolidationTarget holds the chosen destination for a shadow migration.
type shadowConsolidationTarget struct {
	clusterID   model.ClusterID
	constraints model.SchedulingConstraints
}

// shadowFindConsolidationTarget finds the tightest-fit destination cluster for
// a replica, excluding the replica's current cluster.
func shadowFindConsolidationTarget(
	replica model.Replica,
	snap worldstate.WorldStateSnapshot,
) (*shadowConsolidationTarget, bool) {
	var bestTarget *shadowConsolidationTarget
	bestWaste := -1

	for _, cluster := range snap.Clusters {
		if cluster.ClusterID == replica.ClusterID {
			continue
		}

		bestNodeID := model.NodeID("")
		bestNodeWaste := -1
		for _, node := range cluster.Nodes {
			if node.Status != model.NodeStatusReady {
				continue
			}
			if node.FreeGPUs < replica.GPUs {
				continue
			}
			waste := node.FreeGPUs - replica.GPUs
			if bestNodeWaste < 0 || waste < bestNodeWaste {
				bestNodeWaste = waste
				bestNodeID = node.NodeID
			}
		}

		if bestNodeID == "" {
			continue
		}

		if bestWaste < 0 || bestNodeWaste < bestWaste {
			bestWaste = bestNodeWaste

			app, hasApp := snap.Applications[replica.AppID]
			var cacheAffinityModel model.ModelID
			if hasApp {
				cacheAffinityModel = app.ModelID
			}

			bestTarget = &shadowConsolidationTarget{
				clusterID: cluster.ClusterID,
				constraints: model.SchedulingConstraints{
					RequiredGPUs:       replica.GPUs,
					PreferredNodes:     []model.NodeID{bestNodeID},
					CacheAffinityModel: cacheAffinityModel,
				},
			}
		}
	}

	if bestTarget == nil {
		return nil, false
	}
	return bestTarget, true
}

// shadowHasCachedModel checks whether any cache location exists for modelID
// in clusterID.
func shadowHasCachedModel(
	clusterID model.ClusterID,
	modelID model.ModelID,
	snap worldstate.WorldStateSnapshot,
) bool {
	for _, loc := range snap.CacheLocations[modelID] {
		if loc.ClusterID == clusterID {
			return true
		}
	}
	return false
}

// shadowCountRunning counts replicas with ReplicaStatusRunning for the given app.
func shadowCountRunning(appID model.AppID, snap worldstate.WorldStateSnapshot) int {
	count := 0
	for _, r := range engineutil.ReplicasForApp(snap, appID) {
		if r.Status == model.ReplicaStatusRunning {
			count++
		}
	}
	return count
}
