package model

// FailedOp records an operation that failed during plan execution.
type FailedOp struct {
	OperationID OperationID
	Type        OperationType
	Err         error
}

// ExecutionResult summarises the outcome of executing an ExecutionPlan.
type ExecutionResult struct {
	Completed []OperationID
	Failed    []FailedOp
	Aborted   []OperationID
}
