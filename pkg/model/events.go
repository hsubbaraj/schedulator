package model

import "time"

// ClusterEventKind identifies the type of cluster event.
type ClusterEventKind string

const (
	ClusterEventNodeDown      ClusterEventKind = "NodeDown"
	ClusterEventNodeUp        ClusterEventKind = "NodeUp"
	ClusterEventPodTerminated ClusterEventKind = "PodTerminated"
	ClusterEventPodRunning    ClusterEventKind = "PodRunning"
)

// ClusterEvent represents a state change in a cluster (node or pod level).
type ClusterEvent struct {
	Kind      ClusterEventKind
	ClusterID ClusterID
	NodeID    NodeID
	ReplicaID ReplicaID
	AppID     AppID
	GPUs      int
	TotalGPUs int // For node events: total GPU capacity of the node.
	Timestamp time.Time
}
