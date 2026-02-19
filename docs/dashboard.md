# Dashboard Implementation Spec

This is the implementation spec for the Schedulator monitoring dashboard. It references exact Go struct names, JSON serialization keys, API endpoints, and TypeScript types. Every field reference maps to an existing or explicitly-new type.

---

## 1. Current State

### 1.1 Existing Components

The dashboard (`web/src/`) currently has 5 components, used in `App.tsx`:

| Component | File | What it renders |
|-----------|------|-----------------|
| `ClusterCard` | `web/src/components/ClusterCard.tsx` | Cluster-level GPU stats with expandable `NodeBar` |
| `NodeBar` | `web/src/components/NodeBar.tsx` | 8-slot GPU visualization per node |
| `AppCard` | `web/src/components/AppCard.tsx` | Application identity + replica count |
| `EventStream` | `web/src/components/EventStream.tsx` | Real-time SSE event feed |
| `GpuTimeline` | `web/src/components/GpuTimeline.tsx` | Recharts line chart per cluster from `/api/v1/snapshots` |

### 1.2 Existing API Endpoints

Defined in `internal/apiserver/server.go:Handler()`:

| Endpoint | Handler | Returns |
|----------|---------|---------|
| `GET /api/v1/state` | `handleState` | Full `WorldStateSnapshot` as JSON |
| `GET /api/v1/clusters` | `handleClusters` | `snap.Clusters` map |
| `GET /api/v1/applications` | `handleApplications` | Per-app `{app, replica_count, replicas}` |
| `GET /api/v1/events/stream` | `handleEventStream` | SSE stream of `apiserver.Event` |
| `GET /api/v1/events/history` | `handleEventHistory` | `[]eventlog.EventRecord` from SQLite |
| `GET /api/v1/snapshots` | `handleSnapshots` | `[]eventlog.ClusterSnapshot` time series |

### 1.3 Data Available in `/api/v1/state` but Unused by Frontend

`WorldStateSnapshot` (defined in `internal/worldstate/snapshot.go`) serializes these fields to JSON, but the current `web/src/types.ts:WorldState` interface omits them:

| Go field | JSON key | Go type | What it contains |
|----------|----------|---------|------------------|
| `VLLMMetrics` | `VLLMMetrics` | `map[AppID]model.VLLMMetrics` | Per-app runtime metrics: queue depth, KV cache util, TTFT, TPS |
| `Reservations` | `Reservations` | `map[ReservationID]model.GPUReservation` | Active/fulfilled/expired GPU reservations |
| `ScalingHistory` | `ScalingHistory` | `map[AppID]model.ScalingHistory` | Last scale-up/down timestamps, consecutive scale-down signals |
| `CacheLocations` | `CacheLocations` | `map[ModelID][]model.CacheLocation` | Where each model is cached, at which tier |
| `PerformanceProfiles` | `PerformanceProfiles` | `map[AppID]model.PerformanceProfile` | Profiled TPS, TTFT, cold/warm start times |

Additionally, `Application.SLA` exists in `pkg/model/types.go` but is not in the TS `Application` interface.

---

## 2. Backend Changes Required

### 2.1 New SSE Event Type: `cycle_summary`

**Where**: Published at end of `runCycle()` in `internal/controlloop/loop.go`, after step 11 (observability recording).

**How**: The `ControlLoop` needs an `apiserver.EventBus` reference (add to constructor). After recording observability, publish:

```go
cl.eventBus.Publish(apiserver.Event{
    Type:      "cycle_summary",
    Timestamp: time.Now(),
    Data:      cycleSummary, // new struct defined below
})
```

**New struct** — add to `internal/controlloop/loop.go` or a new `internal/controlloop/types.go`:

```go
type CycleSummary struct {
    TriggerKinds      []ingestion.EventKind                `json:"trigger_kinds"`
    ScalingDecisions  map[model.AppID]scaling.ScalingDecision `json:"scaling_decisions"`
    Placement         PlacementSummary                     `json:"placement"`
    Execution         ExecutionSummary                     `json:"execution"`
    CycleDurationMs   int64                                `json:"cycle_duration_ms"`
}

type PlacementSummary struct {
    ScaleUpCount    int                       `json:"scale_up_count"`
    ScaleDownCount  int                       `json:"scale_down_count"`
    PreemptionCount int                       `json:"preemption_count"`
    ScaleUps        []model.ScaleUpDecision   `json:"scale_ups"`
    ScaleDowns      []model.ScaleDownDecision `json:"scale_downs"`
    Preemptions     []model.PreemptionDecision `json:"preemptions"`
}

type ExecutionSummary struct {
    CompletedCount int               `json:"completed_count"`
    FailedCount    int               `json:"failed_count"`
    AbortedCount   int               `json:"aborted_count"`
    Completed      []model.OperationID `json:"completed"`
    Failed         []model.FailedOp  `json:"failed"`
    Aborted        []model.OperationID `json:"aborted"`
}
```

