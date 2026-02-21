package core

import (
	"context"
	"encoding/csv"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/hsubbaraj/schedulator/internal/engine/scaling"
	"github.com/hsubbaraj/schedulator/internal/worldstate"
	"github.com/hsubbaraj/schedulator/pkg/model"
)

const (
	WeightSLA         = 1000.0
	WeightUtilization = 100.0
	WeightChurn       = 10.0
)

type ScoreCard struct {
	TotalTicks     int
	SLAViolations  int
	TotalGPUs      int
	UsedGPUs       int
	ScalingOps     int
	TotalCost      float64
}

type MetricsCollector struct {
	w         *csv.Writer
	f         *os.File
	scoreCard ScoreCard
}

func NewMetricsCollector(path string) (*MetricsCollector, error) {
	f, err := os.Create(path)
	if err != nil {
		return nil, err
	}
	w := csv.NewWriter(f)
	if err := w.Write([]string{"timestamp", "app_id", "target_replicas", "running_replicas", "pending_replicas", "queue_depth", "sla_breach"}); err != nil {
		return nil, err
	}
	return &MetricsCollector{w: w, f: f}, nil
}

func (mc *MetricsCollector) Collect(
	ctx context.Context,
	ws *worldstate.WorldState,
	simTime time.Time,
	decisions map[model.AppID]scaling.ScalingDecision,
) error {
	snap := ws.Snapshot(ctx)
	mc.scoreCard.TotalTicks++

	totalGPUs := 0
	for _, c := range snap.Clusters {
		for _, n := range c.Nodes {
			totalGPUs += n.TotalGPUs
		}
	}
	mc.scoreCard.TotalGPUs += totalGPUs

	usedGPUs := 0
	for _, r := range snap.Replicas {
		if r.Status == model.ReplicaStatusRunning {
			usedGPUs += r.GPUs
		}
	}
	mc.scoreCard.UsedGPUs += usedGPUs

	for appID := range snap.Applications {
		metrics, ok := snap.VLLMMetrics[appID]
		if !ok {
			metrics = model.VLLMMetrics{}
		}

		target := 0
		if d, ok := decisions[appID]; ok {
			target = d.TargetCount
			if d.TargetCount != countReplicas(snap, appID, model.ReplicaStatusRunning)+countReplicas(snap, appID, model.ReplicaStatusPending) {
				// Simplified churn detection: if target != current total, it's an op
				mc.scoreCard.ScalingOps++
			}
		}

		running := countReplicas(snap, appID, model.ReplicaStatusRunning)
		pending := countReplicas(snap, appID, model.ReplicaStatusPending)

		slaBreach := false
		// SLA breach: if P99 TTFT > App SLA
		if app, ok := snap.Applications[appID]; ok && app.SLA.MaxP99TTFTMs > 0 {
			if metrics.P99TimeToFirstTokenMs > float64(app.SLA.MaxP99TTFTMs) {
				slaBreach = true
				mc.scoreCard.SLAViolations++
			}
		}

		record := []string{
			simTime.Format(time.RFC3339),
			string(appID),
			strconv.Itoa(target),
			strconv.Itoa(running),
			strconv.Itoa(pending),
			fmt.Sprintf("%.2f", metrics.AvgWaitingQueueDepth),
			strconv.FormatBool(slaBreach),
		}
		if err := mc.w.Write(record); err != nil {
			return err
		}
	}
	mc.w.Flush()
	return nil
}

func (mc *MetricsCollector) ReportScore() {
	avgUtil := 0.0
	if mc.scoreCard.TotalGPUs > 0 {
		avgUtil = float64(mc.scoreCard.UsedGPUs) / float64(mc.scoreCard.TotalGPUs)
	}

	slaCost := float64(mc.scoreCard.SLAViolations) * WeightSLA
	utilCost := (1.0 - avgUtil) * WeightUtilization
	churnCost := float64(mc.scoreCard.ScalingOps) * WeightChurn
	totalCost := slaCost + utilCost + churnCost

	fmt.Println("\n--- Simulation Scorecard ---")
	fmt.Printf("Total Ticks:      %d\n", mc.scoreCard.TotalTicks)
	fmt.Printf("SLA Violations:   %d (Cost: %.2f)\n", mc.scoreCard.SLAViolations, slaCost)
	fmt.Printf("Avg Utilization:  %.2f%% (Cost: %.2f)\n", avgUtil*100, utilCost)
	fmt.Printf("Scaling Ops:      %d (Cost: %.2f)\n", mc.scoreCard.ScalingOps, churnCost)
	fmt.Printf("Total Cost:       %.2f\n", totalCost)
	fmt.Println("----------------------------")
}

func (mc *MetricsCollector) Close() {
	mc.w.Flush()
	mc.f.Close()
}

func countReplicas(snap worldstate.WorldStateSnapshot, appID model.AppID, status model.ReplicaStatus) int {
	count := 0
	for _, r := range snap.Replicas {
		if r.AppID == appID && r.Status == status {
			count++
		}
	}
	return count
}
