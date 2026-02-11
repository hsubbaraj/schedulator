package observability_test

import (
	"strings"
	"testing"

	"github.com/hsubbaraj/schedulator/internal/observability"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMetricsRegistry_RegisterAndCollect(t *testing.T) {
	reg := observability.NewMetricsRegistry()

	counter := observability.MustNewCounter(reg, prometheus.CounterOpts{
		Name: "test_counter_total",
		Help: "A test counter.",
	})
	counter.Inc()

	families, err := reg.Gather()
	require.NoError(t, err)

	var found bool
	for _, f := range families {
		if f.GetName() == "test_counter_total" {
			require.Len(t, f.GetMetric(), 1)
			assert.Equal(t, 1.0, f.GetMetric()[0].GetCounter().GetValue())
			found = true
			break
		}
	}
	assert.True(t, found, "test_counter_total metric not found")
}

func TestMetricsRegistry_GoCollectorPresent(t *testing.T) {
	reg := observability.NewMetricsRegistry()

	families, err := reg.Gather()
	require.NoError(t, err)

	var found bool
	for _, f := range families {
		if f.GetName() == "go_goroutines" {
			found = true
			break
		}
	}
	assert.True(t, found, "go_goroutines metric not found")
}

func TestRegisterBuildInfo(t *testing.T) {
	reg := prometheus.NewRegistry()
	err := observability.RegisterBuildInfo(reg, "v1.2.3", "abc123")
	require.NoError(t, err)

	expected := `
# HELP schedulator_info Build information for schedulator.
# TYPE schedulator_info gauge
schedulator_info{commit="abc123",version="v1.2.3"} 1
`
	err = testutil.GatherAndCompare(reg, strings.NewReader(expected), "schedulator_info")
	assert.NoError(t, err)
}
