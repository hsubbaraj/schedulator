package k8sclient

import (
	"github.com/hsubbaraj/schedulator/internal/observability"
	"github.com/prometheus/client_golang/prometheus"
)

// Metrics holds Prometheus metrics for the K8s client.
type Metrics struct {
	requestDuration *prometheus.HistogramVec
	errorsTotal     *prometheus.CounterVec
}

// NewMetrics creates and registers K8s client metrics.
func NewMetrics(reg prometheus.Registerer, clusterID string) *Metrics {
	return &Metrics{
		requestDuration: observability.MustNewHistogramVec(reg, prometheus.HistogramOpts{
			Name:        "schedulator_k8sclient_request_duration_seconds",
			Help:        "Duration of K8s API requests.",
			ConstLabels: prometheus.Labels{"cluster": clusterID},
			Buckets:     prometheus.DefBuckets,
		}, []string{"operation"}),
		errorsTotal: observability.MustNewCounterVec(reg, prometheus.CounterOpts{
			Name:        "schedulator_k8sclient_errors_total",
			Help:        "Total number of K8s API request errors.",
			ConstLabels: prometheus.Labels{"cluster": clusterID},
		}, []string{"operation"}),
	}
}
