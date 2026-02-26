package scaling

import (
	"context"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hsubbaraj/schedulator/internal/observability"
	"github.com/hsubbaraj/schedulator/internal/worldstate"
	"github.com/hsubbaraj/schedulator/pkg/model"
)

func newTestEngine() *ScalingEngine {
	return NewScalingEngine(DefaultScalingConfig(), observability.NewNoopTracer(), prometheus.NewRegistry())
}

func baseApp(appID string) model.Application {
	return model.Application{
		AppID:       appID,
		MinReplicas: 1,
		SLA:         model.SLA{AppID: appID, MaxP99TTFTMs: 200},
	}
}

func idleMetrics(appID string) model.VLLMMetrics {
	return model.VLLMMetrics{
		AppID:                 appID,
		AvgWaitingQueueDepth:  0.5,
		MaxKVCacheUtilization: 0.3,
		AvgBatchUtilization:   0.2,
		AvgKVCacheUtilization: 0.3,
		P99TimeToFirstTokenMs: 100,
	}
}

// ---------- computeTargetReplicas ----------

func TestComputeTargetReplicas_QueuePressure(t *testing.T) {
	e := newTestEngine()
	app := baseApp("app-1")
	profile := model.PerformanceProfile{}

	tests := []struct {
		name         string
		queueDepth   float64
		currentCount int
		wantTarget   int
		wantSignal   ScaleSignal
	}{
		{
			name:         "queue above high watermark scales up",
			queueDepth:   20.0,
			currentCount: 4,
			wantTarget:   14, // ceil(4 * 20/5) = 16, capped at 4+10=14
			wantSignal:   ScaleSignalQueue,
		},
		{
			name:         "queue exactly at high watermark does not trigger",
			queueDepth:   10.0,
			currentCount: 4,
			wantTarget:   4,
			wantSignal:   ScaleSignalNone,
		},
		{
			name:         "queue slightly above high watermark triggers",
			queueDepth:   10.1,
			currentCount: 4,
			wantTarget:   9, // ceil(4 * 10.1/5) = ceil(8.08) = 9
			wantSignal:   ScaleSignalQueue,
		},
		{
			name:         "queue pressure capped by MaxScaleUpPerCycle",
			queueDepth:   100.0,
			currentCount: 2,
			wantTarget:   12, // ceil(2 * 100/5) = 40, capped at 2+10=12
			wantSignal:   ScaleSignalQueue,
		},
		{
			name:         "currentCount zero with queue pressure",
			queueDepth:   20.0,
			currentCount: 0,
			wantTarget:   1, // ceil(0 * factor) = 0, floored by min_replicas=1
			wantSignal:   ScaleSignalQueue,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			metrics := idleMetrics("app-1")
			metrics.AvgWaitingQueueDepth = tt.queueDepth
			target, signal := e.computeTargetReplicas(app, metrics, profile, tt.currentCount)
			assert.Equal(t, tt.wantTarget, target)
			assert.Equal(t, tt.wantSignal, signal)
		})
	}
}

func TestComputeTargetReplicas_KVCachePressure(t *testing.T) {
	e := newTestEngine()
	app := baseApp("app-1")
	profile := model.PerformanceProfile{}

	tests := []struct {
		name         string
		maxKVCache   float64
		currentCount int
		wantTarget   int
		wantSignal   ScaleSignal
	}{
		{
			name:         "KV cache above 0.85 scales up",
			maxKVCache:   0.90,
			currentCount: 4,
			wantTarget:   6, // ceil(4 * 0.9/0.7) = ceil(5.14) = 6
			wantSignal:   ScaleSignalKVCache,
		},
		{
			name:         "KV cache exactly at 0.85 does not trigger",
			maxKVCache:   0.85,
			currentCount: 4,
			wantTarget:   4,
			wantSignal:   ScaleSignalNone,
		},
		{
			name:         "KV cache at 0.95 scales up",
			maxKVCache:   0.95,
			currentCount: 4,
			wantTarget:   6, // ceil(4 * 0.95/0.7) = ceil(5.43) = 6
			wantSignal:   ScaleSignalKVCache,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			metrics := idleMetrics("app-1")
			metrics.MaxKVCacheUtilization = tt.maxKVCache
			// Keep other metrics from triggering scale-up.
			metrics.AvgWaitingQueueDepth = 1.0
			target, signal := e.computeTargetReplicas(app, metrics, profile, tt.currentCount)
			assert.Equal(t, tt.wantTarget, target)
			assert.Equal(t, tt.wantSignal, signal)
		})
	}
}

