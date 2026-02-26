# Feature: Unified Planner Abstraction — Heuristic & Solver Backends

## 1. Problem Statement

### 1.1 Background

The current control loop calls `PlacementEngine.ComputePlacement` and
`PreemptionEngine.FindPreemptionOpportunity` sequentially and independently. This produces three
confirmed limitations:

**Greedy myopia.** Placement scores clusters independently per replica. If placing replica 1 of
app-A consumes the last warm-cache slot on cluster-X, replica 1 of app-B (processed next cycle)
finds no good home. The two decisions are never co-optimized.

**Sequential lag.** `RebalancingEngine.FindRebalancingOpportunities` runs after placement as a
separate step. If rebalancing would free the exact capacity a P0 app needs, the P0 app waits a
full cycle. There is a one-cycle blind spot between the two engines.

**Preemption is a fallback, not co-planned.** The placement engine attempts normal placement
first, then calls the preemption engine only on failure. This means preemption and placement
decisions are made independently: the placement engine cannot choose to preempt replica R on
cluster-A as part of the same decision that places new replica S on cluster-A, even when that
combination is globally optimal.

### 1.2 The Proposed Solution

Replace the direct calls to `PlacementEngine` and `PreemptionEngine` in the control loop with a
`Planner` interface. Two concrete implementations are provided:

**`HeuristicPlanner`** — wraps the existing `PlacementEngine`, `PreemptionEngine`, and
`RebalancingEngine` unchanged. This is the current behaviour with no regression risk.

**`SolverPlanner`** — uses Google OR-Tools CP-SAT (MIP) to jointly optimize placement and
preemption in a single pass. A shadow capacity pre-pass, computed before the solver runs,
exposes releasable GPU capacity from migration candidates as costed resources. This resolves
greedy myopia, sequential lag, and co-planned preemption simultaneously.

Both implementations satisfy the same `Planner` interface and feed into the existing
`PlanGenerator.GeneratePlan` → `Executor.Execute` pipeline unchanged. Switching between them
requires only an environment variable change. A `ShadowPlanner` wrapper can run both
simultaneously — the primary on the hot path, the shadow off it — emitting comparison metrics
that allow empirical quality comparison before committing to either in production.

### 1.3 What Is Not Changed

- `ScalingEngine` — scaling signals and target counts are unaffected.
- `PlanGenerator` — already handles `PlacementDecisions` + `[]MigrateDecision` with dependency
  tracking and blue-green sequencing. It receives the same types from both planners.
- `Executor` — dependency-ordered concurrent dispatch is unchanged.
- `RebalancingEngine` — used by `HeuristicPlanner` as today; used by `SolverPlanner`'s shadow
  capacity pre-pass to enumerate migration candidates.

---

## 2. Architecture Overview

```
                    ┌─────────────────────────────────────┐
                    │           Control Loop               │
                    │  scaling.ComputeTargets()            │
                    │          │                           │
                    │          ▼                           │
                    │  planner.Plan(snap, targets)         │  ← NEW abstraction
                    │          │                           │
                    │          ▼                           │
                    │  plangen.GeneratePlan(decisions,     │  ← unchanged
                    │                       migrations)    │
                    │          │                           │
                    │          ▼                           │
                    │  executor.Execute(plan, snap)        │  ← unchanged
                    └─────────────────────────────────────┘

Planner implementations:

  HeuristicPlanner                SolverPlanner               ShadowPlanner
  ─────────────────               ─────────────               ─────────────
  PlacementEngine                 ShadowCap pre-pass          primary Planner
  PreemptionEngine   ──►          CP-SAT solver          +    shadow  Planner (goroutine)
  RebalancingEngine               Result translator           Metrics comparison
        │                               │                           │
        └──────────────────────────────►└───────────────────────────┘
                                        PlannerResult
                               {PlacementDecisions, []MigrateDecision}
```

**Shadow mode data flow:**

```
  Tick N
    │
    ├── primary.Plan()  → result → GeneratePlan → Execute    (hot path, ~ms)
    │
    └── go shadow.Plan() → result → metrics.Record()         (off hot path, own timeout)
                                    (result discarded)
```

---

## 3. Type and Interface Definitions

### 3.1 `pkg/ports/planner.go` (new file)

```go
package ports

import (
    "context"

    "github.com/hsubbaraj/schedulator/internal/engine/scaling"
    "github.com/hsubbaraj/schedulator/internal/worldstate"
    "github.com/hsubbaraj/schedulator/pkg/model"
)

// PlannerResult is the unified output of any Planner implementation.
// It contains all information needed by PlanGenerator.GeneratePlan.
type PlannerResult struct {
    Decisions  model.PlacementDecisions
    Migrations []model.MigrateDecision
}

// Planner is the interface satisfied by HeuristicPlanner, SolverPlanner,
// and ShadowPlanner. The control loop calls Plan once per tick.
//
// Plan must be safe to call concurrently from multiple goroutines.
// All state must come from snap; implementations must not hold mutable state
// that is written during Plan.
type Planner interface {
    Plan(
        ctx context.Context,
        snap worldstate.WorldStateSnapshot,
        targets map[model.AppID]scaling.ScalingDecision,
    ) (PlannerResult, error)

    // Name returns a short identifier used in metrics labels and logs.
    // Examples: "heuristic", "solver", "shadow:heuristic+solver".
    Name() string
}
```

### 3.2 `pkg/model/shadow.go` (new file)

