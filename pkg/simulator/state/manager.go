package state

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/yourorg/scheduler-simulator/pkg/k8s/client"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Manager handles the aggregation of state from multiple clusters
type Manager struct {
	mu            sync.RWMutex
	clientManager *client.ClientManager
	state         ClusterState
	stateVersion  int
}

// NewManager creates a new State Manager
func NewManager(cm *client.ClientManager) *Manager {
	return &Manager{
		clientManager: cm,
		stateVersion:  1,
		state: ClusterState{
			Version:      "1",
			Timestamp:    time.Now(),
			Clusters:     []Cluster{},
			PendingQueue: []Workload{},
		},
	}
}

// GetState returns the current aggregated state
func (m *Manager) GetState() ClusterState {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.state
}

// AddPendingWorkload adds a workload to the pending queue
func (m *Manager) AddPendingWorkload(w Workload) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.state.PendingQueue = append(m.state.PendingQueue, w)
	m.stateVersion++
	m.state.Version = strconv.Itoa(m.stateVersion)
	m.state.Timestamp = time.Now()
}

// GetPendingWorkload returns a pending workload by ID
func (m *Manager) GetPendingWorkload(id string) (*Workload, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	for _, w := range m.state.PendingQueue {
		if w.WorkloadID == id {
			return &w, true
		}
	}
	return nil, false
}

// RemovePendingWorkload removes a workload from the pending queue
func (m *Manager) RemovePendingWorkload(id string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	newQueue := []Workload{}
	for _, w := range m.state.PendingQueue {
		if w.WorkloadID != id {
			newQueue = append(newQueue, w)
		}
	}

	// Only update version if something changed
	if len(newQueue) != len(m.state.PendingQueue) {
		m.state.PendingQueue = newQueue
		m.stateVersion++
		m.state.Version = strconv.Itoa(m.stateVersion)
		m.state.Timestamp = time.Now()
	}
}

// UpdateLoop starts a ticker to update the state periodically
func (m *Manager) UpdateLoop(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	// Initial update
	m.RefreshState(ctx)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.RefreshState(ctx)
		}
	}
}