func TestComputeTargetReplicas_SLABreach(t *testing.T) {
	e := newTestEngine()
	profile := model.PerformanceProfile{}

	tests := []struct {
		name         string
		p99TTFT      float64
		slaMax       int
		currentCount int
		wantTarget   int
		wantSignal   ScaleSignal
	}{
		{
			name:         "P99 TTFT exceeds SLA scales up",
			p99TTFT:      400,
			slaMax:       200,
			currentCount: 4,
			wantTarget:   8, // ceil(4 * 400/200) = 8
			wantSignal:   ScaleSignalSLA,
		},
		{
			name:         "P99 TTFT within SLA no scale",
			p99TTFT:      150,
			slaMax:       200,
			currentCount: 4,
			wantTarget:   4,
			wantSignal:   ScaleSignalNone,
		},
		{
			name:         "SLA MaxP99TTFTMs is zero skips SLA scaling",
			p99TTFT:      9999,
			slaMax:       0,
			currentCount: 4,
			wantTarget:   4,
			wantSignal:   ScaleSignalNone,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			app := baseApp("app-1")
			app.SLA.MaxP99TTFTMs = tt.slaMax
			metrics := idleMetrics("app-1")
			metrics.P99TimeToFirstTokenMs = tt.p99TTFT
			metrics.AvgWaitingQueueDepth = 1.0
			metrics.MaxKVCacheUtilization = 0.3
			target, signal := e.computeTargetReplicas(app, metrics, profile, tt.currentCount)
			assert.Equal(t, tt.wantTarget, target)
			assert.Equal(t, tt.wantSignal, signal)
		})
	}
}

func TestComputeTargetReplicas_LowUtilization(t *testing.T) {
	e := newTestEngine()
	profile := model.PerformanceProfile{}

	tests := []struct {
		name         string
		queueDepth   float64
		batchUtil    float64
		kvCacheUtil  float64
		currentCount int
		minReplicas  int
		wantTarget   int
		wantSignal   ScaleSignal
	}{
		{
			name:         "all metrics low scales down",
			queueDepth:   0.5,
			batchUtil:    0.10,
			kvCacheUtil:  0.20,
			currentCount: 6,
			minReplicas:  1,
			wantTarget:   4, // max utilization = max(0.10, 0.20, 0.5/5=0.1) = 0.20; ceil(6*0.20)=2; dampened: max(2, 6-2)=4
			wantSignal:   ScaleSignalEfficiency,
		},
		{
			name:         "low utilization capped by MaxScaleDownPerCycle",
			queueDepth:   0.0,
			batchUtil:    0.01,
			kvCacheUtil:  0.01,
			currentCount: 10,
			minReplicas:  1,
			wantTarget:   8, // desired=max(ceil(10*0.01),1)=1; dampened=max(1,10-2)=8
			wantSignal:   ScaleSignalEfficiency,
		},
		{
			name:         "current equals min_replicas no scale down",
			queueDepth:   0.5,
			batchUtil:    0.10,
			kvCacheUtil:  0.20,
			currentCount: 1,
			minReplicas:  1,
			wantTarget:   1,
			wantSignal:   ScaleSignalNone,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			app := baseApp("app-1")
			app.MinReplicas = tt.minReplicas
			metrics := model.VLLMMetrics{
				AppID:                 "app-1",
				AvgWaitingQueueDepth:  tt.queueDepth,
				AvgBatchUtilization:   tt.batchUtil,
				AvgKVCacheUtilization: tt.kvCacheUtil,
				MaxKVCacheUtilization: tt.kvCacheUtil,
				P99TimeToFirstTokenMs: 100,
			}
			target, signal := e.computeTargetReplicas(app, metrics, profile, tt.currentCount)
			assert.Equal(t, tt.wantTarget, target)
			assert.Equal(t, tt.wantSignal, signal)
		})
	}
}

