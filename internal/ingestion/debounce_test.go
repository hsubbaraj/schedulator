package ingestion

import (
	"context"
	"testing"
	"time"

	"github.com/hsubbaraj/schedulator/pkg/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testDebounceWindow = 50 * time.Millisecond

func newTestDebouncer() *Debouncer {
	return NewDebouncer(DebouncerConfig{Window: testDebounceWindow})
}

// readBatch reads a batch from outCh with a timeout. Returns nil on timeout.
func readBatch(t *testing.T, outCh <-chan []Event, timeout time.Duration) []Event {
	t.Helper()
	select {
	case batch, ok := <-outCh:
		if !ok {
			return nil
		}
		return batch
	case <-time.After(timeout):
		t.Fatal("timed out waiting for batch")
		return nil
	}
}

// waitClosed waits for the channel to close within timeout.
func waitClosed(t *testing.T, outCh <-chan []Event, timeout time.Duration) {
	t.Helper()
	select {
	case _, ok := <-outCh:
		assert.False(t, ok, "expected channel to be closed")
	case <-time.After(timeout):
		t.Fatal("timed out waiting for channel close")
	}
}

func TestDebouncer_MergesMetricsUpdates(t *testing.T) {
	d := newTestDebouncer()
	inCh := make(chan Event, 10)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	outCh := d.Run(ctx, inCh)

	now := time.Now()
	inCh <- Event{Kind: EventMetricsUpdate, AppID: "app-1", Timestamp: now,
		VLLMMetrics: &model.VLLMMetrics{AppID: "app-1", TotalTokensPerSecond: 10}}
	inCh <- Event{Kind: EventMetricsUpdate, AppID: "app-1", Timestamp: now.Add(time.Second),
		VLLMMetrics: &model.VLLMMetrics{AppID: "app-1", TotalTokensPerSecond: 20}}

	batch := readBatch(t, outCh, time.Second)
	require.Len(t, batch, 1)
	assert.Equal(t, float64(20), batch[0].VLLMMetrics.TotalTokensPerSecond)
}

func TestDebouncer_DifferentAppsNotMerged(t *testing.T) {
	d := newTestDebouncer()
	inCh := make(chan Event, 10)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	outCh := d.Run(ctx, inCh)

	inCh <- Event{Kind: EventMetricsUpdate, AppID: "app-1",
		VLLMMetrics: &model.VLLMMetrics{AppID: "app-1"}}
	inCh <- Event{Kind: EventMetricsUpdate, AppID: "app-2",
		VLLMMetrics: &model.VLLMMetrics{AppID: "app-2"}}

	batch := readBatch(t, outCh, time.Second)
	require.Len(t, batch, 2)
}

func TestDebouncer_WindowExpiry(t *testing.T) {
	d := newTestDebouncer()
	inCh := make(chan Event, 10)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	outCh := d.Run(ctx, inCh)

	// First event triggers timer
	inCh <- Event{Kind: EventMetricsUpdate, AppID: "app-1",
		VLLMMetrics: &model.VLLMMetrics{AppID: "app-1"}}

	batch1 := readBatch(t, outCh, time.Second)
	require.Len(t, batch1, 1)

	// Second event after window expires → new batch
	inCh <- Event{Kind: EventMetricsUpdate, AppID: "app-2",
		VLLMMetrics: &model.VLLMMetrics{AppID: "app-2"}}

	batch2 := readBatch(t, outCh, time.Second)
	require.Len(t, batch2, 1)
	assert.Equal(t, "app-2", batch2[0].AppID)
}

func TestDebouncer_ClusterEventsNotCoalesced(t *testing.T) {
	d := newTestDebouncer()
	inCh := make(chan Event, 10)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	outCh := d.Run(ctx, inCh)

	for i := 0; i < 3; i++ {
		inCh <- Event{Kind: EventClusterEvent,
			ClusterEvent: &model.ClusterEvent{Kind: model.ClusterEventNodeDown, ClusterID: "c1"}}
	}

	batch := readBatch(t, outCh, time.Second)
	require.Len(t, batch, 3)
	for _, ev := range batch {
		assert.Equal(t, EventClusterEvent, ev.Kind)
	}
}

func TestDebouncer_PeriodicEvalDeduplicated(t *testing.T) {
	d := newTestDebouncer()
	inCh := make(chan Event, 10)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	outCh := d.Run(ctx, inCh)

	inCh <- Event{Kind: EventPeriodicEval, Timestamp: time.Now()}
	inCh <- Event{Kind: EventPeriodicEval, Timestamp: time.Now().Add(time.Second)}

	batch := readBatch(t, outCh, time.Second)
	count := 0
	for _, ev := range batch {
		if ev.Kind == EventPeriodicEval {
			count++
		}
	}
	assert.Equal(t, 1, count)
}

func TestDebouncer_MixedEventTypes(t *testing.T) {
	d := newTestDebouncer()
	inCh := make(chan Event, 10)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	outCh := d.Run(ctx, inCh)

	inCh <- Event{Kind: EventMetricsUpdate, AppID: "app-1",
		VLLMMetrics: &model.VLLMMetrics{AppID: "app-1", TotalTokensPerSecond: 10}}
	inCh <- Event{Kind: EventClusterEvent,
		ClusterEvent: &model.ClusterEvent{Kind: model.ClusterEventNodeDown}}
	inCh <- Event{Kind: EventMetricsUpdate, AppID: "app-1",
		VLLMMetrics: &model.VLLMMetrics{AppID: "app-1", TotalTokensPerSecond: 20}}
	inCh <- Event{Kind: EventPeriodicEval}
	inCh <- Event{Kind: EventAppConfigUpdate, AppID: "app-2",
		Application: &model.Application{AppID: "app-2"}}

	batch := readBatch(t, outCh, time.Second)

	// 1 merged MetricsUpdate(app-1) + 1 ClusterEvent + 1 PeriodicEval + 1 AppConfigUpdate = 4
	require.Len(t, batch, 4)

	var metricEvents, clusterEvents, periodicEvents, configEvents int
	for _, ev := range batch {
		switch ev.Kind {
		case EventMetricsUpdate:
			metricEvents++
			assert.Equal(t, float64(20), ev.VLLMMetrics.TotalTokensPerSecond)
		case EventClusterEvent:
			clusterEvents++
		case EventPeriodicEval:
			periodicEvents++
		case EventAppConfigUpdate:
			configEvents++
		}
	}
	assert.Equal(t, 1, metricEvents)
	assert.Equal(t, 1, clusterEvents)
	assert.Equal(t, 1, periodicEvents)
	assert.Equal(t, 1, configEvents)
}

func TestDebouncer_ContextCancellation(t *testing.T) {
	d := newTestDebouncer()
	// Use unbuffered channel so we know the debouncer has received the event
	// once the send completes.
	inCh := make(chan Event)
	ctx, cancel := context.WithCancel(context.Background())

	outCh := d.Run(ctx, inCh)

	// Send on unbuffered channel ensures the debouncer's select has consumed it.
	inCh <- Event{Kind: EventMetricsUpdate, AppID: "app-1",
		VLLMMetrics: &model.VLLMMetrics{AppID: "app-1"}}

	cancel()

	// Should flush remaining and close
	var batches [][]Event
	for batch := range outCh {
		batches = append(batches, batch)
	}
	// At least the pending event should be flushed
	total := 0
	for _, b := range batches {
		total += len(b)
	}
	assert.GreaterOrEqual(t, total, 1)
}

func TestDebouncer_InputChannelClosed(t *testing.T) {
	d := newTestDebouncer()
	inCh := make(chan Event, 10)
	ctx := context.Background()

	outCh := d.Run(ctx, inCh)

	inCh <- Event{Kind: EventPeriodicEval}
	close(inCh)

	// Should flush and close output
	var batches [][]Event
	for batch := range outCh {
		batches = append(batches, batch)
	}
	total := 0
	for _, b := range batches {
		total += len(b)
	}
	assert.Equal(t, 1, total)
}
