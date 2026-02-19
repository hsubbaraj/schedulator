package configstore

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/trace/noop"

	"github.com/hsubbaraj/schedulator/pkg/model"
)

const testConfig = `applications:
  - app_id: llama-70b
    model_id: meta-llama-70b
    gpus_per_replica: 4
    priority: 0
    min_replicas: 1
    failure_domain_rule: spread_clusters
    sla:
      max_p99_ttft_ms: 100
      max_p99_tps: 50
    metrics:
      avg_waiting_queue_depth: 5.0
  - app_id: codegen-7b
    model_id: codegen-mono-7b
    gpus_per_replica: 1
    priority: 2
    min_replicas: 0
`

func writeConfig(t *testing.T, dir, content string) string {
	t.Helper()
	path := filepath.Join(dir, "apps.yaml")
	require.NoError(t, os.WriteFile(path, []byte(content), 0644))
	return path
}

func TestListApplications(t *testing.T) {
	dir := t.TempDir()
	path := writeConfig(t, dir, testConfig)

	tracer := noop.NewTracerProvider().Tracer("test")
	store := NewYAMLStore(path, tracer)

	apps, err := store.ListApplications(context.Background())
	require.NoError(t, err)
	require.Len(t, apps, 2)

	assert.Equal(t, "llama-70b", apps[0].AppID)
	assert.Equal(t, "meta-llama-70b", apps[0].ModelID)
	assert.Equal(t, 4, apps[0].GPUsPerReplica)
	assert.Equal(t, 0, apps[0].Priority)
	assert.Equal(t, 1, apps[0].MinReplicas)
	assert.Equal(t, model.FailureDomainSpreadClusters, apps[0].FailureDomainRule)
	assert.Equal(t, 100, apps[0].SLA.MaxP99TTFTMs)
	assert.Equal(t, 50, apps[0].SLA.MaxP99TPS)

	assert.Equal(t, "codegen-7b", apps[1].AppID)
	assert.Equal(t, 1, apps[1].GPUsPerReplica)
	assert.Equal(t, 2, apps[1].Priority)
	assert.Equal(t, 0, apps[1].MinReplicas)
	assert.Equal(t, model.FailureDomainNone, apps[1].FailureDomainRule)
}

func TestListApplications_FileNotFound(t *testing.T) {
	tracer := noop.NewTracerProvider().Tracer("test")
	store := NewYAMLStore("/nonexistent/path", tracer)

	_, err := store.ListApplications(context.Background())
	require.Error(t, err)
}

func TestListApplications_InvalidYAML(t *testing.T) {
	dir := t.TempDir()
	path := writeConfig(t, dir, "{{invalid yaml")

	tracer := noop.NewTracerProvider().Tracer("test")
	store := NewYAMLStore(path, tracer)

	_, err := store.ListApplications(context.Background())
	require.Error(t, err)
}

func TestListApplications_EmptyFile(t *testing.T) {
	dir := t.TempDir()
	path := writeConfig(t, dir, "applications: []")

	tracer := noop.NewTracerProvider().Tracer("test")
	store := NewYAMLStore(path, tracer)

	apps, err := store.ListApplications(context.Background())
	require.NoError(t, err)
	assert.Empty(t, apps)
}

func TestWatchApplications_DetectsChange(t *testing.T) {
	dir := t.TempDir()
	path := writeConfig(t, dir, testConfig)

	tracer := noop.NewTracerProvider().Tracer("test")
	store := NewYAMLStore(path, tracer)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	ch, err := store.WatchApplications(ctx)
	require.NoError(t, err)

	// Give the watcher time to start.
	time.Sleep(200 * time.Millisecond)

	// Write updated config.
	updatedConfig := `applications:
  - app_id: new-app
    model_id: new-model
    gpus_per_replica: 2
    priority: 1
    min_replicas: 1
`
	require.NoError(t, os.WriteFile(path, []byte(updatedConfig), 0644))

	// Should receive the updated app within debounce window + buffer.
	select {
	case app := <-ch:
		assert.Equal(t, "new-app", app.AppID)
		assert.Equal(t, "new-model", app.ModelID)
		assert.Equal(t, 2, app.GPUsPerReplica)
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for config change event")
	}
}

func TestWatchMetrics_DetectsChange(t *testing.T) {
	dir := t.TempDir()
	path := writeConfig(t, dir, testConfig)

	tracer := noop.NewTracerProvider().Tracer("test")
	store := NewYAMLStore(path, tracer)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	ch, err := store.WatchMetrics(ctx)
	require.NoError(t, err)

	// Give the watcher time to start and consume initial metrics if any.
	time.Sleep(200 * time.Millisecond)

	// Write updated config.
	updatedConfig := `applications:
  - app_id: llama-70b
    metrics:
      avg_waiting_queue_depth: 25.0
`
	require.NoError(t, os.WriteFile(path, []byte(updatedConfig), 0644))

	// Should receive the updated metrics.
	deadline := time.After(3 * time.Second)
	for {
		select {
		case m := <-ch:
			if m.AppID == "llama-70b" && m.AvgWaitingQueueDepth == 25.0 {
				return
			}
		case <-deadline:
			t.Fatal("timed out waiting for metrics change event")
		}
	}
}

func TestWatchApplications_ContextCancellation(t *testing.T) {
	dir := t.TempDir()
	path := writeConfig(t, dir, testConfig)

	tracer := noop.NewTracerProvider().Tracer("test")
	store := NewYAMLStore(path, tracer)

	ctx, cancel := context.WithCancel(context.Background())
	ch, err := store.WatchApplications(ctx)
	require.NoError(t, err)

	cancel()

	// Channel should eventually close.
	select {
	case _, ok := <-ch:
		if ok {
			// Got an event before close, that's fine. Drain.
			for range ch {
			}
		}
	case <-time.After(3 * time.Second):
		t.Fatal("channel not closed after context cancellation")
	}
}