```go
package model

// ShadowSlot represents a pre-computed migration opportunity offered to the
// solver as a costed capacity resource. Claiming a slot commits the plan
// generator to performing a blue-green migration of VictimReplicaID to
// DestClusterID, freeing ReleasableGPUs on SourceClusterID for new placement.
type ShadowSlot struct {
    SlotID           string
    SourceClusterID  ClusterID           // cluster where GPUs are freed
    ReleasableGPUs   int                 // GPUs freed on SourceClusterID
    VictimReplicaID  ReplicaID           // replica to migrate away
    VictimAppID      AppID               // owning app (destination is same app)
    DestClusterID    ClusterID           // where the victim is migrated to
    DestConstraints  SchedulingConstraints
    MigrationCost    float64             // W_shadow applied in objective
}
```

### 3.3 `pkg/model/decisions.go` — extend `PlacementDecisions`

Add one field to the existing struct:

```go
type PlacementDecisions struct {
    ScaleUps          []ScaleUpDecision
    ScaleDowns        []ScaleDownDecision
    Preemptions       []PreemptionDecision
    Reservations      []GPUReservation
    ClaimedShadowSlots []ShadowSlot   // NEW: migrations committed by the solver
}
```

`HeuristicPlanner` always leaves `ClaimedShadowSlots` nil. `SolverPlanner` populates it when
the solver claims one or more shadow slots. `PlanGenerator` converts each claimed slot into a
`MigrateDecision` (see §7).

---

## 4. HeuristicPlanner

### 4.1 Location

`internal/engine/heuristic/planner.go`

### 4.2 Responsibilities

Wraps `PlacementEngine`, `PreemptionEngine` (embedded in `PlacementEngine`), and
`RebalancingEngine`. Delegates to each in turn and bundles results into `PlannerResult`.
This is a thin adapter — no logic changes to any underlying engine.

### 4.3 Signature

```go
package heuristic

type HeuristicPlanner struct {
    placement   *placement.PlacementEngine
    rebalancing *rebalancing.RebalancingEngine
    tracer      trace.Tracer
    latencyHist prometheus.Histogram  // label: planner="heuristic"
}

func NewHeuristicPlanner(
    placement   *placement.PlacementEngine,
    rebalancing *rebalancing.RebalancingEngine,
    tracer      trace.Tracer,
    reg         prometheus.Registerer,
) *HeuristicPlanner

func (p *HeuristicPlanner) Plan(
    ctx     context.Context,
    snap    worldstate.WorldStateSnapshot,
    targets map[model.AppID]scaling.ScalingDecision,
) (ports.PlannerResult, error)

func (p *HeuristicPlanner) Name() string  // returns "heuristic"
```

### 4.4 Plan() Implementation

```
1. decisions = placement.ComputePlacement(ctx, snap, targets)
   // PreemptionEngine is already called inside ComputePlacement when needed.

2. migrations = rebalancing.FindRebalancingOpportunities(ctx, snap)

3. return PlannerResult{Decisions: decisions, Migrations: migrations}
```

`ClaimedShadowSlots` is always nil in the returned `decisions` — the heuristic planner does not
use shadow capacity.

---

## 5. SolverPlanner

### 5.1 Location

```
internal/engine/solver/
  planner.go       — SolverPlanner, config, constructor
  shadowcap.go     — shadow capacity pre-pass
  mipmodel.go      — CP-SAT model construction and variable mapping
  translate.go     — solver result → PlannerResult
```

### 5.2 Dependencies

OR-Tools CP-SAT via CGo bindings: `github.com/google/or-tools` (SWIG-generated Go wrapper).
CGo is acceptable in production builds. Local Mac builds may set `CGO_ENABLED=0` and use a
stub (see §5.8).

### 5.3 Config

```go
// internal/engine/solver/planner.go
type SolverConfig struct {
    TimeoutMs          int     // CP-SAT wall-clock limit; default 200
    MaxShadowSlots     int     // migration budget per cycle; default == RebalancingConfig.MaxMigrationsPerCycle
    WUnmetP0           int64   // default 1_000_000
    WUnmetP1           int64   // default 100_000
    WPreemptP2         int64   // default 50_000
    WPreemptP1         int64   // default 80_000  (P0 preempting P1)
    WShadow            int64   // default 500     (migration churn cost)
    WColdGPUMemory     int64   // default 0       (no extra cost — best tier)
    WColdLocalNVMe     int64   // default 2_000
    WColdClusterStorage int64  // default 5_000
    WColdRemote        int64   // default 10_000
    WPacking           int64   // default 100     (reward — subtracted from J)
    RandomSeed         uint64  // fixed for deterministic tests; default 0 (non-deterministic)
}
```

### 5.4 Shadow Capacity Pre-Pass (`shadowcap.go`)

Runs before the solver. Reuses `RebalancingEngine` internals to enumerate migration candidates,
then caps the result at `SolverConfig.MaxShadowSlots`.

```go
// ComputeShadowSlots returns up to cfg.MaxShadowSlots migration opportunities
// that the solver may claim as costed capacity resources.
func ComputeShadowSlots(
    ctx  context.Context,
    snap worldstate.WorldStateSnapshot,
    cfg  SolverConfig,
) []model.ShadowSlot
```

**Algorithm:**

