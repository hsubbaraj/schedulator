package model

import "time"

// ID type aliases — transparent string aliases for documentation purposes.
type (
	ClusterID     = string
	NodeID        = string
	AppID         = string
	ReplicaID     = string
	ModelID       = string
	ReservationID = string
	OperationID   = string
)

// Cluster represents a Kubernetes cluster in the fleet.
type Cluster struct {
	ClusterID          ClusterID
	Nodes              map[NodeID]Node
	FragmentationScore float64
}

// Node represents a GPU node within a cluster.
type Node struct {
	NodeID        NodeID
	ClusterID     ClusterID
	TotalGPUs     int
	AllocatedGPUs int
	FreeGPUs      int
	Pods          []ReplicaID
	CachedModels  map[ModelID]struct{}
	Status        NodeStatus
}

// Application represents a deployed LLM inference application.
type Application struct {
	AppID             AppID
	ModelID           ModelID
	GPUsPerReplica    int
	Priority          int // 0 = highest
	MinReplicas       int
	FailureDomainRule FailureDomainRule
	SLA               SLA
}

// SLA defines the performance targets for an application.
type SLA struct {
	AppID        AppID
	MaxP99TTFTMs int
	MaxP99TPS    int
}

// Replica represents a single-replica Deployment running an application.
type Replica struct {
	ReplicaID ReplicaID
	AppID     AppID
	ClusterID ClusterID
	NodeID    NodeID
	GPUs      int
	Status    ReplicaStatus
}

// Model represents an LLM model artifact.
type Model struct {
	ModelID ModelID
	Name    string
	SizeGB  int
}

// CacheLocation records where a model is cached. NodeID is empty for
// CLUSTER_STORAGE tier.
type CacheLocation struct {
	ModelID   ModelID
	ClusterID ClusterID
	NodeID    NodeID
	Tier      CacheTier
	CachedAt  time.Time
}

// PerformanceProfile contains profiled performance characteristics for an
// application.
type PerformanceProfile struct {
	AppID            AppID
	TPSPerReplica    float64
	TTFTP99Ms        float64
	ColdStartSeconds float64
	WarmStartSeconds float64
}

// VLLMMetrics contains aggregated vLLM runtime metrics for an application.
type VLLMMetrics struct {
	AppID                 AppID
	AggregatedFrom        int
	AvgWaitingQueueDepth  float64
	MaxWaitingQueueDepth  float64
	AvgRunningQueueDepth  float64
	AvgBatchUtilization   float64
	AvgKVCacheUtilization float64
	MaxKVCacheUtilization float64
	TotalTokensPerSecond  float64
	AvgTimeToFirstTokenMs float64
	P99TimeToFirstTokenMs float64
	MeasuredAt            time.Time
}

// GPUReservation represents reserved GPU capacity for a pending scale-up.
// NodeID is empty for cluster-scoped reservations.
type GPUReservation struct {
	ReservationID ReservationID
	ClusterID     ClusterID
	NodeID        NodeID
	GPUs          int
	ForAppID      AppID
	CreatedAt     time.Time
	TTLSeconds    int
	Status        ReservationStatus
}

// ScalingHistory tracks scaling event timestamps and consecutive signals for
// an application. Zero time.Time means "never scaled" — cooldowns auto-bypass.
type ScalingHistory struct {
	AppID                       AppID
	LastScaleUpAt               time.Time
	LastScaleDownAt             time.Time
	ConsecutiveScaleDownSignals int
}
