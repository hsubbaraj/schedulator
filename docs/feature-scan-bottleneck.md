# Feature: Fix O(R) Replica Scan Bottleneck

## 1. Problem Statement

Every engine in the control loop — scaling, placement, preemption, rebalancing — locates replicas
for a specific application by scanning the entire `snap.Replicas` flat map. This happens repeatedly
within a single control loop cycle.

### 1.1 Scan Inventory

The following call sites perform O(R) scans where R is the total number of replicas across all
apps and clusters:

| File | Function | Called from | Scans per cycle |
| :--- | :--- | :--- | :--- |
| `scaling/engine.go` | `countReplicasByStatus` | `ComputeTargets` (×2 per app: running + pending) | 2A |
| `scaling/engine.go` | stale-pending loop | `computeEffectiveTarget` (×1 per app) | A |
| `placement/engine.go` | `countActiveReplicas` | `ComputePlacement` delta calculation | A |
| `placement/engine.go` | spread-limit count loop | `findBestCluster` (×1 per replica being placed) | up to MaxScaleUp×A |
| `placement/scoring.go` | `selectScaleDownVictims` cluster-count loop | `ComputePlacement` | A |
| `placement/scoring.go` | `selectScaleDownVictims` candidate loop | `ComputePlacement` | A |
| `preemption/engine.go` | victim candidate build loop | `FindPreemptionOpportunity` | 1 (all apps) |
| `preemption/engine.go` | `countActiveReplicas` | `selectVictims` (×1 per candidate) | up to victims |
| `rebalancing/scoring.go` | `countRunningReplicas` | `FindRebalancingOpportunities` (×1 per candidate) | up to candidates |

*Notation: A = number of applications, R = total replicas across all apps.*

At a conservative fleet size of 100 apps × 200 replicas/app = 20,000 replicas, a single control
loop cycle performs approximately **300–500 full scans** of the replica map. Each scan is O(R).
Total work per cycle: O(R × (2A + MaxScaleUp×A)) ≈ O(R²/replicas_per_app × A).

### 1.2 Why This Matters Now

This fix is a prerequisite for two upcoming features:

- **Solver pre-pass (shadow capacity):** The pre-pass must enumerate migration candidates before
  the solver runs. If that enumeration is O(R) per node, it adds O(R×N) work before the solver's
  200ms time-box even starts.
- **MIP model construction:** Building the solver's per-app replica variable sets requires
  grouping replicas by app. Without an index, this is another O(R) scan per solve cycle.

Fixing this now is a self-contained, low-risk change that benefits the existing heuristic path
and unblocks both future features.

### 1.3 Duplicate Function

`countActiveReplicas` is defined identically in both `placement/engine.go:335` and
`preemption/engine.go:272`. This is a maintenance hazard. The fix addresses this as a side
effect.

---

## 2. Solution

Add a `ReplicasByApp map[model.AppID][]model.Replica` secondary index to `WorldStateSnapshot`.
The index is built once during `Snapshot()` — an O(R) pass that was already happening implicitly
as part of `maps.Clone`. All engines then iterate `snap.ReplicasByApp[appID]` instead of
`snap.Replicas`.

**Complexity after fix:**

| Operation | Before | After |
| :--- | :--- | :--- |
| Count replicas for one app | O(R) | O(replicas_for_app) |
| Build victim candidates (preemption) | O(R) | O(A × avg_replicas_per_app) = O(R), better locality |
| `Snapshot()` build cost | O(R) for clone | O(R) for clone + O(R) for index = O(R) unchanged |

The snapshot cost does not increase in complexity class. The index is a single additional pass
over the already-cloned `Replicas` map.

**No behavioural change.** All filtering logic (by status, by cluster, etc.) stays in the call
sites. The index provides replicas grouped by app; engines apply the same status/cluster filters
they do today, just over a smaller slice.

---

## 3. Implementation Spec

### 3.1 `internal/worldstate/snapshot.go` — Add Index Field

Add the field to `WorldStateSnapshot`:

```go
type WorldStateSnapshot struct {
    Clusters            map[model.ClusterID]model.Cluster
    Applications        map[model.AppID]model.Application
    Replicas            map[model.ReplicaID]model.Replica
    ReplicasByApp       map[model.AppID][]model.Replica   // NEW: secondary index
    CacheLocations      map[model.ModelID][]model.CacheLocation
    PerformanceProfiles map[model.AppID]model.PerformanceProfile
    VLLMMetrics         map[model.AppID]model.VLLMMetrics
    Reservations        map[model.ReservationID]model.GPUReservation
    ScalingHistory      map[model.AppID]model.ScalingHistory
    TakenAt             time.Time
}
```

Add a package-level builder function in `snapshot.go`:

```go
// buildReplicasByApp constructs the AppID → []Replica secondary index from
// the already-cloned Replicas map. It is called once per Snapshot().
func buildReplicasByApp(replicas map[model.ReplicaID]model.Replica) map[model.AppID][]model.Replica {
    idx := make(map[model.AppID][]model.Replica, len(replicas)/4) // rough pre-size
    for _, r := range replicas {
        idx[r.AppID] = append(idx[r.AppID], r)
    }
    return idx
}
```

### 3.2 `internal/worldstate/worldstate.go` — Populate Index in `Snapshot()`

In `Snapshot()`, after the existing `maps.Clone(ws.replicas)` line, build the index. The index
is built **outside the lock** — it reads from the already-cloned map, not from live state:

```go
func (ws *WorldState) Snapshot(ctx context.Context) WorldStateSnapshot {
    // ... tracer, start time ...

    ws.mu.RLock()
    snap := WorldStateSnapshot{
        Clusters:            copyClusters(ws.clusters),
        Applications:        maps.Clone(ws.applications),
        Replicas:            maps.Clone(ws.replicas),
        // ReplicasByApp populated below, after lock release
        CacheLocations:      copyCacheLocations(ws.cacheLocations),
        PerformanceProfiles: maps.Clone(ws.performanceProfiles),
        VLLMMetrics:         maps.Clone(ws.vllmMetrics),
        Reservations:        maps.Clone(ws.reservations),
        ScalingHistory:      maps.Clone(ws.scalingHistory),
        TakenAt:             time.Now(),
    }
    ws.mu.RUnlock()

    // Build index from the cloned map — no lock needed.
    snap.ReplicasByApp = buildReplicasByApp(snap.Replicas)

    // ... histogram, span attributes ...
    return snap
}
```

### 3.3 `internal/engine/scaling/engine.go` — Update `countReplicasByStatus`

```go
// Before:
func countReplicasByStatus(appID model.AppID, snap worldstate.WorldStateSnapshot, status model.ReplicaStatus) int {
    count := 0
    for _, r := range snap.Replicas {
        if r.AppID == appID && r.Status == status {
            count++
        }
    }
    return count
}

// After:
func countReplicasByStatus(appID model.AppID, snap worldstate.WorldStateSnapshot, status model.ReplicaStatus) int {
    count := 0
    for _, r := range snap.ReplicasByApp[appID] {
        if r.Status == status {
            count++
        }
    }
    return count
}
```

Update the stale-pending loop in `computeEffectiveTarget`:

```go
// Before:
for _, r := range snap.Replicas {
    if r.AppID != app.AppID || r.Status != model.ReplicaStatusPending {
        continue
    }
    // ...
}

// After:
for _, r := range snap.ReplicasByApp[app.AppID] {
    if r.Status != model.ReplicaStatusPending {
        continue
    }
    // ...
}
```

### 3.4 `internal/engine/placement/engine.go` — Update `countActiveReplicas` and Spread Loop

```go
// Before:
func countActiveReplicas(appID model.AppID, snap worldstate.WorldStateSnapshot) int {
    count := 0
    for _, r := range snap.Replicas {
        if r.AppID == appID && (r.Status == model.ReplicaStatusRunning || r.Status == model.ReplicaStatusPending) {
            count++
        }
    }
    return count
}

// After:
func countActiveReplicas(appID model.AppID, snap worldstate.WorldStateSnapshot) int {
    count := 0
    for _, r := range snap.ReplicasByApp[appID] {
        if r.Status == model.ReplicaStatusRunning || r.Status == model.ReplicaStatusPending {
            count++
        }
    }
    return count
}
```

