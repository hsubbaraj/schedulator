package ports

import (
	"context"

	"github.com/hsubbaraj/schedulator/pkg/model"
)

// ConfigStore provides application configuration.
type ConfigStore interface {
	// WatchApplications returns a channel that emits application configs as
	// they are created or updated.
	WatchApplications(ctx context.Context) (<-chan model.Application, error)

	// ListApplications returns all currently configured applications.
	ListApplications(ctx context.Context) ([]model.Application, error)
}
