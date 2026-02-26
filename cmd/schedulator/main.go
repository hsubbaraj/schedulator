package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.opentelemetry.io/otel/trace"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/hsubbaraj/schedulator/internal/apiserver"
	"github.com/hsubbaraj/schedulator/internal/configstore"
	"github.com/hsubbaraj/schedulator/internal/controlloop"
	"github.com/hsubbaraj/schedulator/internal/engine/heuristic"
	"github.com/hsubbaraj/schedulator/internal/engine/placement"
	"github.com/hsubbaraj/schedulator/internal/engine/preemption"
	"github.com/hsubbaraj/schedulator/internal/engine/rebalancing"
	"github.com/hsubbaraj/schedulator/internal/engine/scaling"
	"github.com/hsubbaraj/schedulator/internal/engine/shadow"
	"github.com/hsubbaraj/schedulator/internal/engine/solver"
	"github.com/hsubbaraj/schedulator/internal/eventlog"
	"github.com/hsubbaraj/schedulator/internal/executor"
	"github.com/hsubbaraj/schedulator/internal/ingestion"
	"github.com/hsubbaraj/schedulator/internal/k8saggregator"
	"github.com/hsubbaraj/schedulator/internal/k8sclient"
	"github.com/hsubbaraj/schedulator/internal/leaderelect"
	"github.com/hsubbaraj/schedulator/internal/observability"
	"github.com/hsubbaraj/schedulator/internal/plangen"
	"github.com/hsubbaraj/schedulator/internal/worldstate"
	"github.com/hsubbaraj/schedulator/pkg/model"
	"github.com/hsubbaraj/schedulator/pkg/ports"
)

// Set via -ldflags at build time.
var (
	version = "dev"
	commit  = "none"
)

type healthResponse struct {
	Status string `json:"status"`
}

func newMux(reg *prometheus.Registry) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(healthResponse{Status: "ok"})
	})
	mux.Handle("/metrics", promhttp.HandlerFor(reg, promhttp.HandlerOpts{}))
	return mux
}

// appConfig holds environment-variable-driven configuration for the scheduler.
type appConfig struct {
	PlannerMode     string
	ShadowPlanner   string
	SolverTimeoutMs int
	ShadowTimeoutMs int
	MaxShadowSlots  int
	SolverSeed      uint64
}

func loadAppConfig() appConfig {
	cfg := appConfig{
		PlannerMode:     getEnvOrDefault("SCHEDULATOR_PLANNER_MODE", "heuristic"),
		ShadowPlanner:   getEnvOrDefault("SCHEDULATOR_SHADOW_PLANNER", "solver"),
		SolverTimeoutMs: getEnvIntOrDefault("SCHEDULATOR_SOLVER_TIMEOUT_MS", 200),
		ShadowTimeoutMs: getEnvIntOrDefault("SCHEDULATOR_SHADOW_TIMEOUT_MS", 500),
		MaxShadowSlots:  getEnvIntOrDefault("SCHEDULATOR_MAX_SHADOW_SLOTS", 2),
	}
	if seed := os.Getenv("SCHEDULATOR_SOLVER_RANDOM_SEED"); seed != "" {
		if v, err := strconv.ParseUint(seed, 10, 64); err == nil {
			cfg.SolverSeed = v
		}
	}
	return cfg
}

func getEnvOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func getEnvIntOrDefault(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if i, err := strconv.Atoi(v); err == nil {
			return i
		}
	}
	return def
}