Update the spread-limit count loop in `findBestCluster`:

```go
// Before:
for _, r := range snap.Replicas {
    if r.AppID == app.AppID && (r.Status == model.ReplicaStatusRunning || r.Status == model.ReplicaStatusPending) {
        targetCount++
    }
}

// After:
for _, r := range snap.ReplicasByApp[app.AppID] {
    if r.Status == model.ReplicaStatusRunning || r.Status == model.ReplicaStatusPending {
        targetCount++
    }
}
```

Update `countAppReplicasInCluster`:

```go
// Before:
func countAppReplicasInCluster(appID model.AppID, clusterID model.ClusterID, snap worldstate.WorldStateSnapshot) int {
    count := 0
    for _, r := range snap.Replicas {
        if r.AppID == appID && r.ClusterID == clusterID &&
            (r.Status == model.ReplicaStatusRunning || r.Status == model.ReplicaStatusPending) {
            count++
        }
    }
    return count
}

// After:
func countAppReplicasInCluster(appID model.AppID, clusterID model.ClusterID, snap worldstate.WorldStateSnapshot) int {
    count := 0
    for _, r := range snap.ReplicasByApp[appID] {
        if r.ClusterID == clusterID &&
            (r.Status == model.ReplicaStatusRunning || r.Status == model.ReplicaStatusPending) {
            count++
        }
    }
    return count
}
```

### 3.5 `internal/engine/placement/scoring.go` — Update `selectScaleDownVictims`

Both loops in `selectScaleDownVictims` currently scan all replicas then filter by `appID`. Replace
both with `snap.ReplicasByApp[appID]` and drop the `appID` filter from the loop body:

```go
// First loop — cluster counts:
// Before: for _, r := range snap.Replicas { if r.AppID != appID { continue } ... }
// After:
for _, r := range snap.ReplicasByApp[appID] {
    if r.Status != model.ReplicaStatusRunning && r.Status != model.ReplicaStatusPending {
        continue
    }
    clusterCounts[r.ClusterID]++
}

// Second loop — candidates:
// Before: for _, r := range snap.Replicas { if r.AppID != appID { continue } ... }
// After:
for _, r := range snap.ReplicasByApp[appID] {
    if r.Status != model.ReplicaStatusRunning && r.Status != model.ReplicaStatusPending {
        continue
    }
    // ... rest of candidate construction unchanged ...
}
```

### 3.6 `internal/engine/preemption/engine.go` — Update Victim Candidate Build and `countActiveReplicas`

The victim candidate build loop currently scans all replicas. Replace with per-app iteration:

```go
// Before: single loop over snap.Replicas filtering by priority
// After: iterate apps first, then use index for each lower-priority app

for victimAppID, victimApp := range snap.Applications {
    if victimApp.Priority <= app.Priority {
        continue
    }
    if model.AppID(victimAppID) == app.AppID {
        continue
    }
    for _, r := range snap.ReplicasByApp[victimAppID] {
        if r.Status != model.ReplicaStatusRunning && r.Status != model.ReplicaStatusPending {
            continue
        }
        vc := victimCandidate{replica: r, app: victimApp}
        switch victimApp.Priority {
        case 2:
            p2Candidates = append(p2Candidates, vc)
        case 1:
            p1Candidates = append(p1Candidates, vc)
        }
    }
}
```

Update `countActiveReplicas` to use the index (same change as in placement — see 3.4). After this
change, the duplicate definition in `preemption/engine.go` and `placement/engine.go` should be
resolved: move the function to a shared internal package (`internal/engine/engineutil`) and import
it in both packages, or accept the small duplication given the function is trivial.

### 3.7 `internal/engine/rebalancing/scoring.go` — Update `countRunningReplicas`

```go
// Before:
func countRunningReplicas(appID model.AppID, snap worldstate.WorldStateSnapshot) int {
    count := 0
    for _, r := range snap.Replicas {
        if r.AppID == appID && r.Status == model.ReplicaStatusRunning {
            count++
        }
    }
    return count
}

// After:
func countRunningReplicas(appID model.AppID, snap worldstate.WorldStateSnapshot) int {
    count := 0
    for _, r := range snap.ReplicasByApp[appID] {
        if r.Status == model.ReplicaStatusRunning {
            count++
        }
    }
    return count
}
```

