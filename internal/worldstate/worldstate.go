package worldstate

import (
	"context"
	"fmt"
	"log"
	"maps"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/hsubbaraj/schedulator/internal/observability"
	"github.com/hsubbaraj/schedulator/pkg/model"
)

// WorldState holds the in-memory representation of global scheduler state.
// All mutations go through exported methods that acquire the write lock
// internally. Engines never hold the lock — they read from an immutable
// WorldStateSnapshot obtained via Snapshot().
type WorldState struct {
	mu                  sync.RWMutex
	clusters            map[model.ClusterID]model.Cluster
	applications        map[model.AppID]model.Application
	replicas            map[model.ReplicaID]model.Replica
	cacheLocations      map[model.ModelID][]model.CacheLocation
	performanceProfiles map[model.AppID]model.PerformanceProfile
	vllmMetrics         map[model.AppID]model.VLLMMetrics
	reservations        map[model.ReservationID]model.GPUReservation
	scalingHistory      map[model.AppID]model.ScalingHistory

	tracer            trace.Tracer
	clustersGauge     prometheus.Gauge
	replicasGaugeVec  *prometheus.GaugeVec
	snapshotHistogram prometheus.Histogram
	reservationsGauge prometheus.Gauge
}

// New creates a WorldState with initialized maps and registered Prometheus
// metrics. The tracer is used for the Snapshot span; metrics are registered
// against reg.
func New(tracer trace.Tracer, reg prometheus.Registerer) *WorldState {
	replicasGaugeVec := observability.MustNewGaugeVec(reg, prometheus.GaugeOpts{
		Name: "schedulator_worldstate_replicas_total",
		Help: "Number of replicas tracked in world state, by status.",
	}, []string{"status"})

	// Pre-initialize all status label values to 0.
	for _, s := range model.AllReplicaStatuses {
		replicasGaugeVec.WithLabelValues(string(s)).Set(0)
	}

	return &WorldState{
		clusters:            make(map[model.ClusterID]model.Cluster),
		applications:        make(map[model.AppID]model.Application),
		replicas:            make(map[model.ReplicaID]model.Replica),
		cacheLocations:      make(map[model.ModelID][]model.CacheLocation),
		performanceProfiles: make(map[model.AppID]model.PerformanceProfile),
		vllmMetrics:         make(map[model.AppID]model.VLLMMetrics),
		reservations:        make(map[model.ReservationID]model.GPUReservation),
		scalingHistory:      make(map[model.AppID]model.ScalingHistory),

		tracer: tracer,
		clustersGauge: observability.MustNewGauge(reg, prometheus.GaugeOpts{
			Name: "schedulator_worldstate_clusters_total",
			Help: "Number of clusters tracked in world state.",
		}),
		replicasGaugeVec: replicasGaugeVec,
		snapshotHistogram: observability.MustNewHistogram(reg, prometheus.HistogramOpts{
			Name: "schedulator_worldstate_snapshot_duration_seconds",
			Help: "Time to create a world state snapshot.",
		}),
		reservationsGauge: observability.MustNewGauge(reg, prometheus.GaugeOpts{
			Name: "schedulator_worldstate_reservations_active",
			Help: "Number of active GPU reservations.",
		}),
	}
}

// Snapshot returns an immutable deep copy of the current world state.
func (ws *WorldState) Snapshot(ctx context.Context) WorldStateSnapshot {
	_, span := ws.tracer.Start(ctx, "worldstate.snapshot")
	defer span.End()

	start := time.Now()

	ws.mu.RLock()
	snap := WorldStateSnapshot{
		Clusters:            copyClusters(ws.clusters),
		Applications:        maps.Clone(ws.applications),
		Replicas:            maps.Clone(ws.replicas),
		CacheLocations:      copyCacheLocations(ws.cacheLocations),
		PerformanceProfiles: maps.Clone(ws.performanceProfiles),
		VLLMMetrics:         maps.Clone(ws.vllmMetrics),
		Reservations:        maps.Clone(ws.reservations),
		ScalingHistory:      maps.Clone(ws.scalingHistory),
		TakenAt:             time.Now(),
	}
	ws.mu.RUnlock()

	duration := time.Since(start)
	ws.snapshotHistogram.Observe(duration.Seconds())

	span.SetAttributes(
		attribute.Int("cluster_count", len(snap.Clusters)),
		attribute.Int("replica_count", len(snap.Replicas)),
	)

	return snap
}

