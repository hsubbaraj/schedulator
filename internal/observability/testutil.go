package observability

import (
	"go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	oteltrace "go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"
)

// NewTestTracer returns a tracer backed by a synchronous in-memory exporter.
// Spans are available immediately after End() — no time.Sleep needed.
func NewTestTracer() (oteltrace.Tracer, *tracetest.InMemoryExporter) {
	exp := tracetest.NewInMemoryExporter()
	tp := trace.NewTracerProvider(trace.WithSyncer(exp))
	return tp.Tracer("test"), exp
}

// NewNoopTracer returns a tracer that records nothing.
func NewNoopTracer() oteltrace.Tracer {
	return noop.NewTracerProvider().Tracer("noop")
}
