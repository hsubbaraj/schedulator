package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/yourorg/scheduler-simulator/pkg/scheduler/types"
	"github.com/yourorg/scheduler-simulator/pkg/simulator/enforcer"
	"github.com/yourorg/scheduler-simulator/pkg/simulator/state"
)

type Server struct {
	stateManager *state.Manager
	enforcer     *enforcer.Enforcer
	httpServer   *http.Server
}

func NewServer(port int, sm *state.Manager, e *enforcer.Enforcer) *Server {
	s := &Server{
		stateManager: sm,
		enforcer:     e,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/v1/state", s.handleGetState)
	mux.HandleFunc("/v1/placement", s.handlePostPlacement)
	mux.HandleFunc("/v1/metrics", s.handleGetMetrics)
	mux.HandleFunc("/v1/workloads", s.handlePostWorkload)

	addr := fmt.Sprintf(":%d", port)
	s.httpServer = &http.Server{
		Addr:    addr,
		Handler: mux,
	}

	return s
}

func (s *Server) Start() error {
	log.Printf("🚀 Simulator API listening on %s", s.httpServer.Addr)
	if err := s.httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}

func (s *Server) Shutdown(ctx context.Context) error {
	return s.httpServer.Shutdown(ctx)
}

func (s *Server) handleGetState(w http.ResponseWriter, r *http.Request) {
	state := s.stateManager.GetState()

	// Handle If-None-Match for efficiency
	if match := r.Header.Get("If-None-Match"); match != "" {
		if match == fmt.Sprintf("\"%s\"", state.Version) {
			w.WriteHeader(http.StatusNotModified)
			return
		}
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("ETag", fmt.Sprintf("\"%s\"", state.Version))

	if err := json.NewEncoder(w).Encode(state); err != nil {
		log.Printf("Error encoding state: %v", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
	}
}

func (s *Server) handlePostPlacement(w http.ResponseWriter, r *http.Request) {
	var decision types.PlacementDecision
	if err := json.NewDecoder(r.Body).Decode(&decision); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	log.Printf("📥 Received decision: %+v", decision)

	result, err := s.enforcer.Enforce(r.Context(), &decision)
	if err != nil {
		log.Printf("Error enforcing decision: %v", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if result.Accepted {
		w.WriteHeader(http.StatusAccepted)
	} else {
		w.WriteHeader(http.StatusConflict) // Or 409 if version mismatch
	}
	json.NewEncoder(w).Encode(result)
}

func (s *Server) handleGetMetrics(w http.ResponseWriter, r *http.Request) {
	state := s.stateManager.GetState()
	
	// Flatten metrics from all clusters
	metrics := map[string]interface{}{
		"timestamp": time.Now(),
		"clusters":  map[string]interface{}{},
	}
	
	for _, c := range state.Clusters {
		clusterMetrics := c.Summary
		metrics["clusters"].(map[string]interface{})[c.ID] = clusterMetrics
	}
	
	w.Header().Set("Content-Type", "application/json")
	
	json.NewEncoder(w).Encode(metrics)
	
}
	

	
func (s *Server) handlePostWorkload(w http.ResponseWriter, r *http.Request) {
	
	if r.Method != http.MethodPost {
	
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	
		return
	
	}
	

	
	var workload state.Workload
	
	if err := json.NewDecoder(r.Body).Decode(&workload); err != nil {
	
		http.Error(w, err.Error(), http.StatusBadRequest)
	
		return
	
	}
	

	
	log.Printf("📥 Received workload: %+v", workload)
	
	s.stateManager.AddPendingWorkload(workload)
	

	
	result := map[string]interface{}{
	
		"queued":     true,
	
		"workloadId": workload.WorkloadID,
	
		"message":    "Workload added to pending queue",
	
	}
	

	
	w.Header().Set("Content-Type", "application/json")
	
	w.WriteHeader(http.StatusAccepted)
	
	json.NewEncoder(w).Encode(result)
	
}
	

