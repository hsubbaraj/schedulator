# Section 06 — Event Ingestion Layer (Pre-impl)

## Overview

The ingestion layer fans-in events from four sources (cluster events, vLLM metrics polling, app config watches, periodic eval timer) into a single debounced output channel for the control loop.

## Types

### `internal/ingestion/event.go`

```go
type EventKind string

const (
    EventMetricsUpdate  EventKind = "MetricsUpdate"
    EventClusterEvent   EventKind = "ClusterEvent"
    EventSLABreach      EventKind = "SLABreach"
    EventPeriodicEval   EventKind = "PeriodicEval"
    EventAppConfigUpdate EventKind = "AppConfigUpdate"
)

type Event struct {
    Kind         EventKind
    Timestamp    time.Time
    AppID        model.AppID
    VLLMMetrics  *model.VLLMMetrics
    ClusterEvent *model.ClusterEvent
    Application  *model.Application
    SLAMetric    string
    SLAThreshold float64
    SLAActual    float64
}
```

- `mergeKey()` returns `"<Kind>:<AppID>"` for mergeable events, `""` for ClusterEvent
- `isMergeable()` returns `false` for ClusterEvent, `true` for all others

### `internal/ingestion/debounce.go`

```go
type DebouncerConfig struct { Window time.Duration }
type Debouncer struct { cfg DebouncerConfig }

func NewDebouncer(cfg DebouncerConfig) *Debouncer
func (d *Debouncer) Run(ctx context.Context, inCh <-chan Event) <-chan []Event
```

Accumulates events in a map (keyed by `mergeKey()`, last-write-wins for mergeable) plus a slice for non-mergeable events. Timer resets on first event after flush. On timer fire, flush batch to output channel. On input close or ctx cancel, flush remaining and close output.

### `internal/ingestion/ingester.go`

```go
type IngesterConfig struct {
    MetricsPollInterval  time.Duration // default 10s
    PeriodicEvalInterval time.Duration // default 60s
    DebounceWindow       time.Duration // default 2s
}

type Ingester struct {
    agg         ports.ClusterAggregator
    cs          ports.ConfigStore
    cfg         IngesterConfig
    tracer      trace.Tracer
    eventsTotal *prometheus.CounterVec
    batchesTotal prometheus.Counter
    batchSize   prometheus.Histogram
    debounceDur prometheus.Histogram
    appRegistry map[model.AppID]model.Application
    mu          sync.RWMutex
}

func NewIngester(...) *Ingester
func (ing *Ingester) Start(ctx context.Context) (<-chan []Event, error)
```

Internal methods:
- `listenClusterEvents(ctx, eventCh, rawCh, wg)` — forwards cluster events
- `listenAppConfig(ctx, appCh, rawCh, wg)` — forwards app config updates, updates registry
- `pollVLLMMetrics(ctx, rawCh, wg)` — polls at interval, checks SLA breaches
- `periodicEvalTimer(ctx, rawCh, wg)` — fires PeriodicEval at interval
- `instrumentBatches(ctx, inCh) <-chan []Event` — wraps batches with spans/metrics

## Merge Strategy

| EventKind       | mergeKey         | Merge Behavior      |
|-----------------|------------------|----------------------|
| MetricsUpdate   | MetricsUpdate:ID | last-write-wins      |
| ClusterEvent    | "" (empty)       | never merged         |
| AppConfigUpdate | AppConfigUpdate:ID | last-write-wins    |
| PeriodicEval    | PeriodicEval:    | deduplicated         |
| SLABreach       | SLABreach:ID     | last-write-wins      |

## Test Cases

### event_test.go
- mergeKey returns correct keys for each event kind
- isMergeable returns false only for ClusterEvent

### debounce_test.go
- MergesMetricsUpdates: 2 same-app MetricsUpdate → 1 event (latest)
- DifferentAppsNotMerged: different apps preserved as separate events
- WindowExpiry: events after window in new batch
- ClusterEventsNotCoalesced: multiple cluster events all appear
- PeriodicEvalDeduplicated: 2 PeriodicEval → 1
- MixedEventTypes: various types coalesced correctly
- ContextCancellation: clean shutdown, output closed
- InputChannelClosed: remaining flushed, output closed

### ingester_test.go
- ClusterEventPassthrough: event appears in output batch
- TimerFires: PeriodicEval appears
- MetricsPolling: vLLM metrics forwarded
- AppConfigUpdate: change forwarded and registry updated
- NewAppAppearsInMetricsPolling: dynamically added app gets polled
- MetricsPollError_ContinuesOtherApps: partial failure resilient
- GracefulShutdown: ctx cancel closes output
- StartError_WatchEventsFails: returns error
- StartError_WatchApplicationsFails: returns error

## Edge Cases

- vLLM metrics poll for an app that was just removed from registry: skip gracefully
- SLA breach detection compares P99 TTFT and TPS against SLA thresholds
- appRegistry protected by RWMutex for concurrent access from poll and watch goroutines
- rawCh buffered at 64, outCh buffered at 1
