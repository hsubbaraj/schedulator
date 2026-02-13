# Schedulator — Claude Instructions

## Project Overview

Schedulator is a global GPU scheduler for LLM inference workloads across a fleet of Kubernetes clusters. It is a pure Go project, currently pre-implementation (design docs exist, no Go code yet).

**Key reference docs (read before implementing any component):**
- `docs/multi-cluster-gpu-scheduler-proposal-v2.md` — primary algorithm reference and source of truth for all business logic
- `docs/05-data-model.mermaid` — canonical data model
- `docs/06-placement-engine-flow.mermaid` — placement and preemption flow
- `docs/07-control-loop-state.mermaid` — control loop state machine
- `plan/IMPLEMENTATION.md` — master implementation blueprint with section ordering and dependencies

## Pre-Implementation Requirement

**Before coding any section, read the corresponding pre-impl doc in `plan/`.** If it doesn't exist yet, create it first (`plan/NN-<section-name>.md`) with exact types, function signatures, test cases, and resolved edge cases. Do not write code without a pre-impl doc.

## Project Structure

```
cmd/schedulator/         # main binary
internal/
  controlloop/           # top-level orchestration
  engine/
    scaling/             # scaling engine
    placement/           # placement engine
    preemption/          # preemption engine
    rebalancing/         # rebalancing engine
  ingestion/             # event ingestion
  executor/              # plan executor
  plangen/               # plan generator
  worldstate/            # in-memory world state
  observability/         # OTel + Prometheus setup
  k8sclient/             # concrete K8s client (implements pkg/ports.ClusterClient)
pkg/
  model/                 # shared domain types
  ports/                 # interfaces for all external dependencies
test/
  integration/           # kind + KWOK integration tests
  fixtures/              # shared test data
  simulator/             # offline simulator (stretch)
deploy/kind/             # kind + KWOK configs
```

## Implementation Rules

### Algorithm Fidelity
- All business logic (scaling algorithm, placement scoring, preemption cascade, rebalancing) **must match the proposal exactly**. The proposal pseudocode is the spec. Do not invent logic or add "improvements" — if the proposal is silent on something, raise it rather than guess.
- Scoring weights are constants from the proposal: `WEIGHT_CACHE_GPU_MEMORY=1000`, `WEIGHT_CACHE_LOCAL_NVME=500`, `WEIGHT_CACHE_CLUSTER_STORAGE=100`, `WEIGHT_FRAGMENTATION=50`, `WEIGHT_BALANCE=30`.

### External Dependencies
- **All external dependencies must be behind interfaces** defined in `pkg/ports/`. Engines never import concrete implementations directly.
- External deps: `ClusterAggregator`, `CacheRegistry`, `PerformanceProfiler`, `ClusterClient`, `ConfigStore`, `LeaderElector`.
- Generate mocks with `mockery` (`make generate`). Never hand-write mocks.
- The `ClusterAggregator` returns raw K8s-style data; the state sync layer in `internal/worldstate/` or `internal/ingestion/` translates to domain types. Do not leak K8s API types into engine packages.

### WorldState
- `WorldState` uses `sync.RWMutex`. Engines **never hold the lock** — they receive an immutable `WorldStateSnapshot` via `Snapshot()`.
- `Snapshot()` deep-copies the maps. Engines read from the snapshot; all mutations go through `WorldState` mutator methods.
- GPU reservations must be subtracted from available capacity in all placement calculations. Reservations have a TTL (2× expected startup time) and auto-expire.

### Observability
- **OTel tracer via `context.Context`** — no global tracer. All engine constructors accept `trace.Tracer`.
- **Prometheus registry via constructor injection** — no `prometheus.DefaultRegisterer` or `prometheus.MustRegister` at package init.
- Span naming: `<component>.<operation>` (e.g., `scaling.compute_target`, `placement.find_best_cluster`).
- Metric naming: `schedulator_<subsystem>_<name>_<unit>` (e.g., `schedulator_scaling_target_replicas`, `schedulator_executor_operation_duration_seconds`).
- Unit tests: use `noop.NewTracerProvider()`. Integration tests: use `tracetest.NewInMemoryExporter()`.

### Testing
- **Table-driven tests** for all algorithmic logic. Test cases must cover the exact thresholds and edge cases from the proposal.
- Test naming: `Test<Function>_<Scenario>` (e.g., `TestComputeTargetReplicas_KVCachePressureScalesUp`).
- Use `testify/assert` (non-fatal) and `testify/require` (fatal). No bare `t.Fatal` or `t.Error`.
- Coverage target: 80%+ on `internal/`; 100% on `engine/scaling`, `engine/placement`, `engine/preemption`.
- Always run tests with `-race`. No data races allowed.
- Do not use `time.Sleep` in tests — use polling helpers or channels.
- When running tests locally on Mac, set CGO_ENABLED=0.

### K8s Replica Model
- Replicas are **single-replica Deployments**, not raw pods and not multi-replica Deployments. Each replica gets its own Deployment with a unique name (e.g., `app-X-replica-a1b2`) and its own pod template with per-replica scheduling constraints.
- Scale-down and preemption = delete the specific Deployment. Never use `kubectl scale`.
- The `ClusterClient` interface abstracts all K8s operations. Concrete implementation in `internal/k8sclient/`.

### Operation Ordering
When generating execution plans, always sequence: **preemptions → scale-downs → scale-ups → migrations**. Migrations are blue-green: scale-up new replica first, scale-down old only after new is confirmed running.

### Git
- Conventional commits: `feat(scaling):`, `fix(placement):`, `test(executor):`, `refactor(worldstate):`, etc.
- Tests ship with code — no commits with new logic but missing tests.
- No merge commits — rebase workflow.
