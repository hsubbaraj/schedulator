package k8sclient

import (
	"context"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/hsubbaraj/schedulator/internal/observability"
	"github.com/hsubbaraj/schedulator/pkg/model"
)

const testNamespace = "default"
const testClusterID = "test-cluster"

func newTestClient(t *testing.T) (*Client, *fake.Clientset) {
	t.Helper()
	cs := fake.NewSimpleClientset()
	reg := prometheus.NewRegistry()
	tracer := observability.NewNoopTracer()
	c := New(cs, testNamespace, testClusterID, tracer, reg)
	return c, cs
}

func TestCreateReplica_CreatesDeployment(t *testing.T) {
	c, cs := newTestClient(t)
	ctx := context.Background()

	constraints := model.SchedulingConstraints{
		RequiredGPUs: 4,
	}
	replicaID, err := c.CreateReplica(ctx, "myapp", constraints)
	require.NoError(t, err)
	assert.Contains(t, replicaID, "myapp-replica-")

	deploy, err := cs.AppsV1().Deployments(testNamespace).Get(ctx, replicaID, metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, replicaID, deploy.Name)
	assert.Equal(t, "myapp", deploy.Labels[labelAppID])
	assert.Equal(t, replicaID, deploy.Labels[labelReplicaID])

	// Verify GPU resource requests.
	container := deploy.Spec.Template.Spec.Containers[0]
	gpuReq := container.Resources.Requests["nvidia.com/gpu"]
	assert.Equal(t, int64(4), gpuReq.Value())
	gpuLim := container.Resources.Limits["nvidia.com/gpu"]
	assert.Equal(t, int64(4), gpuLim.Value())
}

func TestCreateReplica_SetsNodeAffinity(t *testing.T) {
	c, cs := newTestClient(t)
	ctx := context.Background()

	constraints := model.SchedulingConstraints{
		RequiredGPUs:   2,
		PreferredNodes: []string{"node-1", "node-2"},
	}
	replicaID, err := c.CreateReplica(ctx, "affinityapp", constraints)
	require.NoError(t, err)

	deploy, err := cs.AppsV1().Deployments(testNamespace).Get(ctx, replicaID, metav1.GetOptions{})
	require.NoError(t, err)

	affinity := deploy.Spec.Template.Spec.Affinity
	require.NotNil(t, affinity)
	require.NotNil(t, affinity.NodeAffinity)
	preferred := affinity.NodeAffinity.PreferredDuringSchedulingIgnoredDuringExecution
	require.Len(t, preferred, 1)
	assert.Equal(t, int32(100), preferred[0].Weight)
	exprs := preferred[0].Preference.MatchExpressions
	require.Len(t, exprs, 1)
	assert.Equal(t, "kubernetes.io/hostname", exprs[0].Key)
	assert.Equal(t, []string{"node-1", "node-2"}, exprs[0].Values)
}

func TestCreateReplica_RequiredNodeLabels(t *testing.T) {
	c, cs := newTestClient(t)
	ctx := context.Background()

	constraints := model.SchedulingConstraints{
		RequiredGPUs:       2,
		RequiredNodeLabels: map[string]string{"gpu-type": "a100", "zone": "us-east-1a"},
	}
	replicaID, err := c.CreateReplica(ctx, "labelapp", constraints)
	require.NoError(t, err)

	deploy, err := cs.AppsV1().Deployments(testNamespace).Get(ctx, replicaID, metav1.GetOptions{})
	require.NoError(t, err)

	nodeSelector := deploy.Spec.Template.Spec.NodeSelector
	assert.Equal(t, "a100", nodeSelector["gpu-type"])
	assert.Equal(t, "us-east-1a", nodeSelector["zone"])
}

func TestCreateReplica_CacheAffinityAnnotation(t *testing.T) {
	c, cs := newTestClient(t)
	ctx := context.Background()

	constraints := model.SchedulingConstraints{
		RequiredGPUs:       1,
		CacheAffinityModel: "llama-70b",
	}
	replicaID, err := c.CreateReplica(ctx, "cacheapp", constraints)
	require.NoError(t, err)

	deploy, err := cs.AppsV1().Deployments(testNamespace).Get(ctx, replicaID, metav1.GetOptions{})
	require.NoError(t, err)

	assert.Equal(t, "llama-70b", deploy.Annotations[annotationCacheAffinityModel])
}

func TestCreateReplica_NoAffinityWhenEmpty(t *testing.T) {
	c, cs := newTestClient(t)
	ctx := context.Background()

	constraints := model.SchedulingConstraints{RequiredGPUs: 1}
	replicaID, err := c.CreateReplica(ctx, "simpleapp", constraints)
	require.NoError(t, err)

	deploy, err := cs.AppsV1().Deployments(testNamespace).Get(ctx, replicaID, metav1.GetOptions{})
	require.NoError(t, err)

	assert.Nil(t, deploy.Spec.Template.Spec.Affinity)
	assert.Nil(t, deploy.Spec.Template.Spec.NodeSelector)
	assert.Empty(t, deploy.Annotations)
}