Build `CycleSummary` from data already computed in `runCycle`:
- `TriggerKinds`: deduplicated `ev.Kind` values from the `events` slice
- `ScalingDecisions`: the `scalingDecisions` map (already exists)
- `Placement`: from the `placements` variable (`model.PlacementDecisions`)
- `Execution`: from the `result` variable (`model.ExecutionResult`)
- `CycleDurationMs`: `time.Since(start).Milliseconds()` (already computed)

### 2.2 Wire `eventLog.LogEvent()` in Control Loop

**Problem**: `eventlog.Store.LogEvent()` exists but is never called from the control loop. The event history endpoint returns an empty list.

**Where**: In `runCycle()`, after step 9 (Execute). The `ControlLoop` needs an `*eventlog.Store` reference (add to constructor).

**What to log**:

1. One event per completed scale-up:
   ```go
   eventLog.LogEvent(ctx, eventlog.EventRecord{
       Timestamp: time.Now(),
       Type:      "scale_up",
       AppID:     scaleUp.AppID,
       ClusterID: scaleUp.ClusterID,
       Summary:   fmt.Sprintf("Scaled up %s in %s", scaleUp.AppID, scaleUp.ClusterID),
   })
   ```

2. One event per completed scale-down:
   ```go
   eventLog.LogEvent(ctx, eventlog.EventRecord{
       Type:      "scale_down",
       AppID:     scaleDown.AppID,
       Summary:   fmt.Sprintf("Scaled down replica %s of %s", scaleDown.ReplicaID, scaleDown.AppID),
   })
   ```

3. One event per completed preemption:
   ```go
   eventLog.LogEvent(ctx, eventlog.EventRecord{
       Type:      "preempt",
       AppID:     preemption.VictimAppID,
       ClusterID: preemption.ClusterID,
       Summary:   fmt.Sprintf("Preempted %s on %s: %s", preemption.VictimReplicaID, preemption.ClusterID, preemption.Reason),
   })
   ```

4. One `cycle_complete` event per cycle:
   ```go
   eventLog.LogEvent(ctx, eventlog.EventRecord{
       Type:       "cycle_complete",
       Summary:    fmt.Sprintf("+%d scale-up, -%d scale-down, %d preempt", ...),
       DetailJSON: marshaledCycleSummary,
   })
   ```

**Matching operation IDs to decision types**: The control loop has `plan.Operations` (each with `Type` and `ID`) and `result.Completed`/`result.Failed`. To log per-decision events, iterate `plan.Operations`, check if the operation ID is in `result.Completed`, and use `op.Type` to determine the event type.

### 2.3 New API Endpoint: `GET /api/v1/scaling-config`

**Where**: Add to `internal/apiserver/server.go:Handler()`.

**What it returns**: The active `scaling.ScalingConfig` struct. The server needs the config passed in at construction or via a getter.

**ScalingConfig fields** (from `internal/engine/scaling/config.go`):

```json
{
  "QueueHighWatermark": 10.0,
  "QueueTarget": 5.0,
  "QueueLowWatermark": 1.0,
  "KVCacheHighWatermark": 0.85,
  "KVCacheTarget": 0.70,
  "KVCacheLowWatermark": 0.40,
  "BatchLowWatermark": 0.30,
  "MaxScaleDownPerCycle": 2,
  "MaxScaleUpPerCycle": 5,
  "ScaleUpCooldown": 120000000000,
  "ScaleDownCooldown": 300000000000,
  "StabilizationCycles": 3
}
```

Note: `time.Duration` serializes as nanoseconds in JSON by default. The frontend should convert to seconds by dividing by 1e9, or the backend should add a custom marshaler. Recommend custom JSON: add `ScaleUpCooldownSeconds int` / `ScaleDownCooldownSeconds int` computed fields.

---

## 3. Frontend: Information Architecture

All sections on a single page (no routing), matching current `App.tsx` structure:

```
┌─────────────────────────────────────────────────────────┐
│ Hero Status Strip                                       │
├───────────────────────────────┬─────────────────────────┤
│ Fleet Overview                │ Event Stream            │
│  (enhanced ClusterCard +      │ (enhanced, receives     │
│   NodeBar)                    │  cycle_summary events)  │
├───────────────────────────────┤                         │
│ Application Intelligence      │                         │
│  (enhanced AppCard + detail   │                         │
│   drawer)                     │                         │
├───────────────────────────────┤                         │
│ GPU Utilization Timelines     │                         │
│  (existing GpuTimeline,       │                         │
│   enhanced)                   │                         │
├───────────────────────────────┤                         │
│ Reservation Monitor           │                         │
│  (new)                        │                         │
├───────────────────────────────┤                         │
│ "Why Is Nothing Happening?"   │                         │
│  Diagnostic Panel (new)       │                         │
└───────────────────────────────┴─────────────────────────┘
```

---

## 4. Component Specs

### 4.1 Hero Status Strip (new component: `HeroStrip.tsx`)

A horizontal strip of 4 status tiles at the top of the page.

**Data source**: `GET /api/v1/state` (WorldStateSnapshot) + `cycle_summary` SSE events (accumulated in React state).

#### Tile 1: SLA Compliance
- **Computation**: For each app in `state.Applications`, look up `state.VLLMMetrics[appID]`. Compare `VLLMMetrics.P99TimeToFirstTokenMs` against `Application.SLA.MaxP99TTFTMs`. Count apps breaching vs total apps that have an SLA defined (`SLA.MaxP99TTFTMs > 0`).
- **Display**: `"N/M apps meeting SLA"` — green if N==M, yellow if N >= M*0.8, red otherwise.
- **Go types**: `model.VLLMMetrics.P99TimeToFirstTokenMs` (float64), `model.Application.SLA.MaxP99TTFTMs` (int).

#### Tile 2: Fleet GPU Utilization
- **Computation**: Sum `Node.AllocatedGPUs` across all nodes in all clusters / sum `Node.TotalGPUs` across all nodes. Skip nodes where `Node.Status != "ready"`.
- **Display**: Percentage with color: green < 70%, yellow 70-90%, red > 90%.
- **Go types**: `model.Node.AllocatedGPUs` (int), `model.Node.TotalGPUs` (int), `model.Node.Status` (NodeStatus).

#### Tile 3: Active Reservations
- **Computation**: Count entries in `state.Reservations` where `GPUReservation.Status == "active"`.
- **Display**: Count badge. Yellow if > 0 (capacity is held), red if any reservation has TTL remaining < 30s.
- **Go types**: `model.GPUReservation.Status` (ReservationStatus), `model.GPUReservation.CreatedAt` (time.Time), `model.GPUReservation.TTLSeconds` (int).
- **TTL remaining**: `GPUReservation.CreatedAt + TTLSeconds - now`. Compute client-side using `new Date(r.CreatedAt).getTime() + r.TTLSeconds * 1000 - Date.now()`.

#### Tile 4: Control Loop Health
- **Computation**: Derived from the most recent `cycle_summary` SSE event. Show last cycle timestamp and `CycleSummary.CycleDurationMs`.
- **Display**: Green if last cycle < 10s ago and duration < 500ms, yellow if < 30s ago, red if > 30s ago (stale).
- **No data case**: Show "Waiting for first cycle..." in gray.

---

### 4.2 Fleet Overview (enhanced `ClusterCard.tsx` + `NodeBar.tsx`)

**Data source**: `state.Clusters`, `state.Reservations`, `state.CacheLocations`.

#### ClusterCard Enhancements

Add two indicators to each cluster card:

1. **Reservation count per cluster**:
   - Filter `state.Reservations` by `GPUReservation.ClusterID == cluster.ClusterID` and `GPUReservation.Status == "active"`.
   - Display: `"N reserved"` badge, yellow.

2. **Cache warmth indicator**:
   - For all entries in `state.CacheLocations` (keyed by ModelID), count how many models have at least one `CacheLocation` where `CacheLocation.ClusterID == cluster.ClusterID`.
   - Display: `"N models cached"` badge.
   - **Go types**: `model.CacheLocation.ClusterID` (string), `model.CacheLocation.Tier` (CacheTier: `"GPU_MEMORY"` | `"LOCAL_NVME"` | `"CLUSTER_STORAGE"`).

#### NodeBar Enhancements

The 8-slot GPU visualization per node currently shows allocated vs free. Enhance:

- **Reserved GPU slots**: Cross-reference `state.Reservations` where `GPUReservation.NodeID == node.NodeID` and `Status == "active"`. Sum `GPUReservation.GPUs` to get reserved count for this node.
- **Slot states**: allocated (blue), reserved-but-unoccupied (yellow striped), free (dark gray).
- **Slot count**: `Node.AllocatedGPUs` allocated + reserved GPUs reserved + remainder free. The 8 slots render left-to-right: allocated, then reserved, then free.
- **Go types**: `model.Node.AllocatedGPUs` (int), `model.Node.FreeGPUs` (int), `model.Node.TotalGPUs` (int), `model.GPUReservation.NodeID` (string), `model.GPUReservation.GPUs` (int).

---

### 4.3 Application Intelligence (enhanced `AppCard.tsx` + new `AppDetailDrawer.tsx`)

**Data sources**: `state.Applications`, `state.Replicas`, `state.VLLMMetrics`, `state.ScalingHistory`, `state.CacheLocations`, `state.PerformanceProfiles`, `GET /api/v1/scaling-config`, `cycle_summary` SSE events.

#### App Table (replaces current card grid with a table)

Columns:

| Column | Source | Field path |
|--------|--------|------------|
| App ID | `Application.AppID` | `app.AppID` |
| Priority | `Application.Priority` | `app.Priority` (0 = highest) |
| Min Replicas | `Application.MinReplicas` | `app.MinReplicas` |
| Running | Count replicas where `Replica.AppID == appID && Replica.Status == "running"` | Filter `state.Replicas` |
| Pending | Count replicas where `Replica.AppID == appID && Replica.Status == "pending"` | Filter `state.Replicas` |
| Target | From latest `cycle_summary`: `ScalingDecisions[appID].TargetCount` | SSE state |
| TTFT P99 | `VLLMMetrics[appID].P99TimeToFirstTokenMs` | `state.VLLMMetrics` |
| SLA Target | `Application.SLA.MaxP99TTFTMs` | `app.SLA.MaxP99TTFTMs` |
| SLA Status | TTFT P99 > SLA Target → red "BREACH", else green "OK" | Computed |
| Queue Depth | `VLLMMetrics[appID].AvgWaitingQueueDepth` | `state.VLLMMetrics` |
| Last Action | From latest `cycle_summary`: `ScalingDecisions[appID].Direction` | SSE state |

Sort by Priority ascending, then AppID alphabetically.

#### App Detail Drawer

Clicking an app row opens a slide-out drawer with 4 tabs:

**Tab 1: Scaling State**

| Field | Source |
|-------|--------|
| Last scale-up time | `ScalingHistory[appID].LastScaleUpAt` |
| Last scale-down time | `ScalingHistory[appID].LastScaleDownAt` |
| Consecutive scale-down signals | `ScalingHistory[appID].ConsecutiveScaleDownSignals` |
| Scale-up cooldown remaining | `max(0, ScalingHistory.LastScaleUpAt + ScalingConfig.ScaleUpCooldown - now)` |
| Scale-down cooldown remaining | `max(0, ScalingHistory.LastScaleDownAt + ScalingConfig.ScaleDownCooldown - now)` |
| Stabilization progress | `ScalingHistory.ConsecutiveScaleDownSignals` / `ScalingConfig.StabilizationCycles` |
| Current scaling decision | From latest `cycle_summary`: full `ScalingDecision` for this app |

Display cooldowns as countdown timers (green if expired/ready, yellow if active). Stabilization as `N/M signals` progress bar.

**Go types**:
- `model.ScalingHistory.LastScaleUpAt` (time.Time), `.LastScaleDownAt` (time.Time), `.ConsecutiveScaleDownSignals` (int)
- `scaling.ScalingConfig.ScaleUpCooldown` (time.Duration), `.ScaleDownCooldown` (time.Duration), `.StabilizationCycles` (int)
- `scaling.ScalingDecision.Direction` (ScaleDirection: `"up"` | `"down"` | `"unchanged"`), `.Signal` (ScaleSignal: `"queue"` | `"kv_cache"` | `"sla"` | `"efficiency"` | `"none"`), `.CurrentCount` (int), `.TargetCount` (int), `.SLABreach` (bool), `.ScaleActionApproved` (bool), `.ScaleDownSignalObserved` (bool)

**Tab 2: Metrics**

All fields from `model.VLLMMetrics`:

| Display name | JSON key | Type |
|-------------|----------|------|
| Avg Queue Depth | `AvgWaitingQueueDepth` | float64 |
| Max Queue Depth | `MaxWaitingQueueDepth` | float64 |
| Avg Running Queue | `AvgRunningQueueDepth` | float64 |
| Avg Batch Utilization | `AvgBatchUtilization` | float64 (0-1, display as %) |
| Avg KV Cache Utilization | `AvgKVCacheUtilization` | float64 (0-1, display as %) |
| Max KV Cache Utilization | `MaxKVCacheUtilization` | float64 (0-1, display as %) |
| Total TPS | `TotalTokensPerSecond` | float64 |
| Avg TTFT | `AvgTimeToFirstTokenMs` | float64 (ms) |
| P99 TTFT | `P99TimeToFirstTokenMs` | float64 (ms) |
| Measured At | `MeasuredAt` | time.Time |
| Aggregated From | `AggregatedFrom` | int (number of replicas) |

Color thresholds for KV cache: green < `KVCacheLowWatermark` (0.40), yellow < `KVCacheHighWatermark` (0.85), red >= 0.85. For queue: green < `QueueLowWatermark` (1.0), yellow < `QueueHighWatermark` (10.0), red >= 10.0. These values come from `GET /api/v1/scaling-config`.

**Tab 3: Replicas**

Table of replicas for this app, filtered from `state.Replicas` where `Replica.AppID == appID`:

| Column | Field |
|--------|-------|
| Replica ID | `Replica.ReplicaID` |
| Cluster | `Replica.ClusterID` |
| Node | `Replica.NodeID` |
| GPUs | `Replica.GPUs` |
| Status | `Replica.Status` (`"pending"` | `"running"` | `"draining"` | `"terminated"`) |
| Age | Computed from `Replica.CreatedAt` |

Status color: running=green, pending=yellow, draining=orange, terminated=gray.

**Tab 4: Failure Domain**

Shows replica distribution across clusters for apps with `Application.FailureDomainRule == "spread_clusters"`.

- Group replicas by `Replica.ClusterID`, show count per cluster.
- **Compliance check**: For `spread_clusters` rule, app should have replicas in >= 2 distinct clusters (when total replicas >= 2). Show green "Compliant" or red "Non-compliant: all replicas in single cluster".
- For apps with `FailureDomainRule == "none"`, show "No failure domain rule configured" in gray.
- **Go types**: `model.Application.FailureDomainRule` (FailureDomainRule: `"spread_clusters"` | `"none"`).

**Tab 5: Cache Effectiveness** (new, see section 4.8)

---

### 4.4 GPU Utilization Timelines (enhanced `GpuTimeline.tsx`)

**Data source**: `GET /api/v1/snapshots?cluster_id={id}&since={iso}` returning `[]eventlog.ClusterSnapshot`.

**Current**: Line chart with allocated vs total GPUs over time per cluster.

**Enhancement**: Add a reservation overlay:
- Alongside the existing snapshot polling, count active reservations per cluster from `state.Reservations` at each state update.
- Render as a stacked area on top of allocated GPUs, colored yellow, representing `reserved_gpus` (sum of `GPUReservation.GPUs` where `ClusterID == clusterID && Status == "active"`).
- Note: `ClusterSnapshot` (from `eventlog/types.go`) does not include reservation data. Reservations overlay uses current state only, not historical. The line chart shows historical allocated/free, and the reservation overlay is a current-time marker line or annotation.

---

### 4.5 Reservation Monitor (new component: `ReservationMonitor.tsx`)

**Data source**: `state.Reservations` from `GET /api/v1/state`.

Table of all `GPUReservation` entries where `Status == "active"`:

| Column | Field | Notes |
|--------|-------|-------|
| Reservation ID | `GPUReservation.ReservationID` | Truncate to 8 chars |
| For App | `GPUReservation.ForAppID` | Link to app detail |
| Cluster | `GPUReservation.ClusterID` | |
| Node | `GPUReservation.NodeID` | Empty string = cluster-scoped |
| GPUs | `GPUReservation.GPUs` | |
| TTL Remaining | `CreatedAt + TTLSeconds - now` | Countdown |
| Status | `GPUReservation.Status` | Always "active" in this filtered view |

**TTL remaining computation**:
```typescript
const ttlRemainingMs = new Date(r.CreatedAt).getTime() + r.TTLSeconds * 1000 - Date.now();
```

