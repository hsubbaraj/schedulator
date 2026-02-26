package shadow

import (
	"log/slog"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/hsubbaraj/schedulator/internal/observability"
	"github.com/hsubbaraj/schedulator/pkg/ports"
)

// ShadowMetrics records per-planner Prometheus metrics and logs comparison
// diffs between primary and shadow planner results.
type ShadowMetrics struct {
	scaleUps   *prometheus.CounterVec
	preemptions *prometheus.CounterVec
	migrations *prometheus.CounterVec
	latency    *prometheus.HistogramVec
	unmet      *prometheus.CounterVec
	errors     *prometheus.CounterVec
}

// NewShadowMetrics creates a ShadowMetrics with Prometheus metrics registered
// under the given registerer.
func NewShadowMetrics(reg prometheus.Registerer) *ShadowMetrics {
	return &ShadowMetrics{
		scaleUps: observability.MustNewCounterVec(reg, prometheus.CounterOpts{
			Name: "schedulator_planner_scaleups_total",
			Help: "Total scale-up decisions produced by the planner.",
		}, []string{"planner"}),
		preemptions: observability.MustNewCounterVec(reg, prometheus.CounterOpts{
			Name: "schedulator_planner_preemptions_total",
			Help: "Total preemption decisions produced by the planner.",
		}, []string{"planner"}),
		migrations: observability.MustNewCounterVec(reg, prometheus.CounterOpts{
			Name: "schedulator_planner_migrations_total",
			Help: "Total migration decisions produced by the planner.",
		}, []string{"planner"}),
		latency: observability.MustNewHistogramVec(reg, prometheus.HistogramOpts{
			Name:    "schedulator_planner_latency_seconds",
			Help:    "End-to-end planning latency.",
			Buckets: prometheus.DefBuckets,
		}, []string{"planner"}),
		unmet: observability.MustNewCounterVec(reg, prometheus.CounterOpts{
			Name: "schedulator_planner_unmet_demand_total",
			Help: "Replicas that could not be placed (unmet demand).",
		}, []string{"planner"}),
		errors: observability.MustNewCounterVec(reg, prometheus.CounterOpts{
			Name: "schedulator_planner_error_total",
			Help: "Total planning errors.",
		}, []string{"planner"}),
	}
}

// Record records metrics and logs a diff comparing both planner results.
func (m *ShadowMetrics) Record(
	primaryResult ports.PlannerResult,
	primaryErr error,
	shadowResult ports.PlannerResult,
	shadowErr error,
	primaryName, shadowName string,
	primaryLatency, shadowLatency time.Duration,
) {
	// Record primary metrics.
	m.scaleUps.WithLabelValues(primaryName).Add(float64(len(primaryResult.Decisions.ScaleUps)))
	m.preemptions.WithLabelValues(primaryName).Add(float64(len(primaryResult.Decisions.Preemptions)))
	m.migrations.WithLabelValues(primaryName).Add(float64(len(primaryResult.Migrations)))
	m.latency.WithLabelValues(primaryName).Observe(primaryLatency.Seconds())
	if primaryErr != nil {
		m.errors.WithLabelValues(primaryName).Inc()
	}

	// Record shadow metrics.
	m.scaleUps.WithLabelValues(shadowName).Add(float64(len(shadowResult.Decisions.ScaleUps)))
	m.preemptions.WithLabelValues(shadowName).Add(float64(len(shadowResult.Decisions.Preemptions)))
	m.migrations.WithLabelValues(shadowName).Add(float64(len(shadowResult.Migrations)))
	m.latency.WithLabelValues(shadowName).Observe(shadowLatency.Seconds())
	if shadowErr != nil {
		m.errors.WithLabelValues(shadowName).Inc()
	}

	// Log structured diff at DEBUG level.
	slog.Debug("shadow planner comparison",
		"primary", primaryName,
		"shadow", shadowName,
		"delta_scaleups", len(shadowResult.Decisions.ScaleUps)-len(primaryResult.Decisions.ScaleUps),
		"delta_preemptions", len(shadowResult.Decisions.Preemptions)-len(primaryResult.Decisions.Preemptions),
		"delta_migrations", len(shadowResult.Migrations)-len(primaryResult.Migrations),
	)
}
