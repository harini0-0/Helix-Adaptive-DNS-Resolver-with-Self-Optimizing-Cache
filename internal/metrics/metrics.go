// Package metrics defines the resolver's Prometheus instrumentation.
package metrics

import (
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var (
	// QueriesTotal counts every query the resolver receives.
	QueriesTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "dns_queries_total",
		Help: "Total number of DNS queries received.",
	})

	// CacheHits counts queries answered directly from the cache.
	CacheHits = promauto.NewCounter(prometheus.CounterOpts{
		Name: "dns_cache_hits_total",
		Help: "Total number of queries served from cache.",
	})

	// CacheMisses counts queries that required an upstream forward.
	CacheMisses = promauto.NewCounter(prometheus.CounterOpts{
		Name: "dns_cache_misses_total",
		Help: "Total number of queries that missed the cache.",
	})

	// ResolveDuration measures end-to-end latency for a query, whether
	// served from cache or forwarded upstream.
	ResolveDuration = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "dns_resolve_duration_seconds",
		Help:    "Time to fully resolve a query (cache hit or upstream forward).",
		Buckets: prometheus.DefBuckets,
	})

	// CacheSize reports the current number of entries held in the cache.
	CacheSize = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "dns_cache_size",
		Help: "Current number of entries in the cache.",
	})

	// InFlightRequests reports how many queries are currently being handled.
	InFlightRequests = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "dns_in_flight_requests",
		Help: "Number of queries currently being handled.",
	})
)

// Handler returns the HTTP handler that serves the /metrics endpoint.
func Handler() http.Handler {
	return promhttp.Handler()
}
