package integration

import (
	"context"
	"fmt"
	"os/exec"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

// SetupKindCluster creates a kind cluster and returns a rest.Config for it.
func SetupKindCluster(name string) (kubernetes.Interface, error) {
	cmd := exec.Command("kind", "create", "cluster",
		"--name", name,
		"--config", "../../deploy/kind/kind-config.yaml",
		"--wait", "60s",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("kind create cluster: %s: %w", string(out), err)
	}

	kubeconfig := exec.Command("kind", "get", "kubeconfig", "--name", name)
	kubeconfigBytes, err := kubeconfig.Output()
	if err != nil {
		return nil, fmt.Errorf("kind get kubeconfig: %w", err)
	}

	config, err := clientcmd.RESTConfigFromKubeConfig(kubeconfigBytes)
	if err != nil {
		return nil, fmt.Errorf("parsing kubeconfig: %w", err)
	}

	cs, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("creating clientset: %w", err)
	}
	return cs, nil
}

// TeardownKindCluster deletes a kind cluster.
func TeardownKindCluster(name string) error {
	cmd := exec.Command("kind", "delete", "cluster", "--name", name)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("kind delete cluster: %s: %w", string(out), err)
	}
	return nil
}

// CreateKWOKNodes creates simulated GPU nodes using KWOK conventions.
func CreateKWOKNodes(ctx context.Context, clientset kubernetes.Interface, count int) ([]string, error) {
	var names []string
	for i := range count {
		name := fmt.Sprintf("kwok-gpu-node-%d", i)
		node := &corev1.Node{
			ObjectMeta: metav1.ObjectMeta{
				Name: name,
				Labels: map[string]string{
					"node.kubernetes.io/instance-type": "gpu-8",
					"type": "kwok",
				},
				Annotations: map[string]string{
					"kwok.x-k8s.io/node":              "fake",
					"node.alpha.kubernetes.io/ttl":     "0",
				},
			},
			Spec: corev1.NodeSpec{
				Taints: []corev1.Taint{
					{
						Key:    "kwok.x-k8s.io/node",
						Value:  "fake",
						Effect: corev1.TaintEffectNoSchedule,
					},
				},
			},
			Status: corev1.NodeStatus{
				Phase: corev1.NodeRunning,
				Capacity: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("32"),
					corev1.ResourceMemory: resource.MustParse("256Gi"),
					"nvidia.com/gpu":      resource.MustParse("8"),
					corev1.ResourcePods:   resource.MustParse("110"),
				},
				Allocatable: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("32"),
					corev1.ResourceMemory: resource.MustParse("256Gi"),
					"nvidia.com/gpu":      resource.MustParse("8"),
					corev1.ResourcePods:   resource.MustParse("110"),
				},
				Conditions: []corev1.NodeCondition{
					{
						Type:   corev1.NodeReady,
						Status: corev1.ConditionTrue,
						Reason: "KubeletReady",
					},
				},
			},
		}

		_, err := clientset.CoreV1().Nodes().Create(ctx, node, metav1.CreateOptions{})
		if err != nil {
			return names, fmt.Errorf("creating KWOK node %s: %w", name, err)
		}
		names = append(names, name)
	}
	return names, nil
}

// WaitForNodeReady polls until the named node reports Ready=True or ctx expires.
func WaitForNodeReady(ctx context.Context, clientset kubernetes.Interface, nodeName string) error {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("timed out waiting for node %s to be ready", nodeName)
		case <-ticker.C:
			node, err := clientset.CoreV1().Nodes().Get(ctx, nodeName, metav1.GetOptions{})
			if err != nil {
				continue
			}
			for _, cond := range node.Status.Conditions {
				if cond.Type == corev1.NodeReady && cond.Status == corev1.ConditionTrue {
					return nil
				}
			}
		}
	}
}

// InstallKWOK installs the KWOK controller into the cluster using kubectl apply.
func InstallKWOK(ctx context.Context, clientset kubernetes.Interface) error {
	cmd := exec.CommandContext(ctx, "kubectl", "apply", "-f",
		"https://github.com/kubernetes-sigs/kwok/releases/latest/download/kwok.yaml")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("installing KWOK: %s: %w", string(out), err)
	}
	return nil
}
