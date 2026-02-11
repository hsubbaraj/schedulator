package ports

import (
	"context"

	"github.com/hsubbaraj/schedulator/pkg/model"
)

// CacheRegistry queries model cache locations across the fleet.
type CacheRegistry interface {
	// GetCacheLocations returns all cache locations for a given model.
	GetCacheLocations(ctx context.Context, modelID model.ModelID) ([]model.CacheLocation, error)
}