```
slots = []
for each cluster in snap.Clusters:
    if cluster.FragmentationScore < MinFragmentationDelta: continue
    for each fragmented node in cluster (0 < AllocatedGPUs < TotalGPUs):
        for each replica on node:
            app = snap.Applications[replica.AppID]
            if countRunning(app) <= app.MinReplicas: continue   // min_replicas guard
            dest = rebalancing.findConsolidationTarget(replica, app, snap)
            if dest == nil: continue
            if !hasCachedModel(dest.clusterID, app.ModelID, snap): continue
            slots = append(slots, ShadowSlot{
                SlotID:          uuid(),
                SourceClusterID: cluster.ClusterID,
                ReleasableGPUs:  replica.GPUs,
                VictimReplicaID: replica.ReplicaID,
                VictimAppID:     replica.AppID,
                DestClusterID:   dest.clusterID,
                DestConstraints: dest.constraints,
                MigrationCost:   float64(cfg.WShadow),
            })
            if len(slots) >= cfg.MaxShadowSlots: return slots
return slots
```

**Note:** Shadow slots are computed from the snapshot's hard capacity data only. A slot for
cluster-A exposing N GPUs does not consume capacity on cluster-B (the destination) until the
plan generator actually executes the migration in the following cycle. The solver uses the
`ReleasableGPUs` as additional available capacity on `SourceClusterID`; no capacity adjustment
is made on `DestClusterID` during this solve cycle. This is conservative: the destination
capacity is confirmed at execution time by the executor.

### 5.5 MIP Model (`mipmodel.go`)

#### Decision Variables

| Variable | Domain | Meaning |
| :--- | :--- | :--- |
| `x[r][n]` | `{0,1}` | New replica `r` placed on node `n` |
| `p[r]` | `{0,1}` | Existing replica `r` preempted |
| `u[a]` | `Z≥0` | Unmet demand for app `a` (replicas that cannot be placed) |
| `s[k]` | `{0,1}` | Shadow slot `k` claimed (triggers one migration) |

`x[r][n]` variables are only created for **new** replicas (those the scaling engine wants to add
this cycle). Existing replicas that are not preempted stay exactly where they are — the solver
never relocates them.

#### Objective — Minimize `J`

```
J = Σ_a  W_unmet(a.Priority) × u[a]
  + Σ_r  W_preempt(victimApp.Priority) × p[r]
  + Σ_k  s[k] × slots[k].MigrationCost
  + Σ_r Σ_n  W_cold(cacheScore(app, node)) × x[r][n]
  - Σ_r Σ_n  W_packing × packingBenefit(node, app.GPUsPerReplica) × x[r][n]
```

Where:
- `W_unmet(0)` = `WUnmetP0`, `W_unmet(1)` = `WUnmetP1`
- `W_preempt` applied only when a P0 preempts P1 (`WPreemptP1`) or any app preempts P2 (`WPreemptP2`)
- `W_cold(node)` = 0 if GPU_MEM cached, `WColdLocalNVMe` if NVMe, `WColdClusterStorage` if
  cluster storage, `WColdRemote` if no cache
- `packingBenefit(node, g)` = `(node.TotalGPUs - node.FreeGPUs - g) / node.TotalGPUs` (prefer
  nearly-full nodes to consolidate)

#### Constraints

```
C1 — GPU Capacity (per node n):
     Σ_r  x[r][n] × app[r].GPUsPerReplica
   + Σ_k  s[k] × (slots[k].SourceClusterID == clusterOf(n) ? 0 : 0)   // shadow GPUs are on cluster level
   ≤ n.FreeGPUs
   (Shadow slot capacity is applied at cluster level in C1b below)

C1b — Cluster-level shadow capacity:
     Σ_r∈cluster_c  x[r][n] × app.GPUsPerReplica
   ≤ availableGPUs(c) + Σ_k: slots[k].SourceClusterID==c  s[k] × slots[k].ReleasableGPUs

C2 — Contiguity (each new replica placed exactly once, or unmet):
     Σ_n  x[r][n] + isUnmet[r] = 1       for all new replicas r
     where isUnmet[r] contributes to u[app[r]]

C3 — Min Replicas (preemption floor):
     Σ_{r ∈ R_a}  (1 - p[r]) ≥ a.MinReplicas    for all apps a

C4 — Priority Cascade (only lower-priority apps are preemptable by a given requester):
     p[r] may only be 1 if victimApp.Priority > requestingApp.Priority
     Implemented by: only create p[r] variables for valid (requester, victim) pairs.
     P2 apps have no p[r] variables (cannot preempt anyone).

C5 — Migration Budget:
     Σ_k  s[k] ≤ MaxShadowSlots

C6 — Failure Domain Spread (when app.FailureDomainRule == SpreadClusters):
     Σ_{r ∈ R_a, n ∈ cluster_c}  x[r][n] ≤ ceil(targetCount_a / numEligibleClusters)
```

#### Warm-Start Hint

Before solving, provide the current greedy-heuristic placement as an initial feasible solution.
This ensures the solver always has a valid starting point and typically converges quickly. The
`W_shadow` and `W_cold` penalties provide stability across cycles — the solver will not move
an existing replica unless the benefit exceeds the migration cost, since it never actually
moves existing replicas (only preempts or places new ones).

#### Time-Box

CP-SAT is configured with `SetMaxTimeInSeconds(cfg.TimeoutMs / 1000.0)` and
`SetNumSearchWorkers(1)` (deterministic). The solver returns `FEASIBLE` or `OPTIMAL`; if the
status is `INFEASIBLE` or `UNKNOWN` (timeout with no solution), the planner falls back to
`HeuristicPlanner.Plan()` for that cycle and increments a `schedulator_solver_fallback_total`
counter.

