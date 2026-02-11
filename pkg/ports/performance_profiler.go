package ports

import (
	"context"

	"github.com/hsubbaraj/schedulator/pkg/model"
)

// PerformanceProfiler provides profiled performance characteristics for
// applications.
type PerformanceProfiler interface {
	// GetProfile returns the performance profile for an application.
	GetProfile(ctx context.Context, appID model.AppID) (model.PerformanceProfile, error)
}
