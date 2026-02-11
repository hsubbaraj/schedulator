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
//
// When !Enabled or OTLPEndpoint is empty, returns a provider with no exporter
// (spans are created but not exported). The shutdown function is always safe to call.
func NewTracerProvider(cfg TracingConfig) (*sdktrace.TracerProvider, func(context.Context) error, error) {
	res, err := resource.Merge(
		resource.Default(),
		resource.NewWithAttributes(
			semconv.SchemaURL,
			semconv.ServiceName(cfg.ServiceName),
		),
	)
	if err != nil {
		return nil, nil, err
	}

	opts := []sdktrace.TracerProviderOption{
		sdktrace.WithResource(res),
	}

	if cfg.Enabled && cfg.OTLPEndpoint != "" {
		exp, err := otlptracegrpc.New(
			context.Background(),
			otlptracegrpc.WithEndpoint(cfg.OTLPEndpoint),
			otlptracegrpc.WithInsecure(),
		)
		if err != nil {
			return nil, nil, err
		}
		opts = append(opts, sdktrace.WithBatcher(exp))
	}

	tp := sdktrace.NewTracerProvider(opts...)
	shutdown := func(ctx context.Context) error {
		return tp.Shutdown(ctx)
	}
	return tp, shutdown, nil
}
