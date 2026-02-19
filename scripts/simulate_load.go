package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

type StateResponse struct {
	Applications map[string]interface{} `json:"Applications"`
	Replicas     map[string]Replica     `json:"Replicas"`
}

type Replica struct {
	AppID  string `json:"AppID"`
	Status string `json:"Status"`
}

type VLLMMetrics struct {
	AppID                string    `json:"AppID"`
	AvgWaitingQueueDepth float64   `json:"AvgWaitingQueueDepth"`
	MeasuredAt           time.Time `json:"MeasuredAt"`
}

func main() {
	appID := flag.String("app", "llama-70b", "Application ID to simulate load for")
	totalLoad := flag.Float64("load", 5.0, "Baseline waiting request load")
	burstLoad := flag.Float64("burst-load", 50.0, "Total waiting request load during burst")
	burstAfter := flag.Duration("burst-after", 5*time.Second, "Start burst after this duration")
	burstDuration := flag.Duration("burst-duration", 30*time.Second, "How long the burst lasts")
	interval := flag.Duration("interval", 2*time.Second, "Update interval")
	endpoint := flag.String("endpoint", "http://localhost:8080", "Schedulator API endpoint")
	flag.Parse()

	startTime := time.Now()
	ticker := time.NewTicker(*interval)
	defer ticker.Stop()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	fmt.Printf("Starting load simulation for %s\n", *appID)
	fmt.Printf("  Baseline: %.1f, Burst: %.1f\n", *totalLoad, *burstLoad)
	fmt.Printf("  Burst starts in %v and lasts for %v\n", *burstAfter, *burstDuration)

	for {
		select {
		case <-sigCh:
			fmt.Println("\nStopping simulation...")
			return
		case <-ticker.C:
			elapsed := time.Since(startTime)
			currentTotalLoad := *totalLoad

			if elapsed > *burstAfter && elapsed < (*burstAfter+*burstDuration) {
				currentTotalLoad = *burstLoad
				fmt.Printf("[BURST] ")
			}

			runningCount := getRunningReplicaCount(*endpoint, *appID)
			if runningCount == 0 {
				runningCount = 1
			}

			avgQueueDepth := currentTotalLoad / float64(runningCount)
			fmt.Printf("[%s] Replicas: %d, Total Load: %.1f, Avg Queue Depth: %.2f\n", 
				time.Now().Format("15:04:05"), runningCount, currentTotalLoad, avgQueueDepth)

			injectMetrics(*endpoint, *appID, avgQueueDepth)
		}
	}
}

func getRunningReplicaCount(endpoint, appID string) int {
	resp, err := http.Get(endpoint + "/api/v1/state")
	if err != nil {
		log.Printf("Error fetching state: %v", err)
		return 0
	}
	defer resp.Body.Close()

	var state StateResponse
	if err := json.NewDecoder(resp.Body).Decode(&state); err != nil {
		log.Printf("Error decoding state: %v", err)
		return 0
	}

	count := 0
	for _, r := range state.Replicas {
		if r.AppID == appID && r.Status == "Running" {
			count++
		}
	}
	return count
}

func injectMetrics(endpoint, appID string, queueDepth float64) {
	m := VLLMMetrics{
		AppID:                appID,
		AvgWaitingQueueDepth: queueDepth,
		MeasuredAt:           time.Now(),
	}

	data, _ := json.Marshal(m)
	resp, err := http.Post(endpoint+"/api/v1/inject/metrics", "application/json", bytes.NewBuffer(data))
	if err != nil {
		log.Printf("Error injecting metrics: %v", err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusAccepted {
		log.Printf("Injection rejected: %s", resp.Status)
	}
}
