package core

import (
	"context"
	"fmt"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"go.opentelemetry.io/otel/trace"

	"github.com/hsubbaraj/schedulator/internal/engine/placement"
	"github.com/hsubbaraj/schedulator/internal/engine/preemption"
	"github.com/hsubbaraj/schedulator/internal/engine/scaling"
	"github.com/hsubbaraj/schedulator/internal/worldstate"
	"github.com/hsubbaraj/schedulator/pkg/model"
	"github.com/hsubbaraj/schedulator/test/simulator/config"
)

type EventType string

const (
	EventMarkRunning EventType = "MARK_RUNNING"
	EventClusterDown EventType = "CLUSTER_DOWN"
)

type SimulationEvent struct {
	Time      time.Time
	Type      EventType
	ReplicaID string
	AppID     string
	TargetID  string
}

type Simulator struct {
	cfg          config.Scenario
	ws           *worldstate.WorldState
	scaling      *scaling.ScalingEngine
	placement    *placement.PlacementEngine
	preemption   *preemption.PreemptionEngine
	traffic      *TrafficGenerator
	metrics      *MetricsCollector
	currentTime  time.Time
	startTime    time.Time
	endTime      time.Time
	tickInterval time.Duration
	tracer       trace.Tracer
	eventQueue   []SimulationEvent
}

func NewSimulator(
	cfg config.Scenario,
	ws *worldstate.WorldState,
	traffic *TrafficGenerator,
	metrics *MetricsCollector,
	tracer trace.Tracer,
) (*Simulator, error) {
	duration, err := time.ParseDuration(cfg.Duration)
	if err != nil {
		return nil, fmt.Errorf("invalid duration: %w", err)
	}
	tickInterval, err := time.ParseDuration(cfg.TickInterval)
	if err != nil {
		return nil, fmt.Errorf("invalid tick_interval: %w", err)
	}

	scalingCfg := scaling.ScalingConfig{
		QueueHighWatermark:   10.0,
		QueueLowWatermark:    2.0,
		KVCacheHighWatermark: 0.8,
		KVCacheLowWatermark:  0.4,
		QueueTarget:          5.0,
		KVCacheTarget:        0.6,
		MaxScaleUpPerCycle:   5,
		MaxScaleDownPerCycle: 2,
		ScaleUpCooldown:      time.Second * 5,
		ScaleDownCooldown:    time.Second * 10,
		StabilizationCycles:  1,
	}

	placementCfg := placement.DefaultPlacementConfig()
	preemptionCfg := preemption.DefaultPreemptionConfig()

	reg := prometheus.NewRegistry()
	preemptionEngine := preemption.NewPreemptionEngine(preemptionCfg, tracer, reg)

	sim := &Simulator{
		cfg:          cfg,
		ws:           ws,
		scaling:      scaling.NewScalingEngine(scalingCfg, tracer, reg),
		placement:    placement.NewPlacementEngine(placementCfg, preemptionEngine, tracer, reg),
		preemption:   preemptionEngine,
		traffic:      traffic,
		metrics:      metrics,
		currentTime:  time.Date(2026, 2, 20, 0, 0, 0, 0, time.UTC),
		startTime:    time.Date(2026, 2, 20, 0, 0, 0, 0, time.UTC),
		endTime:      time.Date(2026, 2, 20, 0, 0, 0, 0, time.UTC).Add(duration),
		tickInterval: tickInterval,
		tracer:       tracer,
		eventQueue:   make([]SimulationEvent, 0),
	}

	sim.initializeTopology()
	sim.scheduleFailures()
	return sim, nil
}

func (s *Simulator) initializeTopology() {
	// Initialize Clusters
	for _, c := range s.cfg.Topology.Clusters {
		cluster := model.Cluster{
			ClusterID: c.ID,
			Nodes:     make(map[string]model.Node),
		}
		for i := 0; i < c.Nodes; i++ {
			nodeID := fmt.Sprintf("%s-node-%d", c.ID, i)
			cluster.Nodes[nodeID] = model.Node{
				NodeID:    nodeID,
				ClusterID: c.ID,
				TotalGPUs: c.GPUsPerNode,
				FreeGPUs:  c.GPUsPerNode,
				Status:    model.NodeStatusReady,
			}
		}
		s.ws.UpsertCluster(cluster)
	}

	// Initialize Apps
	for _, appCfg := range s.cfg.Apps {
		slaTTFT, _ := time.ParseDuration(appCfg.SLATTFT)
		app := model.Application{
			AppID:          appCfg.ID,
			GPUsPerReplica: appCfg.GPUsPerReplica,
			Priority:       appCfg.Priority,
			MinReplicas:    appCfg.MinReplicas,
			SLA: model.SLA{
				AppID:        appCfg.ID,
				MaxP99TTFTMs: int(slaTTFT.Milliseconds()),
			},
		}
		if app.GPUsPerReplica == 0 {
			app.GPUsPerReplica = 1
		}
		s.ws.UpsertApplication(app)
	}
}

