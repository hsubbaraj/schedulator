package controlloop

import (
	"context"

	"github.com/hsubbaraj/schedulator/internal/engine/scaling"
	"github.com/hsubbaraj/schedulator/internal/eventlog"
	"github.com/hsubbaraj/schedulator/internal/ingestion"
	"github.com/hsubbaraj/schedulator/pkg/model"
)

// CycleSummary is the payload for the "cycle_summary" SSE event, published
// at the end of every scheduling cycle.
type CycleSummary struct {
	TriggerKinds     []ingestion.EventKind                   `json:"trigger_kinds"`
	ScalingDecisions map[model.AppID]scaling.ScalingDecision  `json:"scaling_decisions"`
	VLLMMetrics      map[model.AppID]model.VLLMMetrics        `json:"vllm_metrics"`
	Placement        PlacementSummary                         `json:"placement"`
	Execution        ExecutionSummary                         `json:"execution"`
	CycleDurationMs  int64                                    `json:"cycle_duration_ms"`
}

// PlacementSummary summarises placement decisions for one cycle.
type PlacementSummary struct {
	ScaleUpCount    int                        `json:"scale_up_count"`
	ScaleDownCount  int                        `json:"scale_down_count"`
	PreemptionCount int                        `json:"preemption_count"`
	ScaleUps        []model.ScaleUpDecision    `json:"scale_ups"`
	ScaleDowns      []model.ScaleDownDecision  `json:"scale_downs"`
	Preemptions     []model.PreemptionDecision `json:"preemptions"`
}

// ExecutionSummary summarises the outcome of plan execution.
type ExecutionSummary struct {
	CompletedCount int                 `json:"completed_count"`
	FailedCount    int                 `json:"failed_count"`
	AbortedCount   int                 `json:"aborted_count"`
	Completed      []model.OperationID `json:"completed"`
	Failed         []failedOpJSON      `json:"failed"`
	Aborted        []model.OperationID `json:"aborted"`
}

// failedOpJSON is a JSON-safe version of model.FailedOp (error → string).
type failedOpJSON struct {
	OperationID model.OperationID `json:"OperationID"`
	Type        string            `json:"Type"`
	Err         string            `json:"Err"`
}

// ---------------------------------------------------------------------------
// Local interfaces for new ControlLoop dependencies
// ---------------------------------------------------------------------------

// cycleSummaryPublisher publishes cycle summary events to SSE subscribers.
type cycleSummaryPublisher interface {
	PublishCycleSummary(summary CycleSummary)
}

// cycleEventLogger persists individual scheduler events.
type cycleEventLogger interface {
	LogEvent(ctx context.Context, e eventlog.EventRecord) error
}
