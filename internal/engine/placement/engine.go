package placement

import (
	"context"
	"log/slog"
	"math"
	"sort"
	"time"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/hsubbaraj/schedulator/internal/engine/scaling"
	"github.com/hsubbaraj/schedulator/internal/observability"
	"github.com/hsubbaraj/schedulator/internal/worldstate"
	"github.com/hsubbaraj/schedulator/pkg/model"
)

// preemptionFinder is the interface for the preemption engine. Defined locally
// because the preemption engine (Section 9) is not yet implemented.
type preemptionFinder interface {
	FindPreemptionOpportunity(
		ctx context.Context,
		app model.Application,
		snap worldstate.WorldStateSnapshot,
	) (*model.PreemptionPlan, error)
}

// PlacementEngine computes placement decisions for scaling targets. It is
// stateless — all state comes from the WorldStateSnapshot.
type PlacementEngine struct {
	cfg        PlacementConfig
	preemption preemptionFinder
	tracer     trace.Tracer

	scoreHistogram    *prometheus.HistogramVec
	scaleUpsCounter   *prometheus.CounterVec
	scaleDownsCounter prometheus.Counter
	noCapacityCounter *prometheus.CounterVec
	computeDuration   prometheus.Histogram
}

// NewPlacementEngine creates a PlacementEngine with the given config, optional
// preemption finder, tracer, and Prometheus registerer.
func NewPlacementEngine(
	cfg PlacementConfig,
	preemption preemptionFinder,
	tracer trace.Tracer,
	reg prometheus.Registerer,
) *PlacementEngine {
	return &PlacementEngine{
		cfg:        cfg,
		preemption: preemption,
		tracer:     tracer,
		scoreHistogram: observability.MustNewHistogramVec(reg, prometheus.HistogramOpts{
			Name: "schedulator_placement_score",
			Help: "Placement scores by cache tier.",
		}, []string{"cache_tier"}),
		scaleUpsCounter: observability.MustNewCounterVec(reg, prometheus.CounterOpts{
			Name: "schedulator_placement_scaleups_total",
			Help: "Total scale-up decisions by cluster.",
		}, []string{"cluster_id"}),
		scaleDownsCounter: observability.MustNewCounter(reg, prometheus.CounterOpts{
			Name: "schedulator_placement_scaledowns_total",
			Help: "Total scale-down decisions.",
		}),
		noCapacityCounter: observability.MustNewCounterVec(reg, prometheus.CounterOpts{
			Name: "schedulator_placement_no_capacity_total",
			Help: "Total placement failures due to no capacity.",
		}, []string{"app_id"}),
		computeDuration: observability.MustNewHistogram(reg, prometheus.HistogramOpts{
			Name: "schedulator_placement_compute_duration_seconds",
			Help: "Time to compute placement decisions.",
		}),
	}
}

