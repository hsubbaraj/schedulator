package rebalancing

import (
	"context"
	"sort"

	"github.com/prometheus/client_golang/prometheus"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/hsubbaraj/schedulator/internal/observability"
	"github.com/hsubbaraj/schedulator/internal/worldstate"
	"github.com/hsubbaraj/schedulator/pkg/model"
)

// RebalancingEngine proactively consolidates fragmented GPU allocations via
// blue-green migration. Implements proposal Section 4.8.
type RebalancingEngine struct {
	cfg    RebalancingConfig
	tracer trace.Tracer

	migrationsCounter prometheus.Counter
	improvementHist   prometheus.Histogram
	skippedCounter    *prometheus.CounterVec
}

// NewRebalancingEngine creates a new RebalancingEngine with the given config,
// tracer, and Prometheus registerer.
func NewRebalancingEngine(
	cfg RebalancingConfig,
	tracer trace.Tracer,
	reg prometheus.Registerer,
) *RebalancingEngine {
	return &RebalancingEngine{
		cfg:    cfg,
		tracer: tracer,
		migrationsCounter: observability.MustNewCounter(reg, prometheus.CounterOpts{
			Name: "schedulator_rebalancing_migrations_proposed_total",
			Help: "Total number of rebalancing migrations proposed.",
		}),
		improvementHist: observability.MustNewHistogram(reg, prometheus.HistogramOpts{
			Name:    "schedulator_rebalancing_fragmentation_improvement",
			Help:    "Distribution of fragmentation improvement deltas for proposed migrations.",
			Buckets: prometheus.DefBuckets,
		}),
		skippedCounter: observability.MustNewCounterVec(reg, prometheus.CounterOpts{
			Name: "schedulator_rebalancing_skipped_total",
			Help: "Total number of rebalancing candidates skipped, by reason.",
		}, []string{"reason"}),
	}
}

// FindRebalancingOpportunities scans the world state snapshot for replicas on
// fragmented nodes that could be consolidated to tighter-packed destinations.
func (e *RebalancingEngine) FindRebalancingOpportunities(
	ctx context.Context,
	snap worldstate.WorldStateSnapshot,
) []model.MigrateDecision {
	ctx, span := e.tracer.Start(ctx, "rebalancing.find_opportunities")
	defer span.End()

	var migrations []model.MigrateDecision
	clustersEvaluated := 0

	for _, cluster := range snap.Clusters {
		clustersEvaluated++

		if cluster.FragmentationScore < e.cfg.MinFragmentationDelta {
			e.skippedCounter.WithLabelValues("well_packed").Inc()
			continue
		}

		// Find fragmented nodes: partially allocated and ready.
		for _, node := range cluster.Nodes {
			if node.Status != model.NodeStatusReady {
				continue
			}
			if node.AllocatedGPUs <= 0 || node.AllocatedGPUs >= node.TotalGPUs {
				continue
			}

			for _, replicaID := range node.Pods {
				replica, ok := snap.Replicas[replicaID]
				if !ok {
					continue
				}

				app, ok := snap.Applications[replica.AppID]
				if !ok {
					continue
				}

				// Skip if migration would drop below min_replicas.
				runningCount := countRunningReplicas(replica.AppID, snap)
				if runningCount <= app.MinReplicas {
					e.skippedCounter.WithLabelValues("min_replicas").Inc()
					continue
				}

				// Find a better destination.
				target, found := e.findConsolidationTarget(replica, app, snap)
				if !found {
					continue
				}

				// Skip if destination has no cached model.
				if !hasCachedModel(target.clusterID, app.ModelID, snap) {
					e.skippedCounter.WithLabelValues("no_cache").Inc()
					continue
				}

				// Check if fragmentation improvement is worthwhile.
				destCluster := snap.Clusters[target.clusterID]
				improvement := estimateFragmentationDelta(node, destCluster, replica.GPUs, snap)
				if improvement < e.cfg.MinFragmentationDelta {
					e.skippedCounter.WithLabelValues("low_delta").Inc()
					continue
				}

				migrations = append(migrations, model.MigrateDecision{
					AppID:                 replica.AppID,
					SourceReplicaID:       replica.ReplicaID,
					TargetClusterID:       target.clusterID,
					SchedulingConstraints: target.constraints,
				})
				e.migrationsCounter.Inc()
				e.improvementHist.Observe(improvement)

				if len(migrations) >= e.cfg.MaxMigrationsPerCycle {
					span.SetAttributes(
						attribute.Int("migrations_proposed", len(migrations)),
						attribute.Int("clusters_evaluated", clustersEvaluated),
					)
					return migrations
				}
			}
		}
	}

	// Sort: lower priority apps first (higher Priority int = lower priority).
	// Tiebreaker by ReplicaID for determinism.
	sort.Slice(migrations, func(i, j int) bool {
		pi := snap.Applications[migrations[i].AppID].Priority
		pj := snap.Applications[migrations[j].AppID].Priority
		if pi != pj {
			return pi > pj
		}
		return migrations[i].SourceReplicaID < migrations[j].SourceReplicaID
	})

	if len(migrations) > e.cfg.MaxMigrationsPerCycle {
		migrations = migrations[:e.cfg.MaxMigrationsPerCycle]
	}

	span.SetAttributes(
		attribute.Int("migrations_proposed", len(migrations)),
		attribute.Int("clusters_evaluated", clustersEvaluated),
	)
	return migrations
}