func TestComputeTargetReplicas_RespectsMinReplicas(t *testing.T) {
	e := newTestEngine()
	app := baseApp("app-1")
	app.MinReplicas = 3
	profile := model.PerformanceProfile{}

	// All metrics low, but min_replicas = 3.
	metrics := model.VLLMMetrics{
		AppID:                 "app-1",
		AvgWaitingQueueDepth:  0.0,
		AvgBatchUtilization:   0.01,
		AvgKVCacheUtilization: 0.01,
		MaxKVCacheUtilization: 0.01,
		P99TimeToFirstTokenMs: 50,
	}
	target, _ := e.computeTargetReplicas(app, metrics, profile, 5)
	assert.GreaterOrEqual(t, target, 3)
}

func TestComputeTargetReplicas_Dampening(t *testing.T) {
	e := newTestEngine()
	app := baseApp("app-1")
	profile := model.PerformanceProfile{}

	t.Run("scale up capped at MaxScaleUpPerCycle", func(t *testing.T) {
		metrics := idleMetrics("app-1")
		metrics.AvgWaitingQueueDepth = 100.0
		target, _ := e.computeTargetReplicas(app, metrics, profile, 3)
		assert.Equal(t, 13, target) // 3+10=13
	})

	t.Run("scale down capped at MaxScaleDownPerCycle", func(t *testing.T) {
		metrics := model.VLLMMetrics{
			AppID:                 "app-1",
			AvgWaitingQueueDepth:  0.0,
			AvgBatchUtilization:   0.01,
			AvgKVCacheUtilization: 0.01,
			MaxKVCacheUtilization: 0.01,
			P99TimeToFirstTokenMs: 50,
		}
		target, _ := e.computeTargetReplicas(app, metrics, profile, 10)
		assert.Equal(t, 8, target) // 10-2=8
	})
}

// ---------- computeEffectiveTarget ----------

func TestComputeEffectiveTarget_PendingReplicas(t *testing.T) {
	e := newTestEngine()
	app := baseApp("app-1")
	now := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)

	tests := []struct {
		name      string
		rawTarget int
		running   int
		pending   int
		want      int
	}{
		{
			name:      "pending replicas prevent redundant scale-ups",
			rawTarget: 6,
			running:   3,
			pending:   2,
			want:      6, // additional_needed = max(0, 6-3-2) = 1; effective = 3+2+1 = 6
		},
		{
			name:      "no additional needed when pending covers target",
			rawTarget: 4,
			running:   3,
			pending:   2,
			want:      5, // additional_needed = max(0, 4-3-2) = 0; effective = 3+2+0 = 5
		},
		{
			name:      "all pending covers everything",
			rawTarget: 2,
			running:   0,
			pending:   3,
			want:      3, // additional_needed = 0; effective = 0+3+0 = 3
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			snap := worldstate.WorldStateSnapshot{
				Replicas:            make(map[model.ReplicaID]model.Replica),
				PerformanceProfiles: map[model.AppID]model.PerformanceProfile{},
				VLLMMetrics:         map[model.AppID]model.VLLMMetrics{},
				TakenAt:             now,
			}
			for i := 0; i < tt.running; i++ {
				id := model.ReplicaID("r-" + string(rune('a'+i)))
				snap.Replicas[id] = model.Replica{ReplicaID: id, AppID: "app-1", Status: model.ReplicaStatusRunning}
			}
			for i := 0; i < tt.pending; i++ {
				id := model.ReplicaID("p-" + string(rune('a'+i)))
				snap.Replicas[id] = model.Replica{ReplicaID: id, AppID: "app-1", Status: model.ReplicaStatusPending, CreatedAt: now}
			}

			result := e.computeEffectiveTarget(context.Background(), app, tt.rawTarget, snap)
			assert.Equal(t, tt.want, result)
		})
	}
}