// ComputePlacement takes a snapshot and scaling decisions and returns placement
// decisions including scale-ups, scale-downs, preemptions, and reservations.
func (e *PlacementEngine) ComputePlacement(
	ctx context.Context,
	snap worldstate.WorldStateSnapshot,
	scalingDecisions map[model.AppID]scaling.ScalingDecision,
) model.PlacementDecisions {
	ctx, span := e.tracer.Start(ctx, "placement.compute",
		trace.WithAttributes(attribute.Int("app_count", len(scalingDecisions))))
	defer span.End()

	start := time.Now()
	decisions := model.PlacementDecisions{}
	localReservations := make(map[model.ClusterID]int)
	// localAppPlacement tracks replicas we've decided to place in this cycle.
	localAppPlacement := make(map[model.AppID]map[model.ClusterID]int)

	// 1. Compute deltas for approved scaling actions.
	type appDelta struct {
		appID model.AppID
		delta int
	}
	var scaleDowns []appDelta
	var scaleUps []appDelta

	for appID, sd := range scalingDecisions {
		if !sd.ScaleActionApproved {
			continue
		}
		currentCount := countActiveReplicas(appID, snap)
		delta := sd.TargetCount - currentCount
		if delta < 0 {
			scaleDowns = append(scaleDowns, appDelta{appID: appID, delta: delta})
		} else if delta > 0 {
			scaleUps = append(scaleUps, appDelta{appID: appID, delta: delta})
		}
	}

	// 2. Process scale-downs first (free resources).
	for _, sd := range scaleDowns {
		victims := e.selectScaleDownVictims(sd.appID, -sd.delta, snap)
		for _, victimID := range victims {
			decisions.ScaleDowns = append(decisions.ScaleDowns, model.ScaleDownDecision{
				AppID:     sd.appID,
				ReplicaID: victimID,
			})
		}
		e.scaleDownsCounter.Add(float64(len(victims)))
	}

	// 3. Process scale-ups sorted by priority ASC (P0 first).
	sort.Slice(scaleUps, func(i, j int) bool {
		appI := snap.Applications[scaleUps[i].appID]
		appJ := snap.Applications[scaleUps[j].appID]
		return appI.Priority < appJ.Priority
	})

	for _, su := range scaleUps {
		app := snap.Applications[su.appID]
		if _, ok := localAppPlacement[su.appID]; !ok {
			localAppPlacement[su.appID] = make(map[model.ClusterID]int)
		}

		for i := 0; i < su.delta; i++ {
			clusterID, constraints, ok := e.findBestCluster(ctx, app, snap, localReservations, localAppPlacement[su.appID])

			if !ok {
				// No cluster can fit — try preemption.
				if e.preemption != nil {
					plan, err := e.preemption.FindPreemptionOpportunity(ctx, app, snap)
					if err == nil && plan != nil {
						decisions.Preemptions = append(decisions.Preemptions, plan.Victims...)
						clusterID = plan.TargetCluster
						constraints = plan.Constraints
						ok = true
					} else if err != nil {
						slog.WarnContext(ctx, "preemption failed", "app_id", su.appID, "error", err)
					} else {
						slog.WarnContext(ctx, "no preemption possible, all lower-priority replicas at min_replicas",
							"app_id", su.appID, "gpus_needed", app.GPUsPerReplica)
					}
				}
				if !ok {
					slog.WarnContext(ctx, "no capacity for scale-up", "app_id", su.appID, "gpus_needed", app.GPUsPerReplica)
					e.noCapacityCounter.WithLabelValues(su.appID).Inc()
					continue
				}
			}

			// Create GPU reservation.
			resID := uuid.New().String()
			ttl := e.computeReservationTTL(su.appID, snap)
			reservation := model.GPUReservation{
				ReservationID: resID,
				ClusterID:     clusterID,
				GPUs:          app.GPUsPerReplica,
				ForAppID:      su.appID,
				CreatedAt:     snap.TakenAt,
				TTLSeconds:    ttl,
				Status:        model.ReservationStatusActive,
			}
			decisions.Reservations = append(decisions.Reservations, reservation)

			// Track local reservations to prevent double-booking.
			localReservations[clusterID] += app.GPUsPerReplica
			localAppPlacement[su.appID][clusterID]++

			decisions.ScaleUps = append(decisions.ScaleUps, model.ScaleUpDecision{
				AppID:                 su.appID,
				ClusterID:             clusterID,
				ReservationID:         resID,
				SchedulingConstraints: constraints,
			})
			e.scaleUpsCounter.WithLabelValues(clusterID).Inc()
		}
	}

	e.computeDuration.Observe(time.Since(start).Seconds())
	return decisions
}

// findBestCluster selects the best cluster for placing a replica of the given
// app. Returns the cluster ID, scheduling constraints, and whether a suitable
// cluster was found. Implements proposal Section 4.4 cluster selection scoring.
func (e *PlacementEngine) findBestCluster(
	ctx context.Context,
	app model.Application,
	snap worldstate.WorldStateSnapshot,
	localReservations map[model.ClusterID]int,
	localAppPlacement map[model.ClusterID]int,
) (model.ClusterID, model.SchedulingConstraints, bool) {
	_, span := e.tracer.Start(ctx, "placement.find_best_cluster",
		trace.WithAttributes(attribute.String("app.id", app.AppID)))
	defer span.End()

	gpusNeeded := app.GPUsPerReplica

	type candidate struct {
		clusterID      model.ClusterID
		score          float64
		preferredNodes []model.NodeID
		cacheTier      string
	}

	var eligibleClusters []model.ClusterID
	// First pass: find eligible clusters for spread limit calculation.
	for clusterID, cluster := range snap.Clusters {
		if !hasFittingNode(cluster, gpusNeeded) {
			continue
		}
		available := clusterAvailableGPUs(clusterID, snap, localReservations)
		if available < gpusNeeded {
			continue
		}
		eligibleClusters = append(eligibleClusters, clusterID)
	}

	spreadLimit := 0
	if app.FailureDomainRule == model.FailureDomainSpreadClusters && len(eligibleClusters) > 0 {
		targetCount := 0
		for _, r := range snap.Replicas {
			if r.AppID == app.AppID && (r.Status == model.ReplicaStatusRunning || r.Status == model.ReplicaStatusPending) {
				targetCount++
			}
		}
		// Add currently planned replicas for this app in this cycle.
		for _, count := range localAppPlacement {
			targetCount += count
		}
		// Add 1 for the replica we're about to place.
		targetCount++
		spreadLimit = int(math.Ceil(float64(targetCount) / float64(len(eligibleClusters))))
	}

	var best *candidate
	for _, clusterID := range eligibleClusters {
		cluster := snap.Clusters[clusterID]

		// Spread limit check.
		if app.FailureDomainRule == model.FailureDomainSpreadClusters {
			replicasInCluster := countAppReplicasInCluster(app.AppID, clusterID, snap)
			// Include replicas already planned for this cluster in this cycle.
			replicasInCluster += localAppPlacement[clusterID]

			if replicasInCluster >= spreadLimit {
				continue
			}
		}

		// Soft scoring.
		cacheScore, preferredNodes, cacheTier := e.computeCacheScore(app.ModelID, clusterID, snap)
		packingScore := e.computePackingScore(cluster, gpusNeeded)

		// Balance score: 1 - utilization.
		totalGPUs, allocatedGPUs := clusterGPUTotals(cluster)
		balanceScore := 0.0
		if totalGPUs > 0 {
			balanceScore = 1.0 - float64(allocatedGPUs)/float64(totalGPUs)
		}

		totalScore := cacheScore +
			e.cfg.WeightFragmentation*packingScore +
			e.cfg.WeightBalance*balanceScore

		_, scoreSpan := e.tracer.Start(ctx, "placement.score_cluster",
			trace.WithAttributes(
				attribute.String("cluster_id", clusterID),
				attribute.Float64("cache_score", cacheScore),
				attribute.Float64("packing_score", packingScore),
				attribute.Float64("balance_score", balanceScore),
				attribute.Float64("total_score", totalScore),
			))
		scoreSpan.End()

		e.scoreHistogram.WithLabelValues(cacheTier).Observe(totalScore)

		if best == nil || totalScore > best.score {
			best = &candidate{
				clusterID:      clusterID,
				score:          totalScore,
				preferredNodes: preferredNodes,
				cacheTier:      cacheTier,
			}
		}
	}

	if best == nil {
		span.SetAttributes(attribute.String("selected_cluster", "none"))
		return "", model.SchedulingConstraints{}, false
	}

	span.SetAttributes(
		attribute.String("selected_cluster", best.clusterID),
		attribute.Float64("score", best.score),
		attribute.String("cache_tier", best.cacheTier),
	)

	constraints := model.SchedulingConstraints{
		RequiredGPUs:       gpusNeeded,
		PreferredNodes:     best.preferredNodes,
		CacheAffinityModel: app.ModelID,
	}
	return best.clusterID, constraints, true
}

