package placement

import (
	"math"
	"sort"

	"github.com/hsubbaraj/schedulator/internal/worldstate"
	"github.com/hsubbaraj/schedulator/pkg/model"
)

// computeCacheScore returns a tier-weighted cache affinity score, preferred
// nodes for scheduling, and the cache tier name. Implements the cache scoring
// algorithm from proposal Section 4.4.
func (e *PlacementEngine) computeCacheScore(
	modelID model.ModelID,
	clusterID model.ClusterID,
	snap worldstate.WorldStateSnapshot,
) (float64, []model.NodeID, string) {
	locations := snap.CacheLocations[modelID]

	var gpuMemoryNodes []model.NodeID
	var localNVMeNodes []model.NodeID
	clusterStorage := false

	for _, loc := range locations {
		if loc.ClusterID != clusterID {
			continue
		}
		switch loc.Tier {
		case model.CacheTierGPUMemory:
			gpuMemoryNodes = append(gpuMemoryNodes, loc.NodeID)
		case model.CacheTierLocalNVMe:
			localNVMeNodes = append(localNVMeNodes, loc.NodeID)
		case model.CacheTierClusterStorage:
			clusterStorage = true
		}
	}

	if len(gpuMemoryNodes) > 0 {
		preferred := make([]model.NodeID, 0, len(gpuMemoryNodes)+len(localNVMeNodes))
		preferred = append(preferred, gpuMemoryNodes...)
		preferred = append(preferred, localNVMeNodes...)
		return e.cfg.WeightCacheGPUMemory, preferred, "GPU_MEMORY"
	}
	if len(localNVMeNodes) > 0 {
		return e.cfg.WeightCacheLocalNVMe, localNVMeNodes, "LOCAL_NVME"
	}
	if clusterStorage {
		return e.cfg.WeightCacheClusterStorage, nil, "CLUSTER_STORAGE"
	}
	return 0, nil, "REMOTE"
}

// computePackingScore returns a 0.0–1.0 score reflecting how tightly a cluster
// can pack the requested GPUs. 1.0 means exact fit (zero waste). Implements
// the packing score algorithm from proposal Section 4.4.
func (e *PlacementEngine) computePackingScore(cluster model.Cluster, gpusNeeded int) float64 {
	bestFit := -1
	for _, n := range cluster.Nodes {
		if n.Status != model.NodeStatusReady {
			continue
		}
		if n.FreeGPUs < gpusNeeded {
			continue
		}
		if bestFit < 0 || n.FreeGPUs < bestFit {
			bestFit = n.FreeGPUs
		}
	}
	if bestFit < 0 {
		return 0.0
	}
	waste := bestFit - gpusNeeded
	score := 1.0 - float64(waste)/float64(e.cfg.MaxGPUsPerNode)
	return math.Max(0.0, math.Min(1.0, score))
}

// selectScaleDownVictims selects replicas to remove for a scale-down. Replicas
// are sorted by removal preference:
//  1. Lower node utilization (higher free GPUs) → remove first (to empty nodes)
//  2. Non-cached replicas → remove first (less cache impact)
//  3. Over-represented clusters → remove first (balance)
func (e *PlacementEngine) selectScaleDownVictims(
	appID model.AppID,
	count int,
	snap worldstate.WorldStateSnapshot,
) []model.ReplicaID {
	type candidate struct {
		replicaID    model.ReplicaID
		utilScore    float64
		cached       bool
		clusterCount int
	}

	// Count replicas per cluster for this app.
	clusterCounts := make(map[model.ClusterID]int)
	var candidates []candidate
	for _, r := range snap.Replicas {
		if r.AppID != appID {
			continue
		}
		if r.Status != model.ReplicaStatusRunning && r.Status != model.ReplicaStatusPending {
			continue
		}
		clusterCounts[r.ClusterID]++
	}

	app := snap.Applications[appID]
	for _, r := range snap.Replicas {
		if r.AppID != appID {
			continue
		}
		if r.Status != model.ReplicaStatusRunning && r.Status != model.ReplicaStatusPending {
			continue
		}

		// Node utilization: 1 - free/total. Lower = less utilized = remove first to empty it.
		utilScore := 0.0
		if cluster, ok := snap.Clusters[r.ClusterID]; ok {
			if node, ok := cluster.Nodes[r.NodeID]; ok && node.TotalGPUs > 0 {
				utilScore = 1.0 - float64(node.FreeGPUs)/float64(node.TotalGPUs)
			}
		}

		// Check if this replica's model is cached on its node.
		cached := false
		for _, loc := range snap.CacheLocations[app.ModelID] {
			if loc.ClusterID == r.ClusterID && loc.NodeID == r.NodeID {
				cached = true
				break
			}
		}

		candidates = append(candidates, candidate{
			replicaID:    r.ReplicaID,
			utilScore:    utilScore,
			cached:       cached,
			clusterCount: clusterCounts[r.ClusterID],
		})
	}

	// Sort: prefer removing replicas with lower utilization (to empty nodes),
	// then non-cached, then from over-represented clusters.
	sort.Slice(candidates, func(i, j int) bool {
		a, b := candidates[i], candidates[j]
		// Lower utilization first.
		if a.utilScore != b.utilScore {
			return a.utilScore < b.utilScore
		}
		// Non-cached first.
		if a.cached != b.cached {
			return !a.cached
		}
		// Over-represented cluster first.
		return a.clusterCount > b.clusterCount
	})

	if count > len(candidates) {
		count = len(candidates)
	}
	result := make([]model.ReplicaID, count)
	for i := 0; i < count; i++ {
		result[i] = candidates[i].replicaID
	}
	return result
}
