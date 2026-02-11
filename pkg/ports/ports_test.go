package ports_test

import (
	"github.com/hsubbaraj/schedulator/pkg/ports"
	"github.com/hsubbaraj/schedulator/pkg/ports/mocks"
)

// Compile-time interface satisfaction checks.
var (
	_ ports.ClusterAggregator   = (*mocks.MockClusterAggregator)(nil)
	_ ports.CacheRegistry       = (*mocks.MockCacheRegistry)(nil)
	_ ports.PerformanceProfiler = (*mocks.MockPerformanceProfiler)(nil)
	_ ports.ClusterClient       = (*mocks.MockClusterClient)(nil)
	_ ports.ConfigStore         = (*mocks.MockConfigStore)(nil)
	_ ports.LeaderElector       = (*mocks.MockLeaderElector)(nil)
)
