package ingestion

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/hsubbaraj/schedulator/internal/observability"
	"github.com/hsubbaraj/schedulator/pkg/model"
	"github.com/hsubbaraj/schedulator/pkg/ports"
)

const rawChSize = 64

// IngesterConfig configures the ingestion layer.
type IngesterConfig struct {
	MetricsPollInterval  time.Duration
	PeriodicEvalInterval time.Duration
	DebounceWindow       time.Duration
}

// Ingester fans-in events from external sources and emits debounced batches.
type Ingester struct {
	agg    ports.ClusterAggregator
	cs     ports.ConfigStore
	cfg    IngesterConfig
	tracer trace.Tracer

	eventsTotal  *prometheus.CounterVec
	batchesTotal prometheus.Counter
	batchSize    prometheus.Histogram
	debounceDur  prometheus.Histogram

	mu          sync.RWMutex
	appRegistry map[model.AppID]model.Application

	injectCh chan Event
}

// NewIngester creates an Ingester wired to the given ports and instrumentation.
func NewIngester(
	agg ports.ClusterAggregator,
	cs ports.ConfigStore,
	cfg IngesterConfig,
	tracer trace.Tracer,
	reg prometheus.Registerer,
) *Ingester {
	return &Ingester{
		agg:    agg,
		cs:     cs,
		cfg:    cfg,
		tracer: tracer,
		eventsTotal: observability.MustNewCounterVec(reg, prometheus.CounterOpts{
			Name: "schedulator_ingestion_events_received_total",
			Help: "Total ingestion events received, by kind.",
		}, []string{"kind"}),
		batchesTotal: observability.MustNewCounter(reg, prometheus.CounterOpts{
			Name: "schedulator_ingestion_batches_emitted_total",
			Help: "Total debounced batches emitted.",
		}),
		batchSize: observability.MustNewHistogram(reg, prometheus.HistogramOpts{
			Name:    "schedulator_ingestion_batch_size",
			Help:    "Number of events per debounced batch.",
			Buckets: prometheus.ExponentialBuckets(1, 2, 8),
		}),
		debounceDur: observability.MustNewHistogram(reg, prometheus.HistogramOpts{
			Name:    "schedulator_ingestion_debounce_duration_seconds",
			Help:    "Time spent debouncing before emitting a batch.",
			Buckets: prometheus.DefBuckets,
		}),
		appRegistry: make(map[model.AppID]model.Application),
		injectCh:    make(chan Event, rawChSize),
	}
}

// InjectEvent pushes an externally generated event into the ingestion pipeline.
func (ing *Ingester) InjectEvent(ctx context.Context, e Event) {
	select {
	case ing.injectCh <- e:
	case <-ctx.Done():
	}
}

// Start initializes all listener goroutines and returns a channel of debounced
// event batches. The caller should read from the returned channel until it is
// closed (which happens when ctx is cancelled). Start may only be called once.
func (ing *Ingester) Start(ctx context.Context) (<-chan []Event, error) {
	// Seed app registry.
	apps, err := ing.cs.ListApplications(ctx)
	if err != nil {
		return nil, fmt.Errorf("ingester: list applications: %w", err)
	}
	for _, app := range apps {
		ing.appRegistry[app.AppID] = app
	}

	// Set up watches.
	eventCh, err := ing.agg.WatchEvents(ctx)
	if err != nil {
		return nil, fmt.Errorf("ingester: watch events: %w", err)
	}
	appCh, err := ing.cs.WatchApplications(ctx)
	if err != nil {
		return nil, fmt.Errorf("ingester: watch applications: %w", err)
	}

	rawCh := make(chan Event, rawChSize)

	metricsCh, err := ing.cs.WatchMetrics(ctx)
	if err != nil {
		return nil, fmt.Errorf("ingester: watch metrics: %w", err)
	}

	var wg sync.WaitGroup
	wg.Add(6)
	go ing.listenClusterEvents(ctx, eventCh, rawCh, &wg)
	go ing.listenAppConfig(ctx, appCh, rawCh, &wg)
	go ing.listenMetricsConfig(ctx, metricsCh, rawCh, &wg)
	go ing.listenInjectedEvents(ctx, rawCh, &wg)
	go ing.pollVLLMMetrics(ctx, rawCh, &wg)
	go ing.periodicEvalTimer(ctx, rawCh, &wg)

	// Close rawCh after all listeners exit.
	go func() {
		wg.Wait()
		close(rawCh)
	}()

	debouncer := NewDebouncer(DebouncerConfig{Window: ing.cfg.DebounceWindow})
	debouncedCh := debouncer.Run(ctx, rawCh)
	outCh := ing.instrumentBatches(ctx, debouncedCh)

	return outCh, nil
}

