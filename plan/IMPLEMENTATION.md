# Schedulator Implementation Plan

This document is the master blueprint for building the schedulator — a global GPU scheduler for LLM inference workloads across a fleet of Kubernetes clusters.

**Reference Documents:**
- [`docs/multi-cluster-gpu-scheduler-proposal-v2.md`](../docs/multi-cluster-gpu-scheduler-proposal-v2.md) — primary algorithm reference
- [`docs/05-data-model.mermaid`](../docs/05-data-model.mermaid) — entity relationships → Go structs
- [`docs/06-placement-engine-flow.mermaid`](../docs/06-placement-engine-flow.mermaid) — placement/preemption flow
- [`docs/07-control-loop-state.mermaid`](../docs/07-control-loop-state.mermaid) — control loop state machine

---

## General Practices

### Project Structure

```
schedulator/
├── cmd/
│   └── schedulator/          # main binary
│       └── main.go
├── internal/                  # private application code
│   ├── controlloop/           # top-level orchestration
│   ├── engine/
│   │   ├── scaling/           # scaling engine
│   │   ├── placement/         # placement engine
│   │   ├── preemption/        # preemption engine
│   │   └── rebalancing/       # rebalancing engine
│   ├── ingestion/             # event ingestion layer
│   ├── executor/              # plan executor + execution coordinator
│   ├── plangen/               # plan generator
│   └── worldstate/            # in-memory world state
├── pkg/
│   ├── model/                 # shared domain types (Cluster, Node, App, Replica, etc.)
│   └── ports/                 # interfaces for ALL external dependencies
├── test/
│   ├── integration/           # kind + KWOK integration tests
│   ├── fixtures/              # shared test data
│   └── simulator/             # stretch: offline simulator
├── deploy/
│   └── kind/                  # kind cluster + KWOK configs
├── docs/                      # existing design docs
└── plan/                      # this file + per-section plans
```

### Testing Strategy

- **Table-driven tests** for all algorithmic logic (scaling formulas, scoring, preemption selection).
- **`testify/assert` + `testify/require`** for assertions.
- **Interfaces for all external deps** — defined in `pkg/ports/`, mocked in tests using `mockery`.
- **No global state** — all dependencies injected via constructors.
- **Test naming:** `Test<Function>_<scenario>` (e.g., `TestComputeTargetReplicas_QueuePressureScalesUp`).
- **Coverage target:** 80%+ on `internal/` packages; 100% on scoring/preemption logic.

### Observability Conventions

**OpenTelemetry Tracing:**
- Tracer passed via `context.Context` — no global tracer.
- Unit tests use `noop.TracerProvider`; integration tests use an in-memory exporter.
- Span naming: `<component>.<operation>` (e.g., `scaling.compute_target`, `placement.find_best_cluster`, `executor.dispatch_op`).
- Key attributes on spans: `app.id`, `cluster.id`, `replica.id`, `operation.type`.

**Prometheus Metrics:**
- Registry passed via constructor injection — no `prometheus.DefaultRegisterer`.
- Metric naming: `schedulator_<subsystem>_<name>_<unit>` (e.g., `schedulator_scaling_target_replicas`, `schedulator_placement_score_total`, `schedulator_executor_operation_duration_seconds`).
- All histograms use `DefBuckets` unless domain-specific buckets are justified.
- Every section below lists its expected metrics.

### Git Workflow

- **Conventional commits:** `feat(scaling):`, `fix(placement):`, `test(executor):`, `refactor(worldstate):`, etc.
- **Tests ship with code** — no PR merges without tests for new logic.
- **No merge commits** — rebase workflow.

### Pre-Implementation Doc Workflow

Before coding each section, create `plan/NN-<section-name>.md` containing:
1. Exact Go types and function signatures
2. Detailed test cases (inputs → expected outputs)
3. Edge cases and error handling
4. Open questions resolved

This ensures alignment before writing code. The section descriptions below are summaries; the per-section docs will have full detail.

---

## Implementation Sections

### Dependency Graph

```
01 Scaffolding
 └─▶ 02 Observability
      └─▶ 03 Data Model
           ├─▶ 04 External Dep Interfaces
           │    ├─▶ 05 K8s Test Infra
           │    ├─▶ 06 Event Ingestion ◀── 03
           │    ├─▶ 07 Scaling Engine ◀── 03
           │    ├─▶ 08 Placement Engine ◀── 03, 07
           │    │    ├─▶ 09 Preemption Engine ◀── 03
           │    │    ├─▶ 10 Plan Generator ◀── 09
           │    │    │    └─▶ 11 Plan Executor ◀── 04, 05
           │    │    └─▶ 12 Rebalancing Engine ◀── 03, 10
           │    │
           │    └─▶ 13 Control Loop ◀── 06-12
           │
           └─▶ 14 Integration Tests ◀── all + 05
                └─▶ 15 Simulator (stretch) ◀── 03, 07-10, 12
```

