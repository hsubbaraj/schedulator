package placement

// PlacementConfig contains all tunable parameters for the placement engine.
// Default values match proposal Section 4.4.
type PlacementConfig struct {
	WeightCacheGPUMemory         float64
	WeightCacheLocalNVMe         float64
	WeightCacheClusterStorage    float64
	WeightFragmentation          float64
	WeightBalance                float64
	MaxGPUsPerNode               int
	ReservationTTLMultiplier     int
	DefaultReservationTTLSeconds int
}

// DefaultPlacementConfig returns the proposal defaults from Section 4.4.
func DefaultPlacementConfig() PlacementConfig {
	return PlacementConfig{
		WeightCacheGPUMemory:         1000,
		WeightCacheLocalNVMe:         500,
		WeightCacheClusterStorage:    100,
		WeightFragmentation:          50,
		WeightBalance:                30,
		MaxGPUsPerNode:               8,
		ReservationTTLMultiplier:     2,
		DefaultReservationTTLSeconds: 300,
	}
}