// RefreshState fetches data from all clusters and updates the state
func (m *Manager) RefreshState(ctx context.Context) {
	clusterNames := m.clientManager.ListClusters()
	var clusters []Cluster

	// Sort cluster names for consistent ordering
	sort.Strings(clusterNames)

	for _, clusterName := range clusterNames {
		k8sClient, err := m.clientManager.GetClient(clusterName)
		if err != nil {
			fmt.Printf("Error getting client for %s: %v\n", clusterName, err)
			continue
		}

		// List Nodes
		nodeList, err := k8sClient.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
		if err != nil {
			fmt.Printf("Error listing nodes for %s: %v\n", clusterName, err)
			continue
		}

		// List Pods
	
podList, err := k8sClient.CoreV1().Pods("").List(ctx, metav1.ListOptions{})
		if err != nil {
			fmt.Printf("Error listing pods for %s: %v\n", clusterName, err)
			continue
		}

		// Convert to internal types and map pods to nodes
		nodeMap := make(map[string]*Node)
		var nodes []Node
		
		for _, n := range nodeList.Items {
			node := convertNode(n)
			nodes = append(nodes, node)
			// We need pointers to update Allocated later
			// But we appended by value. Let's fix this in the next loop or use index
		}

		// Re-iterate to build map (inefficient but clear for now)
		for i := range nodes {
			nodeMap[nodes[i].Name] = &nodes[i]
		}

		var pods []Pod
		for _, p := range podList.Items {
			pod := convertPod(p)
			pods = append(pods, pod)
			
			// Update node allocation if pod is assigned
			if pod.NodeName != "" && (pod.Phase == "Running" || pod.Phase == "Pending") {
				if node, exists := nodeMap[pod.NodeName]; exists {
					node.Allocated.CPU += pod.Requests.CPU
					node.Allocated.GPU += pod.Requests.GPU
					// Memory addition omitted for simplicity in this pass
				}
			}
		}

		// Calculate summary
		summary := calculateSummary(nodes, pods)

		clusters = append(clusters, Cluster{
			ID:      clusterName,
			Region:  "us-central1", // Placeholder
			Nodes:   nodes,
			Pods:    pods,
			Summary: summary,
		})
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	// Check if anything changed (naive check for now, always update version)
	m.stateVersion++
	m.state.Version = strconv.Itoa(m.stateVersion)
	m.state.Timestamp = time.Now()
	m.state.Clusters = clusters
	// PendingQueue is preserved
}

func convertNode(n corev1.Node) Node {
	gpuCap := n.Status.Capacity["nvidia.com/gpu"]
gpuAlloc := n.Status.Allocatable["nvidia.com/gpu"]
	
	return Node{
		Name:     n.Name,
		Labels:   n.Labels,
		Capacity: ResourceList{
			CPU:    n.Status.Capacity.Cpu().Value(),
			Memory: n.Status.Capacity.Memory().String(),
			GPU:    int(gpuCap.Value()),
		},
		Allocatable: ResourceList{
			CPU:    n.Status.Allocatable.Cpu().Value(),
			Memory: n.Status.Allocatable.Memory().String(),
			GPU:    int(gpuAlloc.Value()),
		},
		Allocated: ResourceList{}, // Filled later
		Taints:    convertTaints(n.Spec.Taints),
	}
}

func convertPod(p corev1.Pod) Pod {
	req := ResourceList{}
	for _, c := range p.Spec.Containers {
		req.CPU += c.Resources.Requests.Cpu().Value()
		if gpu := c.Resources.Requests["nvidia.com/gpu"]; !gpu.IsZero() {
			req.GPU += int(gpu.Value())
		}
	}

	return Pod{
		Name:      p.Name,
		Namespace: p.Namespace,
		Phase:     string(p.Status.Phase),
		NodeName:  p.Spec.NodeName,
		Requests:  req,
	}
}

func convertTaints(k8sTaints []corev1.Taint) []string {
	taints := make([]string, 0, len(k8sTaints))
	for _, t := range k8sTaints {
		taints = append(taints, fmt.Sprintf("%s=%s:%s", t.Key, t.Value, t.Effect))
	}
	return taints
}

func calculateSummary(nodes []Node, pods []Pod) ClusterSummary {
	totalGPUs := 0
	allocatedGPUs := 0
	
	// Track free GPUs per node for fragmentation
	freeGPUsPerNode := make([]int, 0, len(nodes))

	for _, n := range nodes {
		totalGPUs += n.Capacity.GPU
		// Allocated is now populated in the node struct
		allocatedGPUs += n.Allocated.GPU 
		
		free := n.Allocatable.GPU - n.Allocated.GPU
		if free < 0 {
			free = 0
		}
		freeGPUsPerNode = append(freeGPUsPerNode, free)
	}

	util := 0.0
	if totalGPUs > 0 {
		util = float64(allocatedGPUs) / float64(totalGPUs)
	}

	// Calculate fragmentation
	frag := make(map[string]FragmentationScore)
	sizeClasses := []int{1, 2, 4, 8}

	for _, size := range sizeClasses {
		packable := 0
		for _, free := range freeGPUsPerNode {
			packable += free / size
		}
		
		wasted := 0
		score := 0.0
		
		// If total free is > 0, we can calculate fragmentation
		// Note: The metric spec says "wasted GPUs = free - (packable * N)"
		// This is effectively "unusable free capacity"
		
		// Using totalFree might be slightly off if allocatable != capacity, but good enough for now
		currentTotalFree := 0
		for _, f := range freeGPUsPerNode {
			currentTotalFree += f
		}

		wasted = currentTotalFree - (packable * size)
		
		if currentTotalFree > 0 {
			score = float64(wasted) / float64(currentTotalFree)
		}

		frag[fmt.Sprintf("%dGPU", size)] = FragmentationScore{
			PackableCount: packable,
			WastedGPUs:    wasted,
			Score:         score,
		}
	}

	return ClusterSummary{
		TotalGPUs:     totalGPUs,
		AllocatedGPUs: allocatedGPUs,
		AvailableGPUs: totalGPUs - allocatedGPUs,
		Utilization:   util,
		Fragmentation: frag,
	}
}