---

### Section 01 — Project Scaffolding ✅ DONE

**Pre-impl doc:** `plan/01-project-scaffolding.md`

**Purpose:** Bootstrap the Go module, directory layout, CI, and build tooling. Everything else depends on this.

**Depends on:** Nothing.

**What gets built:**
- `go.mod` with module path `github.com/hsubbaraj/schedulator`
- Directory tree as specified in General Practices
- `cmd/schedulator/main.go` — placeholder `main()` that starts an HTTP health endpoint
- `Makefile` with targets: `build`, `test`, `lint`, `generate` (mockery)
- `.golangci-lint.yml` config
- `Dockerfile` (multi-stage build)
- `.github/workflows/ci.yml` — lint + test on PR

**Key files:**
- `cmd/schedulator/main.go`
- `Makefile`
- `go.mod`

**Expected tests:**
- Build compiles: `go build ./...`
- Health endpoint returns 200: `TestHealthEndpoint`

**Expected traces/metrics:**
- None (observability added in Section 02).

---

### Section 02 — Observability Foundation ✅ DONE

**Pre-impl doc:** `plan/02-observability-foundation.md`

**Purpose:** Establish OTel tracing and Prometheus metrics plumbing so every subsequent section can instrument from day one. Must exist before any business logic.

**Depends on:** 01.

**What gets built:**
- `internal/observability/tracing.go` — `NewTracerProvider(cfg) → *sdktrace.TracerProvider`; supports OTLP exporter + noop for tests.
- `internal/observability/metrics.go` — `NewMetricsRegistry() → *prometheus.Registry`; helper to register histograms/counters/gauges.
- `internal/observability/testutil.go` — `NewTestTracer() → (trace.Tracer, *tracetest.InMemoryExporter)` for asserting spans in tests.
- Prometheus `/metrics` HTTP handler wired into `main.go`.

**Key files:**
- `internal/observability/tracing.go`
- `internal/observability/metrics.go`
- `internal/observability/testutil.go`

**Expected tests:**
- `TestNewTracerProvider_CreatesSpans` — verify spans appear in in-memory exporter.
- `TestMetricsRegistry_RegisterAndCollect` — register a counter, increment, scrape.
- `TestNoopTracer_DoesNotPanic` — verify noop path works for unit tests.

**Expected metrics:**
- `schedulator_info` gauge (version, commit labels) — build info.

---

### Section 03 — Core Data Model / World State ✅ DONE

**Pre-impl doc:** `plan/03-core-data-model.md`

**Purpose:** Define the in-memory representation of global state. Every engine reads from WorldState; event ingestion writes to it. This is the central data structure.

**Depends on:** 01, 02.

**What gets built:**

`pkg/model/` — domain types:
- `Cluster`, `Node`, `Application`, `SLA`, `Replica`, `Model`, `CacheLocation`, `PerformanceProfile`, `VLLMMetrics`, `GPUReservation`, `ScalingHistory`
- Enums: `ReplicaStatus`, `NodeStatus`, `CacheTier`, `FailureDomainRule`, `ReservationStatus`
- Types derived from `docs/05-data-model.mermaid` and proposal Section 4.2.

`internal/worldstate/` — WorldState manager:
- `type WorldState struct` — maps for clusters, applications, replicas, cache locations, performance profiles, vLLM metrics, reservations, scaling history.
- `sync.RWMutex` for concurrent access.
- `Snapshot() WorldStateSnapshot` — read-lock, deep-copy relevant maps, return immutable snapshot for engines.
- Mutators: `UpsertCluster`, `UpsertNode`, `UpsertReplica`, `UpdateVLLMMetrics`, `UpdateCacheLocations`, `CreateReservation`, `FulfillReservation`, `ExpireReservation`, `RecordScaleEvent`, etc.
- `AvailableGPUs(clusterID) int` — free GPUs minus active reservations.
- `HasContiguousGPUs(clusterID, needed) bool` — checks node-level contiguous blocks.

**Key files:**
- `pkg/model/types.go`
- `pkg/model/enums.go`
- `internal/worldstate/worldstate.go`
- `internal/worldstate/snapshot.go`

**Expected tests:**
- `TestSnapshot_IsImmutable` — mutating WorldState after snapshot doesn't affect snapshot.
- `TestUpsertReplica_UpdatesNodeAllocations` — adding a replica decrements free_gpus.
- `TestAvailableGPUs_SubtractsReservations` — reservations reduce available capacity.
- `TestHasContiguousGPUs_VariousNodeLayouts` — table-driven: 8-GPU node with various allocations.
- `TestReservation_Lifecycle` — create → fulfill or expire.
- `TestConcurrentAccess` — goroutines writing and snapshotting concurrently without races (`-race`).

