package observability_test

import (
	"context"
	"testing"

	"github.com/hsubbaraj/schedulator/internal/observability"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewTracerProvider_CreatesSpans(t *testing.T) {
	tracer, exp := observability.NewTestTracer()

	_, span := tracer.Start(context.Background(), "test.operation")
	span.End()

	spans := exp.GetSpans()
	require.Len(t, spans, 1)
	assert.Equal(t, "test.operation", spans[0].Name)
}

func TestNoopTracer_DoesNotPanic(t *testing.T) {
	tracer := observability.NewNoopTracer()

	_, span := tracer.Start(context.Background(), "noop.operation")
	span.End()
}

func TestNewTracerProvider_DisabledConfig(t *testing.T) {
	tp, shutdown, err := observability.NewTracerProvider(observability.TracingConfig{
		ServiceName: "test",
		Enabled:     false,
	})
	require.NoError(t, err)
	require.NotNil(t, tp)
	require.NotNil(t, shutdown)

	err = shutdown(context.Background())
	assert.NoError(t, err)
}
