package observability

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
)

// NewMetricsRegistry creates a *prometheus.Registry pre-loaded with Go runtime
// and process collectors.
func NewMetricsRegistry() *prometheus.Registry {
	reg := prometheus.NewRegistry()
	reg.MustRegister(collectors.NewGoCollector())
	reg.MustRegister(collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
	return reg
}

// RegisterBuildInfo registers a schedulator_info gauge with version and commit
// labels, set to 1.
func RegisterBuildInfo(reg prometheus.Registerer, version, commit string) error {
	info := prometheus.NewGauge(prometheus.GaugeOpts{
		Name:        "schedulator_info",
		Help:        "Build information for schedulator.",
		ConstLabels: prometheus.Labels{"version": version, "commit": commit},
	})
	info.Set(1)
	return reg.Register(info)
}

// MustNewCounter creates, registers, and returns a new Counter.
func MustNewCounter(reg prometheus.Registerer, opts prometheus.CounterOpts) prometheus.Counter {
	c := prometheus.NewCounter(opts)
	reg.MustRegister(c)
	return c
}

// MustNewCounterVec creates, registers, and returns a new CounterVec.
func MustNewCounterVec(reg prometheus.Registerer, opts prometheus.CounterOpts, labelNames []string) *prometheus.CounterVec {
	c := prometheus.NewCounterVec(opts, labelNames)
	reg.MustRegister(c)
	return c
}

// MustNewGauge creates, registers, and returns a new Gauge.
func MustNewGauge(reg prometheus.Registerer, opts prometheus.GaugeOpts) prometheus.Gauge {
	g := prometheus.NewGauge(opts)
	reg.MustRegister(g)
	return g
}

// MustNewGaugeVec creates, registers, and returns a new GaugeVec.
func MustNewGaugeVec(reg prometheus.Registerer, opts prometheus.GaugeOpts, labelNames []string) *prometheus.GaugeVec {
	g := prometheus.NewGaugeVec(opts, labelNames)
	reg.MustRegister(g)
	return g
}

// MustNewHistogram creates, registers, and returns a new Histogram.
func MustNewHistogram(reg prometheus.Registerer, opts prometheus.HistogramOpts) prometheus.Histogram {
	h := prometheus.NewHistogram(opts)
	reg.MustRegister(h)
	return h
}

// MustNewHistogramVec creates, registers, and returns a new HistogramVec.
func MustNewHistogramVec(reg prometheus.Registerer, opts prometheus.HistogramOpts, labelNames []string) *prometheus.HistogramVec {
	h := prometheus.NewHistogramVec(opts, labelNames)
	reg.MustRegister(h)
	return h
}
