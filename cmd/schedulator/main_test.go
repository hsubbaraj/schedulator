package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hsubbaraj/schedulator/internal/observability"
)

func testRegistry(t *testing.T) *prometheus.Registry {
	t.Helper()
	reg := observability.NewMetricsRegistry()
	require.NoError(t, observability.RegisterBuildInfo(reg, "test", "abc123"))
	return reg
}

func TestHealthEndpoint(t *testing.T) {
	srv := httptest.NewServer(newMux(testRegistry(t)))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/healthz")
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "application/json", resp.Header.Get("Content-Type"))

	var body healthResponse
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	assert.Equal(t, "ok", body.Status)
}

func TestMetricsEndpoint(t *testing.T) {
	srv := httptest.NewServer(newMux(testRegistry(t)))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/metrics")
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	bodyStr := string(body)
	assert.True(t, strings.Contains(bodyStr, "schedulator_info"), "expected schedulator_info metric in /metrics output")
	assert.True(t, strings.Contains(bodyStr, `version="test"`), "expected version label")
	assert.True(t, strings.Contains(bodyStr, `commit="abc123"`), "expected commit label")
}