### 5.6 Result Translation (`translate.go`)

```go
// TranslateResult converts a CP-SAT solution into a PlannerResult.
// Called after solver completes.
func TranslateResult(
    solution  CPSATSolution,
    newReplicas []newReplicaDef,    // the x[r][n] variables
    preemptVars []preemptVarDef,    // the p[r] variables
    shadowSlots []model.ShadowSlot, // the s[k] variables
    snap        worldstate.WorldStateSnapshot,
    cfg         SolverConfig,
) ports.PlannerResult
```

**Translation rules:**

- `x[r][n] == 1` → `ScaleUpDecision{AppID, ClusterID: clusterOf(n), SchedulingConstraints: {RequiredGPUs, PreferredNodes: [n]}}`
- `p[r] == 1` → `PreemptionDecision{VictimReplicaID, VictimAppID, NodeID, ClusterID, GracePeriodSeconds}`
- `s[k] == 1` → append `slots[k]` to `PlacementDecisions.ClaimedShadowSlots`
- `u[a] > 0` → logged as `slog.Warn("unmet demand", ...)` + `schedulator_solver_unmet_demand_total` counter

GPU reservations are created for each `ScaleUpDecision` using the same TTL logic as the
heuristic placement engine (`2 × ColdStartSeconds`).

### 5.7 SolverPlanner Constructor

```go
type SolverPlanner struct {
    cfg         SolverConfig
    shadowcap   *ShadowCapComputer  // wraps rebalancing internals
    heuristic   ports.Planner       // fallback on solver failure
    tracer      trace.Tracer

    solveLatency   prometheus.Histogram  // label: planner="solver"
    fallbackCounter prometheus.Counter
    unmetDemand     *prometheus.CounterVec
    solutionStatus  *prometheus.CounterVec  // labels: status="optimal|feasible|fallback"
}

func NewSolverPlanner(
    cfg         SolverConfig,
    rebalancing *rebalancing.RebalancingEngine,
    fallback    ports.Planner,
    tracer      trace.Tracer,
    reg         prometheus.Registerer,
) *SolverPlanner

func (p *SolverPlanner) Plan(...) (ports.PlannerResult, error)
func (p *SolverPlanner) Name() string  // returns "solver"
```

### 5.8 Build Tags and CGo Stub

Tag the OR-Tools import with `//go:build solver`. Provide a stub implementation for
`CGO_ENABLED=0` builds:

```
internal/engine/solver/
  planner.go          // +build solver
  planner_stub.go     // +build !solver — returns ErrSolverNotAvailable, falls back to heuristic
```

When `SCHEDULATOR_PLANNER_MODE=solver` and the stub is active (CGO disabled), the
`SolverPlanner` immediately delegates to its `fallback` planner and logs a warning.

---

## 6. ShadowPlanner

### 6.1 Location

`internal/engine/shadow/planner.go`

### 6.2 Behaviour

Runs the primary planner synchronously on the hot path. Runs the shadow planner in a detached
goroutine with its own context and `ShadowTimeoutMs` deadline. The shadow result is never
returned to the control loop. Both results are passed to `ShadowMetrics.Record` for comparison.

```go
type ShadowPlanner struct {
    primary        ports.Planner
    shadow         ports.Planner
    shadowTimeout  time.Duration
    metrics        *ShadowMetrics
}

func (p *ShadowPlanner) Plan(
    ctx     context.Context,
    snap    worldstate.WorldStateSnapshot,
    targets map[model.AppID]scaling.ScalingDecision,
) (ports.PlannerResult, error) {
    // Hot path — must complete within control loop budget.
    primaryResult, primaryErr := p.primary.Plan(ctx, snap, targets)

    // Shadow path — detached, does not block the hot path.
    // snap is a deep-copied immutable value; safe to pass to goroutine.
    go func() {
        sctx, cancel := context.WithTimeout(context.Background(), p.shadowTimeout)
        defer cancel()
        shadowResult, shadowErr := p.shadow.Plan(sctx, snap, targets)
        p.metrics.Record(primaryResult, primaryErr, shadowResult, shadowErr)
    }()

    return primaryResult, primaryErr
}

func (p *ShadowPlanner) Name() string
// returns "shadow:" + primary.Name() + "+" + shadow.Name()
// e.g. "shadow:heuristic+solver"
```

### 6.3 ShadowMetrics (`internal/engine/shadow/metrics.go`)

Emits per-planner Prometheus metrics with label `planner="heuristic"|"solver"`:

```
schedulator_planner_latency_seconds{planner}         histogram
schedulator_planner_scaleups_total{planner}          counter
schedulator_planner_preemptions_total{planner}       counter
schedulator_planner_migrations_total{planner}        counter
schedulator_planner_unmet_demand_total{planner}      counter  (replicas that couldn't be placed)
schedulator_planner_error_total{planner}             counter
```

`Record` also logs a structured diff at DEBUG level: `Δscaleups`, `Δpreemptions`,
`Δmigrations`, `Δunmet` between primary and shadow, tagged with the tick timestamp.

---

## 7. PlanGenerator — Shadow Slot Integration

`PlanGenerator.GeneratePlan` currently accepts `(PlacementDecisions, []MigrateDecision)`. The
`PlacementDecisions` struct now carries `ClaimedShadowSlots`. The generator must convert each
claimed shadow slot into a `MigrateDecision` before building the plan.

