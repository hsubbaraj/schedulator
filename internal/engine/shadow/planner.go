package shadow

import (
	"context"
	"log/slog"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"go.opentelemetry.io/otel/trace"

	"github.com/hsubbaraj/schedulator/internal/engine/scaling"
	"github.com/hsubbaraj/schedulator/internal/worldstate"
	"github.com/hsubbaraj/schedulator/pkg/model"
	"github.com/hsubbaraj/schedulator/pkg/ports"
)

// ShadowPlanner runs the primary planner synchronously on the hot path and
// the shadow planner asynchronously in a detached goroutine. The shadow result
// is never returned to the control loop; both results are passed to
// ShadowMetrics for comparison.
type ShadowPlanner struct {
	primary       ports.Planner
	shadow        ports.Planner
	shadowTimeout time.Duration
	metrics       *ShadowMetrics
	tracer        trace.Tracer
}

// NewShadowPlanner returns a ShadowPlanner. shadowTimeoutMs is the deadline
// for the shadow goroutine; use 0 to apply the default (500 ms).
func NewShadowPlanner(
	primary ports.Planner,
	shadow ports.Planner,
	shadowTimeoutMs int,
	tracer trace.Tracer,
	reg prometheus.Registerer,
) *ShadowPlanner {
	timeout := time.Duration(shadowTimeoutMs) * time.Millisecond
	if timeout <= 0 {
		timeout = 500 * time.Millisecond
	}
	return &ShadowPlanner{
		primary:       primary,
		shadow:        shadow,
		shadowTimeout: timeout,
		metrics:       NewShadowMetrics(reg),
		tracer:        tracer,
	}
}

// Plan runs the primary planner synchronously and returns its result.
// The shadow planner runs in a detached goroutine and its result is used only
// for metrics comparison. snap is an immutable value type and is safe to pass
// to the goroutine.
func (p *ShadowPlanner) Plan(
	ctx context.Context,
	snap worldstate.WorldStateSnapshot,
	targets map[model.AppID]scaling.ScalingDecision,
) (ports.PlannerResult, error) {
	_, span := p.tracer.Start(ctx, "shadow.plan")
	defer span.End()

	// Hot path — must complete within the control loop budget.
	primaryStart := time.Now()
	primaryResult, primaryErr := p.primary.Plan(ctx, snap, targets)
	primaryLatency := time.Since(primaryStart)

	primaryName := p.primary.Name()
	shadowName := p.shadow.Name()

	// Shadow path — detached, does not block the hot path.
	// targets is a map; maps are reference types but Plan must not mutate them
	// (as documented by the Planner interface contract).
	go func() {
		defer func() {
			if r := recover(); r != nil {
				slog.Error("shadow planner panicked",
					"shadow", shadowName,
					"panic", r,
				)
			}
		}()

		sctx, cancel := context.WithTimeout(context.Background(), p.shadowTimeout)
		defer cancel()

		shadowStart := time.Now()
		shadowResult, shadowErr := p.shadow.Plan(sctx, snap, targets)
		shadowLatency := time.Since(shadowStart)

		p.metrics.Record(
			primaryResult, primaryErr,
			shadowResult, shadowErr,
			primaryName, shadowName,
			primaryLatency, shadowLatency,
		)
	}()

	return primaryResult, primaryErr
}

// Name returns the composite planner identifier.
func (p *ShadowPlanner) Name() string {
	return "shadow:" + p.primary.Name() + "+" + p.shadow.Name()
}
