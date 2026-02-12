package rebalancing

// RebalancingConfig holds tunable parameters for the rebalancing engine.
// Defaults match proposal Section 4.8.
type RebalancingConfig struct {
	MaxMigrationsPerCycle int
	MinFragmentationDelta float64
}

// DefaultRebalancingConfig returns configuration with the proposal's defaults.
func DefaultRebalancingConfig() RebalancingConfig {
	return RebalancingConfig{
		MaxMigrationsPerCycle: 2,
		MinFragmentationDelta: 0.15,
	}
}