**Expected traces:**
- `worldstate.snapshot` — span on snapshot creation (duration matters for perf).

**Expected metrics:**
- `schedulator_worldstate_clusters_total` gauge
- `schedulator_worldstate_replicas_total` gauge (by status)
- `schedulator_worldstate_snapshot_duration_seconds` histogram
- `schedulator_worldstate_reservations_active` gauge

---

### Section 04 — External Dependency Interfaces & Mocks

**Pre-impl doc:** `plan/04-external-interfaces.md`

**Purpose:** Define interfaces for every external system the scheduler talks to. All engines depend on these interfaces, never on concrete implementations. Mocks generated by `mockery` enable unit testing without real infrastructure.

**Depends on:** 03.

**What gets built:**

`pkg/ports/` — interfaces:
- `ClusterAggregator` — `WatchEvents(ctx) <-chan ClusterEvent`, `FullSync(ctx) ([]ClusterState, error)`, `GetVLLMMetrics(ctx, appID) (VLLMMetrics, error)`
- `CacheRegistry` — `GetCacheLocations(ctx, modelID) ([]CacheLocation, error)`
- `PerformanceProfiler` — `GetProfile(ctx, appID) (PerformanceProfile, error)`
- `ClusterClient` — `CreateReplica(ctx, appID, constraints) (ReplicaID, error)`, `DeleteReplica(ctx, replicaID) error`, `GetReplicaStatus(ctx, replicaID) (ReplicaStatus, error)`, `LabelReplica(ctx, replicaID, labels) error`
- `ConfigStore` — `WatchApplications(ctx) <-chan ApplicationConfig`, `ListApplications(ctx) ([]ApplicationConfig, error)`
- `LeaderElector` — `IsLeader() bool`, `OnElected(func())`, `OnLost(func())`

`pkg/ports/mocks/` — generated mocks via `mockery --all --dir=pkg/ports`.

**Key files:**
- `pkg/ports/cluster_aggregator.go`
- `pkg/ports/cache_registry.go`
- `pkg/ports/performance_profiler.go`
- `pkg/ports/cluster_client.go`
- `pkg/ports/config_store.go`
- `pkg/ports/leader_elector.go`

**Expected tests:**
- `TestMockClusterClient_Implements_Interface` — compile-time check.
- Mock generation runs without error (`make generate`).

**Expected traces/metrics:**
- None directly; each consumer adds its own instrumentation around port calls.

---

### Section 05 — K8s Test Infrastructure

**Pre-impl doc:** `plan/05-k8s-test-infra.md`

**Purpose:** Provide a realistic K8s environment for integration tests. `kind` creates real clusters; `KWOK` simulates GPU nodes without real hardware. This is needed before the Plan Executor can be integration-tested.

**Depends on:** 04.

**What gets built:**
- `deploy/kind/kind-config.yaml` — kind cluster config with 1 control plane node.
- `deploy/kind/kwok-nodes.yaml` — KWOK node templates simulating 8-GPU nodes (with `nvidia.com/gpu: 8` capacity).
- `test/integration/helpers.go` — helper functions: `SetupKindCluster()`, `TeardownKindCluster()`, `CreateKWOKNodes(count)`, `WaitForNodeReady()`.
- `test/integration/testmain_test.go` — `TestMain` that creates/destroys kind cluster once per test suite.
- A concrete `ClusterClient` implementation (`internal/k8sclient/client.go`) that wraps `client-go` and implements `pkg/ports.ClusterClient`.

**Key files:**
- `deploy/kind/kind-config.yaml`
- `deploy/kind/kwok-nodes.yaml`
- `test/integration/helpers.go`
- `internal/k8sclient/client.go`

**Expected tests:**
- `TestKWOKNodes_ReportGPUCapacity` — create KWOK nodes, verify they report `nvidia.com/gpu: 8`.
- `TestClusterClient_CreateAndDeleteReplica` — create a Deployment via ClusterClient, verify pod exists, delete it.

**Expected traces:**
- `k8sclient.create_replica`, `k8sclient.delete_replica` — spans around K8s API calls.

**Expected metrics:**
- `schedulator_k8sclient_request_duration_seconds` histogram (by operation, cluster).
- `schedulator_k8sclient_errors_total` counter (by operation, cluster).

---

### Section 06 — Event Ingestion Layer

**Pre-impl doc:** `plan/06-event-ingestion.md`

**Purpose:** Normalize events from multiple sources (vLLM metrics, cluster events, SLA breaches, timer) into a unified stream. Includes debouncing to prevent thrashing. Feeds the control loop.

**Depends on:** 03, 04.

**What gets built:**

