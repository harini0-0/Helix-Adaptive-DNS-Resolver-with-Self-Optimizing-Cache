package main

import (
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
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
	healthCheck := flag.Bool("health-check", false, "probe the local /metrics endpoint, exit 0/1 (for container HEALTHCHECK)")
	flag.Parse()

	// The final image is FROM scratch and has no curl/wget/shell, so the
	// container healthcheck re-execs this same binary to probe its own metrics
	// endpoint and report liveness through the exit code.
	if *healthCheck {
		if err := probeHealth(*metricsAddr); err != nil {
			log.Printf("health check failed: %v", err)
			os.Exit(1)
		}
		os.Exit(0)
	}

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

// probeHealth issues a GET against the local /metrics endpoint. metricsAddr may
// omit the host (e.g. ":9153"), in which case we probe loopback.
func probeHealth(metricsAddr string) error {
	host, port, err := net.SplitHostPort(metricsAddr)
	if err != nil {
		return err
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get("http://" + net.JoinHostPort(host, port) + "/metrics")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected status %d", resp.StatusCode)
	}
	return nil
}
