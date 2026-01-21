package state

import "time"

// ClusterState represents the aggregated state across all clusters
type ClusterState struct {
	Version      string     `json:"version"`
	Timestamp    time.Time  `json:"timestamp"`
	Clusters     []Cluster  `json:"clusters"`
	PendingQueue []Workload `json:"pendingQueue"`
}

// Cluster represents a single K8s cluster
type Cluster struct {
	ID      string         `json:"id"`
	Region  string         `json:"region"`
	Nodes   []Node         `json:"nodes"`
	Pods    []Pod          `json:"pods"`
	Summary ClusterSummary `json:"summary"`
}

// Node represents a K8s node
type Node struct {
	Name        string            `json:"name"`
	Labels      map[string]string `json:"labels"`
	Capacity    ResourceList      `json:"capacity"`
	Allocatable ResourceList      `json:"allocatable"`
	Allocated   ResourceList      `json:"allocated"`
	Conditions  map[string]string `json:"conditions"`
	Taints      []string          `json:"taints"`
}

// Pod represents a running pod
type Pod struct {
	Name      string       `json:"name"`
	Namespace string       `json:"namespace"`
	ModelID   string       `json:"modelId"`
	Phase     string       `json:"phase"`
	NodeName  string       `json:"nodeName"`
	Requests  ResourceList `json:"requests"`
}

// ResourceList represents resource quantities
type ResourceList struct {
	CPU    int64  `json:"cpu"`
	Memory string `json:"memory"`
	GPU    int    `json:"nvidia.com/gpu,omitempty"`
}

// ClusterSummary provides aggregate statistics
type ClusterSummary struct {
	TotalGPUs     int                       `json:"totalGPUs"`
	AllocatedGPUs int                       `json:"allocatedGPUs"`
	AvailableGPUs int                       `json:"availableGPUs"`
	Utilization   float64                   `json:"utilization"`
	Fragmentation map[string]FragmentationScore `json:"fragmentation,omitempty"`
}

// FragmentationScore represents fragmentation metrics for a specific size class
type FragmentationScore struct {
	PackableCount int     `json:"packableCount"`
	WastedGPUs    int     `json:"wastedGPUs"`
	Score         float64 `json:"score"`
}

// Workload represents a pending workload

type Workload struct {

	WorkloadID       string `json:"workloadId" yaml:"workloadId"`

	ModelID          string `json:"modelId" yaml:"modelId"`

	Replicas         int    `json:"replicas" yaml:"replicas"`

	GPUsPerReplica   int    `json:"gpusPerReplica" yaml:"gpusPerReplica"`

	CPUPerReplica    int    `json:"cpuPerReplica" yaml:"cpuPerReplica"`

	MemoryPerReplica string `json:"memoryPerReplica" yaml:"memoryPerReplica"`

	PriorityClass    string `json:"priorityClassName" yaml:"priorityClassName"`

}
