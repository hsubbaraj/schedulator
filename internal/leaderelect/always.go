package leaderelect

// AlwaysLeader is a LeaderElector stub that always considers itself the leader.
// Suitable for single-instance deployments and local development.
type AlwaysLeader struct{}

// IsLeader always returns true.
func (AlwaysLeader) IsLeader() bool { return true }

// OnElected immediately invokes the callback since this instance is always leader.
func (AlwaysLeader) OnElected(callback func()) { callback() }

// OnLost is a no-op — leadership is never lost.
func (AlwaysLeader) OnLost(func()) {}
