package k8saggregator

import (
	"log/slog"
	"strconv"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/client-go/tools/cache"

	"github.com/hsubbaraj/schedulator/pkg/model"
)

// addNodeHandlers registers add/update/delete handlers on the node informer.
func addNodeHandlers(informer cache.SharedIndexInformer, clusterID model.ClusterID, eventCh chan<- model.ClusterEvent) {
	informer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			node, ok := obj.(*corev1.Node)
			if !ok {
				return
			}
			if isNodeReady(node) {
				eventCh <- model.ClusterEvent{
					Kind:      model.ClusterEventNodeUp,
					ClusterID: clusterID,
					NodeID:    node.Name,
					TotalGPUs: extractNodeGPUs(node),
					Timestamp: time.Now(),
				}
			}
		},
		UpdateFunc: func(oldObj, newObj interface{}) {
			oldNode, ok1 := oldObj.(*corev1.Node)
			newNode, ok2 := newObj.(*corev1.Node)
			if !ok1 || !ok2 {
				return
			}
			wasReady := isNodeReady(oldNode)
			nowReady := isNodeReady(newNode)

			if !wasReady && nowReady {
				eventCh <- model.ClusterEvent{
					Kind:      model.ClusterEventNodeUp,
					ClusterID: clusterID,
					NodeID:    newNode.Name,
					TotalGPUs: extractNodeGPUs(newNode),
					Timestamp: time.Now(),
				}
			} else if wasReady && !nowReady {
				eventCh <- model.ClusterEvent{
					Kind:      model.ClusterEventNodeDown,
					ClusterID: clusterID,
					NodeID:    newNode.Name,
					Timestamp: time.Now(),
				}
			}
		},
		DeleteFunc: func(obj interface{}) {
			node, ok := obj.(*corev1.Node)
			if !ok {
				tombstone, ok := obj.(cache.DeletedFinalStateUnknown)
				if !ok {
					return
				}
				node, ok = tombstone.Obj.(*corev1.Node)
				if !ok {
					return
				}
			}
			eventCh <- model.ClusterEvent{
				Kind:      model.ClusterEventNodeDown,
				ClusterID: clusterID,
				NodeID:    node.Name,
				Timestamp: time.Now(),
			}
		},
	})
}

// addPodHandlers registers add/update/delete handlers on the pod informer.
// Only handles pods with the schedulator app-id label.
func addPodHandlers(informer cache.SharedIndexInformer, clusterID model.ClusterID, eventCh chan<- model.ClusterEvent) {
	informer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			pod, ok := obj.(*corev1.Pod)
			if !ok || !isSchedulatorPod(pod) {
				return
			}
			emitPodEvent(pod, clusterID, eventCh)
		},
		UpdateFunc: func(_, newObj interface{}) {
			pod, ok := newObj.(*corev1.Pod)
			if !ok || !isSchedulatorPod(pod) {
				return
			}
			emitPodEvent(pod, clusterID, eventCh)
		},
		DeleteFunc: func(obj interface{}) {
			pod, ok := obj.(*corev1.Pod)
			if !ok {
				tombstone, ok := obj.(cache.DeletedFinalStateUnknown)
				if !ok {
					return
				}
				pod, ok = tombstone.Obj.(*corev1.Pod)
				if !ok {
					return
				}
			}
			if !isSchedulatorPod(pod) {
				return
			}
			appID := pod.Labels[labelAppID]
			eventCh <- model.ClusterEvent{
				Kind:      model.ClusterEventPodTerminated,
				ClusterID: clusterID,
				NodeID:    pod.Spec.NodeName,
				ReplicaID: model.ReplicaID(appID + "-" + pod.Name),
				AppID:     model.AppID(appID),
				GPUs:      extractPodGPUs(pod),
				Timestamp: time.Now(),
			}
		},
	})
}

func emitPodEvent(pod *corev1.Pod, clusterID model.ClusterID, eventCh chan<- model.ClusterEvent) {
	appID := pod.Labels[labelAppID]
	// ReplicaID must match the format used in FullSync: "appID-podName".
	replicaID := model.ReplicaID(appID + "-" + pod.Name)
	gpus := extractPodGPUs(pod)
	switch pod.Status.Phase {
	case corev1.PodRunning:
		eventCh <- model.ClusterEvent{
			Kind:      model.ClusterEventPodRunning,
			ClusterID: clusterID,
			NodeID:    pod.Spec.NodeName,
			ReplicaID: replicaID,
			AppID:     model.AppID(appID),
			GPUs:      gpus,
			Timestamp: time.Now(),
		}
	case corev1.PodSucceeded, corev1.PodFailed:
		eventCh <- model.ClusterEvent{
			Kind:      model.ClusterEventPodTerminated,
			ClusterID: clusterID,
			NodeID:    pod.Spec.NodeName,
			ReplicaID: replicaID,
			AppID:     model.AppID(appID),
			GPUs:      gpus,
			Timestamp: time.Now(),
		}
	}
}

func isSchedulatorPod(pod *corev1.Pod) bool {
	_, ok := pod.Labels[labelAppID]
	return ok
}

func isNodeReady(node *corev1.Node) bool {
	for _, cond := range node.Status.Conditions {
		if cond.Type == corev1.NodeReady {
			return cond.Status == corev1.ConditionTrue
		}
	}
	return false
}

// extractNodeGPUs gets the nvidia.com/gpu capacity from a node.
func extractNodeGPUs(node *corev1.Node) int {
	return extractGPUQuantity(node.Status.Capacity)
}

// extractPodGPUs sums nvidia.com/gpu requests across all containers.
func extractPodGPUs(pod *corev1.Pod) int {
	total := 0
	for i := range pod.Spec.Containers {
		total += extractGPUQuantity(pod.Spec.Containers[i].Resources.Requests)
	}
	return total
}

func extractGPUQuantity(resources corev1.ResourceList) int {
	if resources == nil {
		return 0
	}
	qty, ok := resources[corev1.ResourceName(gpuResource)]
	if !ok {
		return 0
	}
	val, err := strconv.Atoi(qty.String())
	if err != nil {
		// Try as a scaled value.
		return int(qty.ScaledValue(resource.Scale(0)))
	}
	return val
}

// nodeStatusFromConditions maps K8s node conditions to our domain status.
func nodeStatusFromConditions(node *corev1.Node) model.NodeStatus {
	if node.Spec.Unschedulable {
		return model.NodeStatusCordoned
	}
	for _, cond := range node.Status.Conditions {
		if cond.Type == corev1.NodeReady {
			if cond.Status == corev1.ConditionTrue {
				return model.NodeStatusReady
			}
			return model.NodeStatusDown
		}
	}
	return model.NodeStatusDown
}

func init() {
	// Suppress noisy client-go logging.
	_ = slog.Default()
}
