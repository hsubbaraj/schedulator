package types

// PlacementDecision represents scheduler's decision
type PlacementDecision struct {
    StateVersion  string     `json:"stateVersion"`
    PlacementMode string     `json:"placementMode"` // "ClusterOnly"
    Decisions     []Decision `json:"decisions"`
    Explain       []Explanation `json:"explain"`
}

// Decision represents placement for one workload
type Decision struct {
    WorkloadID string `json:"workloadId"`
    Cluster    string `json:"cluster"`
    Replicas   int    `json:"replicas"`
}

// Explanation provides decision rationale
type Explanation struct {
    WorkloadID   string              `json:"workloadId"`
    Reason       string              `json:"reason"`
    Considered   []Alternative       `json:"considered"`
    SolverTimeMs int                 `json:"solverTimeMs"`
}

// Alternative represents a cluster that was considered
type Alternative struct {
    Cluster string `json:"cluster"`
    Reason  string `json:"reason"`
}

// ApplyPlacementResult is simulator's response
type ApplyPlacementResult struct {
    Accepted        bool     `json:"accepted"`
    AppliedVersion  string   `json:"appliedVersion"`
    Conflicts       []string `json:"conflicts"`
    Warnings        []string `json:"warnings"`
    TransitionPlanID string  `json:"transitionPlanId"`
    Message         string   `json:"message"`
}
