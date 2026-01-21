package client

import (
	"fmt"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	"sync"
)

// ClientManager manages connections to multiple Kubernetes clusters
type ClientManager struct {
	mu       sync.RWMutex
	clients  map[string]kubernetes.Interface
}

// NewClientManager creates a new ClientManager
func NewClientManager() *ClientManager {
	return &ClientManager{
		clients: make(map[string]kubernetes.Interface),
	}
}

// AddCluster connects to a cluster using the provided context name and adds it to the manager
func (cm *ClientManager) AddCluster(contextName string) error {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	if _, exists := cm.clients[contextName]; exists {
		return nil // Already connected
	}

	clientset, err := getClientset(contextName)
	if err != nil {
		return fmt.Errorf("failed to create client for context %s: %w", contextName, err)
	}

	cm.clients[contextName] = clientset
	return nil
}

// SetClient allows injecting a client (useful for testing)
func (cm *ClientManager) SetClient(name string, client kubernetes.Interface) {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	cm.clients[name] = client
}

// GetClient returns the clientset for a specific cluster
func (cm *ClientManager) GetClient(contextName string) (kubernetes.Interface, error) {
	cm.mu.RLock()
	defer cm.mu.RUnlock()

	client, ok := cm.clients[contextName]
	if !ok {
		return nil, fmt.Errorf("cluster %s not found", contextName)
	}
	return client, nil
}

// ListClusters returns a list of connected cluster names
func (cm *ClientManager) ListClusters() []string {
	cm.mu.RLock()
	defer cm.mu.RUnlock()

	clusters := make([]string, 0, len(cm.clients))
	for name := range cm.clients {
		clusters = append(clusters, name)
	}
	return clusters
}

// Helper to create a clientset from a context name
func getClientset(contextName string) (*kubernetes.Clientset, error) {
	// Use the default loading rules (looks for KUBECONFIG env var or ~/.kube/config)
	loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
	
	// Override the current context
	configOverrides := &clientcmd.ConfigOverrides{CurrentContext: contextName}

	kubeConfig := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loadingRules, configOverrides)
	
	config, err := kubeConfig.ClientConfig()
	if err != nil {
		return nil, err
	}

	return kubernetes.NewForConfig(config)
}
