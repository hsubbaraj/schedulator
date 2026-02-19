package k8saggregator

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/trace/noop"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/hsubbaraj/schedulator/pkg/model"
)

func newTestTracer() noop.TracerProvider {
	return noop.NewTracerProvider()
}

func nodeWithGPUs(name string, gpus int, ready bool) *corev1.Node {
	conditions := []corev1.NodeCondition{
		{
			Type:   corev1.NodeReady,
			Status: corev1.ConditionFalse,
		},
	}
	if ready {
		conditions[0].Status = corev1.ConditionTrue
	}

	n := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec:       corev1.NodeSpec{},
		Status: corev1.NodeStatus{
			Conditions: conditions,
			Capacity:   corev1.ResourceList{},
		},
	}
	if gpus > 0 {
		n.Status.Capacity[corev1.ResourceName(gpuResource)] = resource.MustParse(
			resourceQuantityStr(gpus),
		)
	}
	return n
}

func resourceQuantityStr(n int) string {
	return fmt.Sprintf("%d", n)
}

func schedulatorPod(name, nodeName, appID string, gpus int, phase corev1.PodPhase) *corev1.Pod {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: "default",
			Labels:    map[string]string{labelAppID: appID},
		},
		Spec: corev1.PodSpec{
			NodeName: nodeName,
			Containers: []corev1.Container{
				{
					Name:  "main",
					Image: "test",
					Resources: corev1.ResourceRequirements{
						Requests: corev1.ResourceList{},
					},
				},
			},
		},
		Status: corev1.PodStatus{Phase: phase},
	}
	if gpus > 0 {
		pod.Spec.Containers[0].Resources.Requests[corev1.ResourceName(gpuResource)] =
			resource.MustParse(resourceQuantityStr(gpus))
	}
	return pod
}

func TestFullSync_EmptyCluster(t *testing.T) {
	tracer := newTestTracer().Tracer("test")
	clientset := fake.NewSimpleClientset()

	agg := New([]ClusterConfig{
		{ClusterID: "cluster-1", Clientset: clientset, Namespace: "default"},
	}, tracer)

	clusters, replicas, err := agg.FullSync(context.Background())
	require.NoError(t, err)
	require.Len(t, clusters, 1)
	assert.Equal(t, "cluster-1", clusters[0].ClusterID)
	assert.Empty(t, clusters[0].Nodes)
	assert.Empty(t, replicas)
}

func TestFullSync_NodesWithGPUs(t *testing.T) {
	tracer := newTestTracer().Tracer("test")
	clientset := fake.NewSimpleClientset(
		nodeWithGPUs("node-1", 4, true),
		nodeWithGPUs("node-2", 8, true),
		nodeWithGPUs("node-3", 4, false), // not ready
	)

	agg := New([]ClusterConfig{
		{ClusterID: "cluster-1", Clientset: clientset, Namespace: "default"},
	}, tracer)

	clusters, replicas, err := agg.FullSync(context.Background())
	require.NoError(t, err)
	require.Len(t, clusters, 1)
	require.Len(t, clusters[0].Nodes, 3)
	assert.Empty(t, replicas)

	assert.Equal(t, 4, clusters[0].Nodes["node-1"].TotalGPUs)
	assert.Equal(t, 4, clusters[0].Nodes["node-1"].FreeGPUs)
	assert.Equal(t, model.NodeStatusReady, clusters[0].Nodes["node-1"].Status)

	assert.Equal(t, 8, clusters[0].Nodes["node-2"].TotalGPUs)
	assert.Equal(t, model.NodeStatusReady, clusters[0].Nodes["node-2"].Status)

	assert.Equal(t, model.NodeStatusDown, clusters[0].Nodes["node-3"].Status)
}

