package apiserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/trace/noop"

	"github.com/hsubbaraj/schedulator/internal/engine/scaling"
	"github.com/hsubbaraj/schedulator/internal/eventlog"
	"github.com/hsubbaraj/schedulator/internal/worldstate"
	"github.com/hsubbaraj/schedulator/pkg/model"
)

func newTestServer(t *testing.T) *Server {
	t.Helper()
	tracer := noop.NewTracerProvider().Tracer("test")
	reg := prometheus.NewRegistry()
	ws := worldstate.New(tracer, reg)
	el, err := eventlog.NewStore(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { el.Close() })

	return New(ws, nil, el, tracer)
}

func TestHandleState(t *testing.T) {
	srv := newTestServer(t)
	handler := srv.Handler()

	req := httptest.NewRequest(http.MethodGet, "/api/v1/state", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))

	var body map[string]interface{}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Contains(t, body, "Clusters")
}

func TestHandleClusters(t *testing.T) {
	srv := newTestServer(t)
	handler := srv.Handler()

	req := httptest.NewRequest(http.MethodGet, "/api/v1/clusters", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestHandleApplications_Empty(t *testing.T) {
	srv := newTestServer(t)
	handler := srv.Handler()

	req := httptest.NewRequest(http.MethodGet, "/api/v1/applications", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestHandleApplications_WithData(t *testing.T) {
	tracer := noop.NewTracerProvider().Tracer("test")
	reg := prometheus.NewRegistry()
	ws := worldstate.New(tracer, reg)
	el, err := eventlog.NewStore(":memory:")
	require.NoError(t, err)
	defer el.Close()

	ws.UpsertApplication(model.Application{
		AppID:          "app-x",
		ModelID:        "model-1",
		GPUsPerReplica: 4,
		Priority:       0,
		MinReplicas:    1,
	})

	srv := New(ws, nil, el, tracer)
	handler := srv.Handler()

	req := httptest.NewRequest(http.MethodGet, "/api/v1/applications", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)

	var body map[string]interface{}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Contains(t, body, "app-x")
}

func TestHandleEventHistory(t *testing.T) {
	srv := newTestServer(t)

	require.NoError(t, srv.eventLog.LogEvent(context.Background(), eventlog.EventRecord{
		Timestamp: time.Now(),
		Type:      "scale_up",
		AppID:     "app-x",
		Summary:   "Scaled up",
	}))

	handler := srv.Handler()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/events/history?limit=10", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)

	var events []eventlog.EventRecord
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &events))
	assert.Len(t, events, 1)
	assert.Equal(t, "scale_up", events[0].Type)
}

func TestHandleSnapshots_RequiresClusterID(t *testing.T) {
	srv := newTestServer(t)
	handler := srv.Handler()

	req := httptest.NewRequest(http.MethodGet, "/api/v1/snapshots", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestHandleSnapshots_WithData(t *testing.T) {
	srv := newTestServer(t)

	require.NoError(t, srv.eventLog.LogStateSnapshot(context.Background(), []eventlog.ClusterSnapshot{
		{
			Timestamp:     time.Now(),
			ClusterID:     "c1",
			TotalGPUs:     16,
			AllocatedGPUs: 8,
			FreeGPUs:      8,
			ReplicaCount:  4,
		},
	}))

	handler := srv.Handler()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/snapshots?cluster_id=c1", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)

	var snaps []eventlog.ClusterSnapshot
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &snaps))
	assert.Len(t, snaps, 1)
}

func TestCORS(t *testing.T) {
	srv := newTestServer(t)
	handler := srv.Handler()

	req := httptest.NewRequest(http.MethodOptions, "/api/v1/state", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusNoContent, rec.Code)
	assert.Equal(t, "*", rec.Header().Get("Access-Control-Allow-Origin"))
}

func TestHandleScalingConfig(t *testing.T) {
	srv := newTestServer(t)
	srv.SetScalingConfig(scaling.DefaultScalingConfig())

	handler := srv.Handler()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/scaling-config", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))

	var body scalingConfigResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, 10.0, body.QueueHighWatermark)
	assert.Equal(t, 0.85, body.KVCacheHighWatermark)
	assert.Equal(t, 1, body.ScaleUpCooldownSeconds)
	assert.Equal(t, 3, body.ScaleDownCooldownSeconds)
	assert.Equal(t, 3, body.StabilizationCycles)
}

func TestHandleScalingConfig_NotSet(t *testing.T) {
	srv := newTestServer(t)

	handler := srv.Handler()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/scaling-config", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
}

func TestEventBus_PublishSubscribe(t *testing.T) {
	bus := NewEventBus()

	ch, unsub := bus.Subscribe()
	defer unsub()

	event := Event{Type: "test", Timestamp: time.Now(), Data: "hello"}
	bus.Publish(event)

	select {
	case received := <-ch:
		assert.Equal(t, "test", received.Type)
		assert.Equal(t, "hello", received.Data)
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for event")
	}
}

func TestEventBus_MultipleSubscribers(t *testing.T) {
	bus := NewEventBus()

	ch1, unsub1 := bus.Subscribe()
	defer unsub1()
	ch2, unsub2 := bus.Subscribe()
	defer unsub2()

	bus.Publish(Event{Type: "test"})

	select {
	case <-ch1:
	case <-time.After(time.Second):
		t.Fatal("subscriber 1 timeout")
	}
	select {
	case <-ch2:
	case <-time.After(time.Second):
		t.Fatal("subscriber 2 timeout")
	}
}

func TestEventBus_Unsubscribe(t *testing.T) {
	bus := NewEventBus()

	_, unsub := bus.Subscribe()
	unsub()

	// Should not panic.
	bus.Publish(Event{Type: "test"})
}
