package worldstate

import (
	"maps"
	"slices"
	"time"

	"github.com/hsubbaraj/schedulator/pkg/model"
)

// WorldStateSnapshot is an immutable deep copy of the world state. Engines
// receive this via WorldState.Snapshot() and may read it freely without locks.
type WorldStateSnapshot struct {
	Clusters            map[model.ClusterID]model.Cluster
	Applications        map[model.AppID]model.Application
	Replicas            map[model.ReplicaID]model.Replica
	ReplicasByApp       map[model.AppID][]model.Replica
	CacheLocations      map[model.ModelID][]model.CacheLocation
	PerformanceProfiles map[model.AppID]model.PerformanceProfile
	VLLMMetrics         map[model.AppID]model.VLLMMetrics
	Reservations        map[model.ReservationID]model.GPUReservation
	ScalingHistory      map[model.AppID]model.ScalingHistory
	TakenAt             time.Time
}

// buildReplicasByApp constructs an AppID -> []Replica secondary index from a
// snapshot's cloned replica map.
func buildReplicasByApp(replicas map[model.ReplicaID]model.Replica) map[model.AppID][]model.Replica {
	idx := make(map[model.AppID][]model.Replica, max(1, len(replicas)/4))
	for _, r := range replicas {
		idx[r.AppID] = append(idx[r.AppID], r)
	}
	return idx
}

// copyClusters deep-copies the clusters map including nested Nodes, Pods
// slices, and CachedModels maps.
func copyClusters(src map[model.ClusterID]model.Cluster) map[model.ClusterID]model.Cluster {
	dst := make(map[model.ClusterID]model.Cluster, len(src))
	for id, c := range src {
		dst[id] = copyCluster(c)
	}
	return dst
}

func copyCluster(c model.Cluster) model.Cluster {
	c.Nodes = copyNodes(c.Nodes)
	return c
}

func copyNodes(src map[model.NodeID]model.Node) map[model.NodeID]model.Node {
	if src == nil {
		return nil
	}
	dst := make(map[model.NodeID]model.Node, len(src))
	for id, n := range src {
		dst[id] = copyNode(n)
	}
	return dst
}

func copyNode(n model.Node) model.Node {
	n.Pods = slices.Clone(n.Pods)
	n.CachedModels = maps.Clone(n.CachedModels)
	return n
}

// copyCacheLocations deep-copies the cache locations map (outer map → inner
// slices).
func copyCacheLocations(src map[model.ModelID][]model.CacheLocation) map[model.ModelID][]model.CacheLocation {
	dst := make(map[model.ModelID][]model.CacheLocation, len(src))
	for id, locs := range src {
		dst[id] = slices.Clone(locs)
	}
	return dst
}
