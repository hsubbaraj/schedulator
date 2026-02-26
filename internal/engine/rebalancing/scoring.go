package rebalancing

import (
	"github.com/hsubbaraj/schedulator/internal/engine/engineutil"
	"github.com/hsubbaraj/schedulator/internal/worldstate"
	"github.com/hsubbaraj/schedulator/pkg/model"
)

// consolidationTarget holds the chosen destination cluster and scheduling
// constraints for a rebalancing migration.
type consolidationTarget struct {
	clusterID   model.ClusterID
	constraints model.SchedulingConstraints
}

// findConsolidationTarget finds the best destination cluster for a replica,
// using tightest-fit packing (same logic as placement's computePackingScore).
// Returns nil if no suitable destination exists.
func (e *RebalancingEngine) findConsolidationTarget(
	replica model.Replica,
	app model.Application,
	snap worldstate.WorldStateSnapshot,
) (*consolidationTarget, bool) {
	var bestTarget *consolidationTarget
	bestWaste := -1

	for _, cluster := range snap.Clusters {
		if cluster.ClusterID == replica.ClusterID {
			continue
		}

		// Find the tightest-fit node in this cluster.
		bestNodeID := ""
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

		// Pick the cluster whose best node has the least waste.
		if bestWaste < 0 || bestNodeWaste < bestWaste {
			bestWaste = bestNodeWaste
			bestTarget = &consolidationTarget{
				clusterID: cluster.ClusterID,
				constraints: model.SchedulingConstraints{
					RequiredGPUs:       replica.GPUs,
					PreferredNodes:     []model.NodeID{bestNodeID},
					CacheAffinityModel: app.ModelID,
				},
			}
		}
	}

	if bestTarget == nil {
		return nil, false
	}
	return bestTarget, true
}

// estimateFragmentationDelta computes the total fragmentation improvement
// across source and destination clusters if gpus are migrated from sourceNode
// to the best-fit node in destCluster.
func estimateFragmentationDelta(
	sourceNode model.Node,
	destCluster model.Cluster,
	gpus int,
	snap worldstate.WorldStateSnapshot,
) float64 {
	// Find source cluster to get current fragmentation.
	sourceCluster, ok := snap.Clusters[sourceNode.ClusterID]
	if !ok {
		return 0.0
	}

	currentSourceFrag := sourceCluster.FragmentationScore
	currentDestFrag := destCluster.FragmentationScore

	// Compute new source fragmentation after removing gpus from sourceNode.
	newSourceFrag := computeFragmentationAfterRemoval(sourceCluster.Nodes, sourceNode.NodeID, gpus)

	// Find best-fit node in dest cluster and compute new dest fragmentation.
	bestNodeID := ""
	bestWaste := -1
	for _, node := range destCluster.Nodes {
		if node.Status != model.NodeStatusReady {
			continue
		}
		if node.FreeGPUs < gpus {
			continue
		}
		waste := node.FreeGPUs - gpus
		if bestWaste < 0 || waste < bestWaste {
			bestWaste = waste
			bestNodeID = node.NodeID
		}
	}

	newDestFrag := currentDestFrag
	if bestNodeID != "" {
		newDestFrag = computeFragmentationAfterAddition(destCluster.Nodes, bestNodeID, gpus)
	}

	return (currentSourceFrag + currentDestFrag) - (newSourceFrag + newDestFrag)
}

// computeFragmentationAfterRemoval computes the fragmentation score for a set
// of nodes after removing gpus from the specified node.
func computeFragmentationAfterRemoval(nodes map[model.NodeID]model.Node, nodeID model.NodeID, gpus int) float64 {
	totalCapacity := 0
	totalFree := 0
	for _, n := range nodes {
		totalCapacity += n.TotalGPUs
		if n.NodeID == nodeID {
			totalFree += n.FreeGPUs + gpus
		} else {
			totalFree += n.FreeGPUs
		}
	}
	if totalCapacity == 0 {
		return 0.0
	}
	return 1.0 - float64(totalFree)/float64(totalCapacity)
}

// computeFragmentationAfterAddition computes the fragmentation score for a set
// of nodes after adding gpus to the specified node.
func computeFragmentationAfterAddition(nodes map[model.NodeID]model.Node, nodeID model.NodeID, gpus int) float64 {
	totalCapacity := 0
	totalFree := 0
	for _, n := range nodes {
		totalCapacity += n.TotalGPUs
		if n.NodeID == nodeID {
			totalFree += n.FreeGPUs - gpus
		} else {
			totalFree += n.FreeGPUs
		}
	}
	if totalCapacity == 0 {
		return 0.0
	}
	return 1.0 - float64(totalFree)/float64(totalCapacity)
}

// hasCachedModel checks whether any cache location exists for the given model
// in the specified cluster. Any cache tier qualifies.
func hasCachedModel(
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

// countRunningReplicas counts replicas with Status == ReplicaStatusRunning for
// the given app. Per the proposal's count_running_replicas, this excludes
// pending replicas.
func countRunningReplicas(appID model.AppID, snap worldstate.WorldStateSnapshot) int {
	count := 0
	for _, r := range engineutil.ReplicasForApp(snap, appID) {
		if r.Status == model.ReplicaStatusRunning {
			count++
		}
	}
	return count
}
