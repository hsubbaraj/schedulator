package scaling

import (
	"context"
	"math"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/hsubbaraj/schedulator/internal/observability"
	"github.com/hsubbaraj/schedulator/internal/worldstate"
	"github.com/hsubbaraj/schedulator/pkg/model"
)

// ScaleDirection indicates the direction of a scaling decision.
type ScaleDirection string

const (
	ScaleDirectionUp        ScaleDirection = "up"
	ScaleDirectionDown      ScaleDirection = "down"
	ScaleDirectionUnchanged ScaleDirection = "unchanged"
)

// ScaleSignal indicates which metric triggered a scaling decision.
type ScaleSignal string

const (
	ScaleSignalQueue      ScaleSignal = "queue"
	ScaleSignalKVCache    ScaleSignal = "kv_cache"
	ScaleSignalSLA        ScaleSignal = "sla"
	ScaleSignalEfficiency ScaleSignal = "efficiency"
	ScaleSignalNone       ScaleSignal = "none"
)

// ScalingDecision is the output of the scaling engine for a single application.
type ScalingDecision struct {
	AppID        model.AppID
	CurrentCount int
	TargetCount  int
	Direction    ScaleDirection
	Signal       ScaleSignal
	SLABreach    bool

	// ScaleActionApproved tells the control loop to call
	// RecordScaleEvent(appID, direction, ActionAt).
	ScaleActionApproved bool
	ActionAt            time.Time

	// ScaleDownSignalObserved tells the control loop to call
	// IncrementScaleDownSignal(appID). This is set even when the
	// scale-down is suppressed by the stabilization window.
	ScaleDownSignalObserved bool
}

// ScalingEngine computes target replica counts per application.
// It is stateless — all state comes from the WorldStateSnapshot.
type ScalingEngine struct {
	cfg    ScalingConfig
	tracer trace.Tracer

	targetReplicasGauge   *prometheus.GaugeVec
	decisionsCounter      *prometheus.CounterVec
	computeDuration       prometheus.Histogram
	stabilitySuppressed   *prometheus.CounterVec
}

// NewScalingEngine creates a ScalingEngine with the given config,
// tracer, and Prometheus registerer.
func NewScalingEngine(cfg ScalingConfig, tracer trace.Tracer, reg prometheus.Registerer) *ScalingEngine {
	return &ScalingEngine{
		cfg:    cfg,
		tracer: tracer,
		targetReplicasGauge: observability.MustNewGaugeVec(reg, prometheus.GaugeOpts{
			Name: "schedulator_scaling_target_replicas",
			Help: "Target replica count per application.",
		}, []string{"app_id"}),
		decisionsCounter: observability.MustNewCounterVec(reg, prometheus.CounterOpts{
			Name: "schedulator_scaling_decisions_total",
			Help: "Total scaling decisions by direction.",
		}, []string{"app_id", "direction"}),
		computeDuration: observability.MustNewHistogram(reg, prometheus.HistogramOpts{
			Name: "schedulator_scaling_compute_duration_seconds",
			Help: "Time to compute all scaling targets.",
		}),
		stabilitySuppressed: observability.MustNewCounterVec(reg, prometheus.CounterOpts{
			Name: "schedulator_scaling_stability_suppressed_total",
			Help: "Scaling decisions suppressed by stability controls.",
		}, []string{"app_id", "reason"}),
	}
}

// ComputeTargets iterates all applications in the snapshot and returns
// a ScalingDecision for each app that has VLLMMetrics.
func (e *ScalingEngine) ComputeTargets(ctx context.Context, snap worldstate.WorldStateSnapshot) map[model.AppID]ScalingDecision {
	ctx, span := e.tracer.Start(ctx, "scaling.compute_targets",
		trace.WithAttributes(attribute.Int("app_count", len(snap.Applications))))
	defer span.End()

	start := snap.TakenAt
	results := make(map[model.AppID]ScalingDecision, len(snap.Applications))

	for appID, app := range snap.Applications {
		metrics, ok := snap.VLLMMetrics[appID]
		if !ok {
			continue
		}

		profile := snap.PerformanceProfiles[appID]
		currentCount := countReplicasByStatus(appID, snap, model.ReplicaStatusRunning) +
			countReplicasByStatus(appID, snap, model.ReplicaStatusPending)

		rawTarget, signal := e.computeTargetReplicas(app, metrics, profile, currentCount)
		effectiveTarget := e.computeEffectiveTarget(ctx, app, rawTarget, snap)
		decision := e.applyStabilityControls(ctx, appID, app, effectiveTarget, currentCount, snap)

		_, childSpan := e.tracer.Start(ctx, "scaling.compute_target",
			trace.WithAttributes(
				attribute.String("app.id", appID),
				attribute.Int("current_count", currentCount),
				attribute.Int("target_count", decision.TargetCount),
				attribute.String("signal", string(decision.Signal)),
			))
		childSpan.End()

		// Preserve the signal from computeTargetReplicas.
		decision.Signal = signal

		e.targetReplicasGauge.WithLabelValues(appID).Set(float64(decision.TargetCount))
		e.decisionsCounter.WithLabelValues(appID, string(decision.Direction)).Inc()

		results[appID] = decision
	}

	e.computeDuration.Observe(snap.TakenAt.Sub(start).Seconds())

	return results
}

