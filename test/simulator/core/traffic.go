package core

import (
	"context"
	"encoding/csv"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/hsubbaraj/schedulator/internal/worldstate"
	"github.com/hsubbaraj/schedulator/pkg/model"
	"github.com/hsubbaraj/schedulator/test/simulator/config"
)

type TrafficEntry struct {
	Timestamp  int
	AppID      string
	RPS        float64
	QueueDepth float64
	KvUtil     float64
}

type TrafficGenerator struct {
	entries       []TrafficEntry
	appConfigs    []config.AppConfig
	currentQueues map[string]float64
	lastTick      time.Time
}

func NewTrafficGenerator(path string, appConfigs []config.AppConfig) (*TrafficGenerator, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	r := csv.NewReader(f)
	records, err := r.ReadAll()
	if err != nil {
		return nil, err
	}

	var entries []TrafficEntry
	for i, record := range records {
		if i == 0 {
			continue
		} // Header
		if len(record) < 5 {
			continue
		}

		ts, err := strconv.Atoi(record[0])
		if err != nil {
			return nil, fmt.Errorf("line %d: invalid timestamp", i+1)
		}
		rps, _ := strconv.ParseFloat(record[2], 64)
		qd, _ := strconv.ParseFloat(record[3], 64)
		kv, _ := strconv.ParseFloat(record[4], 64)

		entries = append(entries, TrafficEntry{
			Timestamp:  ts,
			AppID:      record[1],
			RPS:        rps,
			QueueDepth: qd,
			KvUtil:     kv,
		})
	}
	return &TrafficGenerator{
		entries:       entries,
		appConfigs:    appConfigs,
		currentQueues: make(map[string]float64),
	}, nil
}

func (tg *TrafficGenerator) UpdateMetrics(ws *worldstate.WorldState, simTime time.Time, startTime time.Time, closedLoop bool) {
	elapsed := int(simTime.Sub(startTime).Seconds())

	latest := make(map[string]TrafficEntry)
	for _, e := range tg.entries {
		if e.Timestamp <= elapsed {
			latest[e.AppID] = e
		}
	}

	tickDuration := 1.0 // Default 1s if first tick
	if !tg.lastTick.IsZero() {
		tickDuration = simTime.Sub(tg.lastTick).Seconds()
	}
	tg.lastTick = simTime

	snap := ws.Snapshot(context.Background())

	for appID, entry := range latest {
		queueDepth := entry.QueueDepth
		if closedLoop {
			// Find app config
			var appCfg config.AppConfig
			for _, cfg := range tg.appConfigs {
				if cfg.ID == appID {
					appCfg = cfg
					break
				}
			}

			runningReplicas := 0
			for _, r := range snap.Replicas {
				if string(r.AppID) == appID && r.Status == model.ReplicaStatusRunning {
					// Only count replicas on healthy nodes.
					if cluster, ok := snap.Clusters[r.ClusterID]; ok {
						if node, ok := cluster.Nodes[r.NodeID]; ok && node.Status == model.NodeStatusReady {
							runningReplicas++
						}
					}
				}
			}

			newRequests := entry.RPS * tickDuration
			processed := float64(runningReplicas) * appCfg.ThroughputPerReplica * tickDuration
			
			currentQ := tg.currentQueues[appID]
			currentQ = currentQ + newRequests - processed
			if currentQ < 0 {
				currentQ = 0
			}
			if appCfg.MaxQueueDepth > 0 && currentQ > appCfg.MaxQueueDepth {
				currentQ = appCfg.MaxQueueDepth
			}
			tg.currentQueues[appID] = currentQ
			queueDepth = currentQ
		}

		// Calculate P99 TTFT: (QueueDepth / ThroughputPerReplica) * 1000ms
		// This is a simplified model assuming queue is processed linearly.
		ttft := 0.0
		var appCfg config.AppConfig
		for _, cfg := range tg.appConfigs {
			if cfg.ID == appID {
				appCfg = cfg
				break
			}
		}
		if appCfg.ThroughputPerReplica > 0 {
			ttft = (queueDepth / appCfg.ThroughputPerReplica) * 1000.0
		}

		ws.UpdateVLLMMetrics(model.VLLMMetrics{
			AppID:                 model.AppID(appID),
			AvgWaitingQueueDepth:  queueDepth,
			AvgKVCacheUtilization: entry.KvUtil,
			P99TimeToFirstTokenMs: ttft,
			MeasuredAt:            simTime,
		})
	}
}
