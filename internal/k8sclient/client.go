package k8sclient

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/prometheus/client_golang/prometheus"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"

	"github.com/hsubbaraj/schedulator/pkg/model"
)

const (
	labelAppID     = "app.schedulator.io/app-id"
	labelReplicaID = "app.schedulator.io/replica-id"
	annotationCacheAffinityModel = "schedulator.io/cache-affinity-model"
	defaultContainerImage        = "registry.k8s.io/pause:3.9"
)

// Client implements ports.ClusterClient using client-go.
type Client struct {
	clientset      kubernetes.Interface
	namespace      string
	clusterID      model.ClusterID
	containerImage string
	tracer         trace.Tracer
	metrics        *Metrics
}

// New creates a new K8s ClusterClient.
func New(clientset kubernetes.Interface, namespace, clusterID string, tracer trace.Tracer, reg prometheus.Registerer) *Client {
	return &Client{
		clientset:      clientset,
		namespace:      namespace,
		clusterID:      clusterID,
		containerImage: defaultContainerImage,
		tracer:         tracer,
		metrics:        NewMetrics(reg, clusterID),
	}
}

// CreateReplica creates a single-replica Deployment for the given app.
func (c *Client) CreateReplica(ctx context.Context, appID model.AppID, constraints model.SchedulingConstraints) (model.ReplicaID, error) {
	ctx, span := c.tracer.Start(ctx, "k8sclient.create_replica",
		trace.WithAttributes(
			attribute.String("app_id", appID),
			attribute.Int("required_gpus", constraints.RequiredGPUs),
		))
	defer span.End()

	start := time.Now()
	replicaID, err := c.createReplica(ctx, appID, constraints)
	duration := time.Since(start).Seconds()

	c.metrics.requestDuration.WithLabelValues("create_replica").Observe(duration)
	if err != nil {
		c.metrics.errorsTotal.WithLabelValues("create_replica").Inc()
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
	}
	return replicaID, err
}

func (c *Client) createReplica(ctx context.Context, appID model.AppID, constraints model.SchedulingConstraints) (model.ReplicaID, error) {
	suffix, err := randomSuffix(4)
	if err != nil {
		return "", fmt.Errorf("generating replica ID: %w", err)
	}
	replicaID := fmt.Sprintf("%s-replica-%s", appID, suffix)

	replicas := int32(1)
	labels := map[string]string{
		labelAppID:     appID,
		labelReplicaID: replicaID,
	}

	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:   replicaID,
			Labels: labels,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{
				MatchLabels: labels,
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: labels,
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:  "model",
							Image: c.containerImage,
							Resources: corev1.ResourceRequirements{
								Requests: corev1.ResourceList{
									"nvidia.com/gpu": resource.MustParse(fmt.Sprintf("%d", constraints.RequiredGPUs)),
								},
								Limits: corev1.ResourceList{
									"nvidia.com/gpu": resource.MustParse(fmt.Sprintf("%d", constraints.RequiredGPUs)),
								},
							},
						},
					},
					Tolerations: []corev1.Toleration{
						{
							Key:      "kwok.x-k8s.io/node",
							Operator: corev1.TolerationOpExists,
							Effect:   corev1.TaintEffectNoSchedule,
						},
						{
							Key:      "nvidia.com/gpu",
							Operator: corev1.TolerationOpExists,
							Effect:   corev1.TaintEffectNoSchedule,
						},
					},
				},
			},
		},
	}

	// Set node affinity from PreferredNodes.
	if len(constraints.PreferredNodes) > 0 {
		deployment.Spec.Template.Spec.Affinity = &corev1.Affinity{
			NodeAffinity: &corev1.NodeAffinity{
				PreferredDuringSchedulingIgnoredDuringExecution: []corev1.PreferredSchedulingTerm{
					{
						Weight: 100,
						Preference: corev1.NodeSelectorTerm{
							MatchExpressions: []corev1.NodeSelectorRequirement{
								{
									Key:      "kubernetes.io/hostname",
									Operator: corev1.NodeSelectorOpIn,
									Values:   constraints.PreferredNodes,
								},
							},
						},
					},
				},
			},
		}
	}

	// Set nodeSelector from RequiredNodeLabels.
	if len(constraints.RequiredNodeLabels) > 0 {
		deployment.Spec.Template.Spec.NodeSelector = constraints.RequiredNodeLabels
	}

	// Set cache affinity annotation.
	if constraints.CacheAffinityModel != "" {
		deployment.ObjectMeta.Annotations = map[string]string{
			annotationCacheAffinityModel: constraints.CacheAffinityModel,
		}
	}

	_, err = c.clientset.AppsV1().Deployments(c.namespace).Create(ctx, deployment, metav1.CreateOptions{})
	if err != nil {
		return "", fmt.Errorf("creating deployment %s: %w", replicaID, err)
	}
	return replicaID, nil
}

