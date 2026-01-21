package enforcer

import (
	"context"
	"testing"

	"github.com/yourorg/scheduler-simulator/pkg/k8s/client"
	"github.com/yourorg/scheduler-simulator/pkg/scheduler/types"
	"github.com/yourorg/scheduler-simulator/pkg/simulator/state"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestEnforcer_Enforce(t *testing.T) {
	// 1. Setup Fake Client
	fakeClient := fake.NewSimpleClientset()
	clientManager := client.NewClientManager()
	clientManager.SetClient("test-cluster-1", fakeClient)

	// 2. Setup State Manager
	stateManager := state.NewManager(clientManager)
	
	// Add pending workload
	workloadID := "job-1"
	stateManager.AddPendingWorkload(state.Workload{
		WorkloadID:     workloadID,
		ModelID:        "gpt-2",
		Replicas:       1,
		GPUsPerReplica: 1,
	})
	
	// Get version
	initialState := stateManager.GetState()

	// 3. Initialize Enforcer
	enforcer := NewEnforcer(clientManager, stateManager)

	// 4. Create Decision
	decision := &types.PlacementDecision{
		StateVersion:  initialState.Version,
		PlacementMode: "ClusterOnly",
		Decisions: []types.Decision{
			{
				WorkloadID: workloadID,
				Cluster:    "test-cluster-1",
				Replicas:   1,
			},
		},
	}

	// 5. Test Enforce
	result, err := enforcer.Enforce(context.TODO(), decision)
	if err != nil {
		t.Fatalf("Enforce failed: %v", err)
	}

	if !result.Accepted {
		t.Errorf("Decision was not accepted. Messages: %v", result.Conflicts)
	}

	// 6. Verify Deployment Created
	deployments, err := fakeClient.AppsV1().Deployments("default").List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		t.Fatalf("Failed to list deployments: %v", err)
	}

	if len(deployments.Items) != 1 {
		t.Errorf("Expected 1 deployment, got %d", len(deployments.Items))
	} else {
		d := deployments.Items[0]
		if d.Name != workloadID {
			t.Errorf("Expected deployment name %s, got %s", workloadID, d.Name)
		}
	}

	// 7. Verify Pending Queue is Empty
	finalState := stateManager.GetState()
	if len(finalState.PendingQueue) != 0 {
		t.Errorf("Expected pending queue to be empty, got %d items", len(finalState.PendingQueue))
	}
}
