package ports

import (
	"context"

	"github.com/hsubbaraj/schedulator/pkg/model"
)

// ClusterAggregator aggregates cluster state and vLLM metrics from the fleet.
type ClusterAggregator interface {
	// WatchEvents returns a channel of cluster events (node/pod state changes).
	WatchEvents(ctx context.Context) (<-chan model.ClusterEvent, error)

	// FullSync returns the complete state of all clusters for reconciliation.
	FullSync(ctx context.Context) ([]model.Cluster, []model.Replica, error)

	// GetVLLMMetrics fetches current vLLM runtime metrics for one application.
	GetVLLMMetrics(ctx context.Context, appID model.AppID) (model.VLLMMetrics, error)
}
