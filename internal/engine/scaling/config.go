package scaling

import "time"

// ScalingConfig contains all tunable thresholds for the scaling engine.
// Default values match proposal Section 4.3.
type ScalingConfig struct {
	QueueHighWatermark   float64
	QueueTarget          float64
	QueueLowWatermark    float64
	KVCacheHighWatermark float64
	KVCacheTarget        float64
	KVCacheLowWatermark  float64
	BatchLowWatermark    float64
	MaxScaleDownPerCycle int
	MaxScaleUpPerCycle   int
	ScaleUpCooldown      time.Duration
	ScaleDownCooldown    time.Duration
	StabilizationCycles  int
}

// DefaultScalingConfig returns the proposal defaults from Section 4.3.
func DefaultScalingConfig() ScalingConfig {
	return ScalingConfig{
		QueueHighWatermark:   10.0,
		QueueTarget:          5.0,
		QueueLowWatermark:    1.0,
		KVCacheHighWatermark: 0.85,
		KVCacheTarget:        0.70,
		KVCacheLowWatermark:  0.40,
		BatchLowWatermark:    0.30,
		MaxScaleDownPerCycle: 2,
		MaxScaleUpPerCycle:   10,
		ScaleUpCooldown:      1 * time.Second,
		ScaleDownCooldown:    3 * time.Second,
		StabilizationCycles:  3,
	}
}