// buildPlanner constructs the active planner based on SCHEDULATOR_PLANNER_MODE.
func buildPlanner(
	cfg appConfig,
	placementEngine *placement.PlacementEngine,
	rebalancingEngine *rebalancing.RebalancingEngine,
	tracer trace.Tracer,
	reg prometheus.Registerer,
) ports.Planner {
	solverCfg := solver.DefaultSolverConfig()
	solverCfg.TimeoutMs = cfg.SolverTimeoutMs
	solverCfg.MaxShadowSlots = cfg.MaxShadowSlots
	solverCfg.RandomSeed = cfg.SolverSeed

	heuristicPlanner := heuristic.NewHeuristicPlanner(placementEngine, rebalancingEngine, tracer, reg)

	switch cfg.PlannerMode {
	case "solver":
		return solver.NewSolverPlanner(solverCfg, rebalancingEngine, heuristicPlanner, tracer, reg)

	case "shadow":
		var shadowImpl ports.Planner
		primary := ports.Planner(heuristicPlanner)

		if cfg.ShadowPlanner == "heuristic" {
			// Shadow compares solver (primary) vs heuristic (shadow).
			primary = solver.NewSolverPlanner(solverCfg, rebalancingEngine, heuristicPlanner, tracer, reg)
			shadowImpl = heuristicPlanner
		} else {
			// Default: heuristic primary, solver shadow.
			shadowImpl = solver.NewSolverPlanner(solverCfg, rebalancingEngine, heuristicPlanner, tracer, reg)
		}
		return shadow.NewShadowPlanner(primary, shadowImpl, cfg.ShadowTimeoutMs, tracer, reg)

	default: // "heuristic"
		return heuristicPlanner
	}
}

