package ports

import (
	"context"

	"github.com/hsubbaraj/schedulator/pkg/model"
)

// ClusterClient abstracts Kubernetes operations on a specific cluster.
// Each replica is a single-replica Deployment.
type ClusterClient interface {
	// CreateReplica creates a new single-replica Deployment with the given
	// scheduling constraints.
	CreateReplica(ctx context.Context, appID model.AppID, constraints model.SchedulingConstraints) (model.ReplicaID, error)

	// DeleteReplica deletes the Deployment for the given replica.
	DeleteReplica(ctx context.Context, replicaID model.ReplicaID) error

	// GetReplicaStatus returns the current status of a replica.
	GetReplicaStatus(ctx context.Context, replicaID model.ReplicaID) (model.ReplicaStatus, error)

	// LabelReplica adds or updates labels on a replica's Deployment.
	LabelReplica(ctx context.Context, replicaID model.ReplicaID, labels map[string]string) error
}
