package apiserver

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"go.opentelemetry.io/otel/trace"

	"github.com/hsubbaraj/schedulator/internal/engine/scaling"
	"github.com/hsubbaraj/schedulator/internal/eventlog"
	"github.com/hsubbaraj/schedulator/internal/ingestion"
	"github.com/hsubbaraj/schedulator/internal/worldstate"
	"github.com/hsubbaraj/schedulator/pkg/model"
)

// scalingConfigResponse is the JSON-safe response for GET /api/v1/scaling-config.
// time.Duration fields are converted to seconds.
type scalingConfigResponse struct {
	QueueHighWatermark       float64 `json:"QueueHighWatermark"`
	QueueTarget              float64 `json:"QueueTarget"`
	QueueLowWatermark        float64 `json:"QueueLowWatermark"`
	KVCacheHighWatermark     float64 `json:"KVCacheHighWatermark"`
	KVCacheTarget            float64 `json:"KVCacheTarget"`
	KVCacheLowWatermark      float64 `json:"KVCacheLowWatermark"`
	BatchLowWatermark        float64 `json:"BatchLowWatermark"`
	MaxScaleDownPerCycle     int     `json:"MaxScaleDownPerCycle"`
	MaxScaleUpPerCycle       int     `json:"MaxScaleUpPerCycle"`
	ScaleUpCooldownSeconds   int     `json:"ScaleUpCooldownSeconds"`
	ScaleDownCooldownSeconds int     `json:"ScaleDownCooldownSeconds"`
	StabilizationCycles      int     `json:"StabilizationCycles"`
}

// Server serves the HTTP API and SSE event stream.
type Server struct {
	worldState *worldstate.WorldState
	ingester   *ingestion.Ingester
	eventBus   *EventBus
	eventLog   *eventlog.Store
	tracer     trace.Tracer
	staticDir  string
	scalingCfg *scalingConfigResponse
}

// New creates an API Server.
func New(ws *worldstate.WorldState, ing *ingestion.Ingester, el *eventlog.Store, tracer trace.Tracer) *Server {
	return &Server{
		worldState: ws,
		ingester:   ing,
		eventBus:   NewEventBus(),
		eventLog:   el,
		tracer:     tracer,
	}
}

// SetStaticDir sets the directory for serving the built React dashboard.
func (s *Server) SetStaticDir(dir string) {
	s.staticDir = dir
}

// SetScalingConfig stores the scaling config for the /api/v1/scaling-config endpoint.
func (s *Server) SetScalingConfig(cfg scaling.ScalingConfig) {
	s.scalingCfg = &scalingConfigResponse{
		QueueHighWatermark:       cfg.QueueHighWatermark,
		QueueTarget:              cfg.QueueTarget,
		QueueLowWatermark:        cfg.QueueLowWatermark,
		KVCacheHighWatermark:     cfg.KVCacheHighWatermark,
		KVCacheTarget:            cfg.KVCacheTarget,
		KVCacheLowWatermark:      cfg.KVCacheLowWatermark,
		BatchLowWatermark:        cfg.BatchLowWatermark,
		MaxScaleDownPerCycle:     cfg.MaxScaleDownPerCycle,
		MaxScaleUpPerCycle:       cfg.MaxScaleUpPerCycle,
		ScaleUpCooldownSeconds:   int(cfg.ScaleUpCooldown.Seconds()),
		ScaleDownCooldownSeconds: int(cfg.ScaleDownCooldown.Seconds()),
		StabilizationCycles:      cfg.StabilizationCycles,
	}
}

// EventBus returns the event bus for publishing events from the control loop.
func (s *Server) EventBus() *EventBus {
	return s.eventBus
}

// Handler returns the HTTP handler with all routes registered.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /api/v1/state", s.handleState)
	mux.HandleFunc("GET /api/v1/clusters", s.handleClusters)
	mux.HandleFunc("GET /api/v1/applications", s.handleApplications)
	mux.HandleFunc("GET /api/v1/events/stream", s.handleEventStream)
	mux.HandleFunc("GET /api/v1/events/history", s.handleEventHistory)
	mux.HandleFunc("GET /api/v1/snapshots", s.handleSnapshots)
	mux.HandleFunc("GET /api/v1/scaling-config", s.handleScalingConfig)
	mux.HandleFunc("GET /api/v1/control-loop/history", s.handleCycleHistory)

	mux.HandleFunc("POST /api/v1/inject/event", s.handleInjectEvent)
	mux.HandleFunc("POST /api/v1/inject/metrics", s.handleInjectMetrics)

	if s.staticDir != "" {
		mux.Handle("/", http.FileServer(http.Dir(s.staticDir)))
	}

	return corsMiddleware(mux)
}

func (s *Server) handleState(w http.ResponseWriter, r *http.Request) {
	_, span := s.tracer.Start(r.Context(), "apiserver.get_state")
	defer span.End()

	snap := s.worldState.Snapshot(r.Context())
	writeJSON(w, snap)
}

func (s *Server) handleClusters(w http.ResponseWriter, r *http.Request) {
	snap := s.worldState.Snapshot(r.Context())
	writeJSON(w, snap.Clusters)
}