`internal/ingestion/` —
- `type Event struct` — unified event type with `Kind` enum (`MetricsUpdate`, `ClusterEvent`, `SLABreach`, `PeriodicEval`).
- `type Ingester struct` — starts goroutines for each listener, feeds events into a debounced channel.
- `NewIngester(ports, cfg) *Ingester`
- `Start(ctx) <-chan []Event` — returns channel of coalesced event batches.
- Debouncer: coalesces events over a configurable window (default 1-2s). Multiple `MetricsUpdate` events for the same app within the window are merged, keeping most recent values.

**Key files:**
- `internal/ingestion/event.go`
- `internal/ingestion/ingester.go`
- `internal/ingestion/debounce.go`

**Expected tests:**
- `TestDebouncer_MergesMetricsUpdates` — two MetricsUpdate for same app within window → one event with latest values.
- `TestDebouncer_DifferentAppsNotMerged` — updates for different apps preserved.
- `TestDebouncer_WindowExpiry` — events after window starts a new batch.
- `TestIngester_ClusterEventPassthrough` — ClusterEvents are not coalesced (each is significant).
- `TestIngester_TimerFires` — periodic eval events arrive at configured interval.

**Expected traces:**
- `ingestion.receive_event` — span per raw event.
- `ingestion.emit_batch` — span when debounced batch is emitted.

**Expected metrics:**
- `schedulator_ingestion_events_received_total` counter (by kind).
- `schedulator_ingestion_batches_emitted_total` counter.
- `schedulator_ingestion_batch_size` histogram.
- `schedulator_ingestion_debounce_duration_seconds` histogram.

---

### Section 07 — Scaling Engine

**Pre-impl doc:** `plan/07-scaling-engine.md`

**Purpose:** Compute target replica count per application using vLLM runtime metrics. Implements the algorithm from proposal Section 4.3 including queue pressure, KV cache pressure, SLA compliance, scale-down detection, in-flight awareness, and stability controls.

**Depends on:** 03, 04.

**What gets built:**

`internal/engine/scaling/` —
- `type ScalingEngine struct` — stateless engine; all state comes from WorldStateSnapshot.
- `NewScalingEngine(cfg ScalingConfig, tracer, metrics)`
- `ComputeTargets(ctx, snapshot WorldStateSnapshot) map[AppID]int` — iterates all apps, returns target replica counts.
- `computeTargetReplicas(app, metrics, profile, currentCount) int` — core formula (proposal pseudocode).
- `computeEffectiveTarget(app, rawTarget, snapshot) int` — in-flight awareness (pending replicas, stale pending override).
- `applyStabilityControls(appID, target, current, snapshot) int` — cooldowns, stabilization window, SLA breach override.

`internal/engine/scaling/config.go` —
- `ScalingConfig` struct with all tunable thresholds:
  - `QueueHighWatermark` (default 10), `QueueTarget` (5), `QueueLowWatermark` (1)
  - `KVCacheHighWatermark` (0.85), `KVCacheTarget` (0.70), `KVCacheLowWatermark` (0.40)
  - `BatchLowWatermark` (0.30)
  - `MaxScaleDownPerCycle` (2), `MaxScaleUpPerCycle` (5)
  - `ScaleUpCooldown` (120s), `ScaleDownCooldown` (300s), `StabilizationCycles` (3)

**Key files:**
- `internal/engine/scaling/engine.go`
- `internal/engine/scaling/config.go`

**Expected tests (table-driven):**
- `TestComputeTargetReplicas_QueuePressure` — high queue depth → scale up proportionally.
- `TestComputeTargetReplicas_KVCachePressure` — high KV cache → scale up.
- `TestComputeTargetReplicas_SLABreach` — P99 TTFT exceeds SLA → scale up.
- `TestComputeTargetReplicas_LowUtilization` — all metrics low → scale down.
- `TestComputeTargetReplicas_RespectsMinReplicas` — never goes below min.
- `TestComputeTargetReplicas_Dampening` — scale-up/down capped per cycle.
- `TestComputeEffectiveTarget_PendingReplicas` — pending replicas prevent redundant scale-ups.
- `TestComputeEffectiveTarget_StalePending` — stale pending + SLA breach → request fresh.
- `TestApplyStabilityControls_ScaleUpCooldown` — suppresses scale-down within cooldown after scale-up.
- `TestApplyStabilityControls_StabilizationWindow` — requires N consecutive signals before scale-down.
- `TestApplyStabilityControls_SLABreachBypass` — SLA breach bypasses stabilization.

**Expected traces:**
- `scaling.compute_targets` — parent span for full cycle.
- `scaling.compute_target` — per-app child span with attributes: `app.id`, `current_count`, `target_count`, `signal` (queue/cache/sla/efficiency).