// UpsertCluster replaces the cluster entry. Nodes within the provided cluster
// are preserved as-is.
func (ws *WorldState) UpsertCluster(c model.Cluster) {
	ws.mu.Lock()
	defer ws.mu.Unlock()
	ws.clusters[c.ClusterID] = c
	ws.clustersGauge.Set(float64(len(ws.clusters)))
}

// UpsertNode stores a node inside its parent cluster. If the cluster does not
// exist, a minimal cluster entry is created. FragmentationScore is recomputed.
func (ws *WorldState) UpsertNode(n model.Node) {
	ws.mu.Lock()
	defer ws.mu.Unlock()

	c, ok := ws.clusters[n.ClusterID]
	if !ok {
		c = model.Cluster{
			ClusterID: n.ClusterID,
			Nodes:     make(map[model.NodeID]model.Node),
		}
	}
	if c.Nodes == nil {
		c.Nodes = make(map[model.NodeID]model.Node)
	}
	c.Nodes[n.NodeID] = n
	c.FragmentationScore = computeFragmentation(c.Nodes)
	ws.clusters[n.ClusterID] = c
	ws.clustersGauge.Set(float64(len(ws.clusters)))
}

// UpsertApplication stores an application in the applications map.
func (ws *WorldState) UpsertApplication(app model.Application) {
	ws.mu.Lock()
	defer ws.mu.Unlock()
	ws.applications[app.AppID] = app
}

// UpsertPerformanceProfile stores a performance profile.
func (ws *WorldState) UpsertPerformanceProfile(p model.PerformanceProfile) {
	ws.mu.Lock()
	defer ws.mu.Unlock()
	ws.performanceProfiles[p.AppID] = p
}

// UpsertReplica stores a replica and updates node GPU allocations. If the
// replica previously existed on a different node, the old node's GPUs are
// restored. If the target node is not found, GPU updates are skipped (the
// ingestion layer will send UpsertNode with correct values).
func (ws *WorldState) UpsertReplica(r model.Replica) {
	ws.mu.Lock()
	defer ws.mu.Unlock()

	old, existed := ws.replicas[r.ReplicaID]

	// Restore old node if replica is moving to a different node.
	if existed && (old.NodeID != r.NodeID || old.ClusterID != r.ClusterID) {
		ws.restoreNodeGPUs(old)
	}

	// Decrement replicas gauge for old status.
	if existed {
		ws.replicasGaugeVec.WithLabelValues(string(old.Status)).Dec()
	}

	// Update new node GPU allocations (only if node changed or new replica).
	if !existed || old.NodeID != r.NodeID || old.ClusterID != r.ClusterID {
		ws.allocateNodeGPUs(r)
	}

	ws.replicas[r.ReplicaID] = r
	ws.replicasGaugeVec.WithLabelValues(string(r.Status)).Inc()

	// Recompute fragmentation for affected clusters.
	if c, ok := ws.clusters[r.ClusterID]; ok {
		c.FragmentationScore = computeFragmentation(c.Nodes)
		ws.clusters[r.ClusterID] = c
	}
	if existed && old.ClusterID != r.ClusterID {
		if c, ok := ws.clusters[old.ClusterID]; ok {
			c.FragmentationScore = computeFragmentation(c.Nodes)
			ws.clusters[old.ClusterID] = c
		}
	}
}

// DeleteReplica removes a replica and restores its node's GPU allocation.
// No-op if the replica does not exist.
func (ws *WorldState) DeleteReplica(id model.ReplicaID) {
	ws.mu.Lock()
	defer ws.mu.Unlock()

	r, ok := ws.replicas[id]
	if !ok {
		return
	}

	ws.restoreNodeGPUs(r)
	delete(ws.replicas, id)
	ws.replicasGaugeVec.WithLabelValues(string(r.Status)).Dec()

	if c, ok := ws.clusters[r.ClusterID]; ok {
		c.FragmentationScore = computeFragmentation(c.Nodes)
		ws.clusters[r.ClusterID] = c
	}
}

// UpdateVLLMMetrics replaces the vLLM metrics for an application.
func (ws *WorldState) UpdateVLLMMetrics(m model.VLLMMetrics) {
	ws.mu.Lock()
	defer ws.mu.Unlock()
	ws.vllmMetrics[m.AppID] = m
}

