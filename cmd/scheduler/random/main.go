package main

import (
	"flag"
	"log"
	"math/rand"
	"time"

	"github.com/yourorg/scheduler-simulator/pkg/scheduler/client"
	"github.com/yourorg/scheduler-simulator/pkg/scheduler/types"
)

var (
	simulatorURL = flag.String("simulator-url", "http://localhost:8080", "Simulator API URL")
)

func main() {
	flag.Parse()
	rand.Seed(time.Now().UnixNano())

	log.Printf("🎲 RandomCluster scheduler starting")
	log.Printf("   Simulator: %s", *simulatorURL)

	simClient := client.NewClient(*simulatorURL)
	lastETag := ""

	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		if err := makeDecision(simClient, &lastETag); err != nil {
			log.Printf("❌ Error: %v", err)
		}
	}
}

func makeDecision(simClient *client.Client, lastETag *string) error {
	// 1. Fetch state
	state, etag, err := simClient.GetState(*lastETag)
	if err != nil {
		return err
	}
	if state == nil {
		// No change (304)
		return nil
	}
	*lastETag = etag

	if len(state.PendingQueue) > 0 {
		log.Printf("📥 Fetched state v%s with %d pending workloads",
			state.Version, len(state.PendingQueue))
	}

	// 2. Make random decisions
	if len(state.PendingQueue) == 0 {
		return nil // Nothing to do
	}

	decisions := []types.Decision{}
	explains := []types.Explanation{}

	for _, workload := range state.PendingQueue {
		// Pick random cluster
		if len(state.Clusters) == 0 {
			log.Println("⚠️ No clusters available to schedule")
			break
		}
		
		cluster := state.Clusters[rand.Intn(len(state.Clusters))]

		decisions = append(decisions, types.Decision{
			WorkloadID: workload.WorkloadID,
			Cluster:    cluster.ID,
			Replicas:   workload.Replicas,
		})

		explains = append(explains, types.Explanation{
			WorkloadID:   workload.WorkloadID,
			Reason:       "random_selection",
			SolverTimeMs: 0,
		})
	}

	// 3. Submit decision
	placement := &types.PlacementDecision{
		StateVersion:  state.Version,
		PlacementMode: "ClusterOnly",
		Decisions:     decisions,
		Explain:       explains,
	}

		result, err := simClient.SubmitPlacement(placement)

		if err != nil {

			return err

		}

	

		log.Printf("✅ Submitted %d decisions. Result: %s", len(decisions), result.Message)

	

		// If decisions were accepted, force a state refresh on next iteration

		if result.Accepted {

			*lastETag = ""

		}

		return nil

	}

	