// computeTargetReplicas implements the scaling algorithm from proposal Section 4.3.
// It returns the raw target replica count and the dominant signal.
func (e *ScalingEngine) computeTargetReplicas(
	app model.Application,
	metrics model.VLLMMetrics,
	_ model.PerformanceProfile,
	currentCount int,
) (int, ScaleSignal) {
	// Queue pressure: avg queue > high watermark → scale up.
	requiredForQueue := currentCount
	if metrics.AvgWaitingQueueDepth > e.cfg.QueueHighWatermark && e.cfg.QueueTarget > 0 {
		factor := metrics.AvgWaitingQueueDepth / e.cfg.QueueTarget
		requiredForQueue = ceilInt(float64(currentCount) * factor)
	}

	// KV cache pressure: max KV cache > high watermark → scale up.
	requiredForCache := currentCount
	if metrics.MaxKVCacheUtilization > e.cfg.KVCacheHighWatermark {
		factor := metrics.MaxKVCacheUtilization / e.cfg.KVCacheTarget
		requiredForCache = ceilInt(float64(currentCount) * factor)
	}

	// SLA breach: P99 TTFT > SLA → scale up.
	requiredForSLA := currentCount
	if app.SLA.MaxP99TTFTMs > 0 && metrics.P99TimeToFirstTokenMs > float64(app.SLA.MaxP99TTFTMs) {
		factor := metrics.P99TimeToFirstTokenMs / float64(app.SLA.MaxP99TTFTMs)
		requiredForSLA = ceilInt(float64(currentCount) * factor)
	}

	// Scale-down: all metrics low → efficiency.
	desiredForEfficiency := currentCount
	if metrics.AvgWaitingQueueDepth < e.cfg.QueueLowWatermark &&
		metrics.AvgBatchUtilization < e.cfg.BatchLowWatermark &&
		metrics.AvgKVCacheUtilization < e.cfg.KVCacheLowWatermark &&
		currentCount > app.MinReplicas {
		utilization := math.Max(metrics.AvgBatchUtilization,
			math.Max(metrics.AvgKVCacheUtilization,
				metrics.AvgWaitingQueueDepth/e.cfg.QueueTarget))
		desiredForEfficiency = maxInt(ceilInt(float64(currentCount)*utilization), app.MinReplicas)
	}

	// Target: max of scale-up signals; use efficiency if no pressure.
	scaleUpTarget := maxInt(requiredForQueue, maxInt(requiredForCache, requiredForSLA))

	var target int
	var signal ScaleSignal

	if scaleUpTarget > currentCount {
		target = scaleUpTarget
		// Determine dominant signal.
		switch scaleUpTarget {
		case requiredForQueue:
			signal = ScaleSignalQueue
		case requiredForCache:
			signal = ScaleSignalKVCache
		default:
			signal = ScaleSignalSLA
		}
	} else {
		target = desiredForEfficiency
		if target < currentCount {
			signal = ScaleSignalEfficiency
		} else {
			signal = ScaleSignalNone
		}
	}

	// Floor at min_replicas.
	target = maxInt(target, app.MinReplicas)

	// Dampening: cap scale-down per cycle.
	if target < currentCount {
		target = maxInt(target, currentCount-e.cfg.MaxScaleDownPerCycle)
	}

	// Dampening: cap scale-up per cycle.
	if target > currentCount {
		target = minInt(target, currentCount+e.cfg.MaxScaleUpPerCycle)
	}

	return target, signal
}