func TestComputeEffectiveTarget_StalePending(t *testing.T) {
	e := newTestEngine()
	now := time.Date(2025, 1, 1, 0, 10, 0, 0, time.UTC)

	tests := []struct {
		name      string
		slaBreach bool
		stale     bool
		want      int
	}{
		{
			name:      "stale pending with SLA breach requests fresh",
			slaBreach: true,
			stale:     true,
			want:      4, // running=2, pending=1(stale), additional=max(0,3-2-1)=0, +1 stale = 4
		},
		{
			name:      "stale pending without SLA breach no extra",
			slaBreach: false,
			stale:     true,
			want:      3, // running=2, pending=1, additional=0
		},
		{
			name:      "non-stale pending with SLA breach no extra",
			slaBreach: true,
			stale:     false,
			want:      3, // running=2, pending=1, additional=0
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			app := baseApp("app-1")
			if !tt.slaBreach {
				app.SLA.MaxP99TTFTMs = 0 // disable SLA
			}

			var pendingCreated time.Time
			if tt.stale {
				pendingCreated = now.Add(-10 * time.Minute) // way past 2× cold start
			} else {
				pendingCreated = now.Add(-10 * time.Second) // recent
			}

			snap := worldstate.WorldStateSnapshot{
				Replicas: map[model.ReplicaID]model.Replica{
					"r1": {ReplicaID: "r1", AppID: "app-1", Status: model.ReplicaStatusRunning},
					"r2": {ReplicaID: "r2", AppID: "app-1", Status: model.ReplicaStatusRunning},
					"p1": {ReplicaID: "p1", AppID: "app-1", Status: model.ReplicaStatusPending, CreatedAt: pendingCreated},
				},
				PerformanceProfiles: map[model.AppID]model.PerformanceProfile{
					"app-1": {AppID: "app-1", ColdStartSeconds: 60},
				},
				VLLMMetrics: map[model.AppID]model.VLLMMetrics{
					"app-1": {AppID: "app-1", P99TimeToFirstTokenMs: 500},
				},
				TakenAt: now,
			}

			result := e.computeEffectiveTarget(context.Background(), app, 3, snap)
			assert.Equal(t, tt.want, result)
		})
	}
}

func TestComputeEffectiveTarget_StalePending_ZeroCreatedAt(t *testing.T) {
	e := newTestEngine()
	app := baseApp("app-1")
	now := time.Date(2025, 1, 1, 0, 10, 0, 0, time.UTC)

	snap := worldstate.WorldStateSnapshot{
		Replicas: map[model.ReplicaID]model.Replica{
			"r1": {ReplicaID: "r1", AppID: "app-1", Status: model.ReplicaStatusRunning},
			"p1": {ReplicaID: "p1", AppID: "app-1", Status: model.ReplicaStatusPending}, // zero CreatedAt
		},
		PerformanceProfiles: map[model.AppID]model.PerformanceProfile{
			"app-1": {AppID: "app-1", ColdStartSeconds: 60},
		},
		VLLMMetrics: map[model.AppID]model.VLLMMetrics{
			"app-1": {AppID: "app-1", P99TimeToFirstTokenMs: 500},
		},
		TakenAt: now,
	}

	result := e.computeEffectiveTarget(context.Background(), app, 3, snap)
	// Zero CreatedAt = non-stale, so no extra replicas.
	assert.Equal(t, 3, result) // running=1, pending=1, additional=max(0,3-1-1)=1
}

func TestComputeEffectiveTarget_NoProfile(t *testing.T) {
	e := newTestEngine()
	app := baseApp("app-1")
	now := time.Date(2025, 1, 1, 0, 10, 0, 0, time.UTC)

	snap := worldstate.WorldStateSnapshot{
		Replicas: map[model.ReplicaID]model.Replica{
			"r1": {ReplicaID: "r1", AppID: "app-1", Status: model.ReplicaStatusRunning},
			"p1": {ReplicaID: "p1", AppID: "app-1", Status: model.ReplicaStatusPending, CreatedAt: now.Add(-1 * time.Hour)},
		},
		PerformanceProfiles: map[model.AppID]model.PerformanceProfile{},
		VLLMMetrics: map[model.AppID]model.VLLMMetrics{
			"app-1": {AppID: "app-1", P99TimeToFirstTokenMs: 500},
		},
		TakenAt: now,
	}

	result := e.computeEffectiveTarget(context.Background(), app, 3, snap)
	// No profile → stale check skipped, no extra replicas.
	assert.Equal(t, 3, result) // running=1, pending=1, additional=1
}