// UpdateCacheLocations replaces all cache locations for a model.
func (ws *WorldState) UpdateCacheLocations(modelID model.ModelID, locs []model.CacheLocation) {
	ws.mu.Lock()
	defer ws.mu.Unlock()
	ws.cacheLocations[modelID] = locs
}

// CreateReservation inserts a new GPU reservation. Returns an error if a
// reservation with the same ID already exists.
func (ws *WorldState) CreateReservation(r model.GPUReservation) error {
	ws.mu.Lock()
	defer ws.mu.Unlock()

	if _, exists := ws.reservations[r.ReservationID]; exists {
		return fmt.Errorf("reservation %s already exists", r.ReservationID)
	}
	ws.reservations[r.ReservationID] = r
	if r.Status == model.ReservationStatusActive {
		ws.reservationsGauge.Inc()
	}
	return nil
}

// FulfillReservation transitions an active reservation to fulfilled. Returns
// an error if the reservation is not found or not active.
func (ws *WorldState) FulfillReservation(id model.ReservationID) error {
	ws.mu.Lock()
	defer ws.mu.Unlock()

	r, ok := ws.reservations[id]
	if !ok {
		return fmt.Errorf("reservation %s not found", id)
	}
	if r.Status != model.ReservationStatusActive {
		return fmt.Errorf("reservation %s is %s, not active", id, r.Status)
	}
	r.Status = model.ReservationStatusFulfilled
	ws.reservations[id] = r
	ws.reservationsGauge.Dec()
	return nil
}

// ExpireReservation transitions an active reservation to expired. Returns an
// error if the reservation is not found or not active.
func (ws *WorldState) ExpireReservation(id model.ReservationID) error {
	ws.mu.Lock()
	defer ws.mu.Unlock()

	r, ok := ws.reservations[id]
	if !ok {
		return fmt.Errorf("reservation %s not found", id)
	}
	if r.Status != model.ReservationStatusActive {
		return fmt.Errorf("reservation %s is %s, not active", id, r.Status)
	}
	r.Status = model.ReservationStatusExpired
	ws.reservations[id] = r
	ws.reservationsGauge.Dec()
	return nil
}

// RecordScaleEvent updates scaling history for an application. Direction must
// be "up" or "down". Scale-up resets ConsecutiveScaleDownSignals; scale-down
// increments it.
func (ws *WorldState) RecordScaleEvent(appID model.AppID, direction string, at time.Time) {
	ws.mu.Lock()
	defer ws.mu.Unlock()

	h := ws.scalingHistory[appID]
	h.AppID = appID
	switch direction {
	case "up":
		h.LastScaleUpAt = at
		h.ConsecutiveScaleDownSignals = 0
	case "down":
		h.LastScaleDownAt = at
		h.ConsecutiveScaleDownSignals++
	}
	ws.scalingHistory[appID] = h
}

// IncrementScaleDownSignal increments the consecutive scale-down signal
// counter for an application without updating the last-scale-down timestamp.
// This is used by the control loop when the scaling engine observes a
// scale-down signal that is suppressed by the stabilization window.
func (ws *WorldState) IncrementScaleDownSignal(appID model.AppID) {
	ws.mu.Lock()
	defer ws.mu.Unlock()
	h := ws.scalingHistory[appID]
	h.AppID = appID
	h.ConsecutiveScaleDownSignals++
	ws.scalingHistory[appID] = h
}

// ExpireStaleReservations scans all active reservations and expires those whose
// TTL has elapsed relative to now. Returns the number of expired reservations.
func (ws *WorldState) ExpireStaleReservations(now time.Time) int {
	ws.mu.Lock()
	defer ws.mu.Unlock()

	expired := 0
	for id, r := range ws.reservations {
		if r.Status != model.ReservationStatusActive {
			continue
		}
		if now.Sub(r.CreatedAt) >= time.Duration(r.TTLSeconds)*time.Second {
			r.Status = model.ReservationStatusExpired
			ws.reservations[id] = r
			ws.reservationsGauge.Dec()
			expired++
		}
	}
	return expired
}