func (s *Simulator) scheduleFailures() {
	for _, f := range s.cfg.Failures {
		d, err := time.ParseDuration(f.Time)
		if err != nil {
			continue
		}
		var evtType EventType
		switch f.Type {
		case "cluster_down":
			evtType = EventClusterDown
		default:
			continue
		}
		s.eventQueue = append(s.eventQueue, SimulationEvent{
			Time:     s.startTime.Add(d),
			Type:     evtType,
			TargetID: f.TargetID,
		})
	}
}

func (s *Simulator) Run(ctx context.Context) error {
	for s.currentTime.Before(s.endTime) {
		if err := s.Tick(ctx); err != nil {
			return err
		}
		s.currentTime = s.currentTime.Add(s.tickInterval)
	}
	return nil
}

func (s *Simulator) Tick(ctx context.Context) error {
	// 0. Process events
	s.processEvents(ctx)

	// 0b. Expire stale reservations
	s.ws.ExpireStaleReservations(s.currentTime)

	// 1. Update load
	s.traffic.UpdateMetrics(s.ws, s.currentTime, s.startTime, s.cfg.ClosedLoop)

	// 2. Snapshot
	snap := s.ws.Snapshot(ctx)

	// 3. Scaling
	decisions := s.scaling.ComputeTargets(ctx, snap)

	// 4. Placement
	placementDecisions := s.placement.ComputePlacement(ctx, snap, decisions)

	// 5. Enact decisions
	s.applyDecisions(ctx, placementDecisions)

	// 6. Metrics
	return s.metrics.Collect(ctx, s.ws, s.currentTime, decisions)
}

func (s *Simulator) processEvents(ctx context.Context) {
	if len(s.eventQueue) == 0 {
		return
	}
	
	snap := s.ws.Snapshot(ctx)
	var remaining []SimulationEvent
	for _, e := range s.eventQueue {
		if !e.Time.After(s.currentTime) {
			switch e.Type {
			case EventMarkRunning:
				if r, ok := snap.Replicas[e.ReplicaID]; ok {
					r.Status = model.ReplicaStatusRunning
					s.ws.UpsertReplica(r)
				}
			case EventClusterDown:
				if cluster, ok := snap.Clusters[e.TargetID]; ok {
					for _, node := range cluster.Nodes {
						node.Status = model.NodeStatusDown
						s.ws.UpsertNode(node)
					}
				}
			}
		} else {
			remaining = append(remaining, e)
		}
	}
	s.eventQueue = remaining
}

func (s *Simulator) applyDecisions(ctx context.Context, decisions model.PlacementDecisions) {
	// 1. Process Preemptions
	for _, p := range decisions.Preemptions {
		s.ws.DeleteReplica(p.VictimReplicaID)
	}

	// 2. Create Reservations
	for _, r := range decisions.Reservations {
		s.ws.CreateReservation(r)
	}

	// 3. Process ScaleUps and fulfill reservations
	for i, su := range decisions.ScaleUps {
		snap := s.ws.Snapshot(ctx)
		app := snap.Applications[su.AppID]
		nodeID := s.findNodeWithCapacity(ctx, su.ClusterID, app.GPUsPerReplica)
		if nodeID == "" {
			continue
		}

		replicaID := fmt.Sprintf("%s-%d-%d", su.AppID, s.currentTime.UnixNano(), i)
		replica := model.Replica{
			ReplicaID: replicaID,
			AppID:     su.AppID,
			ClusterID: su.ClusterID,
			NodeID:    nodeID,
			GPUs:      app.GPUsPerReplica,
			Status:    model.ReplicaStatusPending,
			CreatedAt: s.currentTime,
		}
		s.ws.UpsertReplica(replica)

		if su.ReservationID != "" {
			s.ws.FulfillReservation(su.ReservationID)
		}

		coldStart := time.Second * 60 // Default
		for _, appCfg := range s.cfg.Apps {
			if appCfg.ID == string(su.AppID) {
				if d, err := time.ParseDuration(appCfg.ColdStart); err == nil {
					coldStart = d
				}
				break
			}
		}

		s.eventQueue = append(s.eventQueue, SimulationEvent{
			Time:      s.currentTime.Add(coldStart),
			Type:      EventMarkRunning,
			ReplicaID: replicaID,
			AppID:     string(su.AppID),
		})
	}

	for _, sd := range decisions.ScaleDowns {
		s.ws.DeleteReplica(sd.ReplicaID)
	}
}

func (s *Simulator) findNodeWithCapacity(ctx context.Context, clusterID string, gpus int) string {
	snap := s.ws.Snapshot(ctx)
	cluster, ok := snap.Clusters[clusterID]
	if !ok {
		return ""
	}

	for _, node := range cluster.Nodes {
		if node.Status == model.NodeStatusReady && node.FreeGPUs >= gpus {
			return node.NodeID
		}
	}
	return ""
}