// computeReservationTTL returns the TTL in seconds for a GPU reservation.
// Uses 2× cold start seconds from the performance profile, or the default.
func (e *PlacementEngine) computeReservationTTL(appID model.AppID, snap worldstate.WorldStateSnapshot) int {
	if profile, ok := snap.PerformanceProfiles[appID]; ok && profile.ColdStartSeconds > 0 {
		return e.cfg.ReservationTTLMultiplier * int(profile.ColdStartSeconds)
	}
	return e.cfg.DefaultReservationTTLSeconds
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

// hasFittingNode returns true if any ready node in the cluster has at least
// gpusNeeded free GPUs.
func hasFittingNode(cluster model.Cluster, gpusNeeded int) bool {
	for _, n := range cluster.Nodes {
		if n.Status == model.NodeStatusReady && n.FreeGPUs >= gpusNeeded {
			return true
		}
	}
	return false
}

// clusterAvailableGPUs returns the available GPU count for a cluster,
// accounting for both snapshot-level active reservations and local cycle
// reservations.
func clusterAvailableGPUs(
	clusterID model.ClusterID,
	snap worldstate.WorldStateSnapshot,
	localReservations map[model.ClusterID]int,
) int {
	cluster, ok := snap.Clusters[clusterID]
	if !ok {
		return 0
	}

	free := 0
	for _, n := range cluster.Nodes {
		if n.Status == model.NodeStatusReady {
			free += n.FreeGPUs
		}
	}

	// Subtract existing active reservations from snapshot.
	for _, r := range snap.Reservations {
		if r.ClusterID == clusterID && r.Status == model.ReservationStatusActive {
			free -= r.GPUs
		}
	}

	// Subtract local cycle reservations.
	free -= localReservations[clusterID]

	if free < 0 {
		return 0
	}
	return free
}

// countAppReplicasInCluster counts running/pending replicas for an app in a
// specific cluster.
func countAppReplicasInCluster(appID model.AppID, clusterID model.ClusterID, snap worldstate.WorldStateSnapshot) int {
	count := 0
	for _, r := range snap.Replicas {
		if r.AppID == appID && r.ClusterID == clusterID &&
			(r.Status == model.ReplicaStatusRunning || r.Status == model.ReplicaStatusPending) {
			count++
		}
	}
	return count
}

// clusterGPUTotals returns the total and allocated GPU counts across ready
// nodes in a cluster.
func clusterGPUTotals(cluster model.Cluster) (total, allocated int) {
	for _, n := range cluster.Nodes {
		if n.Status == model.NodeStatusReady {
			total += n.TotalGPUs
			allocated += n.AllocatedGPUs
		}
	}
	return total, allocated
}