**Expected metrics:**
- `schedulator_scaling_target_replicas` gauge (by app_id).
- `schedulator_scaling_decisions_total` counter (by app_id, direction: up/down/unchanged).
- `schedulator_scaling_compute_duration_seconds` histogram.
- `schedulator_scaling_stability_suppressed_total` counter (by app_id, reason: cooldown/stabilization).

---

### Section 08 — Placement Engine

**Pre-impl doc:** `plan/08-placement-engine.md`

**Purpose:** Given scaling targets, compute which clusters should host each app's replicas and generate scheduling constraints. Implements cluster scoring (cache-tier dominant), scale-down victim selection, GPU reservations, and failure domain enforcement. Delegates to the Preemption Engine when capacity is insufficient.

**Depends on:** 03, 04, 07 (consumes scaling targets).

**What gets built:**

`internal/engine/placement/` —
- `type PlacementEngine struct`
- `NewPlacementEngine(preemptionEngine, cfg, tracer, metrics)`
- `ComputePlacement(ctx, snapshot, scalingTargets) PlacementDecisions` — main entry point (proposal Section 4.4 algorithm).
- `findBestCluster(app, snapshot) (Cluster, SchedulingConstraints, error)` — scoring function.
- `computeCacheScore(modelID, cluster, snapshot) (float64, []NodeID)` — tiered cache affinity.
- `computePackingScore(cluster, gpusNeeded) float64` — tightest-fit node scoring.
- `selectScaleDownVictims(appID, count, snapshot) []ReplicaID` — prefer high-fragmentation nodes, non-cached, over-represented clusters.

`pkg/model/decisions.go` — decision types:
- `PlacementDecisions` (scale_ups, scale_downs, preemptions)
- `ScaleUpDecision`, `ScaleDownDecision`, `SchedulingConstraints`

`internal/engine/placement/config.go` —
- `PlacementConfig` with scoring weights:
  - `WeightCacheGPUMemory` (1000), `WeightCacheLocalNVMe` (500), `WeightCacheClusterStorage` (100), `WeightCacheRemote` (0)
  - `WeightFragmentation` (50), `WeightBalance` (30)

**Key files:**
- `internal/engine/placement/engine.go`
- `internal/engine/placement/scoring.go`
- `internal/engine/placement/config.go`
- `pkg/model/decisions.go`

**Expected tests:**
- `TestFindBestCluster_PrefersWarmCache` — cluster with LOCAL_NVME beats cluster with better packing but REMOTE cache.
- `TestFindBestCluster_GPUMemoryDominates` — GPU_MEMORY cluster always wins.
- `TestComputePackingScore_TightestFit` — node with exactly `gpusNeeded` free scores 1.0.
- `TestComputePackingScore_NoFittingNode` — returns 0.0.
- `TestComputePlacement_ScaleDownFirst` — scale-downs processed before scale-ups (frees capacity).
- `TestComputePlacement_FailureDomainSpread` — spread_clusters rule prevents over-concentration.
- `TestComputePlacement_CreatesReservations` — each scale-up creates a GPU reservation.
- `TestSelectScaleDownVictims_PrefersHighFragmentation` — victims from highly fragmented nodes preferred.

**Expected traces:**
- `placement.compute` — parent span.
- `placement.find_best_cluster` — per-app span with `app.id`, `selected_cluster`, `score`.
- `placement.score_cluster` — per-candidate span with score breakdown.

**Expected metrics:**
- `schedulator_placement_score` histogram (by cache_tier).
- `schedulator_placement_scaleups_total` counter (by cluster_id).
- `schedulator_placement_scaledowns_total` counter.
- `schedulator_placement_no_capacity_total` counter (by app_id).
- `schedulator_placement_compute_duration_seconds` histogram.

---

### Section 09 — Preemption Engine

**Pre-impl doc:** `plan/09-preemption-engine.md`

**Purpose:** Find preemption victims when the Placement Engine cannot find capacity for a higher-priority app. Implements the priority cascade policy (P0 → P2 → P1; P1 → P2; P2 → none) with min_replicas protection, same-cluster preference, and packing benefit sorting.

**Depends on:** 03, 08 (called by Placement Engine).

**What gets built:**

`internal/engine/preemption/` —
- `type PreemptionEngine struct`
- `NewPreemptionEngine(cfg, tracer, metrics)`
- `FindPreemptionOpportunity(ctx, requestingApp, snapshot) (*PreemptionPlan, error)` — main entry point.
- `selectVictims(candidates, requiredGPUs, snapshot, preferCluster) ([]PreemptionDecision, int, ClusterID)` — victim selection.
- `preemptionSortKey(replica, snapshot) sortKey` — above min_replicas first, then packing benefit.