func TestFullSync_WithPods(t *testing.T) {
	tracer := newTestTracer().Tracer("test")
	clientset := fake.NewSimpleClientset(
		nodeWithGPUs("node-1", 4, true),
		schedulatorPod("app-x-replica-1", "node-1", "app-x", 2, corev1.PodRunning),
	)

	agg := New([]ClusterConfig{
		{ClusterID: "c1", Clientset: clientset, Namespace: "default"},
	}, tracer)

	clusters, replicas, err := agg.FullSync(context.Background())
	require.NoError(t, err)
	require.Len(t, replicas, 1)

	node := clusters[0].Nodes["node-1"]
	assert.Equal(t, 4, node.TotalGPUs)
	assert.Equal(t, 0, node.AllocatedGPUs) // Reset during sync, populated by UpsertReplica in main
	assert.Equal(t, 4, node.FreeGPUs)
	assert.Empty(t, node.Pods)
}

func TestFullSync_MultipleClusters(t *testing.T) {
	tracer := newTestTracer().Tracer("test")
	cs1 := fake.NewSimpleClientset(nodeWithGPUs("n1", 4, true))
	cs2 := fake.NewSimpleClientset(nodeWithGPUs("n2", 8, true))

	agg := New([]ClusterConfig{
		{ClusterID: "c1", Clientset: cs1, Namespace: "default"},
		{ClusterID: "c2", Clientset: cs2, Namespace: "default"},
	}, tracer)

	clusters, replicas, err := agg.FullSync(context.Background())
	require.NoError(t, err)
	assert.Len(t, clusters, 2)
	assert.Empty(t, replicas)
}

func TestGetVLLMMetrics_ReturnsZeroMetrics(t *testing.T) {
	tracer := newTestTracer().Tracer("test")
	agg := New(nil, tracer)

	m, err := agg.GetVLLMMetrics(context.Background(), "app-x")
	require.NoError(t, err)
	assert.Equal(t, "app-x", m.AppID)
	assert.False(t, m.MeasuredAt.IsZero())
}

func TestExtractNodeGPUs(t *testing.T) {
	tests := []struct {
		name     string
		node     *corev1.Node
		expected int
	}{
		{
			name:     "no GPU resource",
			node:     nodeWithGPUs("n", 0, true),
			expected: 0,
		},
		{
			name:     "4 GPUs",
			node:     nodeWithGPUs("n", 4, true),
			expected: 4,
		},
		{
			name:     "8 GPUs",
			node:     nodeWithGPUs("n", 8, true),
			expected: 8,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, extractNodeGPUs(tt.node))
		})
	}
}

func TestNodeStatusFromConditions(t *testing.T) {
	tests := []struct {
		name     string
		node     *corev1.Node
		expected model.NodeStatus
	}{
		{
			name:     "ready node",
			node:     nodeWithGPUs("n", 0, true),
			expected: model.NodeStatusReady,
		},
		{
			name:     "not ready node",
			node:     nodeWithGPUs("n", 0, false),
			expected: model.NodeStatusDown,
		},
		{
			name: "cordoned node",
			node: func() *corev1.Node {
				n := nodeWithGPUs("n", 0, true)
				n.Spec.Unschedulable = true
				return n
			}(),
			expected: model.NodeStatusCordoned,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, nodeStatusFromConditions(tt.node))
		})
	}
}

func TestIsNodeReady(t *testing.T) {
	assert.True(t, isNodeReady(nodeWithGPUs("n", 0, true)))
	assert.False(t, isNodeReady(nodeWithGPUs("n", 0, false)))
}

func TestIsSchedulatorPod(t *testing.T) {
	labeled := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Labels: map[string]string{labelAppID: "test"},
		},
	}
	unlabeled := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{},
	}

	assert.True(t, isSchedulatorPod(labeled))
	assert.False(t, isSchedulatorPod(unlabeled))
}

// Ensure WatchEvents does not block.
func TestWatchEvents_ReturnsChannel(t *testing.T) {
	tracer := newTestTracer().Tracer("test")
	cs := fake.NewSimpleClientset()

	agg := New([]ClusterConfig{
		{ClusterID: "c1", Clientset: cs, Namespace: "default"},
	}, tracer)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	ch, err := agg.WatchEvents(ctx)
	require.NoError(t, err)
	require.NotNil(t, ch)
}
