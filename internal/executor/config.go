package executor

import "time"

// ExecutorConfig holds tunable parameters for the plan executor.
type ExecutorConfig struct {
	// PollInterval is the delay between status polls when waiting for a
	// replica to become Running or to be deleted. Default: 5s.
	PollInterval time.Duration

	// WaitForDeletionTimeout is the maximum time to wait for a replica to be
	// fully deleted after DeleteReplica is called. Default: 120s.
	WaitForDeletionTimeout time.Duration

	// DefaultReservationTTL is the fallback TTL used when a scale-up
	// operation's reservation is not found in the snapshot. Default: 300s.
	DefaultReservationTTL time.Duration
}

// DefaultExecutorConfig returns an ExecutorConfig with production defaults.
func DefaultExecutorConfig() ExecutorConfig {
	return ExecutorConfig{
		PollInterval:           5 * time.Second,
		WaitForDeletionTimeout: 120 * time.Second,
		DefaultReservationTTL:  300 * time.Second,
	}
}