`pkg/model/decisions.go` (additions):
- `PreemptionDecision` — `VictimReplicaID`, `VictimAppID`, `NodeID`, `GracePeriodSeconds`, `Reason`.
- `PreemptionPlan` — `Victims`, `TargetCluster`, `Constraints`.

`internal/engine/preemption/config.go` —
- `PreemptionConfig` with `DefaultGracePeriod` (10s).

**Key files:**
- `internal/engine/preemption/engine.go`
- `internal/engine/preemption/config.go`

**Expected tests:**
- `TestPreemption_P0CanPreemptP2` — P0 app finds P2 victims.
- `TestPreemption_P0EscalatesToP1` — P0 exhausts P2, then preempts P1.
- `TestPreemption_P1CannotEscalateToP0` — P1 only preempts P2, never P0.
- `TestPreemption_P2CannotPreempt` — P2 returns nil.
- `TestPreemption_RespectsMinReplicas` — victims above min_replicas preferred; apps at min_replicas protected.
- `TestPreemption_SameClusterPreference` — all victims from same cluster.
- `TestPreemption_PackingBenefit` — prefers victims that free contiguous blocks matching requester's needs.
- `TestPreemption_InsufficientCapacity` — returns nil when even full cascade can't free enough.

**Expected traces:**
- `preemption.find_opportunity` — span with `requesting_app.id`, `requesting_app.priority`, `victims_count`, `freed_gpus`.

**Expected metrics:**
- `schedulator_preemption_events_total` counter (by requesting_priority, victim_priority).
- `schedulator_preemption_victims_total` counter.
- `schedulator_preemption_insufficient_capacity_total` counter.

---

### Section 10 — Plan Generator

**Pre-impl doc:** `plan/10-plan-generator.md`

**Purpose:** Transform `PlacementDecisions` into an ordered `ExecutionPlan` with dependency tracking. Ensures correct operation sequencing: preemptions → scale-downs → scale-ups → migrations.

**Depends on:** 08, 09.

**What gets built:**

`internal/plangen/` —
- `type PlanGenerator struct`
- `NewPlanGenerator(tracer, metrics)`
- `GeneratePlan(ctx, decisions PlacementDecisions) ExecutionPlan` — creates ordered operations with dependencies.
- Decomposes `MigrateDecision` into paired scale-up + scale-down with dependency (scale-down depends on scale-up completing).

`pkg/model/plan.go` —
- `ExecutionPlan` — `Operations []Operation`
- `Operation` — `ID`, `Type` (preempt/scale_down/scale_up/migrate), `Payload`, `DependsOn []OperationID`

**Key files:**
- `internal/plangen/generator.go`
- `pkg/model/plan.go`

**Expected tests:**
- `TestGeneratePlan_OperationOrdering` — preemptions before scale-downs before scale-ups before migrations.
- `TestGeneratePlan_PreemptionDependency` — scale-up that targets preempted capacity depends on that preemption.
- `TestGeneratePlan_MigrationDecomposition` — MigrateDecision becomes scale-up + scale-down pair; scale-down depends on scale-up.
- `TestGeneratePlan_IndependentOpsNoDependency` — unrelated operations have no dependencies.
- `TestGeneratePlan_EmptyDecisions` — returns empty plan, no panic.

**Expected traces:**
- `plangen.generate` — span with `operations_count`, operation type breakdown.

**Expected metrics:**
- `schedulator_plangen_operations_total` counter (by type).
- `schedulator_plangen_generate_duration_seconds` histogram.

---

### Section 11 — Plan Executor

**Pre-impl doc:** `plan/11-plan-executor.md`

**Purpose:** Execute the plan by dispatching operations to cluster clients, respecting operation dependencies, handling failures, and managing reservation lifecycle. Implements the Execution Coordinator from proposal Section 4.6.

**Depends on:** 04, 05, 10.

**What gets built:**

`internal/executor/` —
- `type Executor struct`
- `NewExecutor(clusterClients map[ClusterID]ports.ClusterClient, tracer, metrics)`
- `Execute(ctx, plan ExecutionPlan) ExecutionResult` — main loop: dispatch ops when dependencies met, wait for completion, handle failures.
- `dispatchOp(ctx, op Operation) error` — dispatches to correct cluster client based on operation type.
- `scaleUp(ctx, client, decision, reservation)` — create replica, wait for running, fulfill/expire reservation.
- `preempt(ctx, client, decision)` — label draining, delete, wait for termination.
- `scaleDown(ctx, client, decision)` — delete and wait.
- `abortDependents(opID, plan)` — mark dependent operations as aborted on failure.

`pkg/model/result.go` —
- `ExecutionResult` — `Completed []OperationID`, `Failed []FailedOp`, `Aborted []OperationID`.

**Key files:**
- `internal/executor/executor.go`
- `internal/executor/operations.go`
- `pkg/model/result.go`

