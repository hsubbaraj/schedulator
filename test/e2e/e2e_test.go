package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/yourorg/scheduler-simulator/pkg/k8s/client"
	"github.com/yourorg/scheduler-simulator/pkg/simulator/state"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// This test assumes:
// 1. Simulator is running at localhost:8080
// 2. Scheduler is running
// 3. Kind clusters are up (cluster-1, 2, 3)
// 4. Client kubeconfig is configured
func TestE2E_WorkloadPlacement(t *testing.T) {
	simulatorURL := "http://localhost:8080"
	workloadID := fmt.Sprintf("e2e-job-%d", time.Now().Unix())

	// 1. Submit Workload
	t.Logf("Submitting workload %s...", workloadID)
	w := state.Workload{
		WorkloadID:     workloadID,
		ModelID:        "e2e-test-model",
		Replicas:       1,
		GPUsPerReplica: 1,
	}
	if err := submitWorkload(simulatorURL, w); err != nil {
		t.Fatalf("Failed to submit workload: %v", err)
	}

	// 2. Wait for Enforcer (Poll for deployment)
	t.Log("Waiting for deployment to appear in one of the clusters...")
	
	clientManager := client.NewClientManager()
	clusters := []string{"kind-cluster-1", "kind-cluster-2", "kind-cluster-3"}
	
	found := false
	var foundCluster string

	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		for _, ctx := range clusters {
			if err := clientManager.AddCluster(ctx); err != nil {
				continue
			}
			
			k8sClient, _ := clientManager.GetClient(ctx)
			_, err := k8sClient.AppsV1().Deployments("default").Get(context.TODO(), workloadID, metav1.GetOptions{})
			if err == nil {
				found = true
				foundCluster = ctx
				break
			}
		}
		if found {
			break
		}
		time.Sleep(1 * time.Second)
	}

	if !found {
		t.Fatalf("Timeout waiting for deployment %s to be created", workloadID)
	}

	t.Logf("✅ Deployment found in %s", foundCluster)

	// 3. Verify Pending Queue is Empty (via API)
	// (Optional check)
}

func submitWorkload(url string, w state.Workload) error {
	body, _ := json.Marshal(w)
	resp, err := http.Post(url+"/v1/workloads", "application/json", bytes.NewBuffer(body))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		return fmt.Errorf("bad status: %d", resp.StatusCode)
	}
	return nil
}
