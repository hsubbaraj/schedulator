package enforcer

import (
	"context"
	"fmt"
	"log"

	"github.com/yourorg/scheduler-simulator/pkg/k8s/client"
	"github.com/yourorg/scheduler-simulator/pkg/scheduler/types"
	"github.com/yourorg/scheduler-simulator/pkg/simulator/state"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"time"
)

// Enforcer applies scheduling decisions to the clusters
type Enforcer struct {
	clientManager *client.ClientManager
	stateManager  *state.Manager
}

// NewEnforcer creates a new Enforcer
func NewEnforcer(cm *client.ClientManager, sm *state.Manager) *Enforcer {
	return &Enforcer{
		clientManager: cm,
		stateManager:  sm,
	}
}

// Enforce applies the decisions in the PlacementDecision
func (e *Enforcer) Enforce(ctx context.Context, decision *types.PlacementDecision) (*types.ApplyPlacementResult, error) {
	// 1. Validate State Version (Optimistic Concurrency)
	currentState := e.stateManager.GetState()
	if decision.StateVersion != currentState.Version {
		return &types.ApplyPlacementResult{
			Accepted:       false,
			AppliedVersion: currentState.Version,
			Message:        fmt.Sprintf("State version mismatch: expected %s, got %s", currentState.Version, decision.StateVersion),
			Conflicts:      []string{"State changed"},
		}, nil
	}

	conflicts := []string{}
	warnings := []string{}
	applied := 0

	// 2. Iterate through decisions
	for _, d := range decision.Decisions {
		// A. Get Workload details
		workload, exists := e.stateManager.GetPendingWorkload(d.WorkloadID)
		if !exists {
			msg := fmt.Sprintf("Workload %s not found in pending queue", d.WorkloadID)
			conflicts = append(conflicts, msg)
			log.Printf("⚠️ %s", msg)
			continue
		}

		// B. Get Target Cluster Client
		k8sClient, err := e.clientManager.GetClient(d.Cluster)
		if err != nil {
			msg := fmt.Sprintf("Cluster %s not found: %v", d.Cluster, err)
			conflicts = append(conflicts, msg)
			log.Printf("⚠️ %s", msg)
			continue
		}

		// C. Create Deployment
		deployment := buildDeployment(workload, d)
		_, err = k8sClient.AppsV1().Deployments("default").Create(ctx, deployment, metav1.CreateOptions{})
		if err != nil {
			msg := fmt.Sprintf("Failed to create deployment for %s on %s: %v", d.WorkloadID, d.Cluster, err)
			conflicts = append(conflicts, msg)
			log.Printf("❌ %s", msg)
			continue
		}

		// D. Remove from Pending Queue
		e.stateManager.RemovePendingWorkload(d.WorkloadID)
		log.Printf("✅ Enforced: Placed %s on %s", d.WorkloadID, d.Cluster)
		applied++
	}

	// 3. Return Result
	currentState = e.stateManager.GetState() // Get new version
	return &types.ApplyPlacementResult{
		Accepted:       len(conflicts) == 0, // Accepted if no conflicts, or maybe partial success?
		AppliedVersion: currentState.Version,
		Conflicts:      conflicts,
		Warnings:       warnings,
		Message:        fmt.Sprintf("Applied %d decisions", applied),
	}, nil
}

// CleanupDeployments deletes all deployments managed by schedulator across all clusters.
func (e *Enforcer) CleanupDeployments(ctx context.Context) {
	log.Println("🧹 Cleaning up old deployments managed by schedulator...")
	for _, clusterName := range e.clientManager.ListClusters() {
		k8sClient, err := e.clientManager.GetClient(clusterName)
		if err != nil {
			log.Printf("⚠️  Error getting client for cluster %s during cleanup: %v", clusterName, err)
			continue
		}

		listOptions := metav1.ListOptions{
			LabelSelector: "managed-by=schedulator",
		}
		
		deployments, err := k8sClient.AppsV1().Deployments("default").List(ctx, listOptions)
		if err != nil {
			log.Printf("⚠️  Error listing deployments in cluster %s during cleanup: %v", clusterName, err)
			continue
		}

		for _, dep := range deployments.Items {
			log.Printf("   Deleting deployment %s/%s in cluster %s", dep.Namespace, dep.Name, clusterName)
			propagationPolicy := metav1.DeletePropagationForeground
			deleteOptions := metav1.DeleteOptions{
				PropagationPolicy: &propagationPolicy,
			}
			if err := k8sClient.AppsV1().Deployments(dep.Namespace).Delete(ctx, dep.Name, deleteOptions); err != nil {
				log.Printf("❌ Error deleting deployment %s/%s in cluster %s: %v", dep.Namespace, dep.Name, clusterName, err)
			}
		}
	}
	log.Println("✅ Cleanup complete.")
}

// WaitForDeploymentsDeleted polls clusters until all managed deployments are gone or timeout.
func (e *Enforcer) WaitForDeploymentsDeleted(ctx context.Context, timeout time.Duration) error {
	log.Printf("⏳ Waiting for deployments to be deleted (timeout: %s)...", timeout)
	start := time.Now()

	for time.Since(start) < timeout {
		allGone := true
		for _, clusterName := range e.clientManager.ListClusters() {
			k8sClient, err := e.clientManager.GetClient(clusterName)
			if err != nil {
				log.Printf("⚠️  Error getting client for cluster %s during wait for deletion: %v", clusterName, err)
				continue
			}

			listOptions := metav1.ListOptions{
				LabelSelector: "managed-by=schedulator",
			}
			deployments, err := k8sClient.AppsV1().Deployments("default").List(ctx, listOptions)
			if err != nil {
				log.Printf("⚠️  Error listing deployments in cluster %s during wait for deletion: %v", clusterName, err)
				continue
			}

			if len(deployments.Items) > 0 {
				allGone = false
				log.Printf("   Cluster %s still has %d deployments managed by schedulator", clusterName, len(deployments.Items))
				break // Check next cluster, then poll again
			}
		}

		if allGone {
			log.Println("✅ All managed deployments deleted.")
			return nil
		}
		time.Sleep(1 * time.Second) // Poll every second
	}

	return fmt.Errorf("timeout waiting for deployments to be deleted after %s", timeout)
}

func buildDeployment(w *state.Workload, d types.Decision) *appsv1.Deployment {
	replicas := int32(d.Replicas) // Scheduler decides replicas, usually matches workload
	
	// Create resource requirements
	res := corev1.ResourceRequirements{
		Limits:   corev1.ResourceList{},
		Requests: corev1.ResourceList{},
	}
	
	if w.GPUsPerReplica > 0 {
		qty := resource.MustParse(fmt.Sprintf("%d", w.GPUsPerReplica))
		res.Limits["nvidia.com/gpu"] = qty
		res.Requests["nvidia.com/gpu"] = qty
	}
	
	// Default CPU/Mem if not specified
	res.Requests[corev1.ResourceCPU] = resource.MustParse("100m")
	res.Requests[corev1.ResourceMemory] = resource.MustParse("128Mi")

	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      w.WorkloadID, // Use WorkloadID as deployment name
			Namespace: "default",
			Labels: map[string]string{
				"app":        w.WorkloadID,
				"managed-by": "schedulator",
				"model-id":   w.ModelID,
			},
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					"app": w.WorkloadID,
				},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"app": w.WorkloadID,
					},
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:  "main",
							Image: "registry.k8s.io/pause:3.9",
							Resources: res,
						},
					},
					// Optional: Add PriorityClass if needed
				},
			},
		},
	}
}