**Change to `plangen/generator.go`:**

Add a pre-processing step at the start of `GeneratePlan`:

```go
// Convert claimed shadow slots to MigrateDecisions and append to migrations.
for _, slot := range decisions.ClaimedShadowSlots {
    migrations = append(migrations, model.MigrateDecision{
        AppID:                 slot.VictimAppID,
        SourceReplicaID:       slot.VictimReplicaID,
        TargetClusterID:       slot.DestClusterID,
        SchedulingConstraints: slot.DestConstraints,
    })
}
```

The existing migration decomposition logic (Phase 4 in `GeneratePlan`) handles these correctly
without further changes. The blue-green dependency graph (new scale-up must complete before
old scale-down) is already implemented.

**Sequencing note:** Scale-ups that depend on shadow capacity (i.e., their target cluster is the
`SourceClusterID` of a claimed slot) must depend on the shadow migration's scale-up completing.
Update the dependency wiring in Phase 3:

```go
// In Phase 3 (scale-ups), also depend on shadow migration scale-ups in the same cluster.
for _, slot := range decisions.ClaimedShadowSlots {
    if su.ClusterID == slot.SourceClusterID {
        op.DependsOn = append(op.DependsOn, shadowMigrationScaleUpIDFor[slot.SlotID])
    }
}
```

Track `shadowMigrationScaleUpIDFor map[string]model.OperationID` during Phase 4 processing of
shadow-slot-derived migrations.

---

## 8. Control Loop Integration

### 8.1 Environment Variables

| Variable | Values | Default | Effect |
| :--- | :--- | :--- | :--- |
| `SCHEDULATOR_PLANNER_MODE` | `heuristic`, `solver`, `shadow` | `heuristic` | Selects active planner |
| `SCHEDULATOR_SHADOW_PLANNER` | `heuristic`, `solver` | `solver` | Shadow planner when mode=`shadow` |
| `SCHEDULATOR_SOLVER_TIMEOUT_MS` | integer | `200` | CP-SAT time-box |
| `SCHEDULATOR_SHADOW_TIMEOUT_MS` | integer | `500` | Shadow goroutine deadline |
| `SCHEDULATOR_MAX_SHADOW_SLOTS` | integer | `2` | Migration budget (matches RebalancingConfig.MaxMigrationsPerCycle) |
| `SCHEDULATOR_SOLVER_RANDOM_SEED` | uint64 | `0` | `0` = non-deterministic; non-zero = deterministic |

### 8.2 Wiring in `cmd/schedulator/main.go`

```go
func buildPlanner(
    cfg         AppConfig,
    placement   *placement.PlacementEngine,
    preemption  *preemption.PreemptionEngine,
    rebalancing *rebalancing.RebalancingEngine,
    tracer      trace.Tracer,
    reg         prometheus.Registerer,
) ports.Planner {
    heuristic := heuristic.NewHeuristicPlanner(placement, rebalancing, tracer, reg)

    switch cfg.PlannerMode {
    case "solver":
        return solver.NewSolverPlanner(cfg.SolverConfig, rebalancing, heuristic, tracer, reg)
    case "shadow":
        var shadowImpl ports.Planner
        if cfg.ShadowPlanner == "solver" {
            shadowImpl = solver.NewSolverPlanner(cfg.SolverConfig, rebalancing, heuristic, tracer, reg)
        } else {
            shadowImpl = heuristic
        }
        primary := heuristic  // default shadow primary is heuristic
        if cfg.ShadowPlanner == "heuristic" {
            // shadow compares solver (primary) vs heuristic (shadow)
            primary = solver.NewSolverPlanner(cfg.SolverConfig, rebalancing, heuristic, tracer, reg)
            shadowImpl = heuristic
        }
        return shadow.NewShadowPlanner(primary, shadowImpl, cfg.ShadowTimeoutMs, tracer, reg)
    default: // "heuristic"
        return heuristic
    }
}
```

### 8.3 Control Loop Call Site

Replace the direct engine calls with:

```go
// Before (in control loop):
placementDecisions := placementEngine.ComputePlacement(ctx, snap, scalingDecisions)
rebalancingMigrations := rebalancingEngine.FindRebalancingOpportunities(ctx, snap)
plan := planGen.GeneratePlan(ctx, placementDecisions, rebalancingMigrations)

// After:
plannerResult, err := planner.Plan(ctx, snap, scalingDecisions)
if err != nil {
    slog.ErrorContext(ctx, "planner failed", "error", err)
    // continue — executor will be a no-op with empty plan
}
plan := planGen.GeneratePlan(ctx, plannerResult.Decisions, plannerResult.Migrations)
```

---

## 9. Testing

### 9.1 `HeuristicPlanner` — `internal/engine/heuristic/planner_test.go`

| Test | Scenario | Assertion |
| :--- | :--- | :--- |
| `TestHeuristicPlanner_DelegatesPlacement` | Single app needs 2 scale-ups, capacity available | `PlannerResult.Decisions.ScaleUps` len == 2, clusters valid |
| `TestHeuristicPlanner_DelegatesPreemption` | Cluster full, P0 needs capacity, P2 replica present | `Decisions.Preemptions` non-empty, P2 replica is victim |
| `TestHeuristicPlanner_IncludesRebalancingMigrations` | Fragmented cluster, replica above min_replicas | `PlannerResult.Migrations` non-empty |
| `TestHeuristicPlanner_Name` | — | `Name() == "heuristic"` |
| `TestHeuristicPlanner_RaceCondition` | Concurrent `Plan()` calls with shared snap | `-race` clean |

