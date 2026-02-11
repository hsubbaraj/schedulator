package ports

// LeaderElector manages leader election for high availability.
type LeaderElector interface {
	// IsLeader returns true if this instance currently holds the leader lease.
	IsLeader() bool

	// OnElected registers a callback invoked when this instance becomes leader.
	OnElected(callback func())

	// OnLost registers a callback invoked when this instance loses leadership.
	OnLost(callback func())
}
