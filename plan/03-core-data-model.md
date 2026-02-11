# Section 03 — Core Data Model / World State

Pre-implementation document. Source of truth: `docs/05-data-model.mermaid` + proposal Section 4.2.

## Package: `pkg/model`

### Enums (`enums.go`)

All enums use `type X string` with `const` blocks (readable JSON, matches proposal strings).

```go
type NodeStatus string
const (
    NodeStatusReady    NodeStatus = "ready"
    NodeStatusCordoned NodeStatus = "cordoned"
    NodeStatusDraining NodeStatus = "draining"
    NodeStatusDown     NodeStatus = "down"
)

type ReplicaStatus string
const (
    ReplicaStatusPending    ReplicaStatus = "pending"
    ReplicaStatusRunning    ReplicaStatus = "running"
    ReplicaStatusDraining   ReplicaStatus = "draining"
    ReplicaStatusTerminated ReplicaStatus = "terminated"
)

type CacheTier string
const (
    CacheTierGPUMemory      CacheTier = "GPU_MEMORY"
    CacheTierLocalNVMe      CacheTier = "LOCAL_NVME"
    CacheTierClusterStorage CacheTier = "CLUSTER_STORAGE"
)

type FailureDomainRule string
const (
    FailureDomainSpreadClusters FailureDomainRule = "spread_clusters"
    FailureDomainNone           FailureDomainRule = "none"
)

type ReservationStatus string
const (
    ReservationStatusActive    ReservationStatus = "active"
    ReservationStatusFulfilled ReservationStatus = "fulfilled"
    ReservationStatusExpired   ReservationStatus = "expired"
)
```

### ID Types (`types.go`)

Transparent `= string` aliases for documentation:

```go
type ClusterID = string
type NodeID = string
type AppID = string
type ReplicaID = string
type ModelID = string
type ReservationID = string
type OperationID = string
```

### Entity Structs (`types.go`)

All structs are pure value types. Matching `docs/05-data-model.mermaid` exactly, plus `Pods` and `CachedModels` from proposal.

```go
type Cluster struct {
    ClusterID          ClusterID
    Nodes              map[NodeID]Node
    FragmentationScore float64
}

type Node struct {
    NodeID        NodeID
    ClusterID     ClusterID
    TotalGPUs     int
    AllocatedGPUs int
    FreeGPUs      int
    Pods          []ReplicaID
    CachedModels  map[ModelID]struct{}
    Status        NodeStatus
}

type Application struct {
    AppID             AppID
    ModelID           ModelID
    GPUsPerReplica    int
    Priority          int
    MinReplicas       int
    FailureDomainRule FailureDomainRule
    SLA               SLA
}

type SLA struct {
    AppID        AppID
    MaxP99TTFTMs int
    MaxP99TPS    int
}

type Replica struct {
    ReplicaID ReplicaID
    AppID     AppID
    ClusterID ClusterID
    NodeID    NodeID
    GPUs      int
    Status    ReplicaStatus
}

type Model struct {
    ModelID ModelID
    Name    string
    SizeGB  int
}

type CacheLocation struct {
    ModelID   ModelID
    ClusterID ClusterID
    NodeID    NodeID  // empty for CLUSTER_STORAGE
    Tier      CacheTier
    CachedAt  time.Time
}

type PerformanceProfile struct {
    AppID            AppID
    TPSPerReplica    float64
    TTFTP99Ms        float64
    ColdStartSeconds float64
    WarmStartSeconds float64
}

type VLLMMetrics struct {
    AppID                    AppID
    AggregatedFrom           int
    AvgWaitingQueueDepth     float64
    MaxWaitingQueueDepth     float64
    AvgRunningQueueDepth     float64
    AvgBatchUtilization      float64
    AvgKVCacheUtilization    float64
    MaxKVCacheUtilization    float64
    TotalTokensPerSecond     float64
    AvgTimeToFirstTokenMs    float64
    P99TimeToFirstTokenMs    float64
    MeasuredAt               time.Time
}

type GPUReservation struct {
    ReservationID ReservationID
    ClusterID     ClusterID
    NodeID        NodeID  // empty for cluster-scoped
    GPUs          int
    ForAppID      AppID
    CreatedAt     time.Time
    TTLSeconds    int
    Status        ReservationStatus
}

type ScalingHistory struct {
    AppID                       AppID
    LastScaleUpAt               time.Time
    LastScaleDownAt             time.Time
    ConsecutiveScaleDownSignals int
}
```