All existing `placement/engine_test.go` and `preemption/engine_test.go` tests continue to pass
and serve as the engine-level regression suite.

### 9.2 Shadow Capacity Pre-Pass — `internal/engine/solver/shadowcap_test.go`

| Test | Scenario | Assertion |
| :--- | :--- | :--- |
| `TestComputeShadowSlots_Empty` | No fragmented nodes | Returns empty slice |
| `TestComputeShadowSlots_FragmentedNode_OneCandidate` | One fragmented node, one migratable replica | Returns one slot with correct fields |
| `TestComputeShadowSlots_MinReplicasProtected` | Replica at min_replicas | Slot not offered; returns empty |
| `TestComputeShadowSlots_BudgetCapped` | 10 candidates, MaxShadowSlots=3 | Returns exactly 3 slots |
| `TestComputeShadowSlots_NoDestination_Skipped` | No consolidation target exists | Returns empty |
| `TestComputeShadowSlots_NoCachedModel_Skipped` | Destination has no cached model | Returns empty |
| `TestComputeShadowSlots_CostAssignment` | WShadow=500 in config | `slot.MigrationCost == 500.0` |
| `TestComputeShadowSlots_ClusterBelowFragThreshold` | FragmentationScore < MinFragmentationDelta | Returns empty |

### 9.3 SolverPlanner — `internal/engine/solver/planner_test.go`

All tests tagged `//go:build solver`. A parallel `planner_stub_test.go` (no build tag) tests
the stub fallback path.

**Constraint satisfaction tests (table-driven, `TestSolverPlanner_Constraints`):**

| Case | Setup | Assertion |
| :--- | :--- | :--- |
| P0 unmet demand satisfied | P0 app target=3, cluster has capacity | `u[P0app] == 0`; 3 ScaleUps emitted |
| P0 preempts P2 for capacity | Cluster full with P2 replicas, P0 needs space | Preemption emitted for P2; `u[P0app] == 0` |
| P0 preempts P1 only after P2 exhausted | P0 needs 2 replicas; 1 P2 (above min), 1 P1 (above min) | P2 preempted first; P1 preempted only if needed |
| P1 cannot preempt P0 | P1 needs capacity; only P0 replicas available | `Decisions.Preemptions` empty; `u[P1app] > 0` |
| min_replicas prevents preemption | P2 at min_replicas=1, only 1 replica | Preemption not emitted; unmet demand recorded |
| No capacity anywhere | Cluster full, no preemptable replicas | All demand unmet; `Decisions.ScaleUps` empty |
| GPU capacity constraint | Node has 4 free GPUs; only one 4-GPU replica can fit | Exactly one ScaleUp per node |
| Spread constraint | 2 eligible clusters, 2 replicas, SpreadClusters rule | One replica per cluster |
| Shadow slot claimed — high benefit | Shadow slot on GPU_MEM cluster (W_cold=0 < W_shadow=500) | Slot claimed; migration emitted |
| Shadow slot not claimed — low benefit | Shadow slot on REMOTE cluster (W_cold=10000 > benefit) | Slot NOT claimed; migration not emitted |
| Migration budget respected | 5 shadow slots available, MaxShadowSlots=2 | At most 2 ClaimedShadowSlots |
| Solver timeout fallback | `TimeoutMs=1`, very large fleet | Falls back to heuristic; `fallback_total` counter incremented |

**Determinism test (`TestSolverPlanner_Determinism`):**
With `RandomSeed=42`, same snapshot input → identical `PlannerResult` across 10 consecutive
calls.

**Stability test (`TestSolverPlanner_WarmStartStability`):**
Two consecutive solve cycles with unchanged load → no new preemptions or migrations emitted
in cycle 2 (warm-start inertia via `W_shadow` and `W_preempt` keeps current assignment).

### 9.4 ShadowPlanner — `internal/engine/shadow/planner_test.go`

| Test | Scenario | Assertion |
| :--- | :--- | :--- |
| `TestShadowPlanner_ReturnsPrimaryResult` | Primary returns result A, shadow returns result B | `Plan()` returns result A |
| `TestShadowPlanner_ShadowPanic_Recovered` | Shadow planner panics | `Plan()` returns primary result; no panic propagated |
| `TestShadowPlanner_BothPlannersCalled` | Both planners instrumented with call counters | After `Plan()`, both counters == 1 |
| `TestShadowPlanner_PrimaryError_Returned` | Primary returns error | `Plan()` returns that error |
| `TestShadowPlanner_ShadowTimeout_Respected` | Shadow planner sleeps > ShadowTimeoutMs | Shadow goroutine cancelled; primary result unaffected |
| `TestShadowPlanner_MetricsRecorded` | Both planners return results | `ShadowMetrics.Record()` called with both results |
| `TestShadowPlanner_Name` | — | `Name()` contains both planner names |

### 9.5 PlanGenerator Shadow Slot Integration — `internal/plangen/generator_test.go`

Extend existing generator tests:

