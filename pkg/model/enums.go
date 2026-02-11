package model

// NodeStatus represents the operational state of a node.
type NodeStatus string

const (
	NodeStatusReady    NodeStatus = "ready"
	NodeStatusCordoned NodeStatus = "cordoned"
	NodeStatusDraining NodeStatus = "draining"
	NodeStatusDown     NodeStatus = "down"
)

// ReplicaStatus represents the lifecycle state of a replica.
type ReplicaStatus string

const (
	ReplicaStatusPending    ReplicaStatus = "pending"
	ReplicaStatusRunning    ReplicaStatus = "running"
	ReplicaStatusDraining   ReplicaStatus = "draining"
	ReplicaStatusTerminated ReplicaStatus = "terminated"
)

// AllReplicaStatuses enumerates every valid ReplicaStatus value.
var AllReplicaStatuses = []ReplicaStatus{
	ReplicaStatusPending,
	ReplicaStatusRunning,
	ReplicaStatusDraining,
	ReplicaStatusTerminated,
}

// CacheTier represents the storage tier where a model is cached.
type CacheTier string

const (
	CacheTierGPUMemory      CacheTier = "GPU_MEMORY"
	CacheTierLocalNVMe      CacheTier = "LOCAL_NVME"
	CacheTierClusterStorage CacheTier = "CLUSTER_STORAGE"
)

// FailureDomainRule controls replica placement across failure domains.
type FailureDomainRule string

const (
	FailureDomainSpreadClusters FailureDomainRule = "spread_clusters"
	FailureDomainNone           FailureDomainRule = "none"
)

// ReservationStatus represents the lifecycle state of a GPU reservation.
type ReservationStatus string

const (
	ReservationStatusActive    ReservationStatus = "active"
	ReservationStatusFulfilled ReservationStatus = "fulfilled"
	ReservationStatusExpired   ReservationStatus = "expired"
)