**Expected tests:**
- `TestExecutor_HappyPath` — all ops succeed in order.
- `TestExecutor_DependencyRespected` — scale-up waits for preemption to complete.
- `TestExecutor_FailureAbortsDependents` — preemption failure aborts dependent scale-up.
- `TestExecutor_ParallelIndependentOps` — independent ops dispatched concurrently (mock cluster clients with latency).
- `TestExecutor_ScaleUpTimeout` — reservation expires when pod doesn't become running; replica cleaned up.
- `TestExecutor_ReservationFulfilled` — successful scale-up marks reservation fulfilled.
- Integration test (Section 14): `TestExecutor_RealKindCluster` — create/delete Deployments on kind + KWOK.

**Expected traces:**
- `executor.execute` — parent span with `plan_id`, `operations_count`.
- `executor.dispatch_op` — per-operation span with `op.id`, `op.type`, `cluster.id`.
- `executor.wait_for_running` — span during pod readiness wait.

**Expected metrics:**
- `schedulator_executor_operation_duration_seconds` histogram (by type, status: success/failure).
- `schedulator_executor_operations_inflight` gauge.
- `schedulator_executor_failures_total` counter (by type, reason).
- `schedulator_executor_reservations_expired_total` counter.

---

### Section 12 — Rebalancing Engine

**Pre-impl doc:** `plan/12-rebalancing-engine.md`

**Purpose:** Proactively consolidate fragmented GPU allocations via blue-green migration. Runs as the final phase of placement computation. Generates `MigrateDecision` entries that the Plan Generator decomposes into paired operations.

**Depends on:** 03, 08, 10.

**What gets built:**

`internal/engine/rebalancing/` —
- `type RebalancingEngine struct`
- `NewRebalancingEngine(cfg, tracer, metrics)`
- `FindRebalancingOpportunities(ctx, snapshot) []MigrateDecision` — proposal Section 4.8 algorithm.
- `findConsolidationTarget(replica, app, snapshot) (Cluster, SchedulingConstraints)` — find better-packed destination.
- `estimateFragmentationDelta(sourceNode, destCluster, gpus, snapshot) float64` — improvement estimate.

`internal/engine/rebalancing/config.go` —
- `RebalancingConfig` with `MaxMigrationsPerCycle` (2), `MinFragmentationDelta` (0.15).

**Key files:**
- `internal/engine/rebalancing/engine.go`
- `internal/engine/rebalancing/config.go`

**Expected tests:**
- `TestRebalancing_FindsConsolidation` — two partially-filled nodes can consolidate; migration proposed.
- `TestRebalancing_SkipsWellPackedClusters` — low fragmentation → no migrations.
- `TestRebalancing_RespectsMinReplicas` — app at min_replicas not eligible for migration.
- `TestRebalancing_RequiresCachedModel` — skip destination without cached model.
- `TestRebalancing_MinFragDelta` — improvement below threshold → skip.
- `TestRebalancing_MaxMigrationsPerCycle` — caps at configured limit.
- `TestRebalancing_PrefersLowPriority` — lower-priority apps migrated first.

**Expected traces:**
- `rebalancing.find_opportunities` — span with `migrations_proposed`, `clusters_evaluated`.

**Expected metrics:**
- `schedulator_rebalancing_migrations_proposed_total` counter.
- `schedulator_rebalancing_fragmentation_improvement` histogram.
- `schedulator_rebalancing_skipped_total` counter (by reason: well_packed/min_replicas/no_cache/low_delta).

---

### Section 13 — Control Loop

**Pre-impl doc:** `plan/13-control-loop.md`

**Purpose:** Orchestrate the full scheduling cycle: receive events → snapshot → scale → place → generate plan → validate → execute → log. Implements the state machine from `docs/07-control-loop-state.mermaid`. Includes leader election, distributed locking, and plan validation.

**Depends on:** 06, 07, 08, 09, 10, 11, 12 (all engines).

**What gets built:**