| Test | Scenario | Assertion |
| :--- | :--- | :--- |
| `TestGeneratePlan_ClaimedShadowSlot_EmitsMigrateOp` | `PlacementDecisions` has one `ClaimedShadowSlot` | Plan contains a scale-up and a scale-down for the victim, linked by dependency |
| `TestGeneratePlan_ScaleUpDependsOnShadowMigration` | ScaleUp targeting same cluster as shadow slot source | ScaleUp op's `DependsOn` includes shadow migration's scale-up op ID |
| `TestGeneratePlan_NoShadowSlots_Unchanged` | `ClaimedShadowSlots` nil | Plan identical to pre-change behaviour |

### 9.6 Integration / Comparison Tests — `test/integration/planner_comparison_test.go`

These tests run both planners on the same snapshot and assert that the solver satisfies all
constraints the heuristic satisfies, and improves on at least one dimension in constrained
scenarios.

| Test | Fixture | Assertion |
| :--- | :--- | :--- |
| `TestComparison_PreemptionScenario` | Tight cluster, P0 needs capacity, P2 at min+1 | Solver places ≥ heuristic; both respect min_replicas |
| `TestComparison_MultiClusterPlacement` | 2 clusters, different cache tiers | Solver total cache score ≥ heuristic total cache score |
| `TestComparison_ConstraintEquivalence` | Random snapshots (table of 20) | For each: solver unmet demand ≤ heuristic unmet demand |

### 9.7 Running All Tests

```bash
# Standard: all packages, race detector, CGO disabled (heuristic + stub solver)
CGO_ENABLED=0 go test -race ./...

# Solver-enabled tests (requires OR-Tools installed):
CGO_ENABLED=1 go test -race -tags solver ./...

# Specific packages:
CGO_ENABLED=0 go test -race -v ./internal/engine/heuristic/...
CGO_ENABLED=0 go test -race -v ./internal/engine/shadow/...
CGO_ENABLED=0 go test -race -v ./internal/plangen/...
CGO_ENABLED=1 go test -race -tags solver -v ./internal/engine/solver/...
CGO_ENABLED=0 go test -race -v ./test/integration/...

# All with verbose output and coverage:
CGO_ENABLED=0 go test -race -coverprofile=coverage.out ./...
go tool cover -html=coverage.out
```

Coverage targets (per CLAUDE.md):
- `internal/engine/heuristic`: 100% (thin adapter, all paths exercised)
- `internal/engine/solver` (stub path): 100%
- `internal/engine/solver` (solver path, `-tags solver`): 80%+
- `internal/engine/shadow`: 100%
- `internal/plangen`: 80%+

---

## 10. Benchmarking

### 10.1 Go Micro-Benchmarks

Add to `internal/engine/heuristic/planner_test.go` and `internal/engine/solver/planner_test.go`:

```go
// Benchmark fleet sizes: small (10 apps × 20 replicas), medium (50 × 100), large (100 × 500).
// Run with: go test -bench=. -benchmem -count=5 ./internal/engine/heuristic/...
//           go test -bench=. -benchmem -tags solver -count=5 ./internal/engine/solver/...

func BenchmarkHeuristicPlanner_10Apps(b *testing.B)  { benchmarkPlanner(b, 10, 20, heuristic) }
func BenchmarkHeuristicPlanner_50Apps(b *testing.B)  { benchmarkPlanner(b, 50, 100, heuristic) }
func BenchmarkHeuristicPlanner_100Apps(b *testing.B) { benchmarkPlanner(b, 100, 500, heuristic) }

func BenchmarkSolverPlanner_10Apps(b *testing.B)  { benchmarkPlanner(b, 10, 20, solver) }
func BenchmarkSolverPlanner_50Apps(b *testing.B)  { benchmarkPlanner(b, 50, 100, solver) }
func BenchmarkSolverPlanner_100Apps(b *testing.B) { benchmarkPlanner(b, 100, 500, solver) }
```

Compare with:
```bash
go test -bench=BenchmarkHeuristicPlanner -benchmem -count=5 ./internal/engine/heuristic/ \
  | tee /tmp/heuristic.bench
go test -bench=BenchmarkSolverPlanner -benchmem -tags solver -count=5 ./internal/engine/solver/ \
  | tee /tmp/solver.bench
benchstat /tmp/heuristic.bench /tmp/solver.bench
```

(`benchstat` from `golang.org/x/perf/cmd/benchstat`)

### 10.2 Simulator Quality Comparison

Add `--planner` and `--compare` flags to `test/simulator/cmd/main.go`:

```
--planner heuristic|solver     Run a single planner (default: heuristic)
--compare                       Run both planners on every tick; write two output CSVs
                                and print comparative scorecards
```

In `--compare` mode, the simulator runs each tick twice from the same state (using snapshots),
applies only the primary (heuristic) decisions to advance world state, and records both
scorecards independently. This produces a quality comparison without solver decisions
contaminating world state.

```bash
# Compare on cluster_failure scenario:
go run ./test/simulator/cmd \
  --config test/simulator/config/cluster_failure.yaml \
  --compare \
  --output /tmp/sim_results

# Output:
#   /tmp/sim_results_heuristic.csv
#   /tmp/sim_results_solver.csv
#   Comparative scorecard printed to stdout
```

Example scorecard output:
```
--- Comparative Scorecard ---
                  Heuristic    Solver     Delta
SLA Violations:   12           7          -5    ✓
Avg GPU Util:     71.3%        74.1%      +2.8% ✓
Scaling Ops:      48           52         +4    (solver does more migrations)
Total Cost:       12,048       7,492      -4,556 ✓
-----------------------------
```

### 10.3 Solver Latency Under Pressure

