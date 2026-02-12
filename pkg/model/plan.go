package model

// OperationType identifies the kind of operation in an execution plan.
type OperationType string

const (
	OperationPreempt   OperationType = "preempt"
	OperationScaleDown OperationType = "scale_down"
	OperationScaleUp   OperationType = "scale_up"
	OperationMigrate   OperationType = "migrate" // internal only; decomposed into scale_up + scale_down
)

// Operation is a single unit of work in an execution plan.
type Operation struct {
	ID        OperationID
	Type      OperationType
	Payload   any
	DependsOn []OperationID
}

// ExecutionPlan is an ordered list of operations produced by the plan
// generator. Operations are sequenced: preemptions → scale-downs →
// scale-ups → migrations (decomposed into scale-up + scale-down pairs).
type ExecutionPlan struct {
	Operations []Operation
}

// MigrateDecision instructs the plan generator to create a blue-green
// migration: scale-up a new replica in the target cluster, then scale-down
// the old replica once the new one is confirmed running.
type MigrateDecision struct {
	AppID                 AppID
	SourceReplicaID       ReplicaID
	TargetClusterID       ClusterID
	SchedulingConstraints SchedulingConstraints
}
