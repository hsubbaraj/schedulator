package preemption

import (
	"context"
	"fmt"
	"math"
	"sort"

	"github.com/prometheus/client_golang/prometheus"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/hsubbaraj/schedulator/internal/observability"
	"github.com/hsubbaraj/schedulator/internal/worldstate"
	"github.com/hsubbaraj/schedulator/pkg/model"
)

// PreemptionEngine implements the priority cascade preemption policy from the
// proposal. It satisfies the preemptionFinder interface defined in the
// placement engine.
type PreemptionEngine struct {
	cfg    PreemptionConfig
	tracer trace.Tracer

	eventsCounter             *prometheus.CounterVec
	victimsCounter            prometheus.Counter
	insufficientCapacityCount prometheus.Counter
}

// NewPreemptionEngine creates a PreemptionEngine with the given config, tracer,
// and Prometheus registerer.
func NewPreemptionEngine(
	cfg PreemptionConfig,
	tracer trace.Tracer,
	reg prometheus.Registerer,
) *PreemptionEngine {
	return &PreemptionEngine{
		cfg:    cfg,
		tracer: tracer,
		eventsCounter: observability.MustNewCounterVec(reg, prometheus.CounterOpts{
			Name: "schedulator_preemption_events_total",
			Help: "Total preemption events by requesting and victim priority.",
		}, []string{"requesting_priority", "victim_priority"}),
		victimsCounter: observability.MustNewCounter(reg, prometheus.CounterOpts{
			Name: "schedulator_preemption_victims_total",
			Help: "Total number of preemption victims selected.",
		}),
		insufficientCapacityCount: observability.MustNewCounter(reg, prometheus.CounterOpts{
			Name: "schedulator_preemption_insufficient_capacity_total",
			Help: "Total preemption attempts that failed due to insufficient preemptable capacity.",
		}),
	}
}

// victimCandidate holds a replica and its owning app for sorting and selection.
type victimCandidate struct {
	replica model.Replica
	app     model.Application
}

// FindPreemptionOpportunity implements the priority cascade preemption policy.
// It returns a PreemptionPlan if sufficient lower-priority replicas can be
// evicted, or nil if preemption is not possible.
func (e *PreemptionEngine) FindPreemptionOpportunity(
	ctx context.Context,
	app model.Application,
	snap worldstate.WorldStateSnapshot,
) (*model.PreemptionPlan, error) {
	_, span := e.tracer.Start(ctx, "preemption.find_opportunity",
		trace.WithAttributes(
			attribute.String("requesting_app.id", app.AppID),
			attribute.Int("requesting_app.priority", app.Priority),
		))
	defer span.End()

	// P2 cannot preempt anyone.
	if app.Priority == 2 {
		return nil, nil
	}

	gpusNeeded := app.GPUsPerReplica

	// Build candidate pools: only running/pending replicas of strictly lower
	// priority (higher Priority int) apps.
	var p2Candidates, p1Candidates []victimCandidate
	for _, r := range snap.Replicas {
		if r.Status != model.ReplicaStatusRunning && r.Status != model.ReplicaStatusPending {
			continue
		}
		victimApp, ok := snap.Applications[r.AppID]
		if !ok || r.AppID == app.AppID {
			continue
		}
		if victimApp.Priority <= app.Priority {
			continue
		}
		vc := victimCandidate{replica: r, app: victimApp}
		switch victimApp.Priority {
		case 2:
			p2Candidates = append(p2Candidates, vc)
		case 1:
			p1Candidates = append(p1Candidates, vc)
		}
	}

	// Sort each pool by preemption sort key.
	sortCandidates(p2Candidates, gpusNeeded, snap)
	sortCandidates(p1Candidates, gpusNeeded, snap)

	// Select victims from P2 first.
	victims, freed, targetCluster := e.selectVictims(p2Candidates, gpusNeeded, snap, "")

	// If P2 is insufficient and requester is P0, escalate to P1.
	if freed < gpusNeeded && app.Priority == 0 {
		remaining := gpusNeeded - freed
		p1Victims, p1Freed, tc := e.selectVictims(p1Candidates, remaining, snap, targetCluster)
		victims = append(victims, p1Victims...)
		freed += p1Freed
		if targetCluster == "" {
			targetCluster = tc
		}
	}

	// Insufficient capacity — cannot preempt enough.
	if freed < gpusNeeded {
		e.insufficientCapacityCount.Inc()
		span.SetAttributes(attribute.Bool("insufficient_capacity", true))
		return nil, nil
	}

	// Record metrics.
	reqPri := fmt.Sprintf("%d", app.Priority)
	for _, v := range victims {
		victimApp := snap.Applications[v.VictimAppID]
		e.eventsCounter.WithLabelValues(reqPri, fmt.Sprintf("%d", victimApp.Priority)).Inc()
	}
	e.victimsCounter.Add(float64(len(victims)))

	span.SetAttributes(
		attribute.Int("victims_count", len(victims)),
		attribute.Int("freed_gpus", freed),
		attribute.String("target_cluster", targetCluster),
	)

	return &model.PreemptionPlan{
		Victims:       victims,
		TargetCluster: targetCluster,
		Constraints: model.SchedulingConstraints{
			RequiredGPUs:       gpusNeeded,
			CacheAffinityModel: app.ModelID,
		},
	}, nil
}