Run the `cluster_failure` scenario with `--planner solver` and inspect the
`schedulator_planner_latency_seconds` histogram at the tick where the cluster goes down (t=60s).
This is the empirical validation of the 200ms latency bound under maximum constraint pressure
described in `docs/feature-placement-solver.md` §8.1.

---

## 11. New File Summary

| File | Status | Description |
| :--- | :--- | :--- |
| `pkg/ports/planner.go` | New | `Planner` interface, `PlannerResult` type |
| `pkg/model/shadow.go` | New | `ShadowSlot` type |
| `pkg/model/decisions.go` | Modified | Add `ClaimedShadowSlots []ShadowSlot` to `PlacementDecisions` |
| `internal/engine/heuristic/planner.go` | New | `HeuristicPlanner` adapter |
| `internal/engine/heuristic/planner_test.go` | New | Unit tests |
| `internal/engine/solver/planner.go` | New | `SolverPlanner` (build tag: `solver`) |
| `internal/engine/solver/planner_stub.go` | New | Stub fallback (build tag: `!solver`) |
| `internal/engine/solver/shadowcap.go` | New | Shadow capacity pre-pass |
| `internal/engine/solver/mipmodel.go` | New | CP-SAT model construction |
| `internal/engine/solver/translate.go` | New | Solver result → `PlannerResult` |
| `internal/engine/solver/planner_test.go` | New | Constraint satisfaction + determinism tests |
| `internal/engine/solver/shadowcap_test.go` | New | Pre-pass unit tests |
| `internal/engine/shadow/planner.go` | New | `ShadowPlanner` wrapper |
| `internal/engine/shadow/metrics.go` | New | `ShadowMetrics` comparison recorder |
| `internal/engine/shadow/planner_test.go` | New | Shadow planner unit tests |
| `internal/plangen/generator.go` | Modified | Convert `ClaimedShadowSlots` → migrations; shadow-dep wiring |
| `internal/plangen/generator_test.go` | Modified | Shadow slot integration tests |
| `test/integration/planner_comparison_test.go` | New | Cross-planner comparison tests |
| `test/simulator/cmd/main.go` | Modified | `--planner` and `--compare` flags |
| `cmd/schedulator/main.go` | Modified | `buildPlanner()` wiring via env vars |

Existing engine packages (`placement/`, `preemption/`, `rebalancing/`) are **unchanged**.

---

## 12. Documentation and Diagram Updates

The following documents must be updated once implementation is complete:

- **`docs/multi-cluster-gpu-scheduler-proposal-v2.md`** — Add §4.X describing the `Planner`
  abstraction and the solver option. Update the control loop pseudocode to reference
  `planner.Plan()` instead of direct engine calls.

- **`docs/05-data-model.mermaid`** — Add `ShadowSlot` to the decision model. Add `ReplicasByApp`
  index to `WorldStateSnapshot`.

- **`docs/06-placement-engine-flow.mermaid`** — Replace the single "placement engine" box with
  two parallel paths: HeuristicPlanner and SolverPlanner (with shadow cap pre-pass box). Show
  convergence at `PlanGenerator`.

- **`docs/07-control-loop-state.mermaid`** — Update the state that calls placement to call
  `planner.Plan()`. Add `SHADOW` state for the shadow goroutine (off-path, non-blocking).

- **`docs/feature-placement-solver.md`** — Update §4.1 to remove `m_r` (migration variable).
  Update §4.3 to remove `W_churn` (replaced by `WShadow` on shadow slots). Mark §8.2 as
  resolved. Add forward reference to this document.

- **`plan/IMPLEMENTATION.md`** — Record this feature as a new section dependency after the
  scan bottleneck fix (`docs/feature-scan-bottleneck.md`).

---

## 13. Completion Criteria

The feature is complete when all of the following hold:

1. `ports.Planner` interface exists and is satisfied by `HeuristicPlanner`, `SolverPlanner`,
   and `ShadowPlanner`.

2. The control loop calls `planner.Plan()` exactly once per tick. Direct calls to
   `PlacementEngine.ComputePlacement` and `RebalancingEngine.FindRebalancingOpportunities` no
   longer appear in the control loop.

3. `SCHEDULATOR_PLANNER_MODE=heuristic` produces identical behaviour to the current codebase
   (verified by running all existing engine tests against `HeuristicPlanner`).

4. `SCHEDULATOR_PLANNER_MODE=solver` starts, runs without panicking, and falls back to
   heuristic gracefully when the solver is unavailable (`CGO_ENABLED=0`).

5. `SCHEDULATOR_PLANNER_MODE=shadow` runs both planners, returns primary result, and emits
   comparison metrics — verified by `TestShadowPlanner_MetricsRecorded` and a running
   Prometheus scrape.

6. All new tests pass: `CGO_ENABLED=0 go test -race ./...` is green.
   Solver-tagged tests pass: `CGO_ENABLED=1 go test -race -tags solver ./...` is green.

7. Simulator `--compare` mode produces two valid output CSVs and a scorecard for each of the
   five existing scenarios.

8. Solver latency under the `cluster_failure` scenario is benchmarked and results recorded in
   `docs/feature-placement-solver.md` §8.1 to replace the unsubstantiated 300ms claim.

9. All documentation and diagrams listed in §12 are updated.

10. No direct `O(R)` replica scans remain in `internal/engine/` (prerequisite from
    `docs/feature-scan-bottleneck.md` must be merged first).