// DeleteReplica deletes the Deployment for the given replica. Idempotent: returns nil if not found.
func (c *Client) DeleteReplica(ctx context.Context, replicaID model.ReplicaID) error {
	ctx, span := c.tracer.Start(ctx, "k8sclient.delete_replica",
		trace.WithAttributes(attribute.String("replica_id", replicaID)))
	defer span.End()

	start := time.Now()
	err := c.clientset.AppsV1().Deployments(c.namespace).Delete(ctx, replicaID, metav1.DeleteOptions{})
	duration := time.Since(start).Seconds()

	c.metrics.requestDuration.WithLabelValues("delete_replica").Observe(duration)
	if err != nil {
		if errors.IsNotFound(err) {
			return nil
		}
		c.metrics.errorsTotal.WithLabelValues("delete_replica").Inc()
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return fmt.Errorf("deleting deployment %s: %w", replicaID, err)
	}
	return nil
}

// GetReplicaStatus returns the current status of a replica by inspecting its Deployment.
func (c *Client) GetReplicaStatus(ctx context.Context, replicaID model.ReplicaID) (model.ReplicaStatus, error) {
	ctx, span := c.tracer.Start(ctx, "k8sclient.get_replica_status",
		trace.WithAttributes(attribute.String("replica_id", replicaID)))
	defer span.End()

	start := time.Now()
	status, err := c.getReplicaStatus(ctx, replicaID)
	duration := time.Since(start).Seconds()

	c.metrics.requestDuration.WithLabelValues("get_replica_status").Observe(duration)
	if err != nil {
		c.metrics.errorsTotal.WithLabelValues("get_replica_status").Inc()
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
	}
	return status, err
}

func (c *Client) getReplicaStatus(ctx context.Context, replicaID model.ReplicaID) (model.ReplicaStatus, error) {
	deploy, err := c.clientset.AppsV1().Deployments(c.namespace).Get(ctx, replicaID, metav1.GetOptions{})
	if err != nil {
		if errors.IsNotFound(err) {
			return model.ReplicaStatusTerminated, nil
		}
		return "", fmt.Errorf("getting deployment %s: %w", replicaID, err)
	}

	if deploy.Status.AvailableReplicas >= 1 {
		return model.ReplicaStatusRunning, nil
	}
	return model.ReplicaStatusPending, nil
}

// LabelReplica adds or updates labels on a replica's Deployment.
func (c *Client) LabelReplica(ctx context.Context, replicaID model.ReplicaID, labels map[string]string) error {
	ctx, span := c.tracer.Start(ctx, "k8sclient.label_replica",
		trace.WithAttributes(attribute.String("replica_id", replicaID)))
	defer span.End()

	start := time.Now()
	err := c.labelReplica(ctx, replicaID, labels)
	duration := time.Since(start).Seconds()

	c.metrics.requestDuration.WithLabelValues("label_replica").Observe(duration)
	if err != nil {
		c.metrics.errorsTotal.WithLabelValues("label_replica").Inc()
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
	}
	return err
}

func (c *Client) labelReplica(ctx context.Context, replicaID model.ReplicaID, labels map[string]string) error {
	patch := map[string]interface{}{
		"metadata": map[string]interface{}{
			"labels": labels,
		},
	}
	patchBytes, err := json.Marshal(patch)
	if err != nil {
		return fmt.Errorf("marshaling label patch: %w", err)
	}

	_, err = c.clientset.AppsV1().Deployments(c.namespace).Patch(
		ctx, replicaID, types.MergePatchType, patchBytes, metav1.PatchOptions{},
	)
	if err != nil {
		return fmt.Errorf("patching deployment %s labels: %w", replicaID, err)
	}
	return nil
}

func randomSuffix(bytes int) (string, error) {
	b := make([]byte, bytes)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b)[:bytes*2], nil
}