// ---------- applyStabilityControls ----------

func TestApplyStabilityControls_ScaleUpCooldown(t *testing.T) {
	e := newTestEngine()
	app := baseApp("app-1")
	now := time.Date(2025, 1, 1, 0, 5, 0, 0, time.UTC)

	tests := []struct {
		name          string
		lastScaleUpAt time.Time
		wantApproved  bool
		wantTarget    int
	}{
		{
			name:          "within cooldown suppresses scale-up",
			lastScaleUpAt: now.Add(-500 * time.Millisecond), // within 1s cooldown
			wantApproved:  false,
			wantTarget:    4,
		},
		{
			name:          "at boundary allows scale-up",
			lastScaleUpAt: now.Add(-1 * time.Second), // exactly at 1s boundary
			wantApproved:  true,
			wantTarget:    6,
		},
		{
			name:          "past cooldown allows scale-up",
			lastScaleUpAt: now.Add(-180 * time.Second),
			wantApproved:  true,
			wantTarget:    6,
		},
		{
			name:          "zero LastScaleUpAt allows scale-up",
			lastScaleUpAt: time.Time{},
			wantApproved:  true,
			wantTarget:    6,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			snap := worldstate.WorldStateSnapshot{
				ScalingHistory: map[model.AppID]model.ScalingHistory{
					"app-1": {AppID: "app-1", LastScaleUpAt: tt.lastScaleUpAt},
				},
				VLLMMetrics: map[model.AppID]model.VLLMMetrics{
					"app-1": idleMetrics("app-1"),
				},
				TakenAt: now,
			}

			decision := e.applyStabilityControls(context.Background(), "app-1", app, 6, 4, snap)
			assert.Equal(t, tt.wantApproved, decision.ScaleActionApproved)
			assert.Equal(t, tt.wantTarget, decision.TargetCount)
		})
	}
}

func TestApplyStabilityControls_ScaleDownCooldown(t *testing.T) {
	e := newTestEngine()
	app := baseApp("app-1")
	now := time.Date(2025, 1, 1, 0, 10, 0, 0, time.UTC)

	tests := []struct {
		name            string
		lastScaleDownAt time.Time
		wantApproved    bool
		wantTarget      int
	}{
		{
			name:            "within cooldown suppresses scale-down",
			lastScaleDownAt: now.Add(-2 * time.Second),
			wantApproved:    false,
			wantTarget:      4,
		},
		{
			name:            "at boundary allows scale-down with enough signals",
			lastScaleDownAt: now.Add(-3 * time.Second),
			wantApproved:    true,
			wantTarget:      2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			snap := worldstate.WorldStateSnapshot{
				ScalingHistory: map[model.AppID]model.ScalingHistory{
					"app-1": {
						AppID:                       "app-1",
						LastScaleDownAt:             tt.lastScaleDownAt,
						ConsecutiveScaleDownSignals: 5, // enough signals
					},
				},
				VLLMMetrics: map[model.AppID]model.VLLMMetrics{
					"app-1": idleMetrics("app-1"),
				},
				TakenAt: now,
			}

			decision := e.applyStabilityControls(context.Background(), "app-1", app, 2, 4, snap)
			assert.Equal(t, tt.wantApproved, decision.ScaleActionApproved)
			assert.Equal(t, tt.wantTarget, decision.TargetCount)
		})
	}
}

