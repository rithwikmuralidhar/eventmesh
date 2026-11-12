package observability

import (
	"net/http"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Metrics holds every Prometheus metric of the system. One instance is
// created per process and passed to whatever records into it; there is no
// global registry.
//
// Label values are always drawn from small fixed sets (status, category,
// route template). User, endpoint, event and delivery IDs are never labels:
// each distinct label value creates a new time series, so an ID label would
// grow without bound and exhaust the metrics store.
type Metrics struct {
	registry *prometheus.Registry

	EventsIngested      *prometheus.CounterVec
	DeliveryAttempts    *prometheus.CounterVec
	DeliveriesCompleted *prometheus.CounterVec
	AttemptDuration     *prometheus.HistogramVec
	DeliveryLatency     prometheus.Histogram
	QueueDepth          *prometheus.GaugeVec
	WorkersBusy         prometheus.Gauge
	RateLimited         *prometheus.CounterVec
	HTTPDuration        *prometheus.HistogramVec
}

// NewMetrics creates and registers all metrics on a fresh registry.
func NewMetrics() *Metrics {
	m := &Metrics{
		registry: prometheus.NewRegistry(),
		EventsIngested: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "eventmesh_events_ingested_total",
			Help: "Ingestion requests by result (created, replayed, conflict, invalid, error).",
		}, []string{"result"}),
		DeliveryAttempts: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "eventmesh_delivery_attempts_total",
			Help: "HTTP delivery attempts by outcome (success, failure) and error category.",
		}, []string{"outcome", "error_category"}),
		DeliveriesCompleted: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "eventmesh_deliveries_completed_total",
			Help: "Deliveries that reached a terminal status.",
		}, []string{"final_status"}),
		AttemptDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "eventmesh_delivery_attempt_duration_seconds",
			Help:    "Duration of one HTTP delivery attempt.",
			Buckets: []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 15},
		}, []string{"outcome"}),
		DeliveryLatency: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name:    "eventmesh_delivery_latency_seconds",
			Help:    "Time from event creation to successful delivery.",
			Buckets: []float64{0.01, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60, 300, 900},
		}),
		QueueDepth: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "eventmesh_queue_depth",
			Help: "Number of jobs in each Redis queue structure.",
		}, []string{"queue"}),
		WorkersBusy: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "eventmesh_workers_busy",
			Help: "Workers currently processing a delivery.",
		}),
		RateLimited: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "eventmesh_rate_limited_total",
			Help: "Requests or deliveries held back by a rate limit, by scope (api, ingest, endpoint).",
		}, []string{"scope"}),
		HTTPDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "eventmesh_http_request_duration_seconds",
			Help:    "API request duration by route template, method and status.",
			Buckets: prometheus.DefBuckets,
		}, []string{"route", "method", "status"}),
	}
	m.registry.MustRegister(
		m.EventsIngested, m.DeliveryAttempts, m.DeliveriesCompleted, m.AttemptDuration,
		m.DeliveryLatency, m.QueueDepth, m.WorkersBusy, m.RateLimited, m.HTTPDuration,
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)
	return m
}

// Handler serves the metrics in Prometheus text format. It is mounted on the
// internal metrics port only, never on the public API.
func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.registry, promhttp.HandlerOpts{})
}

// Registry exposes the registry so tests can read metric values.
func (m *Metrics) Registry() *prometheus.Registry { return m.registry }

// ObserveHTTP records one API request.
func (m *Metrics) ObserveHTTP(route, method string, status int, duration time.Duration) {
	m.HTTPDuration.WithLabelValues(route, method, strconv.Itoa(status)).Observe(duration.Seconds())
}

// CountRateLimited increments the rate-limited counter for a scope.
func (m *Metrics) CountRateLimited(scope string) {
	m.RateLimited.WithLabelValues(scope).Inc()
}
