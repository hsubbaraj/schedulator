package config

// Scenario defines the configuration for a simulation run.
type Scenario struct {
	Name         string      `yaml:"name"`
	Duration     string      `yaml:"duration"`
	TickInterval string      `yaml:"tick_interval"`
	Topology     Topology    `yaml:"topology"`
	Apps         []AppConfig `yaml:"apps"`
	Workload     string      `yaml:"workload"`
	ClosedLoop   bool        `yaml:"closed_loop"`
}

// Topology defines the cluster infrastructure.
type Topology struct {
	Clusters []ClusterConfig `yaml:"clusters"`
}

// ClusterConfig defines a single cluster's capacity.
type ClusterConfig struct {
	ID          string `yaml:"id"`
	Nodes       int    `yaml:"nodes"`
	GPUsPerNode int    `yaml:"gpus_per_node"`
}

// AppConfig defines an application's requirements and SLA.
type AppConfig struct {
	ID                   string  `yaml:"id"`
	SLATTFT              string  `yaml:"sla_ttft"`               // e.g., "200ms"
	ColdStart            string  `yaml:"cold_start"`             // e.g. "60s"
	Profile              string  `yaml:"profile"`                // path to profile json
	ThroughputPerReplica float64 `yaml:"throughput_per_replica"` // requests per second
	MaxQueueDepth        float64 `yaml:"max_queue_depth"`        // for closed loop
}
