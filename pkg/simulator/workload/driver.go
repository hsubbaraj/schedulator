package workload

import (
	"context"
	"log"
	"time"

	"github.com/yourorg/scheduler-simulator/pkg/simulator/state"
)

// Driver reads a scenario and injects workloads into the simulator's pending queue.
type Driver struct {
	scenario     *Scenario
	stateManager *state.Manager
	stopCh       chan struct{}
	doneCh       chan struct{}
}

// NewDriver creates a new Workload Driver.
func NewDriver(s *Scenario, sm *state.Manager) *Driver {
	return &Driver{
		scenario:     s,
		stateManager: sm,
		stopCh:       make(chan struct{}),
		doneCh:       make(chan struct{}),
	}
}

// Start begins injecting workloads according to the scenario.
func (d *Driver) Start(ctx context.Context) {
	log.Printf("🚀 Starting workload driver for scenario: %s (Duration: %s)", d.scenario.Name, d.scenario.Duration)

	workloadTimers := make(map[string]*time.Timer)

	go func() {
		defer close(d.doneCh)
		defer log.Println("Workload driver goroutine finished")

		// Inject workloads based on their delay
		for _, sw := range d.scenario.Workloads {
			sw := sw // Capture local copy for closure
			duration, err := time.ParseDuration(sw.Delay)
			if err != nil {
				log.Printf("⚠️  Skipping workload %s due to invalid delay '%s': %v", sw.WorkloadID, sw.Delay, err)
				continue
			}

			timer := time.AfterFunc(duration, func() {
				log.Printf("➕ Injecting workload: %s (Replicas: %d, GPUs: %d)", sw.Workload.WorkloadID, sw.Workload.Replicas, sw.Workload.GPUsPerReplica)
				d.stateManager.AddPendingWorkload(sw.Workload)
			})
			workloadTimers[sw.WorkloadID] = timer
		}

		// Wait for scenario duration or stop signal
		select {
		case <-time.After(d.scenario.Duration):
			log.Printf("✅ Scenario %s completed after %s", d.scenario.Name, d.scenario.Duration)
		case <-d.stopCh:
			log.Printf("🛑 Workload driver for scenario %s stopped prematurely", d.scenario.Name)
		case <-ctx.Done():
			log.Printf("🛑 Workload driver for scenario %s stopped due to context cancellation", d.scenario.Name)
		}

		// Stop any remaining timers
		for _, timer := range workloadTimers {
			timer.Stop()
		}
	}()
}

// Stop signals the driver to stop injecting workloads.
func (d *Driver) Stop() {
	close(d.stopCh)
	<-d.doneCh // Wait for the goroutine to finish
}

// IsDone returns true if the scenario has completed.
func (d *Driver) IsDone() bool {
	select {
	case <-d.doneCh:
		return true
	default:
		return false
	}
}