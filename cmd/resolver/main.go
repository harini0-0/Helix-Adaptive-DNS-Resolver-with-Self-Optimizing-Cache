package main

import (
	"flag"
	"log"
	"net/http"
	"time"

	"github.com/harini0-0/Adaptive-DNS-Resolver-with-Self-Optimizing-Cache/internal/cache"
	"github.com/harini0-0/Adaptive-DNS-Resolver-with-Self-Optimizing-Cache/internal/dns"
	"github.com/harini0-0/Adaptive-DNS-Resolver-with-Self-Optimizing-Cache/internal/metrics"
)

func main() {
	listen := flag.String("listen", ":8053", "UDP address to listen on")
	upstream := flag.String("upstream", "1.1.1.1:53", "upstream DNS resolver")
	timeout := flag.Duration("timeout", 5*time.Second, "upstream query timeout")
	workers := flag.Int("workers", 6, "number of concurrent query handlers")
	queue := flag.Int("queue", 2, "pending-query buffer size")
	cacheSize := flag.Int("cache-size", 10000, "max number of cached questions")
	noCache := flag.Bool("no-cache", false, "bypass the cache entirely (benchmark baseline)")
	metricsAddr := flag.String("metrics-addr", ":9153", "address to serve /metrics on")
	flag.Parse()

	go func() {
		mux := http.NewServeMux()
		mux.Handle("/metrics", metrics.Handler())
		log.Printf("metrics listening on %s", *metricsAddr)
		log.Println(http.ListenAndServe(*metricsAddr, mux))
	}()

	srv := &dns.Server{
		Addr:      *listen,
		Upstream:  *upstream,
		Timeout:   *timeout,
		Workers:   *workers,
		QueueSize: *queue,
		Cache:     cache.New(*cacheSize),
		NoCache:   *noCache,
	}
	log.Fatal(srv.ListenAndServe())
}
