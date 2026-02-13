package k8saggregator

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"

	"github.com/hsubbaraj/schedulator/pkg/model"
)

const (
	labelAppID = "app.schedulator.io/app-id"
	gpuResource = "nvidia.com/gpu"
)

// ClusterConfig describes a single cluster to watch.
type ClusterConfig struct {
	ClusterID model.ClusterID
	Clientset kubernetes.Interface
	Namespace string
}

// K8sAggregator implements ports.ClusterAggregator using client-go informers.
type K8sAggregator struct {
	clusters map[model.ClusterID]*clusterWatcher
	eventCh  chan model.ClusterEvent
	tracer   trace.Tracer
}

type clusterWatcher struct {
	clusterID model.ClusterID
	clientset kubernetes.Interface
	namespace string
}

// New creates a K8sAggregator for the given clusters.
func New(configs []ClusterConfig, tracer trace.Tracer) *K8sAggregator {
	clusters := make(map[model.ClusterID]*clusterWatcher, len(configs))
	for _, cfg := range configs {
		clusters[cfg.ClusterID] = &clusterWatcher{
			clusterID: cfg.ClusterID,
			clientset: cfg.Clientset,
			namespace: cfg.Namespace,
		}
	}
	return &K8sAggregator{
		clusters: clusters,
		eventCh:  make(chan model.ClusterEvent, 64),
		tracer:   tracer,
	}
}

// WatchEvents starts informers for all clusters and returns a channel of events.
func (a *K8sAggregator) WatchEvents(ctx context.Context) (<-chan model.ClusterEvent, error) {
	_, span := a.tracer.Start(ctx, "k8saggregator.watch_events")
	defer span.End()

	for _, cw := range a.clusters {
		if err := a.startInformers(ctx, cw); err != nil {
			return nil, fmt.Errorf("start informers for cluster %s: %w", cw.clusterID, err)
		}
	}
	return a.eventCh, nil
}

// FullSync lists all nodes and pods across all clusters and returns the current state.
func (a *K8sAggregator) FullSync(ctx context.Context) ([]model.Cluster, error) {
	ctx, span := a.tracer.Start(ctx, "k8saggregator.full_sync",
		trace.WithAttributes(attribute.Int("cluster_count", len(a.clusters))))
	defer span.End()

	clusters := make([]model.Cluster, 0, len(a.clusters))
	for _, cw := range a.clusters {
		cluster, err := a.syncCluster(ctx, cw)
		if err != nil {
			return nil, fmt.Errorf("sync cluster %s: %w", cw.clusterID, err)
		}
		clusters = append(clusters, cluster)
	}

	span.SetAttributes(attribute.Int("clusters_synced", len(clusters)))
	return clusters, nil
}

// GetVLLMMetrics returns zero metrics for the live demo. Real vLLM metric
// scraping can be added later. The scaling engine handles zero-metric cases
// gracefully (returns current count as target).
func (a *K8sAggregator) GetVLLMMetrics(_ context.Context, appID model.AppID) (model.VLLMMetrics, error) {
	return model.VLLMMetrics{
		AppID:      appID,
		MeasuredAt: time.Now(),
	}, nil
}

func (a *K8sAggregator) syncCluster(ctx context.Context, cw *clusterWatcher) (model.Cluster, error) {
	nodeList, err := cw.clientset.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return model.Cluster{}, fmt.Errorf("list nodes: %w", err)
	}

	labelSelector := labelAppID
	podList, err := cw.clientset.CoreV1().Pods(cw.namespace).List(ctx, metav1.ListOptions{
		LabelSelector: labelSelector,
	})
	if err != nil {
		return model.Cluster{}, fmt.Errorf("list pods: %w", err)
	}

	// Build node-to-pods map.
	nodePods := make(map[string][]model.ReplicaID)
	nodeAllocatedGPUs := make(map[string]int)
	for i := range podList.Items {
		pod := &podList.Items[i]
		if pod.Spec.NodeName == "" {
			continue
		}
		replicaID := pod.Labels[labelAppID] + "-" + pod.Name
		nodePods[pod.Spec.NodeName] = append(nodePods[pod.Spec.NodeName], replicaID)
		nodeAllocatedGPUs[pod.Spec.NodeName] += extractPodGPUs(pod)
	}

	nodes := make(map[model.NodeID]model.Node, len(nodeList.Items))
	for i := range nodeList.Items {
		node := &nodeList.Items[i]
		totalGPUs := extractNodeGPUs(node)
		allocated := nodeAllocatedGPUs[node.Name]
		free := totalGPUs - allocated
		if free < 0 {
			free = 0
		}

		status := nodeStatusFromConditions(node)
		nodes[node.Name] = model.Node{
			NodeID:        node.Name,
			ClusterID:     cw.clusterID,
			TotalGPUs:     totalGPUs,
			AllocatedGPUs: allocated,
			FreeGPUs:      free,
			Pods:          nodePods[node.Name],
			CachedModels:  make(map[model.ModelID]struct{}),
			Status:        status,
		}
	}

	return model.Cluster{
		ClusterID: cw.clusterID,
		Nodes:     nodes,
	}, nil
}

func (a *K8sAggregator) startInformers(ctx context.Context, cw *clusterWatcher) error {
	factory := informers.NewSharedInformerFactoryWithOptions(
		cw.clientset,
		30*time.Second,
		informers.WithNamespace(corev1.NamespaceAll),
	)

	nodeInformer := factory.Core().V1().Nodes().Informer()
	addNodeHandlers(nodeInformer, cw.clusterID, a.eventCh)

	podFactory := informers.NewSharedInformerFactoryWithOptions(
		cw.clientset,
		30*time.Second,
		informers.WithNamespace(cw.namespace),
	)
	podInformer := podFactory.Core().V1().Pods().Informer()
	addPodHandlers(podInformer, cw.clusterID, a.eventCh)

	factory.Start(ctx.Done())
	podFactory.Start(ctx.Done())

	slog.Info("started informers", "cluster", cw.clusterID)
	return nil
}