---

## 4. Testing

### 4.1 `internal/worldstate/snapshot_test.go` — Index Correctness

Add a `TestBuildReplicasByApp` table-driven test:

| Case | Input | Expected |
| :--- | :--- | :--- |
| Empty replicas map | `{}` | empty index |
| Single app, single replica | `{r1: {AppID: "a"}}` | `{"a": [r1]}` |
| Single app, multiple replicas | `{r1,r2,r3 all AppID:"a"}` | `{"a": [r1,r2,r3]}` (any order) |
| Multiple apps | `{r1: AppID:"a", r2: AppID:"b", r3: AppID:"a"}` | `{"a":[r1,r3], "b":[r2]}` |
| App with no replicas | replicas contain no entry for `"c"` | `snap.ReplicasByApp["c"]` is nil (safe to range) |

Add `TestSnapshot_ReplicasByApp` verifying that after `WorldState.Snapshot()`, the index is
consistent with `snap.Replicas`: for every replica in `snap.Replicas`, it must appear in exactly
one `snap.ReplicasByApp` entry matching its `AppID`, and vice versa.

### 4.2 Engine Regression Tests — Identical Output

For each engine, add or extend a test that:
1. Constructs a `WorldStateSnapshot` with `ReplicasByApp` populated (use `buildReplicasByApp`
   directly in test helpers).
2. Runs the engine function.
3. Asserts output is identical to a snapshot where `ReplicasByApp` was absent (simulated by
   temporarily zeroing it and using the old O(R) helper) — confirming the refactor is
   behaviour-neutral.

Affected test files: `scaling/engine_test.go`, `placement/engine_test.go`,
`preemption/engine_test.go`, `rebalancing/engine_test.go`.

In practice, all existing passing tests serve as the regression suite — no new scenarios are
needed, just confirmation existing tests still pass after the refactor.

### 4.3 Benchmark Tests

Add `BenchmarkSnapshot` in `internal/worldstate/worldstate_test.go`:

```go
// Sizes to bench: 10 apps × 10 replicas, 100 apps × 200 replicas, 100 apps × 1000 replicas.
func BenchmarkSnapshot_100Apps_200Replicas(b *testing.B) { ... }
```

Add `BenchmarkComputeTargets` in `scaling/engine_test.go` at the same sizes. The benchmark should
show that per-app scaling time is now proportional to `replicas_per_app`, not `total_replicas`.
Specifically: doubling the number of apps with the same replicas-per-app should double benchmark
time, not quadruple it.

### 4.4 Race Detector

All tests must pass under `-race`. The index is built outside the lock on the already-cloned map,
so there is no new shared state — but the race detector must confirm this.

Run: `CGO_ENABLED=0 go test -race ./internal/worldstate/... ./internal/engine/...`

---

## 5. Completion Criteria

The feature is complete when all of the following are true:

1. **`WorldStateSnapshot` has `ReplicasByApp`** populated by `Snapshot()` and consistent with
   `Replicas` (every replica appears in exactly one app bucket).

2. **All 9 O(R) scan sites listed in section 1.1 are eliminated.** No call site in
   `scaling/`, `placement/`, `preemption/`, or `rebalancing/` iterates `snap.Replicas` to find
   replicas for a specific app. (Iterating `snap.Replicas` for non-app-specific operations such as
   counting total GPU usage across all replicas is still acceptable.)

3. **The duplicate `countActiveReplicas` is resolved** — either extracted to a shared util or
   documented as an accepted duplication with an explicit comment.

4. **All existing engine tests pass unchanged** — confirming behaviour neutrality. No test
   assertions may be modified to accommodate the refactor.

5. **New index correctness tests pass** per section 4.1.

6. **Benchmarks at 100 apps × 200 replicas show per-cycle engine time scales with
   `replicas_per_app`** — adding more apps at the same density does not increase per-app cost.

7. **`CGO_ENABLED=0 go test -race ./...` passes** with no data races.

8. **No new O(R) scans are introduced** — any new helper function that iterates replicas must use
   `snap.ReplicasByApp[appID]` unless it genuinely needs to scan all replicas regardless of app.
