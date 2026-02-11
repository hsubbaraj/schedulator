package ingestion

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/hsubbaraj/schedulator/internal/observability"
	"github.com/hsubbaraj/schedulator/pkg/model"
	"github.com/hsubbaraj/schedulator/pkg/ports/mocks"
)

func testIngesterConfig() IngesterConfig {
	return IngesterConfig{
		MetricsPollInterval:  50 * time.Millisecond,
		PeriodicEvalInterval: 50 * time.Millisecond,
		DebounceWindow:       30 * time.Millisecond,
	}
}

func newTestIngester(t *testing.T, agg *mocks.MockClusterAggregator, cs *mocks.MockConfigStore) *Ingester {
	t.Helper()
	tracer := observability.NewNoopTracer()
	reg := prometheus.NewRegistry()
	return NewIngester(agg, cs, testIngesterConfig(), tracer, reg)
}

// readBatchFromIngester reads a batch with a timeout, failing the test on timeout.
func readBatchFromIngester(t *testing.T, outCh <-chan []Event, timeout time.Duration) []Event {
	t.Helper()
	select {
	case batch, ok := <-outCh:
		if !ok {
			t.Fatal("output channel closed unexpectedly")
		}
		return batch
	case <-time.After(timeout):
		t.Fatal("timed out waiting for batch from ingester")
		return nil
	}
}

func TestIngester_ClusterEventPassthrough(t *testing.T) {
	agg := mocks.NewMockClusterAggregator(t)
	cs := mocks.NewMockConfigStore(t)

	eventCh := make(chan model.ClusterEvent, 1)
	appCh := make(chan model.Application)

	cs.EXPECT().ListApplications(mock.Anything).Return(nil, nil)
	agg.EXPECT().WatchEvents(mock.Anything).Return(eventCh, nil)
	cs.EXPECT().WatchApplications(mock.Anything).Return(appCh, nil)

	ing := newTestIngester(t, agg, cs)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	outCh, err := ing.Start(ctx)
	require.NoError(t, err)

	eventCh <- model.ClusterEvent{
		Kind:      model.ClusterEventNodeDown,
		ClusterID: "cluster-1",
		Timestamp: time.Now(),
	}

	batch := readBatchFromIngester(t, outCh, 2*time.Second)
	found := false
	for _, ev := range batch {
		if ev.Kind == EventClusterEvent && ev.ClusterEvent.ClusterID == "cluster-1" {
			found = true
			break
		}
	}
	assert.True(t, found, "cluster event should appear in batch")
}

func TestIngester_TimerFires(t *testing.T) {
	agg := mocks.NewMockClusterAggregator(t)
	cs := mocks.NewMockConfigStore(t)

	eventCh := make(chan model.ClusterEvent)
	appCh := make(chan model.Application)

	cs.EXPECT().ListApplications(mock.Anything).Return(nil, nil)
	agg.EXPECT().WatchEvents(mock.Anything).Return(eventCh, nil)
	cs.EXPECT().WatchApplications(mock.Anything).Return(appCh, nil)

	ing := newTestIngester(t, agg, cs)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	outCh, err := ing.Start(ctx)
	require.NoError(t, err)

	batch := readBatchFromIngester(t, outCh, 2*time.Second)
	found := false
	for _, ev := range batch {
		if ev.Kind == EventPeriodicEval {
			found = true
			break
		}
	}
	assert.True(t, found, "periodic eval event should appear in batch")
}

