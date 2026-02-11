package preemption

// PreemptionConfig contains tunable parameters for the preemption engine.
type PreemptionConfig struct {
	DefaultGracePeriodSeconds int
}

// DefaultPreemptionConfig returns the proposal defaults.
func DefaultPreemptionConfig() PreemptionConfig {
	return PreemptionConfig{
		DefaultGracePeriodSeconds: 10,
	}
}
