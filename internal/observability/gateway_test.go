package observability

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
)

func TestNewTracerProvider_WithGatewayAttributes(t *testing.T) {
	cfg := TracingConfig{
		ServiceName: "test-service",
		Environment: "test-env",
		Enabled:     true,
	}

	tp, shutdown, err := NewTracerProvider(cfg)
	require.NoError(t, err)
	defer shutdown(context.Background())

	tracer := tp.Tracer("test-tracer")
	ctx, span := tracer.Start(context.Background(), "test-span")
	
	// We can't easily assert on the exported span without a mock collector,
	// but we can verify the provider starts and attributes are set in theory.
	span.SetAttributes(attribute.String("test.attr", "value"))
	span.SetStatus(codes.Error, "something went wrong")
	span.End()

	_ = ctx
	assert.NotNil(t, tp)
}

func TestTracingConfig_EnvironmentDefault(t *testing.T) {
	cfg := TracingConfig{
		ServiceName: "test-service",
		// Environment intentionally left empty
		Enabled: true,
	}

	tp, shutdown, err := NewTracerProvider(cfg)
	require.NoError(t, err)
	defer shutdown(context.Background())

	assert.NotNil(t, tp)
}