func TestIngester_MetricsPolling(t *testing.T) {
	agg := mocks.NewMockClusterAggregator(t)
	cs := mocks.NewMockConfigStore(t)

	eventCh := make(chan model.ClusterEvent)
	appCh := make(chan model.Application)

	apps := []model.Application{{AppID: "app-1", ModelID: "model-1"}}
	cs.EXPECT().ListApplications(mock.Anything).Return(apps, nil)
	agg.EXPECT().WatchEvents(mock.Anything).Return(eventCh, nil)
	cs.EXPECT().WatchApplications(mock.Anything).Return(appCh, nil)
	agg.EXPECT().GetVLLMMetrics(mock.Anything, "app-1").Return(
		model.VLLMMetrics{AppID: "app-1", TotalTokensPerSecond: 100, MeasuredAt: time.Now()}, nil,
	).Maybe()

	ing := newTestIngester(t, agg, cs)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	outCh, err := ing.Start(ctx)
	require.NoError(t, err)

	// Look for a MetricsUpdate event across batches
	deadline := time.After(2 * time.Second)
	for {
		select {
		case batch, ok := <-outCh:
			if !ok {
				t.Fatal("output closed before finding metrics event")
			}
			for _, ev := range batch {
				if ev.Kind == EventMetricsUpdate && ev.AppID == "app-1" {
					assert.NotNil(t, ev.VLLMMetrics)
					return
				}
			}
		case <-deadline:
			t.Fatal("timed out waiting for metrics update")
		}
	}
}

func TestIngester_AppConfigUpdate(t *testing.T) {
	agg := mocks.NewMockClusterAggregator(t)
	cs := mocks.NewMockConfigStore(t)

	eventCh := make(chan model.ClusterEvent)
	appCh := make(chan model.Application, 1)

	cs.EXPECT().ListApplications(mock.Anything).Return(nil, nil)
	agg.EXPECT().WatchEvents(mock.Anything).Return(eventCh, nil)
	cs.EXPECT().WatchApplications(mock.Anything).Return(appCh, nil)
	// After the new app is registered, metrics poll may call GetVLLMMetrics for it.
	agg.EXPECT().GetVLLMMetrics(mock.Anything, mock.Anything).Return(
		model.VLLMMetrics{}, nil,
	).Maybe()

	ing := newTestIngester(t, agg, cs)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	outCh, err := ing.Start(ctx)
	require.NoError(t, err)

	appCh <- model.Application{AppID: "new-app", ModelID: "model-2", Priority: 1}

	deadline := time.After(2 * time.Second)
	for {
		select {
		case batch, ok := <-outCh:
			if !ok {
				t.Fatal("output closed before finding app config event")
			}
			for _, ev := range batch {
				if ev.Kind == EventAppConfigUpdate && ev.AppID == "new-app" {
					assert.Equal(t, "model-2", ev.Application.ModelID)
					// Verify app was added to registry.
					ing.mu.RLock()
					_, exists := ing.appRegistry["new-app"]
					ing.mu.RUnlock()
					assert.True(t, exists)
					return
				}
			}
		case <-deadline:
			t.Fatal("timed out waiting for app config update")
		}
	}
}

func TestIngester_NewAppAppearsInMetricsPolling(t *testing.T) {
	agg := mocks.NewMockClusterAggregator(t)
	cs := mocks.NewMockConfigStore(t)

	eventCh := make(chan model.ClusterEvent)
	appCh := make(chan model.Application, 1)

	cs.EXPECT().ListApplications(mock.Anything).Return(nil, nil)
	agg.EXPECT().WatchEvents(mock.Anything).Return(eventCh, nil)
	cs.EXPECT().WatchApplications(mock.Anything).Return(appCh, nil)
	agg.EXPECT().GetVLLMMetrics(mock.Anything, "dynamic-app").Return(
		model.VLLMMetrics{AppID: "dynamic-app", TotalTokensPerSecond: 50}, nil,
	).Maybe()

	ing := newTestIngester(t, agg, cs)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	outCh, err := ing.Start(ctx)
	require.NoError(t, err)

	// Add app dynamically via config watch.
	appCh <- model.Application{AppID: "dynamic-app", ModelID: "model-3"}

	// Wait for a MetricsUpdate for the dynamically added app.
	deadline := time.After(2 * time.Second)
	for {
		select {
		case batch, ok := <-outCh:
			if !ok {
				t.Fatal("output closed")
			}
			for _, ev := range batch {
				if ev.Kind == EventMetricsUpdate && ev.AppID == "dynamic-app" {
					return
				}
			}
		case <-deadline:
			t.Fatal("timed out waiting for metrics poll of dynamically added app")
		}
	}
}

