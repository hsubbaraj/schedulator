package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/hsubbaraj/schedulator/internal/observability"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Set via -ldflags at build time.
var (
	version = "dev"
	commit  = "none"
)

type healthResponse struct {
	Status string `json:"status"`
}

func newMux(reg *prometheus.Registry) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(healthResponse{Status: "ok"})
	})
	mux.Handle("/metrics", promhttp.HandlerFor(reg, promhttp.HandlerOpts{}))
	return mux
}

func run(ctx context.Context, addr string) error {
	reg := observability.NewMetricsRegistry()
	if err := observability.RegisterBuildInfo(reg, version, commit); err != nil {
		return fmt.Errorf("register build info: %w", err)
	}

	otlpEndpoint := os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT")
	_, shutdown, err := observability.NewTracerProvider(observability.TracingConfig{
		ServiceName:  "schedulator",
		OTLPEndpoint: otlpEndpoint,
		Enabled:      otlpEndpoint != "",
	})
	if err != nil {
		return fmt.Errorf("create tracer provider: %w", err)
	}
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := shutdown(shutdownCtx); err != nil {
			log.Printf("tracer shutdown error: %v", err)
		}
	}()

	srv := &http.Server{
		Addr:    addr,
		Handler: newMux(reg),
	}

	errCh := make(chan error, 1)
	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
		close(errCh)
	}()

	log.Printf("starting schedulator %s (%s) on %s", version, commit, addr)

	<-ctx.Done()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("server shutdown: %w", err)
	}

	return <-errCh
}

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, ":"+port); err != nil {
		log.Fatalf("server error: %v", err)
	}
}
