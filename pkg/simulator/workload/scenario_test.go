package workload

import (
	"os"
	"testing"
	"time"
)

func TestLoadScenario(t *testing.T) {
	// Create a dummy scenario file
	scenarioContent := `
name: TestScenario
duration: 1m
workloads:
  - delay: 5s
    workloadId: workload-1
    workload:
      modelId: model-a
      replicas: 1
      gpusPerReplica: 4
  - delay: 10s
    workloadId: workload-2
    workload:
      modelId: model-b
      replicas: 2
      gpusPerReplica: 8
  - delay: 15s
    # workloadId will be auto-generated
    workload:
      modelId: model-c
      replicas: 1
      gpusPerReplica: 1
`
	filePath := "test_scenario.yaml"
	if err := os.WriteFile(filePath, []byte(scenarioContent), 0644); err != nil {
		t.Fatalf("Failed to write test scenario file: %v", err)
	}
	defer os.Remove(filePath)

	s, err := LoadScenario(filePath)
	if err != nil {
		t.Fatalf("Failed to load scenario: %v", err)
	}

	if s.Name != "TestScenario" {
		t.Errorf("Expected scenario name TestScenario, got %s", s.Name)
	}
	if s.Duration != 1*time.Minute {
		t.Errorf("Expected scenario duration 1m, got %s", s.Duration)
	}

	if len(s.Workloads) != 3 {
		t.Errorf("Expected 3 workloads, got %d", len(s.Workloads))
	}

	t.Logf("Parsed Workload 0: %+v", s.Workloads[0])

	// Check first workload (explicitly provided ID)
	if s.Workloads[0].WorkloadID != "workload-1" {
		t.Errorf("Expected ScenarioWorkload.WorkloadID workload-1, got %s", s.Workloads[0].WorkloadID)
	}
	if s.Workloads[0].Delay != "5s" {
		t.Errorf("Expected workload-1 delay 5s, got %s", s.Workloads[0].Delay)
	}
	if s.Workloads[0].Workload.ModelID != "model-a" {
		t.Errorf("Expected workload-1 ModelID model-a, got %s", s.Workloads[0].Workload.ModelID)
	}
	if s.Workloads[0].Workload.GPUsPerReplica != 4 {
		t.Errorf("Expected workload-1 GPUsPerReplica 4, got %d", s.Workloads[0].Workload.GPUsPerReplica)
	}

	// Check second workload (explicitly provided ID)
	if s.Workloads[1].WorkloadID != "workload-2" {
		t.Errorf("Expected ScenarioWorkload.WorkloadID workload-2, got %s", s.Workloads[1].WorkloadID)
	}
	if s.Workloads[1].Delay != "10s" {
		t.Errorf("Expected workload-2 delay 10s, got %s", s.Workloads[1].Delay)
	}
	if s.Workloads[1].Workload.Replicas != 2 {
		t.Errorf("Expected workload-2 Replicas 2, got %d", s.Workloads[1].Workload.Replicas)
	}
	if s.Workloads[1].Workload.GPUsPerReplica != 8 {
		t.Errorf("Expected workload-2 GPUsPerReplica 8, got %d", s.Workloads[1].Workload.GPUsPerReplica)
	}

	// Check third workload (auto-generated ID)
	if s.Workloads[2].WorkloadID == "" {
		t.Errorf("Expected auto-generated WorkloadID for third workload, got empty string")
	}
	if s.Workloads[2].Workload.ModelID != "model-c" {
		t.Errorf("Expected workload-3 ModelID model-c, got %s", s.Workloads[2].Workload.ModelID)
	}
}