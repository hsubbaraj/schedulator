package solver

// SolverConfig holds tunable parameters for the CP-SAT solver.
// Defaults match the solver-spec.md §5.3.
type SolverConfig struct {
	TimeoutMs           int    // CP-SAT wall-clock limit; default 200
	MaxShadowSlots      int    // migration budget per cycle; default 2
	WUnmetP0            int64  // default 1_000_000
	WUnmetP1            int64  // default 100_000
	WPreemptP2          int64  // default 50_000
	WPreemptP1          int64  // default 80_000 (P0 preempting P1)
	WShadow             int64  // default 500 (migration churn cost)
	WColdGPUMemory      int64  // default 0 (no extra cost — best tier)
	WColdLocalNVMe      int64  // default 2_000
	WColdClusterStorage int64  // default 5_000
	WColdRemote         int64  // default 10_000
	WPacking            int64  // default 100 (reward — subtracted from J)
	RandomSeed          uint64 // fixed for deterministic tests; 0 = non-deterministic
}

// DefaultSolverConfig returns a SolverConfig with spec-defined defaults.
func DefaultSolverConfig() SolverConfig {
	return SolverConfig{
		TimeoutMs:           200,
		MaxShadowSlots:      2,
		WUnmetP0:            1_000_000,
		WUnmetP1:            100_000,
		WPreemptP2:          50_000,
		WPreemptP1:          80_000,
		WShadow:             500,
		WColdGPUMemory:      0,
		WColdLocalNVMe:      2_000,
		WColdClusterStorage: 5_000,
		WColdRemote:         10_000,
		WPacking:            100,
		RandomSeed:          0,
	}
}