func TestApplyStabilityControls_StabilizationWindow(t *testing.T) {
	e := newTestEngine()
	app := baseApp("app-1")
	now := time.Date(2025, 1, 1, 0, 10, 0, 0, time.UTC)

	tests := []struct {
		name              string
		consecutiveSignal int
		wantApproved      bool
		wantTarget        int
		wantSignalObs     bool
	}{
		{
			name:              "0 consecutive signals suppresses",
			consecutiveSignal: 0,
			wantApproved:      false,
			wantTarget:        4,
			wantSignalObs:     true,
		},
		{
			name:              "1 consecutive signal suppresses",
			consecutiveSignal: 1,
			wantApproved:      false,
			wantTarget:        4,
			wantSignalObs:     true,
		},
		{
			name:              "2 consecutive signals allows (2+1=3 >= StabilizationCycles)",
			consecutiveSignal: 2,
			wantApproved:      true,
			wantTarget:        2,
			wantSignalObs:     true,
		},
		{
			name:              "5 consecutive signals allows",
			consecutiveSignal: 5,
			wantApproved:      true,
			wantTarget:        2,
			wantSignalObs:     true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			snap := worldstate.WorldStateSnapshot{
				ScalingHistory: map[model.AppID]model.ScalingHistory{
					"app-1": {
						AppID:                       "app-1",
						LastScaleDownAt:             now.Add(-600 * time.Second), // past cooldown
						ConsecutiveScaleDownSignals: tt.consecutiveSignal,
					},
				},
				VLLMMetrics: map[model.AppID]model.VLLMMetrics{
					"app-1": idleMetrics("app-1"),
				},
				TakenAt: now,
			}

			decision := e.applyStabilityControls(context.Background(), "app-1", app, 2, 4, snap)
			assert.Equal(t, tt.wantApproved, decision.ScaleActionApproved, "ScaleActionApproved")
			assert.Equal(t, tt.wantTarget, decision.TargetCount, "TargetCount")
			assert.Equal(t, tt.wantSignalObs, decision.ScaleDownSignalObserved, "ScaleDownSignalObserved")
		})
	}
}

func TestApplyStabilityControls_SLABreachBypass(t *testing.T) {
	e := newTestEngine()
	now := time.Date(2025, 1, 1, 0, 10, 0, 0, time.UTC)

	t.Run("SLA breach bypasses stabilization", func(t *testing.T) {
		app := baseApp("app-1")
		app.SLA.MaxP99TTFTMs = 200

		snap := worldstate.WorldStateSnapshot{
			ScalingHistory: map[model.AppID]model.ScalingHistory{
				"app-1": {
					AppID:                       "app-1",
					LastScaleDownAt:             now.Add(-600 * time.Second),
					ConsecutiveScaleDownSignals: 0, // no previous signals
				},
			},
			VLLMMetrics: map[model.AppID]model.VLLMMetrics{
				"app-1": {AppID: "app-1", P99TimeToFirstTokenMs: 500}, // breach!
			},
			TakenAt: now,
		}

		decision := e.applyStabilityControls(context.Background(), "app-1", app, 2, 4, snap)
		assert.True(t, decision.ScaleActionApproved, "SLA breach should bypass stabilization")
		assert.Equal(t, 2, decision.TargetCount)
		assert.True(t, decision.SLABreach)
	})

	t.Run("SLA breach does NOT bypass cooldown", func(t *testing.T) {
		app := baseApp("app-1")
		app.SLA.MaxP99TTFTMs = 200

		snap := worldstate.WorldStateSnapshot{
			ScalingHistory: map[model.AppID]model.ScalingHistory{
				"app-1": {
					AppID:           "app-1",
					LastScaleDownAt: now.Add(-2 * time.Second), // within cooldown
				},
			},
			VLLMMetrics: map[model.AppID]model.VLLMMetrics{
				"app-1": {AppID: "app-1", P99TimeToFirstTokenMs: 500},
			},
			TakenAt: now,
		}

		decision := e.applyStabilityControls(context.Background(), "app-1", app, 2, 4, snap)
		assert.False(t, decision.ScaleActionApproved, "SLA breach should NOT bypass cooldown")
		assert.Equal(t, 4, decision.TargetCount)
	})
}

// ---------- ComputeTargets (integration) ----------

func TestComputeTargets_SkipsAppsWithNoMetrics(t *testing.T) {
	e := newTestEngine()

	snap := worldstate.WorldStateSnapshot{
		Applications: map[model.AppID]model.Application{
			"app-1": baseApp("app-1"),
			"app-2": baseApp("app-2"),
		},
		VLLMMetrics: map[model.AppID]model.VLLMMetrics{
			"app-1": idleMetrics("app-1"),
			// app-2 has no metrics
		},
		Replicas: map[model.ReplicaID]model.Replica{
			"r1": {ReplicaID: "r1", AppID: "app-1", Status: model.ReplicaStatusRunning},
		},
		PerformanceProfiles: map[model.AppID]model.PerformanceProfile{},
		ScalingHistory:      map[model.AppID]model.ScalingHistory{},
		TakenAt:             time.Now(),
	}

	results := e.ComputeTargets(context.Background(), snap)
	assert.Contains(t, results, model.AppID("app-1"))
	assert.NotContains(t, results, model.AppID("app-2"))
}