## Package: `internal/worldstate`

### `WorldStateSnapshot` (`snapshot.go`)

Immutable copy of all world state. Engines receive this via `Snapshot()`.

```go
type WorldStateSnapshot struct {
    Clusters            map[model.ClusterID]model.Cluster
    Applications        map[model.AppID]model.Application
    Replicas            map[model.ReplicaID]model.Replica
    CacheLocations      map[model.ModelID][]model.CacheLocation
    PerformanceProfiles map[model.AppID]model.PerformanceProfile
    VLLMMetrics         map[model.AppID]model.VLLMMetrics
    Reservations        map[model.ReservationID]model.GPUReservation
    ScalingHistory      map[model.AppID]model.ScalingHistory
    TakenAt             time.Time
}
```

Deep-copy uses `maps.Clone` for scalar-value maps, explicit helpers for nested types (Cluster→Nodes→Pods/CachedModels, CacheLocations slices).

### `WorldState` (`worldstate.go`)

```go
type WorldState struct {
    mu                  sync.RWMutex
    clusters            map[model.ClusterID]model.Cluster
    applications        map[model.AppID]model.Application
    replicas            map[model.ReplicaID]model.Replica
    cacheLocations      map[model.ModelID][]model.CacheLocation
    performanceProfiles map[model.AppID]model.PerformanceProfile
    vllmMetrics         map[model.AppID]model.VLLMMetrics
    reservations        map[model.ReservationID]model.GPUReservation
    scalingHistory      map[model.AppID]model.ScalingHistory

    tracer             trace.Tracer
    clustersGauge      prometheus.Gauge
    replicasGaugeVec   *prometheus.GaugeVec
    snapshotHistogram  prometheus.Histogram
    reservationsGauge  prometheus.Gauge
}

func New(tracer trace.Tracer, reg prometheus.Registerer) *WorldState
```

### Metrics

| Metric | Type |
|--------|------|
| `schedulator_worldstate_clusters_total` | Gauge |
| `schedulator_worldstate_replicas_total` (label: `status`) | GaugeVec |
| `schedulator_worldstate_snapshot_duration_seconds` | Histogram |
| `schedulator_worldstate_reservations_active` | Gauge |

### Mutators

All acquire write lock internally. See plan for full behavior table.

### Capacity Helpers

All acquire read lock internally. `AvailableGPUs` and `HasContiguousGPUs` subtract active reservations.

## Test Cases (`worldstate_test.go`)

Package `worldstate_test` (black-box).

| Test | Description |
|------|-------------|
| `TestSnapshot_IsImmutable` | Snapshot unaffected by subsequent mutations; WorldState unaffected by snapshot mutation |
| `TestUpsertReplica_UpdatesNodeAllocations` | Table-driven GPU tracking across add/delete/status-only update |
| `TestAvailableGPUs_SubtractsReservations` | Table-driven: no reservations, active, fulfilled, expired, different cluster, floor at 0 |
| `TestHasContiguousGPUs_VariousNodeLayouts` | Table-driven: single node fits, doesn't fit, two nodes, reservation reduces, cordoned excluded |
| `TestReservation_Lifecycle` | Create→fulfill, create→expire, TTL expiry, duplicate error, double-fulfill error |
| `TestConcurrentAccess` | 10 goroutines, `-race` clean |
| `TestRecordScaleEvent` | Up resets consecutive; down increments |
| `TestUpsertNode_RecomputesFragmentation` | Fragmentation calculation verified |
| `TestDeleteReplica_NonExistent` | No-op, no panic |
| `TestUpsertReplica_MoveAcrossNodes` | Old node restored, new node decremented |
