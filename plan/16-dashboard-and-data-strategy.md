# Dashboard & Data Strategy Design

This document outlines the architecture for a comprehensive observability and visualization system for the Schedulator. It addresses the need for real-time operational insight, historical analysis, and auditability of scheduling decisions.

## 1. Goals

1.  **Real-Time Fleet Visibility:** Visualize the current state of all GPU clusters (utilization, fragmentation, health) at a glance.
2.  **Decision Explainability:** Enable users to answer "Why did my app scale up?" or "Why was my replica preempted?"
3.  **Performance Tracking:** Monitor scheduler latency, packing efficiency, and SLA compliance over time.
4.  **Operational Auditing:** Maintain a persistent record of all disruptive actions (preemptions, scale-downs, migrations).

## 2. Dashboard Architecture

The dashboard will be a web-based application (React/Next.js) consuming data from three sources: the Schedulator API (live state), Prometheus (metrics/trends), and a structured event log (audit history).

### 2.1 Views

#### A. Fleet Overview (NOC View)
*Target Audience: Platform Engineers, SREs*

*   **Global Map/Grid:**
    *   Status indicators for each cluster (Healthy, Degraded, Down).
    *   Aggregate GPU utilization (Allocated / Total).
    *   Fragmentation heat map (Green < 10%, Red > 40%).
*   **Cluster Drill-Down:**
    *   **Node Tetris:** Visual block representation of every node in a selected cluster.
        *   8 slots per node.
        *   Color-coded by AppID.
        *   **Overlays:**
            *   **Reserved:** Hatch pattern for capacity held by `GPUReservation`.
            *   **Cached:** Border/Icon indicating which models are cached on NVMe.
            *   **Cordons:** Greyed out slots for maintenance.

#### B. Application Operations (App Owner View)
*Target Audience: ML Engineers*

*   **SLA Tracker:**
    *   Line chart: Actual Replicas vs. Target Replicas.
    *   Line chart: P99 TTFT vs. SLA Threshold.
    *   Line chart: Queue Depth & KV Cache Utilization.
*   **Life Event Feed:**
    *   Scrolling list of operational events for the specific AppID.
    *   *Examples:* "Scaled up +2 (Queue > 10)", "Preempted by P0 workload", "Migrated to reduced fragmentation".

#### C. Scheduler Diagnostics (Debug View)
*Target Audience: Scheduler Developers*

*   **Control Loop Gantt:** Trace view of a single cycle breakdown (Snapshot -> Scale -> Place -> Plan -> Execute).
*   **Rejection Analysis:** Bar chart of scheduling failures by reason (NoCapacity, AffinityMismatch, spread_constraints).
*   **Churn Metrics:** Rate of preemptions and migrations per hour.

## 3. Data Strategy

We employ a tiered data strategy to balance real-time performance with long-term retention.

### Tier 1: In-Memory (Real-Time State)
*   **Source:** `WorldState` (via Schedulator API).
*   **Use Case:** "Right Now" visualization (Node Tetris, current replica counts).
*   **Retention:** Ephemeral (lost on restart).
*   **API Endpoints:**
    *   `GET /api/v1/clusters` -> List of clusters with summary stats.
    *   `GET /api/v1/cluster/{id}/nodes` -> Detailed node map for Tetris view.
    *   `GET /api/v1/reservations` -> Active reservation list.

### Tier 2: Time-Series (Trends & Alerts)
*   **Source:** Prometheus / VictoriaMetrics.
*   **Use Case:** Line charts, historical trends (7-30 days), alerting.
*   **Key Metrics (Existing + New):**
    *   `schedulator_cluster_gpu_utilization` (Gauge)
    *   `schedulator_cluster_fragmentation_score` (Gauge)
    *   `schedulator_app_replica_count` (Gauge by status)
    *   `schedulator_scaling_decisions_total` (Counter by reason)
    *   `schedulator_preemption_events_total` (Counter)
    *   `schedulator_scheduler_latency_seconds` (Histogram)

### Tier 3: Structured History (Audit Log)
*   **Source:** SQLite (Embedded) or PostgreSQL.
*   **Use Case:** "Why" debugging, audit trails, long-term retention (90+ days).
*   **Implementation:** The `Executor` and `Engines` will emit structured events to a persistent store.

#### Schema Proposal

**1. ScalingEvents Table**
Records every scaling decision (up, down, or unchanged with signal).
```sql
CREATE TABLE scaling_events (
    id UUID PRIMARY KEY,
    timestamp TIMESTAMP,
    app_id TEXT,
    direction TEXT CHECK(direction IN ('UP', 'DOWN')),
    reason TEXT, -- e.g., "QueueDepth > 10", "Efficiency"
    old_count INT,
    new_count INT,
    metrics_snapshot JSONB -- { "queue": 12.5, "kv_cache": 0.85 }
);
```

**2. PlacementDecisions Table**
Records why a specific cluster/node was chosen.
```sql
CREATE TABLE placement_decisions (
    id UUID PRIMARY KEY,
    timestamp TIMESTAMP,
    app_id TEXT,
    selected_cluster_id TEXT,
    decision_type TEXT, -- 'SCALE_UP', 'MIGRATE'
    score_breakdown JSONB, -- { "cache": 1000, "packing": 50, "balance": 10 }
    constraints JSONB -- { "required_gpus": 8, "affinity": "model-a" }
);
```

**3. Disruptions Table**
Records preemptions and evictions.
```sql
CREATE TABLE disruptions (
    id UUID PRIMARY KEY,
    timestamp TIMESTAMP,
    type TEXT CHECK(type IN ('PREEMPTION', 'EVICTION')),
    victim_replica_id TEXT,
    victim_app_id TEXT,
    requester_app_id TEXT, -- NULL if eviction/maintenance
    cluster_id TEXT,
    node_id TEXT,
    reason TEXT
);
```

## 4. Implementation Plan

### Phase 1: API & Metrics (Foundation)
1.  Expose `WorldState` via read-only HTTP API endpoints (`/api/v1/...`) in `cmd/schedulator/main.go`.
2.  Enhance Prometheus metrics to include granular fragmentation scores and decision reasons.
3.  Build a Grafana dashboard consuming these metrics.

### Phase 2: Persistent Event Logging
1.  Introduce an `EventLogger` interface in `internal/observability`.
2.  Implement a SQLite-based `EventLogger`.
3.  Instrument `ScalingEngine`, `PlacementEngine`, and `PreemptionEngine` to log structured decisions.
4.  Expose query endpoints (`GET /api/v1/history/app/{id}`) to retrieve these logs.

### Phase 3: Frontend Application
1.  Scaffold a Next.js application.
2.  Implement the "Node Tetris" component using the API from Phase 1.
3.  Implement the "Life Event Feed" using the API from Phase 2.