func TestComputeTargets_Integration(t *testing.T) {
	e := newTestEngine()
	now := time.Date(2025, 1, 1, 0, 10, 0, 0, time.UTC)

	snap := worldstate.WorldStateSnapshot{
		Applications: map[model.AppID]model.Application{
			"app-queue": {
				AppID:       "app-queue",
				MinReplicas: 1,
				SLA:         model.SLA{AppID: "app-queue", MaxP99TTFTMs: 200},
			},
			"app-idle": {
				AppID:       "app-idle",
				MinReplicas: 2,
				SLA:         model.SLA{AppID: "app-idle", MaxP99TTFTMs: 200},
			},
			"app-stable": {
				AppID:       "app-stable",
				MinReplicas: 1,
				SLA:         model.SLA{AppID: "app-stable", MaxP99TTFTMs: 200},
			},
		},
		VLLMMetrics: map[model.AppID]model.VLLMMetrics{
			"app-queue": {
				AppID:                 "app-queue",
				AvgWaitingQueueDepth:  25.0,
				MaxKVCacheUtilization: 0.5,
				AvgKVCacheUtilization: 0.5,
				AvgBatchUtilization:   0.5,
				P99TimeToFirstTokenMs: 100,
			},
			"app-idle": {
				AppID:                 "app-idle",
				AvgWaitingQueueDepth:  0.1,
				MaxKVCacheUtilization: 0.1,
				AvgKVCacheUtilization: 0.1,
				AvgBatchUtilization:   0.05,
				P99TimeToFirstTokenMs: 50,
			},
			"app-stable": {
				AppID:                 "app-stable",
				AvgWaitingQueueDepth:  3.0,
				MaxKVCacheUtilization: 0.5,
				AvgKVCacheUtilization: 0.5,
				AvgBatchUtilization:   0.5,
				P99TimeToFirstTokenMs: 100,
			},
		},
		Replicas: map[model.ReplicaID]model.Replica{
			"rq1": {ReplicaID: "rq1", AppID: "app-queue", Status: model.ReplicaStatusRunning},
			"rq2": {ReplicaID: "rq2", AppID: "app-queue", Status: model.ReplicaStatusRunning},
			"rq3": {ReplicaID: "rq3", AppID: "app-queue", Status: model.ReplicaStatusRunning},
			"ri1": {ReplicaID: "ri1", AppID: "app-idle", Status: model.ReplicaStatusRunning},
			"ri2": {ReplicaID: "ri2", AppID: "app-idle", Status: model.ReplicaStatusRunning},
			"ri3": {ReplicaID: "ri3", AppID: "app-idle", Status: model.ReplicaStatusRunning},
			"ri4": {ReplicaID: "ri4", AppID: "app-idle", Status: model.ReplicaStatusRunning},
			"rs1": {ReplicaID: "rs1", AppID: "app-stable", Status: model.ReplicaStatusRunning},
			"rs2": {ReplicaID: "rs2", AppID: "app-stable", Status: model.ReplicaStatusRunning},
		},
		PerformanceProfiles: map[model.AppID]model.PerformanceProfile{},
		ScalingHistory:      map[model.AppID]model.ScalingHistory{},
		TakenAt:             now,
	}

	results := e.ComputeTargets(context.Background(), snap)
	require.Len(t, results, 3)

	// app-queue: queue=25 > 10, factor=25/5=5, required=ceil(3*5)=15, capped at 3+10=13
	queueDecision := results["app-queue"]
	assert.Equal(t, ScaleDirectionUp, queueDecision.Direction)
	assert.Equal(t, 13, queueDecision.TargetCount)
	assert.True(t, queueDecision.ScaleActionApproved)

	// app-idle: all metrics low, current=4, min=2
	// utilization = max(0.05, 0.1, 0.1/5) = 0.1
	// desired = max(ceil(4*0.1), 2) = max(1, 2) = 2
	// dampened: max(2, 4-2) = 2
	// scale-down signal observed but suppressed by stabilization (0 consecutive signals)
	idleDecision := results["app-idle"]
	assert.Equal(t, ScaleDirectionUnchanged, idleDecision.Direction)
	assert.Equal(t, 4, idleDecision.TargetCount) // suppressed by stabilization

	// app-stable: no pressure, current = target = 2
	stableDecision := results["app-stable"]
	assert.Equal(t, ScaleDirectionUnchanged, stableDecision.Direction)
	assert.Equal(t, 2, stableDecision.TargetCount)
}

