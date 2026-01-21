package main

import (
	"context"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/yourorg/scheduler-simulator/pkg/k8s/client"
	"github.com/yourorg/scheduler-simulator/pkg/simulator/api"
	"github.com/yourorg/scheduler-simulator/pkg/simulator/enforcer"
	"github.com/yourorg/scheduler-simulator/pkg/simulator/state"
	"github.com/yourorg/scheduler-simulator/pkg/simulator/workload"
)

var (
	port       = flag.Int("port", 8080, "API server port")
	scenarioPath = flag.String("scenario", "", "Path to a scenario YAML file to run")
)

func main() {
	flag.Parse()

	log.SetOutput(os.Stdout)

	// 1. Initialize Client Manager
	clientManager := client.NewClientManager()
	contexts := []string{"kind-cluster-1", "kind-cluster-2", "kind-cluster-3"}
	
	for _, ctx := range contexts {
		if err := clientManager.AddCluster(ctx); err != nil {
			log.Printf("⚠️  Warning: Failed to connect to %s: %v", ctx, err)
		} else {
			log.Printf("✅ Connected to %s", ctx)
		}
	}

	// 2. Initialize State Manager
	stateManager := state.NewManager(clientManager)

	// 3. Start State Aggregation Loop (Background)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go stateManager.UpdateLoop(ctx, 5*time.Second)

	// 4. Initialize Enforcer and API Server
	enforcer := enforcer.NewEnforcer(clientManager, stateManager)
	apiServer := api.NewServer(*port, stateManager, enforcer)

	// 5. Initialize and Start Workload Driver (if scenario is provided)
	var driver *workload.Driver
	if *scenarioPath != "" {
		// Clean up previous deployments before starting a new scenario
		enforcer.CleanupDeployments(ctx)
		if err := enforcer.WaitForDeploymentsDeleted(ctx, 60*time.Second); err != nil {
			log.Fatalf("Error waiting for old deployments to be deleted: %v", err)
		}

		scenario, err := workload.LoadScenario(*scenarioPath)
		if err != nil {
			log.Fatalf("Failed to load scenario %s: %v", *scenarioPath, err)
		}
		driver = workload.NewDriver(scenario, stateManager)
		go driver.Start(ctx)
	}

	// 6. Start API Server
	go func() {
		if err := apiServer.Start(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("Server error: %v", err)
		}
	}()

	// Graceful shutdown
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)

	<-sigCh
	log.Println("Shutting down...")
	
	// Stop state loop
	cancel()
	
	// Stop workload driver if it was started
	if driver != nil {
		driver.Stop()
	}

	// Stop HTTP server
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()
	apiServer.Shutdown(shutdownCtx)
}