// computeEffectiveTarget accounts for in-flight (pending) replicas to prevent
// redundant scale-ups. Implements proposal Section 4.3 "In-Flight Awareness".
func (e *ScalingEngine) computeEffectiveTarget(
	ctx context.Context,
	app model.Application,
	rawTarget int,
	snap worldstate.WorldStateSnapshot,
) int {
	_ = ctx

	running := countReplicasByStatus(app.AppID, snap, model.ReplicaStatusRunning)
	pending := countReplicasByStatus(app.AppID, snap, model.ReplicaStatusPending)

	additionalNeeded := maxInt(0, rawTarget-running-pending)

	// Stale-pending override: if pending replicas have been pending longer
	// than 2× cold_start_seconds AND there's an SLA breach, treat them as
	// stuck and request fresh scale-ups.
	profile, hasProfile := snap.PerformanceProfiles[app.AppID]
	if hasProfile && profile.ColdStartSeconds > 0 {
		expectedStartup := time.Duration(profile.ColdStartSeconds*2) * time.Second
		stalePending := 0
		for _, r := range snap.Replicas {
			if r.AppID != app.AppID || r.Status != model.ReplicaStatusPending {
				continue
			}
			if r.CreatedAt.IsZero() {
				continue
			}
			if snap.TakenAt.Sub(r.CreatedAt) > expectedStartup {
				stalePending++
			}
		}

		if stalePending > 0 {
			metrics, hasMetrics := snap.VLLMMetrics[app.AppID]
			if hasMetrics && isSLABreach(app, metrics) {
				additionalNeeded += stalePending
			}
		}
	}

	return running + pending + additionalNeeded
}

// applyStabilityControls implements cooldowns, stabilization window, and
// SLA breach bypass from proposal Section 4.3.
func (e *ScalingEngine) applyStabilityControls(
	ctx context.Context,
	appID model.AppID,
	app model.Application,
	target int,
	current int,
	snap worldstate.WorldStateSnapshot,
) ScalingDecision {
	history := snap.ScalingHistory[appID]
	now := snap.TakenAt
	slaBreach := false
	if metrics, ok := snap.VLLMMetrics[appID]; ok {
		slaBreach = isSLABreach(app, metrics)
	}

	decision := ScalingDecision{
		AppID:        appID,
		CurrentCount: current,
		TargetCount:  target,
		SLABreach:    slaBreach,
	}

	if target > current {
		decision.Direction = ScaleDirectionUp
		decision.Signal = ScaleSignalNone

		// Scale-up cooldown: don't scale up again too soon.
		if !history.LastScaleUpAt.IsZero() && now.Sub(history.LastScaleUpAt) < e.cfg.ScaleUpCooldown {
			decision.TargetCount = current
			decision.Direction = ScaleDirectionUnchanged
			e.stabilitySuppressed.WithLabelValues(appID, "cooldown").Inc()
			return decision
		}

		decision.ScaleActionApproved = true
		decision.ActionAt = now
		return decision
	}

	if target < current {
		decision.Direction = ScaleDirectionDown
		decision.Signal = ScaleSignalEfficiency

		// Scale-down cooldown: don't scale down too soon after last scale-down.
		if !history.LastScaleDownAt.IsZero() && now.Sub(history.LastScaleDownAt) < e.cfg.ScaleDownCooldown {
			decision.TargetCount = current
			decision.Direction = ScaleDirectionUnchanged
			e.stabilitySuppressed.WithLabelValues(appID, "cooldown").Inc()
			return decision
		}

		// Always observe the scale-down signal (increment counter via control loop).
		decision.ScaleDownSignalObserved = true

		// SLA breach bypasses stabilization window.
		if slaBreach {
			decision.ScaleActionApproved = true
			decision.ActionAt = now
			return decision
		}

		// Stabilization: require N consecutive signals before acting.
		// The counter has already been incremented for this cycle via
		// ScaleDownSignalObserved, so the control loop will increment
		// before the next cycle sees it. We compare against the count
		// that will exist AFTER the increment.
		if history.ConsecutiveScaleDownSignals+1 < e.cfg.StabilizationCycles {
			decision.TargetCount = current
			decision.Direction = ScaleDirectionUnchanged
			e.stabilitySuppressed.WithLabelValues(appID, "stabilization").Inc()
			return decision
		}

		decision.ScaleActionApproved = true
		decision.ActionAt = now
		return decision
	}

	// Unchanged.
	decision.Direction = ScaleDirectionUnchanged
	decision.Signal = ScaleSignalNone
	return decision
}

// isSLABreach returns true if the application's P99 TTFT exceeds its SLA target.
func isSLABreach(app model.Application, metrics model.VLLMMetrics) bool {
	if app.SLA.MaxP99TTFTMs == 0 {
		return false
	}
	return metrics.P99TimeToFirstTokenMs > float64(app.SLA.MaxP99TTFTMs)
}

// countReplicasByStatus counts replicas for an app with the given status.
func countReplicasByStatus(appID model.AppID, snap worldstate.WorldStateSnapshot, status model.ReplicaStatus) int {
	count := 0
	for _, r := range snap.Replicas {
		if r.AppID == appID && r.Status == status {
			count++
		}
	}
	return count
}

func ceilInt(f float64) int {
	return int(math.Ceil(f))
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
