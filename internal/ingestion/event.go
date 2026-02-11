package ingestion

import (
	"fmt"
	"time"

	"github.com/hsubbaraj/schedulator/pkg/model"
)

// EventKind identifies the type of ingestion event.
type EventKind string

const (
	EventMetricsUpdate   EventKind = "MetricsUpdate"
	EventClusterEvent    EventKind = "ClusterEvent"
	EventSLABreach       EventKind = "SLABreach"
	EventPeriodicEval    EventKind = "PeriodicEval"
	EventAppConfigUpdate EventKind = "AppConfigUpdate"
)

// Event is a normalized event produced by the ingestion layer.
type Event struct {
	Kind         EventKind
	Timestamp    time.Time
	AppID        model.AppID
	VLLMMetrics  *model.VLLMMetrics
	ClusterEvent *model.ClusterEvent
	Application  *model.Application
	SLAMetric    string
	SLAThreshold float64
	SLAActual    float64
}

// mergeKey returns a coalescing key for the debouncer. ClusterEvents return ""
// (never merged). PeriodicEval uses a fixed key so duplicates are deduplicated.
func (e Event) mergeKey() string {
	if !e.isMergeable() {
		return ""
	}
	return fmt.Sprintf("%s:%s", e.Kind, e.AppID)
}

// isMergeable returns true if this event kind supports last-write-wins merging.
// ClusterEvents are never merged because each represents a distinct state change.
func (e Event) isMergeable() bool {
	return e.Kind != EventClusterEvent
}