func (ing *Ingester) listenInjectedEvents(ctx context.Context, rawCh chan<- Event, wg *sync.WaitGroup) {
	defer wg.Done()
	for {
		select {
		case <-ctx.Done():
			return
		case e, ok := <-ing.injectCh:
			if !ok {
				return
			}
			_, span := ing.tracer.Start(ctx, "ingestion.receive_event",
				trace.WithAttributes(attribute.String("kind", string(e.Kind)), attribute.Bool("injected", true)))
			ing.eventsTotal.WithLabelValues(string(e.Kind)).Inc()

			rawCh <- e
			span.End()

			// If it's a metrics update, also check for SLA breaches.
			if e.Kind == EventMetricsUpdate && e.VLLMMetrics != nil {
				ing.mu.RLock()
				app, ok := ing.appRegistry[e.VLLMMetrics.AppID]
				ing.mu.RUnlock()
				if ok {
					ing.checkSLABreach(ctx, app, *e.VLLMMetrics, rawCh)
				}
			}
		}
	}
}

func (ing *Ingester) listenClusterEvents(ctx context.Context, eventCh <-chan model.ClusterEvent, rawCh chan<- Event, wg *sync.WaitGroup) {
	defer wg.Done()
	for {
		select {
		case <-ctx.Done():
			return
		case ce, ok := <-eventCh:
			if !ok {
				return
			}
			_, span := ing.tracer.Start(ctx, "ingestion.receive_event",
				trace.WithAttributes(attribute.String("kind", string(EventClusterEvent))))
			ing.eventsTotal.WithLabelValues(string(EventClusterEvent)).Inc()
			rawCh <- Event{
				Kind:         EventClusterEvent,
				Timestamp:    ce.Timestamp,
				ClusterEvent: &ce,
			}
			span.End()
		}
	}
}

func (ing *Ingester) listenAppConfig(ctx context.Context, appCh <-chan model.Application, rawCh chan<- Event, wg *sync.WaitGroup) {
	defer wg.Done()
	for {
		select {
		case <-ctx.Done():
			return
		case app, ok := <-appCh:
			if !ok {
				return
			}
			_, span := ing.tracer.Start(ctx, "ingestion.receive_event",
				trace.WithAttributes(attribute.String("kind", string(EventAppConfigUpdate))))
			ing.eventsTotal.WithLabelValues(string(EventAppConfigUpdate)).Inc()

			ing.mu.Lock()
			ing.appRegistry[app.AppID] = app
			ing.mu.Unlock()

			rawCh <- Event{
				Kind:        EventAppConfigUpdate,
				Timestamp:   time.Now(),
				AppID:       app.AppID,
				Application: &app,
			}
			span.End()
		}
	}
}

func (ing *Ingester) listenMetricsConfig(ctx context.Context, metricsCh <-chan model.VLLMMetrics, rawCh chan<- Event, wg *sync.WaitGroup) {
	defer wg.Done()
	for {
		select {
		case <-ctx.Done():
			return
		case metrics, ok := <-metricsCh:
			if !ok {
				return
			}
			_, span := ing.tracer.Start(ctx, "ingestion.receive_event",
				trace.WithAttributes(attribute.String("kind", string(EventMetricsUpdate))))
			ing.eventsTotal.WithLabelValues(string(EventMetricsUpdate)).Inc()

			rawCh <- Event{
				Kind:        EventMetricsUpdate,
				Timestamp:   metrics.MeasuredAt,
				AppID:       metrics.AppID,
				VLLMMetrics: &metrics,
			}
			span.End()

			// Check SLA breaches using the app from registry.
			ing.mu.RLock()
			app, ok := ing.appRegistry[metrics.AppID]
			ing.mu.RUnlock()
			if ok {
				ing.checkSLABreach(ctx, app, metrics, rawCh)
			}
		}
	}
}

