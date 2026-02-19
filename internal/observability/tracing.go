package observability

import (
	"context"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/exporters/stdout/stdouttrace"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.39.0"
)

// TracingConfig controls how the tracer provider is created.
type TracingConfig struct {
	ServiceName   string // e.g. "schedulator"
	Environment   string // e.g. "production", "staging"
	OTLPEndpoint  string // gRPC target, e.g. "localhost:4317"; empty → no exporter
	Enabled       bool
	Stdout        bool    // If true, export to stdout
	SamplingRatio float64 // 0.0 to 1.0; 0.0 means "always off", 1.0 means "always on" (default if 0.0 and enabled)
}

// NewTracerProvider creates an sdktrace.TracerProvider.
//
// When !Enabled or OTLPEndpoint is empty, returns a provider with no exporter
// (spans are created but not exported). The shutdown function is always safe to call.
func NewTracerProvider(cfg TracingConfig) (*sdktrace.TracerProvider, func(context.Context) error, error) {
	if cfg.Environment == "" {
		cfg.Environment = "development"
	}
	res, err := resource.Merge(
		resource.Default(),
		resource.NewWithAttributes(
			semconv.SchemaURL,
			semconv.ServiceName(cfg.ServiceName),
			attribute.String("deployment.environment", cfg.Environment),
		),
	)
	if err != nil {
		return nil, nil, err
	}

	opts := []sdktrace.TracerProviderOption{
		sdktrace.WithResource(res),
	}

	if cfg.Enabled {
		// Set up sampling: ParentBased(TraceIDRatioBased).
		// If ratio is 0.0 but enabled, we default to 1.0 (AlwaysOn) to avoid
		// accidental complete silencing.
		ratio := cfg.SamplingRatio
		if ratio <= 0 {
			ratio = 1.0
		}
		opts = append(opts, sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.TraceIDRatioBased(ratio))))

		if cfg.OTLPEndpoint != "" {
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
		if cfg.Stdout {
			exp, err := stdouttrace.New(stdouttrace.WithPrettyPrint())
			if err != nil {
				return nil, nil, err
			}
			opts = append(opts, sdktrace.WithBatcher(exp))
		}
	}

	tp := sdktrace.NewTracerProvider(opts...)
	shutdown := func(ctx context.Context) error {
		return tp.Shutdown(ctx)
	}
	return tp, shutdown, nil
}