func TestDeleteReplica_DeletesDeployment(t *testing.T) {
	c, cs := newTestClient(t)
	ctx := context.Background()

	// Create a deployment first.
	replicaID, err := c.CreateReplica(ctx, "delapp", model.SchedulingConstraints{RequiredGPUs: 1})
	require.NoError(t, err)

	err = c.DeleteReplica(ctx, replicaID)
	require.NoError(t, err)

	_, err = cs.AppsV1().Deployments(testNamespace).Get(ctx, replicaID, metav1.GetOptions{})
	assert.True(t, k8serrors.IsNotFound(err))
}

func TestDeleteReplica_NotFound(t *testing.T) {
	c, _ := newTestClient(t)
	ctx := context.Background()

	err := c.DeleteReplica(ctx, "nonexistent-replica")
	assert.NoError(t, err)
}

func TestGetReplicaStatus_Running(t *testing.T) {
	c, cs := newTestClient(t)
	ctx := context.Background()

	replicaID, err := c.CreateReplica(ctx, "runapp", model.SchedulingConstraints{RequiredGPUs: 1})
	require.NoError(t, err)

	// Simulate the deployment becoming available.
	deploy, err := cs.AppsV1().Deployments(testNamespace).Get(ctx, replicaID, metav1.GetOptions{})
	require.NoError(t, err)
	deploy.Status.AvailableReplicas = 1
	_, err = cs.AppsV1().Deployments(testNamespace).UpdateStatus(ctx, deploy, metav1.UpdateOptions{})
	require.NoError(t, err)

	status, err := c.GetReplicaStatus(ctx, replicaID)
	require.NoError(t, err)
	assert.Equal(t, model.ReplicaStatusRunning, status)
}

func TestGetReplicaStatus_Pending(t *testing.T) {
	c, _ := newTestClient(t)
	ctx := context.Background()

	replicaID, err := c.CreateReplica(ctx, "pendapp", model.SchedulingConstraints{RequiredGPUs: 1})
	require.NoError(t, err)

	// Deployment exists but has 0 available replicas (default).
	status, err := c.GetReplicaStatus(ctx, replicaID)
	require.NoError(t, err)
	assert.Equal(t, model.ReplicaStatusPending, status)
}

func TestGetReplicaStatus_NotFound(t *testing.T) {
	c, _ := newTestClient(t)
	ctx := context.Background()

	status, err := c.GetReplicaStatus(ctx, "ghost-replica")
	require.NoError(t, err)
	assert.Equal(t, model.ReplicaStatusTerminated, status)
}

func TestLabelReplica_AddsLabels(t *testing.T) {
	c, cs := newTestClient(t)
	ctx := context.Background()

	replicaID, err := c.CreateReplica(ctx, "lblapp", model.SchedulingConstraints{RequiredGPUs: 1})
	require.NoError(t, err)

	err = c.LabelReplica(ctx, replicaID, map[string]string{"env": "prod", "tier": "gpu"})
	require.NoError(t, err)

	deploy, err := cs.AppsV1().Deployments(testNamespace).Get(ctx, replicaID, metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, "prod", deploy.Labels["env"])
	assert.Equal(t, "gpu", deploy.Labels["tier"])
}

func TestCreateReplica_TracesSpan(t *testing.T) {
	cs := fake.NewSimpleClientset()
	reg := prometheus.NewRegistry()
	tracer, exp := observability.NewTestTracer()
	c := New(cs, testNamespace, testClusterID, tracer, reg)

	ctx := context.Background()
	_, err := c.CreateReplica(ctx, "traceapp", model.SchedulingConstraints{RequiredGPUs: 1})
	require.NoError(t, err)

	spans := exp.GetSpans()
	require.NotEmpty(t, spans)

	var found bool
	for _, s := range spans {
		if s.Name == "k8sclient.create_replica" {
			found = true
			break
		}
	}
	assert.True(t, found, "expected k8sclient.create_replica span")
}

func TestCreateReplica_RecordsMetrics(t *testing.T) {
	cs := fake.NewSimpleClientset()
	reg := prometheus.NewRegistry()
	tracer := observability.NewNoopTracer()
	c := New(cs, testNamespace, testClusterID, tracer, reg)

	ctx := context.Background()
	_, err := c.CreateReplica(ctx, "metricapp", model.SchedulingConstraints{RequiredGPUs: 1})
	require.NoError(t, err)

	// Gather metrics and verify histogram was observed.
	metrics, err := reg.Gather()
	require.NoError(t, err)

	var foundDuration bool
	for _, mf := range metrics {
		if mf.GetName() == "schedulator_k8sclient_request_duration_seconds" {
			foundDuration = true
			break
		}
	}
	assert.True(t, foundDuration, "expected request duration metric")
}