func TestComputeTargets_QueueTargetZero(t *testing.T) {
	cfg := DefaultScalingConfig()
	cfg.QueueTarget = 0 // edge case: avoid division by zero
	e := NewScalingEngine(cfg, observability.NewNoopTracer(), prometheus.NewRegistry())

	snap := worldstate.WorldStateSnapshot{
		Applications: map[model.AppID]model.Application{
			"app-1": baseApp("app-1"),
		},
		VLLMMetrics: map[model.AppID]model.VLLMMetrics{
			"app-1": {
				AppID:                 "app-1",
				AvgWaitingQueueDepth:  20.0,
				MaxKVCacheUtilization: 0.3,
				AvgKVCacheUtilization: 0.3,
				AvgBatchUtilization:   0.2,
				P99TimeToFirstTokenMs: 100,
			},
		},
		Replicas: map[model.ReplicaID]model.Replica{
			"r1": {ReplicaID: "r1", AppID: "app-1", Status: model.ReplicaStatusRunning},
		},
		PerformanceProfiles: map[model.AppID]model.PerformanceProfile{},
		ScalingHistory:      map[model.AppID]model.ScalingHistory{},
		TakenAt:             time.Now(),
	}

	// Should not panic.
	results := e.ComputeTargets(context.Background(), snap)
	assert.Contains(t, results, model.AppID("app-1"))
}

// ---------- helpers ----------

func TestIsSLABreach(t *testing.T) {
	tests := []struct {
		name   string
		slaMax int
		p99    float64
		want   bool
	}{
		{"breach", 200, 300, true},
		{"no breach", 200, 100, false},
		{"sla disabled", 0, 9999, false},
		{"exactly at sla", 200, 200, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			app := model.Application{SLA: model.SLA{MaxP99TTFTMs: tt.slaMax}}
			metrics := model.VLLMMetrics{P99TimeToFirstTokenMs: tt.p99}
			assert.Equal(t, tt.want, isSLABreach(app, metrics))
		})
	}
}

func TestCeilInt(t *testing.T) {
	assert.Equal(t, 4, ceilInt(3.1))
	assert.Equal(t, 3, ceilInt(3.0))
	assert.Equal(t, 1, ceilInt(0.1))
	assert.Equal(t, 0, ceilInt(0.0))
}

func TestCountReplicasByStatus(t *testing.T) {
	snap := worldstate.WorldStateSnapshot{
		Replicas: map[model.ReplicaID]model.Replica{
			"r1": {ReplicaID: "r1", AppID: "app-1", Status: model.ReplicaStatusRunning},
			"r2": {ReplicaID: "r2", AppID: "app-1", Status: model.ReplicaStatusRunning},
			"r3": {ReplicaID: "r3", AppID: "app-1", Status: model.ReplicaStatusPending},
			"r4": {ReplicaID: "r4", AppID: "app-2", Status: model.ReplicaStatusRunning},
		},
	}
	assert.Equal(t, 2, countReplicasByStatus("app-1", snap, model.ReplicaStatusRunning))
	assert.Equal(t, 1, countReplicasByStatus("app-1", snap, model.ReplicaStatusPending))
	assert.Equal(t, 1, countReplicasByStatus("app-2", snap, model.ReplicaStatusRunning))
	assert.Equal(t, 0, countReplicasByStatus("app-3", snap, model.ReplicaStatusRunning))
}