func run(ctx context.Context, addr string) error {
	// ---------------------------------------------------------------
	// 1. Observability setup
	// ---------------------------------------------------------------
	reg := observability.NewMetricsRegistry()
	if err := observability.RegisterBuildInfo(reg, version, commit); err != nil {
		return fmt.Errorf("register build info: %w", err)
	}

	otlpEndpoint := os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT")
	environment := os.Getenv("APP_ENV")
	if environment == "" {
		environment = "development"
	}
	samplingRatio := 1.0
	if ratioStr := os.Getenv("OTEL_SAMPLING_RATIO"); ratioStr != "" {
		if r, err := strconv.ParseFloat(ratioStr, 64); err == nil {
			samplingRatio = r
		}
	}

	tp, shutdown, err := observability.NewTracerProvider(observability.TracingConfig{
		ServiceName:   "schedulator",
		Environment:   environment,
		OTLPEndpoint:  otlpEndpoint,
		Enabled:       otlpEndpoint != "" || os.Getenv("OTEL_STDOUT") == "true",
		Stdout:        os.Getenv("OTEL_STDOUT") == "true",
		SamplingRatio: samplingRatio,
	})
	if err != nil {
		return fmt.Errorf("create tracer provider: %w", err)
	}
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := shutdown(shutdownCtx); err != nil {
			log.Printf("tracer shutdown error: %v", err)
		}
	}()

	tracer := tp.Tracer("schedulator")

	// ---------------------------------------------------------------
	// 2. Build cluster clientsets from KUBECONFIG_<CLUSTER_ID> env vars
	// ---------------------------------------------------------------
	clusterConfigs, clusterClients, err := buildClusterClients(tracer, reg)
	if err != nil {
		return fmt.Errorf("build cluster clients: %w", err)
	}

	slog.Info("discovered clusters", "count", len(clusterConfigs))

	// ---------------------------------------------------------------
	// 3. Create K8s aggregator
	// ---------------------------------------------------------------
	aggregator := k8saggregator.New(clusterConfigs, tracer)

	// ---------------------------------------------------------------
	// 4. Create config store
	// ---------------------------------------------------------------
	appsConfig := os.Getenv("APPS_CONFIG")
	if appsConfig == "" {
		appsConfig = "deploy/kind/apps.yaml"
	}
	cfgStore := configstore.NewYAMLStore(appsConfig, tracer)

	// ---------------------------------------------------------------
	// 5. Create world state
	// ---------------------------------------------------------------
	ws := worldstate.New(tracer, reg)

	// Seed world state with a full sync.
	clusters, replicas, err := aggregator.FullSync(ctx)
	if err != nil {
		slog.Warn("full sync failed, starting with empty state", "error", err)
	} else {
		for _, c := range clusters {
			ws.UpsertCluster(c)
			for _, n := range c.Nodes {
				ws.UpsertNode(n)
			}
		}
		for _, r := range replicas {
			ws.UpsertReplica(r)
		}
		slog.Info("initial sync complete", "clusters", len(clusters), "replicas", len(replicas))
	}

	// Seed applications.
	apps, err := cfgStore.ListApplications(ctx)
	if err != nil {
		slog.Warn("failed to list applications", "error", err)
	} else {
		for _, app := range apps {
			ws.UpsertApplication(app)
		}
		slog.Info("loaded applications", "count", len(apps))
	}

	// ---------------------------------------------------------------
	// 6. Create event log (SQLite)
	// ---------------------------------------------------------------
	dbPath := os.Getenv("DB_PATH")
	if dbPath == "" {
		dbPath = "schedulator.db"
	}
	eventLog, err := eventlog.NewStore(dbPath)
	if err != nil {
		return fmt.Errorf("create event log: %w", err)
	}
	defer eventLog.Close()

	// ---------------------------------------------------------------
	// 7. Create engines
	// ---------------------------------------------------------------
	scalingCfg := scaling.DefaultScalingConfig()
	scalingEngine := scaling.NewScalingEngine(scalingCfg, tracer, reg)
	preemptionEngine := preemption.NewPreemptionEngine(preemption.DefaultPreemptionConfig(), tracer, reg)
	placementEngine := placement.NewPlacementEngine(placement.DefaultPlacementConfig(), preemptionEngine, tracer, reg)
	rebalancingEngine := rebalancing.NewRebalancingEngine(rebalancing.DefaultRebalancingConfig(), tracer, reg)

	// ---------------------------------------------------------------
	// 8. Build planner (heuristic, solver, or shadow)
	// ---------------------------------------------------------------
	appCfg := loadAppConfig()
	activePlanner := buildPlanner(appCfg, placementEngine, rebalancingEngine, tracer, reg)
	slog.Info("planner initialized", "mode", activePlanner.Name())

	// ---------------------------------------------------------------
	// 9. Create plan generator
	// ---------------------------------------------------------------
	planGen := plangen.NewPlanGenerator(tracer, reg)

	// ---------------------------------------------------------------
	// 10. Create executor
	// ---------------------------------------------------------------
	exec := executor.NewExecutor(executor.DefaultExecutorConfig(), clusterClients, ws, tracer, reg)

	// ---------------------------------------------------------------
	// 11. Create ingester
	// ---------------------------------------------------------------
	ingester := ingestion.NewIngester(aggregator, cfgStore, ingestion.IngesterConfig{
		MetricsPollInterval:  30 * time.Second,
		PeriodicEvalInterval: 30 * time.Second,
		DebounceWindow:       2 * time.Second,
	}, tracer, reg)

	// ---------------------------------------------------------------
	// 12. Create API server (before control loop so eventBus is available)
	// ---------------------------------------------------------------
	apiSrv := apiserver.New(ws, ingester, eventLog, tracer)
	apiSrv.SetScalingConfig(scalingCfg)
	staticDir := os.Getenv("STATIC_DIR")
	if staticDir == "" {
		staticDir = "web/dist"
	}
	if info, err := os.Stat(staticDir); err == nil && info.IsDir() {
		apiSrv.SetStaticDir(staticDir)
		slog.Info("serving dashboard", "dir", staticDir)
	}

	// ---------------------------------------------------------------
	// 13. Create control loop
	// ---------------------------------------------------------------
	leader := leaderelect.AlwaysLeader{}
	publisher := &eventBusPublisher{bus: apiSrv.EventBus()}
	loop := controlloop.NewControlLoop(
		controlloop.ControlLoopConfig{},
		ingester,
		scalingEngine,
		activePlanner,
		planGen,
		exec,
		ws,
		leader,
		publisher,
		eventLog,
		tracer,
		reg,
	)

	// ---------------------------------------------------------------
	// 14. Build HTTP mux
	// ---------------------------------------------------------------
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(healthResponse{Status: "ok"})
	})
	mux.Handle("/metrics", promhttp.HandlerFor(reg, promhttp.HandlerOpts{}))
	mux.Handle("/", apiSrv.Handler())

	srv := &http.Server{
		Addr:    addr,
		Handler: mux,
	}

	// ---------------------------------------------------------------
	// 15. Start control loop in background
	// ---------------------------------------------------------------
	loopErrCh := make(chan error, 1)
	go func() {
		slog.Info("starting control loop")
		if err := loop.Run(ctx); err != nil && ctx.Err() == nil {
			loopErrCh <- err
		}
		close(loopErrCh)
	}()

	// Periodic state snapshot logging (every 30s).
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				apiSrv.PublishWorldStateSnapshot(ctx)
			}
		}
	}()

	// ---------------------------------------------------------------
	// 16. Start HTTP server
	// ---------------------------------------------------------------
	httpErrCh := make(chan error, 1)
	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			httpErrCh <- err
		}
		close(httpErrCh)
	}()

	log.Printf("starting schedulator %s (%s) on %s", version, commit, addr)

	// Wait for shutdown signal.
	<-ctx.Done()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("server shutdown: %w", err)
	}

	// Check for control loop errors.
	if err := <-loopErrCh; err != nil {
		slog.Error("control loop error", "error", err)
	}

	return <-httpErrCh
}

