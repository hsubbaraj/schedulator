package eventlog

import "time"

// EventRecord represents a single scheduler event for persistence.
type EventRecord struct {
	ID         int64     `json:"id"`
	Timestamp  time.Time `json:"timestamp"`
	Type       string    `json:"type"`       // scale_up, scale_down, preempt, migrate, node_down, node_up, app_added, cycle_complete
	AppID      string    `json:"app_id"`
	ClusterID  string    `json:"cluster_id"`
	Summary    string    `json:"summary"`
	DetailJSON string    `json:"detail_json"`
}

// ClusterSnapshot represents a point-in-time GPU utilization snapshot.
type ClusterSnapshot struct {
	ID            int64     `json:"id"`
	Timestamp     time.Time `json:"timestamp"`
	ClusterID     string    `json:"cluster_id"`
	TotalGPUs     int       `json:"total_gpus"`
	AllocatedGPUs int       `json:"allocated_gpus"`
	FreeGPUs      int       `json:"free_gpus"`
	ReplicaCount  int       `json:"replica_count"`
}
