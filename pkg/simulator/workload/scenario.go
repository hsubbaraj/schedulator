package workload

import (
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

// LoadScenario loads a scenario from a YAML file.
func LoadScenario(filePath string) (*Scenario, error) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return nil, fmt.Errorf("failed to read scenario file %s: %w", filePath, err)
	}

	var s Scenario
	if err := yaml.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("failed to unmarshal scenario YAML from %s: %w", filePath, err)
	}

	// Process workloads: parse delays, ensure unique WorkloadIDs
	for i := range s.Workloads {
		// Generate unique WorkloadID if empty in ScenarioWorkload
		if s.Workloads[i].WorkloadID == "" {
			s.Workloads[i].WorkloadID = fmt.Sprintf("scenario-workload-%d-%d", time.Now().UnixNano(), i)
		}
		// Assign the resolved WorkloadID to the embedded state.Workload
		s.Workloads[i].Workload.WorkloadID = s.Workloads[i].WorkloadID
	}

	return &s, nil
}
