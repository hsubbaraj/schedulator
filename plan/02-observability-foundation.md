# Section 02 — Observability Foundation: Pre-Implementation Doc

## Overview

Establish OTel tracing and Prometheus metrics plumbing so every subsequent section can instrument from day one.

## Package

`internal/observability/`

---

## Types & Function Signatures

### tracing.go

```go
package observability

import (
    "context"

    "go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
    "go.opentelemetry.io/otel/sdk/resource"
    sdktrace "go.opentelemetry.io/otel/sdk/trace"
    semconv "go.opentelemetry.io/otel/semconv/v1.39.0"
)

// TracingConfig controls how the tracer provider is created.
type TracingConfig struct {
    ServiceName  string // e.g. "schedulator"
    OTLPEndpoint string // gRPC target, e.g. "localhost:4317"; empty → no exporter
    Enabled      bool
}

// NewTracerProvider creates an sdktrace.TracerProvider.
// When !Enabled or OTLPEndpoint is empty, returns a provider with no exporter.
// Returns: provider, shutdown function, error.
func NewTracerProvider(cfg TracingConfig) (*sdktrace.TracerProvider, func(context.Context) error, error)
```

### metrics.go

```go
package observability

import "github.com/prometheus/client_golang/prometheus"

// NewMetricsRegistry creates a *prometheus.Registry pre-loaded with Go runtime
// and process collectors.
func NewMetricsRegistry() *prometheus.Registry

// RegisterBuildInfo registers a schedulator_info gauge with version and commit labels, set to 1.
func RegisterBuildInfo(reg prometheus.Registerer, version, commit string) error

// Metric registration helpers — accept Registerer interface, register and return.
func MustNewCounter(reg prometheus.Registerer, opts prometheus.CounterOpts) prometheus.Counter
func MustNewCounterVec(reg prometheus.Registerer, opts prometheus.CounterOpts, labelNames []string) *prometheus.CounterVec
func MustNewGauge(reg prometheus.Registerer, opts prometheus.GaugeOpts) prometheus.Gauge
func MustNewGaugeVec(reg prometheus.Registerer, opts prometheus.GaugeOpts, labelNames []string) *prometheus.GaugeVec
func MustNewHistogram(reg prometheus.Registerer, opts prometheus.HistogramOpts) prometheus.Histogram
func MustNewHistogramVec(reg prometheus.Registerer, opts prometheus.HistogramOpts, labelNames []string) *prometheus.HistogramVec
```

### testutil.go

```go
package observability

import (
    "go.opentelemetry.io/otel/sdk/trace/tracetest"
    "go.opentelemetry.io/otel/trace"
)

// NewTestTracer returns a tracer backed by a synchronous in-memory exporter.
// Spans are available immediately after End() — no time.Sleep needed.
func NewTestTracer() (trace.Tracer, *tracetest.InMemoryExporter)

// NewNoopTracer returns a tracer from noop.NewTracerProvider().
func NewNoopTracer() trace.Tracer
```

---

## Test Cases

### tracing_test.go (package `observability_test`)

| Test | Description |
|------|-------------|
| `TestNewTracerProvider_CreatesSpans` | Use `NewTestTracer`, start+end a span, assert it appears in the in-memory exporter with correct name |
| `TestNoopTracer_DoesNotPanic` | Create a span with `NewNoopTracer`, end it — no panic |
| `TestNewTracerProvider_DisabledConfig` | `TracingConfig{Enabled: false}` → returns non-nil provider and non-nil shutdown; shutdown returns nil |

### metrics_test.go (package `observability_test`)

| Test | Description |
|------|-------------|
| `TestMetricsRegistry_RegisterAndCollect` | Register a counter via `MustNewCounter`, increment, `Gather()`, assert value == 1 |
| `TestMetricsRegistry_GoCollectorPresent` | `NewMetricsRegistry()`, `Gather()`, assert `go_goroutines` metric family exists |
| `TestRegisterBuildInfo` | Register build info, gather, assert `schedulator_info{version="test",commit="abc123"} 1` |

---

## Edge Cases

- `NewTracerProvider` with `Enabled=true` but `OTLPEndpoint=""` → no exporter, no error
- `NewTracerProvider` with `Enabled=false` → no exporter, no error
- `MustNew*` helpers panic if registration fails (duplicate metric) — consistent with prometheus `Must*` convention
- `RegisterBuildInfo` called twice with same registry → returns error (duplicate registration)

---

## Integration with main.go

- `newMux(reg *prometheus.Registry)` adds `/metrics` endpoint via `promhttp.HandlerFor(reg, promhttp.HandlerOpts{})`
- `run()` creates registry, registers build info, creates tracer provider, defers shutdown
- Build-time variables: `var version, commit string` set via `-ldflags`
- Reads `OTEL_EXPORTER_OTLP_ENDPOINT` env var for tracing config