**Color rules**:
- TTL > 60s: green
- TTL 30-60s: yellow
- TTL < 30s: red (pulsing)
- TTL <= 0: should have been expired by backend; show "Expiring..." in red

**Empty state**: "No active reservations" in gray.

---

### 4.6 Diagnostic Panel: "Why Is Nothing Happening?" (new component: `DiagnosticPanel.tsx`)

**Data sources**: `state.VLLMMetrics`, `state.ScalingHistory`, `GET /api/v1/scaling-config`, latest `cycle_summary` SSE event.

**Interaction**: Dropdown to select an app (populated from `state.Applications` keys). Once selected, render a 5-stage pipeline visualization:

#### Stage 1: Metrics Received?
- **Check**: `state.VLLMMetrics[appID]` exists and `VLLMMetrics.MeasuredAt` is within last 60s.
- **Pass**: Green checkmark, show `MeasuredAt` timestamp.
- **Fail**: Red X, show "No metrics" or "Stale: last received {age}s ago".
- **Go type**: `model.VLLMMetrics.MeasuredAt` (time.Time).

#### Stage 2: Scaling Decision?
- **Check**: Latest `cycle_summary` SSE event contains `ScalingDecisions[appID]`.
- **Pass (action needed)**: Show direction (`"up"` / `"down"`), signal (`"queue"` / `"kv_cache"` / `"sla"` / `"efficiency"`), current → target count.
- **Pass (no action)**: Direction is `"unchanged"`, show "No scaling needed" in green.
- **No data**: Gray "Waiting for cycle data...".
- **Go type**: `scaling.ScalingDecision.Direction`, `.Signal`, `.CurrentCount`, `.TargetCount`.

#### Stage 3: Blocked by Stability?
- **Check if cooldown active**:
  - Scale-up cooldown: `ScalingHistory.LastScaleUpAt + ScalingConfig.ScaleUpCooldown > now`
  - Scale-down cooldown: `ScalingHistory.LastScaleDownAt + ScalingConfig.ScaleDownCooldown > now`
- **Check if stabilization insufficient**: `ScalingHistory.ConsecutiveScaleDownSignals < ScalingConfig.StabilizationCycles`
- **Blocked**: Yellow pause icon, show which check blocked and countdown.
- **Not blocked**: Green checkmark.
- **Verification**: Cross-reference with `ScalingDecision.ScaleActionApproved` — if `false` and direction is not `"unchanged"`, the decision was suppressed. If `ScaleDownSignalObserved` is `true` but `ScaleActionApproved` is `false`, stabilization window is the blocker.

#### Stage 4: Placement Found?
- **Check**: From latest `cycle_summary`, look at `Placement.ScaleUps` for entries with `ScaleUpDecision.AppID == appID`.
- **Pass**: Green, show target cluster ID (`ScaleUpDecision.ClusterID`).
- **Fail**: Red, "No placement found — check cluster capacity".
- **N/A**: If Stage 2 direction is `"unchanged"` or `"down"`, show gray "Not applicable".
- **Go type**: `model.ScaleUpDecision.AppID`, `.ClusterID`.

#### Stage 5: Execution Succeeded?
- **Check**: From latest `cycle_summary`, `Execution.CompletedCount > 0` and `Execution.FailedCount == 0`.
- **Pass**: Green, show completed count.
- **Partial**: Yellow, show `"N completed, M failed"`.
- **Failed**: Red, show failed operation IDs and error.
- **Go type**: `model.ExecutionResult.Completed` ([]OperationID), `.Failed` ([]FailedOp with `.Err`).

---

### 4.7 Event Stream (enhanced `EventStream.tsx`)

**Data source**: SSE stream at `GET /api/v1/events/stream`, historical at `GET /api/v1/events/history`.

**Current behavior**: Renders all SSE events as JSON.

**Enhancements**:

1. **`cycle_summary` event rendering**: When `event.type == "cycle_summary"`, render as a compact card instead of raw JSON:
   ```
   🔄 Cycle completed in {CycleDurationMs}ms
   +{ScaleUpCount} scale-up  -{ScaleDownCount} scale-down  ⚡{PreemptionCount} preempt
   ```
   Show scaling decisions with direction != `"unchanged"` as sub-items: `"{AppID}: {Direction} ({Signal})"`.

2. **`state_update` event**: Continue rendering as before (connection heartbeat indicator).

3. **Historical events**: From `GET /api/v1/events/history`, now includes actual scheduler actions (scale_up, scale_down, preempt, cycle_complete) because of the wiring in section 2.2.