`internal/controlloop/` —
- `type ControlLoop struct` — composes all engines and the executor.
- `NewControlLoop(ingester, scalingEngine, placementEngine, rebalancingEngine, planGenerator, executor, worldState, leaderElector, tracer, metrics)`
- `Run(ctx)` — main loop: wait for leader, consume events, run cycle.
- `runCycle(ctx, events []Event) error` — snapshot → scale → place → rebalance → generate plan → validate → execute.
- `validatePlan(plan ExecutionPlan, snapshot) error` — sanity checks (no negative replicas, reservations don't exceed capacity, no self-preemption).

**Key files:**
- `internal/controlloop/loop.go`
- `internal/controlloop/validation.go`

**Expected tests:**
- `TestControlLoop_FullCycle` — mock all engines, verify correct call sequence.
- `TestControlLoop_LeaderElection` — non-leader skips processing.
- `TestControlLoop_ValidationRejects` — invalid plan (e.g., preempting own app) → cycle aborted, error logged.
- `TestControlLoop_ContextCancellation` — graceful shutdown.
- `TestValidatePlan_NoSelfPreemption` — app cannot preempt its own replicas.
- `TestValidatePlan_ReservationCapacity` — reservations don't exceed cluster capacity.

**Expected traces:**
- `controlloop.cycle` — parent span for entire cycle with `event_count`, `cycle_duration`, `outcome` (success/validation_failure/execution_failure).

**Expected metrics:**
- `schedulator_controlloop_cycles_total` counter (by outcome).
- `schedulator_controlloop_cycle_duration_seconds` histogram.
- `schedulator_controlloop_events_processed_total` counter.
- `schedulator_controlloop_leader_is_leader` gauge (0 or 1).

---

### Section 14 — Integration Tests

**Pre-impl doc:** `plan/14-integration-tests.md`

**Purpose:** End-to-end tests that exercise the full scheduling cycle against a real K8s cluster (kind + KWOK). Validates that the system works as a whole, not just individual engines.

**Depends on:** All previous sections + 05 (K8s test infra).

**What gets built:**

`test/integration/` —
- `TestEndToEnd_ScaleUp` — inject metrics indicating queue pressure → verify new Deployments created on kind cluster.
- `TestEndToEnd_ScaleDown` — inject low-utilization metrics for enough cycles → verify Deployments deleted.
- `TestEndToEnd_Preemption` — P0 app arrives, no capacity, P2 exists → verify P2 deleted and P0 scheduled.
- `TestEndToEnd_Rebalancing` — create fragmented allocation → verify migration (new replica created before old removed).
- `TestEndToEnd_CacheAffinity` — two clusters, one with cached model → verify preference for warm cluster.
- `TestEndToEnd_FailureDomainSpread` — app with spread_clusters rule → verify replicas across clusters.

**Key files:**
- `test/integration/scale_test.go`
- `test/integration/preemption_test.go`
- `test/integration/rebalancing_test.go`

**Expected tests:**
- All tests listed above. Each test:
  1. Sets up WorldState with known cluster/node topology.
  2. Injects events (mock ClusterAggregator or direct WorldState mutation).
  3. Runs one or more control loop cycles.
  4. Asserts K8s state (Deployments exist/deleted on kind cluster).

**Expected traces:**
- Full trace per test cycle, exported to in-memory exporter for assertion.

**Expected metrics:**
- Validate metric values after test cycles (e.g., `schedulator_scaling_decisions_total` incremented correctly).

---

### Section 15 — Simulator (Stretch)

**Pre-impl doc:** `plan/15-simulator.md`

**Purpose:** Offline simulator for testing scheduling algorithms against synthetic fleet topologies and traffic patterns without K8s. Enables rapid iteration on scoring weights, thresholds, and policy changes.

**Depends on:** 03, 07, 08, 09, 10, 12.

**What gets built:**

`test/simulator/` —
- `type Simulator struct` — composes engines with a simulated WorldState (no real K8s, no real ports).
- `type Scenario struct` — defines initial fleet topology, app configs, and a sequence of traffic/event steps.
- `RunScenario(scenario) SimulationResult` — steps through events, runs scheduling cycles, records decisions.
- `SimulationResult` — timeline of decisions, final state, metrics (fragmentation over time, preemptions, SLA violations).
- Scenario YAML loader for defining test scenarios declaratively.

**Key files:**
- `test/simulator/simulator.go`
- `test/simulator/scenario.go`
- `test/simulator/scenarios/` — YAML scenario files.

**Expected tests:**
- `TestSimulator_BaselineScenario` — known fleet, known traffic → expected decisions.
- `TestSimulator_ThresholdSensitivity` — vary queue watermarks, verify behavior changes.
- `TestSimulator_PreemptionCascade` — P0 arrival in full fleet triggers expected cascade.

**Expected traces:**
- Optional: trace export for each simulated cycle.

**Expected metrics:**
- Simulated metrics collected in-memory for analysis.

---

## Verification Checklist

- [ ] All 15 sections present with consistent format
- [ ] Dependency graph has no cycles (verified by topological ordering above)
- [ ] Every section specifies: purpose, dependencies, packages, key files, tests, traces, metrics
- [ ] All external deps behind interfaces in `pkg/ports/`
- [ ] Prometheus registry: constructor injection, no global
- [ ] OTel tracer: context propagation, noop in unit tests
- [ ] WorldState: `sync.RWMutex` + `Snapshot()` for engine reads
- [ ] K8s replicas modeled as single-replica Deployments (per proposal Section 4.6)
- [ ] Pre-implementation doc convention: `plan/NN-<section-name>.md`