// buildClusterClients discovers clusters from KUBECONFIG_* env vars and creates
// clientsets and ClusterClient implementations.
func buildClusterClients(tracer trace.Tracer, reg prometheus.Registerer) (
	[]k8saggregator.ClusterConfig,
	map[model.ClusterID]ports.ClusterClient,
	error,
) {
	var aggConfigs []k8saggregator.ClusterConfig
	clusterClients := make(map[model.ClusterID]ports.ClusterClient)

	namespace := os.Getenv("SCHEDULATOR_NAMESPACE")
	if namespace == "" {
		namespace = "default"
	}

	for _, env := range os.Environ() {
		if !strings.HasPrefix(env, "KUBECONFIG_") {
			continue
		}
		parts := strings.SplitN(env, "=", 2)
		if len(parts) != 2 {
			continue
		}
		clusterID := strings.TrimPrefix(parts[0], "KUBECONFIG_")
		clusterID = strings.ToLower(strings.ReplaceAll(clusterID, "_", "-"))
		kubeconfigPath := parts[1]

		config, err := clientcmd.BuildConfigFromFlags("", kubeconfigPath)
		if err != nil {
			return nil, nil, fmt.Errorf("build config for cluster %s from %s: %w", clusterID, kubeconfigPath, err)
		}

		clientset, err := kubernetes.NewForConfig(config)
		if err != nil {
			return nil, nil, fmt.Errorf("create clientset for cluster %s: %w", clusterID, err)
		}

		aggConfigs = append(aggConfigs, k8saggregator.ClusterConfig{
			ClusterID: clusterID,
			Clientset: clientset,
			Namespace: namespace,
		})

		// Create a separate Prometheus registry for each cluster client
		// to avoid metric name collisions.
		clusterReg := prometheus.WrapRegistererWithPrefix(
			fmt.Sprintf("cluster_%s_", strings.ReplaceAll(clusterID, "-", "_")),
			reg,
		)
		clusterClients[clusterID] = k8sclient.New(clientset, namespace, clusterID, tracer, clusterReg)

		slog.Info("configured cluster", "id", clusterID, "kubeconfig", kubeconfigPath)
	}

	return aggConfigs, clusterClients, nil
}

// eventBusPublisher adapts apiserver.EventBus to the controlloop.cycleSummaryPublisher interface.
type eventBusPublisher struct {
	bus *apiserver.EventBus
}

func (p *eventBusPublisher) PublishCycleSummary(summary controlloop.CycleSummary) {
	p.bus.Publish(apiserver.Event{
		Type:      "cycle_summary",
		Timestamp: time.Now(),
		Data:      summary,
	})
}

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, ":"+port); err != nil {
		log.Fatalf("server error: %v", err)
	}
}