4. **Event type color coding**:
   - `scale_up`: green
   - `scale_down`: orange
   - `preempt`: red
   - `cycle_complete`: blue
   - `state_update`: gray

**Go types**:
- SSE payload: `apiserver.Event{Type string, Timestamp time.Time, Data interface{}}` (from `internal/apiserver/eventbus.go`)
- Historical: `eventlog.EventRecord{ID int64, Timestamp time.Time, Type string, AppID string, ClusterID string, Summary string, DetailJSON string}` (from `internal/eventlog/types.go`)

---

### 4.8 Cache Effectiveness (new, rendered inside App Detail Drawer Tab 5)

**Data source**: `state.CacheLocations`, `state.Applications`, `state.Replicas`.

**For the selected app**:

1. Look up `Application.ModelID` for the app.
2. Get `state.CacheLocations[modelID]` — this is a `[]CacheLocation`.
3. Group by `CacheLocation.ClusterID`.
4. For each cluster, show the highest cache tier present:
   - `GPU_MEMORY` (green, best)
   - `LOCAL_NVME` (yellow, good)
   - `CLUSTER_STORAGE` (orange, slow)
   - Not cached (red)

5. Cross-reference with replica placement: for each cluster that has replicas of this app (from `state.Replicas`), show whether the model is cached there.

**Display**: Table with columns:

| Column | Source |
|--------|--------|
| Cluster ID | `CacheLocation.ClusterID` |
| Cache Tier | `CacheLocation.Tier` (highest tier if multiple) |
| Replicas Here | Count of `Replica.ClusterID == clusterID && Replica.AppID == appID` |
| Placement Match | Green check if cached AND replicas present, red X if replicas present but not cached (cold start risk) |

**Tier hierarchy for "highest"**: `GPU_MEMORY` > `LOCAL_NVME` > `CLUSTER_STORAGE`.

**Go types**: `model.CacheLocation{ModelID, ClusterID, NodeID, Tier, CachedAt}`, `model.CacheTier` (`"GPU_MEMORY"` | `"LOCAL_NVME"` | `"CLUSTER_STORAGE"`), `model.Application.ModelID`.

---

## 5. TypeScript Type Updates

Update `web/src/types.ts` to include all WorldStateSnapshot fields and new types.

### Add to existing `WorldState` interface:

```typescript
export interface WorldState {
  Clusters: Record<string, Cluster>;
  Applications: Record<string, Application>;
  Replicas: Record<string, Replica>;
  VLLMMetrics: Record<string, VLLMMetrics>;
  Reservations: Record<string, GPUReservation>;
  ScalingHistory: Record<string, ScalingHistory>;
  CacheLocations: Record<string, CacheLocation[]>;
  PerformanceProfiles: Record<string, PerformanceProfile>;
  TakenAt: string;
}
```

### Add `SLA` to `Application`:

```typescript
export interface Application {
  AppID: string;
  ModelID: string;
  GPUsPerReplica: number;
  Priority: number;
  MinReplicas: number;
  FailureDomainRule: string; // "spread_clusters" | "none"
  SLA: SLA;
}

export interface SLA {
  AppID: string;
  MaxP99TTFTMs: number;
  MaxP99TPS: number;
}
```

### New interfaces:

```typescript
export interface VLLMMetrics {
  AppID: string;
  AggregatedFrom: number;
  AvgWaitingQueueDepth: number;
  MaxWaitingQueueDepth: number;
  AvgRunningQueueDepth: number;
  AvgBatchUtilization: number;
  AvgKVCacheUtilization: number;
  MaxKVCacheUtilization: number;
  TotalTokensPerSecond: number;
  AvgTimeToFirstTokenMs: number;
  P99TimeToFirstTokenMs: number;
  MeasuredAt: string; // ISO 8601
}

export interface GPUReservation {
  ReservationID: string;
  ClusterID: string;
  NodeID: string;
  GPUs: number;
  ForAppID: string;
  CreatedAt: string; // ISO 8601
  TTLSeconds: number;
  Status: string; // "active" | "fulfilled" | "expired"
}

export interface ScalingHistory {
  AppID: string;
  LastScaleUpAt: string;   // ISO 8601, zero value = "0001-01-01T00:00:00Z"
  LastScaleDownAt: string;
  ConsecutiveScaleDownSignals: number;
}

export interface CacheLocation {
  ModelID: string;
  ClusterID: string;
  NodeID: string;
  Tier: string; // "GPU_MEMORY" | "LOCAL_NVME" | "CLUSTER_STORAGE"
  CachedAt: string;
}

export interface PerformanceProfile {
  AppID: string;
  TPSPerReplica: number;
  TTFTP99Ms: number;
  ColdStartSeconds: number;
  WarmStartSeconds: number;
}

// --- cycle_summary SSE event payload ---

export interface CycleSummary {
  trigger_kinds: string[];
  scaling_decisions: Record<string, ScalingDecision>;
  placement: PlacementSummary;
  execution: ExecutionSummary;
  cycle_duration_ms: number;
}

export interface ScalingDecision {
  AppID: string;
  CurrentCount: number;
  TargetCount: number;
  Direction: string;  // "up" | "down" | "unchanged"
  Signal: string;     // "queue" | "kv_cache" | "sla" | "efficiency" | "none"
  SLABreach: boolean;
  ScaleActionApproved: boolean;
  ActionAt: string;
  ScaleDownSignalObserved: boolean;
}

export interface PlacementSummary {
  scale_up_count: number;
  scale_down_count: number;
  preemption_count: number;
  scale_ups: ScaleUpDecision[];
  scale_downs: ScaleDownDecision[];
  preemptions: PreemptionDecision[];
}

export interface ScaleUpDecision {
  AppID: string;
  ClusterID: string;
  ReservationID: string;
}

export interface ScaleDownDecision {
  AppID: string;
  ReplicaID: string;
}

export interface PreemptionDecision {
  VictimReplicaID: string;
  VictimAppID: string;
  NodeID: string;
  ClusterID: string;
  GracePeriodSeconds: number;
  Reason: string;
}

export interface ExecutionSummary {
  completed_count: number;
  failed_count: number;
  aborted_count: number;
  completed: string[];
  failed: FailedOp[];
  aborted: string[];
}

export interface FailedOp {
  OperationID: string;
  Type: string;
  Err: string;
}

// --- scaling config endpoint ---

export interface ScalingConfig {
  QueueHighWatermark: number;
  QueueTarget: number;
  QueueLowWatermark: number;
  KVCacheHighWatermark: number;
  KVCacheTarget: number;
  KVCacheLowWatermark: number;
  BatchLowWatermark: number;
  MaxScaleDownPerCycle: number;
  MaxScaleUpPerCycle: number;
  ScaleUpCooldownSeconds: number;
  ScaleDownCooldownSeconds: number;
  StabilizationCycles: number;
}
```

---

## 6. Files Modified Summary

### Backend (Go)

| File | Change |
|------|--------|
| `internal/controlloop/loop.go` | Add `eventBus` and `eventLog` fields to `ControlLoop`. Publish `cycle_summary` SSE event and log events via `eventLog.LogEvent()` at end of `runCycle()`. |
| `internal/controlloop/types.go` | New file: `CycleSummary`, `PlacementSummary`, `ExecutionSummary` structs. |
| `internal/apiserver/server.go` | Add `GET /api/v1/scaling-config` endpoint. Add `scalingConfig` field or getter to `Server`. |

### Frontend (TypeScript/React)

| File | Change |
|------|--------|
| `web/src/types.ts` | Add all missing interfaces per section 5. |
| `web/src/App.tsx` | Add `HeroStrip`, `ReservationMonitor`, `DiagnosticPanel` sections. Convert app cards section to table layout. |
| `web/src/components/HeroStrip.tsx` | New component per section 4.1. |
| `web/src/components/ClusterCard.tsx` | Add reservation count and cache warmth badges per section 4.2. |
| `web/src/components/NodeBar.tsx` | Add reserved GPU slot rendering per section 4.2. |
| `web/src/components/AppCard.tsx` | Rewrite as table row + detail drawer per section 4.3. Or: new `AppTable.tsx` + `AppDetailDrawer.tsx` replacing `AppCard`. |
| `web/src/components/GpuTimeline.tsx` | Add reservation overlay per section 4.4. |
| `web/src/components/ReservationMonitor.tsx` | New component per section 4.5. |
| `web/src/components/DiagnosticPanel.tsx` | New component per section 4.6. |
| `web/src/components/EventStream.tsx` | Enhanced `cycle_summary` rendering per section 4.7. |
| `web/src/components/CacheEffectiveness.tsx` | New component per section 4.8, used inside app detail drawer. |
| `web/src/hooks/useWorldState.ts` | Accumulate latest `cycle_summary` event in React state and expose it. Fetch `ScalingConfig` on mount. |
