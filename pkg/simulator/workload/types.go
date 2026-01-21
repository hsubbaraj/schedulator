package workload

import (
	"time"

	"github.com/yourorg/scheduler-simulator/pkg/simulator/state"
)

// Scenario represents a complete test scenario for the simulator.
type Scenario struct {
	Name       string             `yaml:"name"`
	Duration   time.Duration      `yaml:"duration"` // Total duration of the scenario
	Workloads  []ScenarioWorkload `yaml:"workloads"`
}

// ScenarioWorkload defines a workload to be injected at a specific time.
type ScenarioWorkload struct {
	// Delay before this workload is injected (from scenario start).
	// e.g., "10s", "1m", "0" (immediate)
	Delay string `yaml:"delay"`
	WorkloadID string `yaml:"workloadId,omitempty"`
	Workload   state.Workload `yaml:"workload"` // Explicitly nest the workload details
}
