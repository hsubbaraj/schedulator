package main

import (
	"context"
	"flag"
	"log"
	"os"

	"gopkg.in/yaml.v3"
	"go.opentelemetry.io/otel/trace/noop"

	"github.com/hsubbaraj/schedulator/internal/worldstate"
	"github.com/hsubbaraj/schedulator/test/simulator/config"
	"github.com/hsubbaraj/schedulator/test/simulator/core"
	"github.com/prometheus/client_golang/prometheus"
)

func main() {
	configPath := flag.String("config", "", "Path to scenario config file")
	outputPath := flag.String("output", "simulation_results.csv", "Path to output CSV file")
	flag.Parse()

	actualConfigPath := *configPath
	if actualConfigPath == "" {
		if flag.NArg() > 0 {
			actualConfigPath = flag.Arg(0)
		} else {
			actualConfigPath = "test/simulator/config/simple.yaml"
		}
	}

	// Load config
	cfgBytes, err := os.ReadFile(actualConfigPath)
	if err != nil {
		log.Fatalf("failed to read config: %v", err)
	}
	var cfg config.Scenario
	if err := yaml.Unmarshal(cfgBytes, &cfg); err != nil {
		log.Fatalf("failed to parse config: %v", err)
	}

	// Initialize components
	tracer := noop.NewTracerProvider().Tracer("simulator")
	reg := prometheus.NewRegistry()
	ws := worldstate.New(tracer, reg)

	traffic, err := core.NewTrafficGenerator(cfg.Workload, cfg.Apps)
	if err != nil {
		log.Fatalf("failed to load traffic: %v", err)
	}

	metrics, err := core.NewMetricsCollector(*outputPath)
	if err != nil {
		log.Fatalf("failed to create metrics collector: %v", err)
	}
	defer metrics.Close()

	sim, err := core.NewSimulator(cfg, ws, traffic, metrics, tracer)
	if err != nil {
		log.Fatalf("failed to create simulator: %v", err)
	}

	log.Printf("Starting simulation: %s (%s)", cfg.Name, cfg.Duration)
	if err := sim.Run(context.Background()); err != nil {
		log.Fatalf("simulation failed: %v", err)
	}
	log.Printf("Simulation complete. Results written to %s", *outputPath)
	metrics.ReportScore()
}