// AvailableGPUs returns the number of available GPUs in a cluster: the sum of
// FreeGPUs across ready nodes minus GPUs held by active reservations, floored
// at 0.
func (ws *WorldState) AvailableGPUs(clusterID model.ClusterID) int {
	ws.mu.RLock()
	defer ws.mu.RUnlock()

	c, ok := ws.clusters[clusterID]
	if !ok {
		return 0
	}

	free := 0
	for _, n := range c.Nodes {
		if n.Status == model.NodeStatusReady {
			free += n.FreeGPUs
		}
	}

	reserved := 0
	for _, r := range ws.reservations {
		if r.ClusterID == clusterID && r.Status == model.ReservationStatusActive {
			reserved += r.GPUs
		}
	}

	result := free - reserved
	if result < 0 {
		return 0
	}
	return result
}

// HasContiguousGPUs returns true if any ready node in the cluster has at least
// needed contiguous free GPUs after subtracting node-scoped active reservations.
func (ws *WorldState) HasContiguousGPUs(clusterID model.ClusterID, needed int) bool {
	ws.mu.RLock()
	defer ws.mu.RUnlock()

	c, ok := ws.clusters[clusterID]
	if !ok {
		return false
	}

	// Build per-node reserved GPU count from node-scoped active reservations.
	nodeReserved := make(map[model.NodeID]int)
	for _, r := range ws.reservations {
		if r.ClusterID == clusterID && r.Status == model.ReservationStatusActive && r.NodeID != "" {
			nodeReserved[r.NodeID] += r.GPUs
		}
	}

	for _, n := range c.Nodes {
		if n.Status != model.NodeStatusReady {
			continue
		}
		available := n.FreeGPUs - nodeReserved[n.NodeID]
		if available >= needed {
			return true
		}
	}
	return false
}

// restoreNodeGPUs restores GPU allocation on a node when a replica is removed
// or moved. Must be called with write lock held.
func (ws *WorldState) restoreNodeGPUs(r model.Replica) {
	c, ok := ws.clusters[r.ClusterID]
	if !ok {
		log.Printf("worldstate: cannot restore GPUs — cluster %s not found for replica %s", r.ClusterID, r.ReplicaID)
		return
	}
	n, ok := c.Nodes[r.NodeID]
	if !ok {
		log.Printf("worldstate: cannot restore GPUs — node %s not found for replica %s", r.NodeID, r.ReplicaID)
		return
	}
	n.AllocatedGPUs -= r.GPUs
	n.FreeGPUs += r.GPUs
	n.Pods = removeFromSlice(n.Pods, r.ReplicaID)
	c.Nodes[r.NodeID] = n
	ws.clusters[r.ClusterID] = c
}

// allocateNodeGPUs decrements free GPUs and adds the replica to the node's Pods
// list. Must be called with write lock held.
func (ws *WorldState) allocateNodeGPUs(r model.Replica) {
	c, ok := ws.clusters[r.ClusterID]
	if !ok {
		log.Printf("worldstate: cannot allocate GPUs — cluster %s not found for replica %s", r.ClusterID, r.ReplicaID)
		return
	}
	n, ok := c.Nodes[r.NodeID]
	if !ok {
		log.Printf("worldstate: cannot allocate GPUs — node %s not found for replica %s", r.NodeID, r.ReplicaID)
		return
	}
	n.AllocatedGPUs += r.GPUs
	n.FreeGPUs -= r.GPUs
	n.Pods = append(n.Pods, r.ReplicaID)
	c.Nodes[r.NodeID] = n
	ws.clusters[r.ClusterID] = c
}

// computeFragmentation computes the fragmentation score for a set of nodes:
// 1.0 - totalFree/totalCapacity. Returns 0.0 if there are no nodes.
func computeFragmentation(nodes map[model.NodeID]model.Node) float64 {
	totalCapacity := 0
	totalFree := 0
	for _, n := range nodes {
		totalCapacity += n.TotalGPUs
		totalFree += n.FreeGPUs
	}
	if totalCapacity == 0 {
		return 0.0
	}
	return 1.0 - float64(totalFree)/float64(totalCapacity)
}

// removeFromSlice removes the first occurrence of val from s.
func removeFromSlice(s []model.ReplicaID, val model.ReplicaID) []model.ReplicaID {
	for i, v := range s {
		if v == val {
			return append(s[:i], s[i+1:]...)
		}
	}
	return s
}
