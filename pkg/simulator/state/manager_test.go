package state

import (
	"testing"
)

func TestCalculateSummary(t *testing.T) {
	tests := []struct {
		name     string
		nodes    []Node
		pods     []Pod
		expected ClusterSummary
	}{
		{
			name: "Empty Cluster",
			nodes: []Node{},
			pods:  []Pod{},
			expected: ClusterSummary{
				TotalGPUs:     0,
				AllocatedGPUs: 0,
				AvailableGPUs: 0,
				Utilization:   0.0,
			},
		},
		{
			name: "Single Node, No Pods",
			nodes: []Node{
				{
					Capacity:    ResourceList{GPU: 8},
					Allocatable: ResourceList{GPU: 8},
					Allocated:   ResourceList{GPU: 0},
				},
			},
			pods: []Pod{},
			expected: ClusterSummary{
				TotalGPUs:     8,
				AllocatedGPUs: 0,
				AvailableGPUs: 8,
				Utilization:   0.0,
				Fragmentation: map[string]FragmentationScore{
					"1GPU": {PackableCount: 8, WastedGPUs: 0, Score: 0.0},
					"2GPU": {PackableCount: 4, WastedGPUs: 0, Score: 0.0},
					"4GPU": {PackableCount: 2, WastedGPUs: 0, Score: 0.0},
					"8GPU": {PackableCount: 1, WastedGPUs: 0, Score: 0.0},
				},
			},
		},
		{
			name: "Single Node, Half Full",
			nodes: []Node{
				{
					Capacity:    ResourceList{GPU: 8},
					Allocatable: ResourceList{GPU: 8},
					Allocated:   ResourceList{GPU: 4},
				},
			},
			pods: []Pod{
				{Requests: ResourceList{GPU: 4}, Phase: "Running"},
			},
			expected: ClusterSummary{
				TotalGPUs:     8,
				AllocatedGPUs: 4,
				AvailableGPUs: 4,
				Utilization:   0.5,
				Fragmentation: map[string]FragmentationScore{
					"1GPU": {PackableCount: 4, WastedGPUs: 0, Score: 0.0},
					"2GPU": {PackableCount: 2, WastedGPUs: 0, Score: 0.0},
					"4GPU": {PackableCount: 1, WastedGPUs: 0, Score: 0.0},
					"8GPU": {PackableCount: 0, WastedGPUs: 4, Score: 1.0}, // 4 free, need 8. Wasted = 4. Score = 4/4 = 1.0
				},
			},
		},
		{
			name: "Fragmentation Test: 2 Nodes with 3 free each",
			nodes: []Node{
				{
					Capacity:    ResourceList{GPU: 8},
					Allocatable: ResourceList{GPU: 8},
					Allocated:   ResourceList{GPU: 5}, // 3 free
				},
				{
					Capacity:    ResourceList{GPU: 8},
					Allocatable: ResourceList{GPU: 8},
					Allocated:   ResourceList{GPU: 5}, // 3 free
				},
			},
			pods: []Pod{
				{Requests: ResourceList{GPU: 5}, Phase: "Running"},
				{Requests: ResourceList{GPU: 5}, Phase: "Running"},
			},
			expected: ClusterSummary{
				TotalGPUs:     16,
				AllocatedGPUs: 10,
				AvailableGPUs: 6,
				Utilization:   0.625,
				Fragmentation: map[string]FragmentationScore{
					"1GPU": {PackableCount: 6, WastedGPUs: 0, Score: 0.0}, // 3//1 + 3//1 = 6. Wasted = 6 - 6 = 0
					"2GPU": {PackableCount: 2, WastedGPUs: 2, Score: 0.3333333333333333}, // 3//2 + 3//2 = 1 + 1 = 2. Wasted = 6 - (2*2) = 2. Score = 2/6 = 0.33
					"4GPU": {PackableCount: 0, WastedGPUs: 6, Score: 1.0}, // 3//4 + 3//4 = 0. Wasted = 6 - 0 = 6. Score = 1.0
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := calculateSummary(tt.nodes, tt.pods)

			if got.TotalGPUs != tt.expected.TotalGPUs {
				t.Errorf("TotalGPUs = %d, want %d", got.TotalGPUs, tt.expected.TotalGPUs)
			}
			if got.AllocatedGPUs != tt.expected.AllocatedGPUs {
				t.Errorf("AllocatedGPUs = %d, want %d", got.AllocatedGPUs, tt.expected.AllocatedGPUs)
			}
			if got.Utilization != tt.expected.Utilization {
				t.Errorf("Utilization = %f, want %f", got.Utilization, tt.expected.Utilization)
			}

			if tt.expected.Fragmentation != nil {
				for size, expectedFrag := range tt.expected.Fragmentation {
					gotFrag, ok := got.Fragmentation[size]
					if !ok {
						t.Errorf("Missing fragmentation for size %s", size)
						continue
					}
					if gotFrag.PackableCount != expectedFrag.PackableCount {
						t.Errorf("Frag %s PackableCount = %d, want %d", size, gotFrag.PackableCount, expectedFrag.PackableCount)
					}
					if gotFrag.WastedGPUs != expectedFrag.WastedGPUs {
						t.Errorf("Frag %s WastedGPUs = %d, want %d", size, gotFrag.WastedGPUs, expectedFrag.WastedGPUs)
					}
					if gotFrag.Score != expectedFrag.Score {
						t.Errorf("Frag %s Score = %f, want %f", size, gotFrag.Score, expectedFrag.Score)
					}
				}
			}
		})
	}
}
