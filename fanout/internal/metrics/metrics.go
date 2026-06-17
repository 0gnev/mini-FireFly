// Package metrics defines the Prometheus collectors fanout exposes (SPEC
// §12.1), with exact metric names, types, labels, and histogram buckets.
package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
)

// Metrics bundles all fanout collectors. Construct with New and register on a
// dedicated registry so /metrics exposes exactly these (plus Go/process
// collectors).
type Metrics struct {
	ProviderRequests *prometheus.CounterVec   // fanout_provider_requests_total{provider,status}
	ProviderLatency  *prometheus.HistogramVec // fanout_provider_latency_seconds{provider}
	BreakerState     *prometheus.GaugeVec     // fanout_breaker_state{provider}
	Retries          *prometheus.CounterVec   // fanout_retries_total{provider}
	RateLimited      *prometheus.CounterVec   // fanout_rate_limited_total{provider}
	Inflight         *prometheus.GaugeVec     // fanout_inflight{provider}
	RequestDuration  prometheus.Histogram     // fanout_request_duration_seconds
}

// latencyBuckets per SPEC §12.1: .05 .1 .2 .4 .8 1.6 3.2
var latencyBuckets = []float64{0.05, 0.1, 0.2, 0.4, 0.8, 1.6, 3.2}

// New constructs and registers all collectors on reg.
func New(reg prometheus.Registerer) *Metrics {
	m := &Metrics{
		ProviderRequests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "fanout_provider_requests_total",
			Help: "Total provider requests by provider and final status.",
		}, []string{"provider", "status"}),

		ProviderLatency: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "fanout_provider_latency_seconds",
			Help:    "Per-provider call latency in seconds.",
			Buckets: latencyBuckets,
		}, []string{"provider"}),

		BreakerState: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "fanout_breaker_state",
			Help: "Breaker state per provider (0 closed / 1 half_open / 2 open).",
		}, []string{"provider"}),

		Retries: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "fanout_retries_total",
			Help: "Total retry attempts per provider.",
		}, []string{"provider"}),

		RateLimited: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "fanout_rate_limited_total",
			Help: "Total client-side rate-limited decisions per provider.",
		}, []string{"provider"}),

		Inflight: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "fanout_inflight",
			Help: "In-flight provider calls per provider.",
		}, []string{"provider"}),

		RequestDuration: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name:    "fanout_request_duration_seconds",
			Help:    "Whole fan-out request duration in seconds.",
			Buckets: latencyBuckets,
		}),
	}
	reg.MustRegister(
		m.ProviderRequests,
		m.ProviderLatency,
		m.BreakerState,
		m.Retries,
		m.RateLimited,
		m.Inflight,
		m.RequestDuration,
	)
	return m
}