// selectVictims picks victims from a sorted candidate list. The first victim
// pins the target cluster; subsequent victims must be from the same cluster.
// It respects min_replicas by tracking how many victims have already been
// selected per app.
func (e *PreemptionEngine) selectVictims(
	candidates []victimCandidate,
	requiredGPUs int,
	snap worldstate.WorldStateSnapshot,
	preferCluster model.ClusterID,
) ([]model.PreemptionDecision, int, model.ClusterID) {
	var victims []model.PreemptionDecision
	freed := 0
	targetCluster := preferCluster
	preemptingPerApp := make(map[model.AppID]int)

	for _, c := range candidates {
		if freed >= requiredGPUs {
			break
		}

		// Same-cluster constraint: skip if cluster doesn't match.
		if targetCluster != "" && c.replica.ClusterID != targetCluster {
			continue
		}

		// min_replicas protection.
		activeCount := countActiveReplicas(c.app.AppID, snap)
		if activeCount-preemptingPerApp[c.app.AppID] <= c.app.MinReplicas {
			continue
		}

		// First victim sets the target cluster.
		if targetCluster == "" {
			targetCluster = c.replica.ClusterID
		}

		victims = append(victims, model.PreemptionDecision{
			VictimReplicaID:    c.replica.ReplicaID,
			VictimAppID:        c.app.AppID,
			NodeID:             c.replica.NodeID,
			ClusterID:          c.replica.ClusterID,
			GracePeriodSeconds: e.cfg.DefaultGracePeriodSeconds,
			Reason:             fmt.Sprintf("preempted for higher-priority app %s (P%d)", "", c.app.Priority),
		})
		preemptingPerApp[c.app.AppID]++
		freed += c.replica.GPUs
	}
	return victims, freed, targetCluster
}

// sortCandidates sorts victim candidates by preemption preference:
// 1. Above min_replicas first (above_minimum = 0, at_minimum = 1)
// 2. Cluster with most freeable GPUs first (same-cluster grouping)
// 3. Higher packing benefit first (better GPU match)
// 4. ReplicaID for determinism
func sortCandidates(candidates []victimCandidate, gpusNeeded int, snap worldstate.WorldStateSnapshot) {
	// Pre-compute total freeable GPUs per cluster for cluster-preference sorting.
	clusterGPUs := make(map[model.ClusterID]int)
	for _, c := range candidates {
		clusterGPUs[c.replica.ClusterID] += c.replica.GPUs
	}

	sort.SliceStable(candidates, func(i, j int) bool {
		ci, cj := candidates[i], candidates[j]
		aboveI := aboveMinOrder(ci, snap)
		aboveJ := aboveMinOrder(cj, snap)
		if aboveI != aboveJ {
			return aboveI < aboveJ
		}
		// Prefer cluster with more freeable capacity.
		cgI := clusterGPUs[ci.replica.ClusterID]
		cgJ := clusterGPUs[cj.replica.ClusterID]
		if cgI != cgJ {
			return cgI > cgJ
		}
		// Group by cluster for same-cluster selection.
		if ci.replica.ClusterID != cj.replica.ClusterID {
			return ci.replica.ClusterID < cj.replica.ClusterID
		}
		pbI := packingBenefit(ci.replica.GPUs, gpusNeeded)
		pbJ := packingBenefit(cj.replica.GPUs, gpusNeeded)
		if pbI != pbJ {
			return pbI > pbJ
		}
		return ci.replica.ReplicaID < cj.replica.ReplicaID
	})
}

// aboveMinOrder returns 0 if the app has replicas above min_replicas (prefer
// these first), 1 if at or below min_replicas.
func aboveMinOrder(c victimCandidate, snap worldstate.WorldStateSnapshot) int {
	active := countActiveReplicas(c.app.AppID, snap)
	if active > c.app.MinReplicas {
		return 0
	}
	return 1
}

// packingBenefit computes how well the freed GPUs match what the requester
// needs. Perfect match returns 1.0.
func packingBenefit(replicaGPUs, gpusNeeded int) float64 {
	if replicaGPUs == gpusNeeded {
		return 1.0
	}
	return 1.0 / (1.0 + math.Abs(float64(replicaGPUs-gpusNeeded)))
}

// countActiveReplicas counts running or pending replicas for an app.
func countActiveReplicas(appID model.AppID, snap worldstate.WorldStateSnapshot) int {
	count := 0
	for _, r := range snap.Replicas {
		if r.AppID == appID && (r.Status == model.ReplicaStatusRunning || r.Status == model.ReplicaStatusPending) {
			count++
		}
	}
	return count
}