func TestIngester_MetricsPollError_ContinuesOtherApps(t *testing.T) {
	agg := mocks.NewMockClusterAggregator(t)
	cs := mocks.NewMockConfigStore(t)

	eventCh := make(chan model.ClusterEvent)
	appCh := make(chan model.Application)

	apps := []model.Application{
		{AppID: "app-ok", ModelID: "m1"},
		{AppID: "app-fail", ModelID: "m2"},
	}
	cs.EXPECT().ListApplications(mock.Anything).Return(apps, nil)
	agg.EXPECT().WatchEvents(mock.Anything).Return(eventCh, nil)
	cs.EXPECT().WatchApplications(mock.Anything).Return(appCh, nil)

	agg.EXPECT().GetVLLMMetrics(mock.Anything, "app-fail").Return(
		model.VLLMMetrics{}, errors.New("connection refused"),
	).Maybe()
	agg.EXPECT().GetVLLMMetrics(mock.Anything, "app-ok").Return(
		model.VLLMMetrics{AppID: "app-ok", TotalTokensPerSecond: 42}, nil,
	).Maybe()

	ing := newTestIngester(t, agg, cs)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	outCh, err := ing.Start(ctx)
	require.NoError(t, err)

	deadline := time.After(2 * time.Second)
	for {
		select {
		case batch, ok := <-outCh:
			if !ok {
				t.Fatal("output closed")
			}
			for _, ev := range batch {
				if ev.Kind == EventMetricsUpdate && ev.AppID == "app-ok" {
					return
				}
			}
		case <-deadline:
			t.Fatal("timed out: app-ok metrics never received despite app-fail error")
		}
	}
}

func TestIngester_GracefulShutdown(t *testing.T) {
	agg := mocks.NewMockClusterAggregator(t)
	cs := mocks.NewMockConfigStore(t)

	eventCh := make(chan model.ClusterEvent)
	appCh := make(chan model.Application)

	cs.EXPECT().ListApplications(mock.Anything).Return(nil, nil)
	agg.EXPECT().WatchEvents(mock.Anything).Return(eventCh, nil)
	cs.EXPECT().WatchApplications(mock.Anything).Return(appCh, nil)

	ing := newTestIngester(t, agg, cs)
	ctx, cancel := context.WithCancel(context.Background())

	outCh, err := ing.Start(ctx)
	require.NoError(t, err)

	cancel()

	// Output channel should eventually close.
	select {
	case _, ok := <-outCh:
		if ok {
			// May get a flush batch — drain remaining.
			for range outCh {
			}
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for output channel to close after cancel")
	}
}

func TestIngester_StartError_WatchEventsFails(t *testing.T) {
	agg := mocks.NewMockClusterAggregator(t)
	cs := mocks.NewMockConfigStore(t)

	cs.EXPECT().ListApplications(mock.Anything).Return(nil, nil)
	agg.EXPECT().WatchEvents(mock.Anything).Return(nil, errors.New("watch failed"))

	ing := newTestIngester(t, agg, cs)
	ctx := context.Background()

	outCh, err := ing.Start(ctx)
	assert.Nil(t, outCh)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "watch events")
}

func TestIngester_StartError_WatchApplicationsFails(t *testing.T) {
	agg := mocks.NewMockClusterAggregator(t)
	cs := mocks.NewMockConfigStore(t)

	eventCh := make(chan model.ClusterEvent)

	cs.EXPECT().ListApplications(mock.Anything).Return(nil, nil)
	agg.EXPECT().WatchEvents(mock.Anything).Return(eventCh, nil)
	cs.EXPECT().WatchApplications(mock.Anything).Return(nil, errors.New("watch apps failed"))

	ing := newTestIngester(t, agg, cs)
	ctx := context.Background()

	outCh, err := ing.Start(ctx)
	assert.Nil(t, outCh)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "watch applications")
}