func (s *Server) handleApplications(w http.ResponseWriter, r *http.Request) {
	snap := s.worldState.Snapshot(r.Context())

	type appResponse struct {
		App          interface{}            `json:"app"`
		ReplicaCount int                    `json:"replica_count"`
		Replicas     []interface{}          `json:"replicas"`
	}

	result := make(map[string]appResponse)
	for appID, app := range snap.Applications {
		var replicas []interface{}
		count := 0
		for _, r := range snap.Replicas {
			if r.AppID == appID {
				replicas = append(replicas, r)
				count++
			}
		}
		result[appID] = appResponse{
			App:          app,
			ReplicaCount: count,
			Replicas:     replicas,
		}
	}
	writeJSON(w, result)
}

func (s *Server) handleEventStream(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	ch, unsub := s.eventBus.Subscribe()
	defer unsub()

	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case event, ok := <-ch:
			if !ok {
				return
			}
			data, err := json.Marshal(event)
			if err != nil {
				slog.Error("marshal SSE event", "error", err)
				continue
			}
			fmt.Fprintf(w, "data: %s\n\n", data)
			flusher.Flush()
		}
	}
}

func (s *Server) handleEventHistory(w http.ResponseWriter, r *http.Request) {
	sinceStr := r.URL.Query().Get("since")
	limitStr := r.URL.Query().Get("limit")

	since := time.Now().Add(-time.Hour)
	if sinceStr != "" {
		if t, err := time.Parse(time.RFC3339, sinceStr); err == nil {
			since = t
		}
	}

	limit := 100
	if limitStr != "" {
		if n, err := strconv.Atoi(limitStr); err == nil && n > 0 {
			limit = n
		}
	}

	events, err := s.eventLog.QueryEvents(r.Context(), since, limit)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, events)
}

func (s *Server) handleSnapshots(w http.ResponseWriter, r *http.Request) {
	clusterID := r.URL.Query().Get("cluster_id")
	sinceStr := r.URL.Query().Get("since")

	if clusterID == "" {
		http.Error(w, "cluster_id is required", http.StatusBadRequest)
		return
	}

	since := time.Now().Add(-time.Hour)
	if sinceStr != "" {
		if t, err := time.Parse(time.RFC3339, sinceStr); err == nil {
			since = t
		}
	}

	snaps, err := s.eventLog.QuerySnapshots(r.Context(), clusterID, since)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, snaps)
}

func (s *Server) handleScalingConfig(w http.ResponseWriter, _ *http.Request) {
	if s.scalingCfg == nil {
		http.Error(w, "scaling config not set", http.StatusServiceUnavailable)
		return
	}
	writeJSON(w, s.scalingCfg)
}

func (s *Server) handleCycleHistory(w http.ResponseWriter, r *http.Request) {
	limitStr := r.URL.Query().Get("limit")
	limit := 50
	if limitStr != "" {
		if n, err := strconv.Atoi(limitStr); err == nil && n > 0 {
			limit = n
		}
	}

	// We use QueryEvents but filter for type="cycle_complete" in the DB if we had a dedicated query,
	// but for now QueryEvents is general. Let's add a more specific query to eventlog.Store.
	events, err := s.eventLog.QueryEventsByType(r.Context(), "cycle_complete", limit)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, events)
}

func (s *Server) handleInjectEvent(w http.ResponseWriter, r *http.Request) {
	var ev ingestion.Event
	if err := json.NewDecoder(r.Body).Decode(&ev); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if ev.Timestamp.IsZero() {
		ev.Timestamp = time.Now()
	}

	s.ingester.InjectEvent(r.Context(), ev)
	w.WriteHeader(http.StatusAccepted)
}

func (s *Server) handleInjectMetrics(w http.ResponseWriter, r *http.Request) {
	var m model.VLLMMetrics
	if err := json.NewDecoder(r.Body).Decode(&m); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if m.MeasuredAt.IsZero() {
		m.MeasuredAt = time.Now()
	}

	ev := ingestion.Event{
		Kind:        ingestion.EventMetricsUpdate,
		Timestamp:   m.MeasuredAt,
		AppID:       m.AppID,
		VLLMMetrics: &m,
	}

	s.ingester.InjectEvent(r.Context(), ev)
	w.WriteHeader(http.StatusAccepted)
}

func writeJSON(w http.ResponseWriter, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Error("write JSON response", "error", err)
	}
}

func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// PublishWorldStateSnapshot takes a snapshot and logs it, then publishes an SSE event.
func (s *Server) PublishWorldStateSnapshot(ctx context.Context) {
	snap := s.worldState.Snapshot(ctx)
	now := time.Now()

	var clusterSnaps []eventlog.ClusterSnapshot
	for clusterID, cluster := range snap.Clusters {
		totalGPUs := 0
		allocatedGPUs := 0
		freeGPUs := 0
		for _, node := range cluster.Nodes {
			totalGPUs += node.TotalGPUs
			allocatedGPUs += node.AllocatedGPUs
			freeGPUs += node.FreeGPUs
		}
		replicaCount := 0
		for _, r := range snap.Replicas {
			if r.ClusterID == clusterID {
				replicaCount++
			}
		}
		clusterSnaps = append(clusterSnaps, eventlog.ClusterSnapshot{
			Timestamp:     now,
			ClusterID:     clusterID,
			TotalGPUs:     totalGPUs,
			AllocatedGPUs: allocatedGPUs,
			FreeGPUs:      freeGPUs,
			ReplicaCount:  replicaCount,
		})
	}

	if len(clusterSnaps) > 0 {
		if err := s.eventLog.LogStateSnapshot(ctx, clusterSnaps); err != nil {
			slog.Error("log state snapshot", "error", err)
		}
	}

	s.eventBus.Publish(Event{
		Type:      "state_update",
		Timestamp: now,
		Data:      snap,
	})
}