func (ing *Ingester) pollVLLMMetrics(ctx context.Context, rawCh chan<- Event, wg *sync.WaitGroup) {
	defer wg.Done()

	ticker := time.NewTicker(ing.cfg.MetricsPollInterval)
	defer ticker.Stop()

	// Do an initial poll immediately.
	ing.doPollMetrics(ctx, rawCh)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			ing.doPollMetrics(ctx, rawCh)
		}
	}
}

func (ing *Ingester) doPollMetrics(ctx context.Context, rawCh chan<- Event) {
	ing.mu.RLock()
	apps := make([]model.Application, 0, len(ing.appRegistry))
	for _, app := range ing.appRegistry {
		apps = append(apps, app)
	}
	ing.mu.RUnlock()

	for _, app := range apps {
		metrics, err := ing.agg.GetVLLMMetrics(ctx, app.AppID)
		if err != nil {
			continue
		}

		_, span := ing.tracer.Start(ctx, "ingestion.receive_event",
			trace.WithAttributes(attribute.String("kind", string(EventMetricsUpdate))))
		ing.eventsTotal.WithLabelValues(string(EventMetricsUpdate)).Inc()
		rawCh <- Event{
			Kind:        EventMetricsUpdate,
			Timestamp:   metrics.MeasuredAt,
			AppID:       app.AppID,
			VLLMMetrics: &metrics,
		}
		span.End()

		// Check SLA breaches.
		ing.checkSLABreach(ctx, app, metrics, rawCh)
	}
}

func (ing *Ingester) checkSLABreach(ctx context.Context, app model.Application, metrics model.VLLMMetrics, rawCh chan<- Event) {
	if app.SLA.MaxP99TTFTMs > 0 && metrics.P99TimeToFirstTokenMs > float64(app.SLA.MaxP99TTFTMs) {
		_, span := ing.tracer.Start(ctx, "ingestion.receive_event",
			trace.WithAttributes(attribute.String("kind", string(EventSLABreach))))
		ing.eventsTotal.WithLabelValues(string(EventSLABreach)).Inc()
		rawCh <- Event{
			Kind:         EventSLABreach,
			Timestamp:    time.Now(),
			AppID:        app.AppID,
			SLAMetric:    "P99TTFT",
			SLAThreshold: float64(app.SLA.MaxP99TTFTMs),
			SLAActual:    metrics.P99TimeToFirstTokenMs,
		}
		span.End()
	}

	if app.SLA.MaxP99TPS > 0 && metrics.TotalTokensPerSecond < float64(app.SLA.MaxP99TPS) {
		_, span := ing.tracer.Start(ctx, "ingestion.receive_event",
			trace.WithAttributes(attribute.String("kind", string(EventSLABreach))))
		ing.eventsTotal.WithLabelValues(string(EventSLABreach)).Inc()
		rawCh <- Event{
			Kind:         EventSLABreach,
			Timestamp:    time.Now(),
			AppID:        app.AppID,
			SLAMetric:    "TPS",
			SLAThreshold: float64(app.SLA.MaxP99TPS),
			SLAActual:    metrics.TotalTokensPerSecond,
		}
		span.End()
	}
}

func (ing *Ingester) periodicEvalTimer(ctx context.Context, rawCh chan<- Event, wg *sync.WaitGroup) {
	defer wg.Done()

	ticker := time.NewTicker(ing.cfg.PeriodicEvalInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			_, span := ing.tracer.Start(ctx, "ingestion.receive_event",
				trace.WithAttributes(attribute.String("kind", string(EventPeriodicEval))))
			ing.eventsTotal.WithLabelValues(string(EventPeriodicEval)).Inc()
			rawCh <- Event{
				Kind:      EventPeriodicEval,
				Timestamp: time.Now(),
			}
			span.End()
		}
	}
}

func (ing *Ingester) instrumentBatches(ctx context.Context, inCh <-chan []Event) <-chan []Event {
	outCh := make(chan []Event, 1)
	go func() {
		defer close(outCh)
		for {
			select {
			case <-ctx.Done():
				return
			case batch, ok := <-inCh:
				if !ok {
					return
				}
				_, span := ing.tracer.Start(ctx, "ingestion.emit_batch",
					trace.WithAttributes(attribute.Int("batch_size", len(batch))))
				ing.batchesTotal.Inc()
				ing.batchSize.Observe(float64(len(batch)))
				span.End()

				select {
				case outCh <- batch:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	return outCh
}
