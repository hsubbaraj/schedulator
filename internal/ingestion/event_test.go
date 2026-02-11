package ingestion

import (
	"testing"
	"time"

	"github.com/hsubbaraj/schedulator/pkg/model"
	"github.com/stretchr/testify/assert"
)

func TestEvent_MergeKey(t *testing.T) {
	tests := []struct {
		name string
		event Event
		want  string
	}{
		{
			name: "MetricsUpdate includes AppID",
			event: Event{Kind: EventMetricsUpdate, AppID: "app-1"},
			want:  "MetricsUpdate:app-1",
		},
		{
			name: "ClusterEvent returns empty",
			event: Event{Kind: EventClusterEvent, AppID: "app-1"},
			want:  "",
		},
		{
			name: "SLABreach includes AppID",
			event: Event{Kind: EventSLABreach, AppID: "app-2"},
			want:  "SLABreach:app-2",
		},
		{
			name: "PeriodicEval has fixed key with empty AppID",
			event: Event{Kind: EventPeriodicEval},
			want:  "PeriodicEval:",
		},
		{
			name: "AppConfigUpdate includes AppID",
			event: Event{Kind: EventAppConfigUpdate, AppID: "app-3"},
			want:  "AppConfigUpdate:app-3",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, tt.event.mergeKey())
		})
	}
}

func TestEvent_IsMergeable(t *testing.T) {
	tests := []struct {
		name string
		kind EventKind
		want bool
	}{
		{"MetricsUpdate is mergeable", EventMetricsUpdate, true},
		{"ClusterEvent is not mergeable", EventClusterEvent, false},
		{"SLABreach is mergeable", EventSLABreach, true},
		{"PeriodicEval is mergeable", EventPeriodicEval, true},
		{"AppConfigUpdate is mergeable", EventAppConfigUpdate, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := Event{Kind: tt.kind}
			assert.Equal(t, tt.want, e.isMergeable())
		})
	}
}

func TestEvent_MergeKey_SameKindDifferentApps(t *testing.T) {
	e1 := Event{Kind: EventMetricsUpdate, AppID: "app-1"}
	e2 := Event{Kind: EventMetricsUpdate, AppID: "app-2"}
	assert.NotEqual(t, e1.mergeKey(), e2.mergeKey())
}

func TestEvent_MergeKey_DifferentKindsSameApp(t *testing.T) {
	e1 := Event{Kind: EventMetricsUpdate, AppID: "app-1"}
	e2 := Event{Kind: EventSLABreach, AppID: "app-1"}
	assert.NotEqual(t, e1.mergeKey(), e2.mergeKey())
}

func TestEvent_FieldsPreserved(t *testing.T) {
	now := time.Now()
	metrics := &model.VLLMMetrics{AppID: "app-1", TotalTokensPerSecond: 42.0}
	e := Event{
		Kind:        EventMetricsUpdate,
		Timestamp:   now,
		AppID:       "app-1",
		VLLMMetrics: metrics,
	}
	assert.Equal(t, EventMetricsUpdate, e.Kind)
	assert.Equal(t, now, e.Timestamp)
	assert.Equal(t, "app-1", e.AppID)
	assert.Equal(t, metrics, e.VLLMMetrics)
}
