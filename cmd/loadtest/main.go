// Command loadtest fires a concurrent stream of DNS A-record queries at the
// resolver and reports throughput and latency percentiles (p50/p95/p99). The
// workload is a mix of a small "hot" set of repeated names (which exercise the
// cache) and a long tail of unique names (which force misses), controlled by
// -hot and -phot. Point -metrics-url at the resolver's /metrics to also print
// the cache hit ratio measured over the run.
package main

import (
	"encoding/binary"
	"flag"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:8053", "resolver UDP address")
	duration := flag.Duration("duration", 10*time.Second, "how long to generate load")
	concurrency := flag.Int("concurrency", 50, "number of concurrent senders")
	hot := flag.Int("hot", 20, "size of the hot (repeated) domain set")
	phot := flag.Float64("phot", 0.9, "probability a query targets the hot set")
	timeout := flag.Duration("timeout", 2*time.Second, "per-query response timeout")
	metricsURL := flag.String("metrics-url", "", "resolver /metrics URL for hit-ratio (optional)")
	workload := flag.String("workload", "zipf", "workload: zipf (hot set + unique tail) or revival (round-robin recurring + noise)")
	recurring := flag.Int("recurring", 200, "revival: size of the recurring key set")
	noise := flag.Float64("noise", 0.5, "revival: probability a query is unique noise")
	flag.Parse()

	hotNames := make([]string, *hot)
	for i := range hotNames {
		hotNames[i] = fmt.Sprintf("hot%d.example.com", i)
	}

	var uniqueCounter uint64 // distinct noise/tail keys generated
	var recCounter uint64    // round-robin cursor over the recurring set

	// pickName selects the target domain for one query. In "revival", recurring
	// keys are requested ONE AT A TIME in round-robin order (no bursts), so the
	// only way to serve one from cache is to have kept it alive across the many
	// unique noise keys seen since its last access. A key accessed every
	// `recurring` requests but evicted by noise in between is the case recency
	// handles badly (it looks stale) and frequency handles well (it is popular).
	pickName := func(rng *rand.Rand, elapsed time.Duration) string {
		if *workload == "revival" {
			if rng.Float64() < *noise {
				n := atomic.AddUint64(&uniqueCounter, 1)
				return "noise" + strconv.FormatUint(n, 10) + ".example.com"
			}
			idx := int(atomic.AddUint64(&recCounter, 1) % uint64(*recurring))
			return "rec" + strconv.Itoa(idx) + ".example.com"
		}
		// default "zipf": hot set + unique tail
		if rng.Float64() < *phot {
			return hotNames[rng.Intn(len(hotNames))]
		}
		n := atomic.AddUint64(&uniqueCounter, 1)
		return "u" + strconv.FormatUint(n, 10) + ".example.com"
	}

	hitsBefore, missesBefore := scrapeCacheCounters(*metricsURL)

	var (
		totalOK   uint64
		totalErr  uint64
		wg        sync.WaitGroup
		mu        sync.Mutex
		latencies []time.Duration
	)

	deadline := time.Now().Add(*duration)
	start := time.Now()
	wg.Add(*concurrency)
	for w := 0; w < *concurrency; w++ {
		go func(seed int64) {
			defer wg.Done()
			rng := rand.New(rand.NewSource(seed))
			conn, err := net.Dial("udp", *addr)
			if err != nil {
				log.Printf("dial error: %v", err)
				return
			}
			defer conn.Close()

			local := make([]time.Duration, 0, 4096)
			readBuf := make([]byte, 512)
			for time.Now().Before(deadline) {
				name := pickName(rng, time.Since(start))
				query := buildQuery(uint16(rng.Intn(65536)), name)

				t0 := time.Now()
				_ = conn.SetDeadline(time.Now().Add(*timeout))
				if _, err := conn.Write(query); err != nil {
					atomic.AddUint64(&totalErr, 1)
					continue
				}
				if _, err := conn.Read(readBuf); err != nil {
					atomic.AddUint64(&totalErr, 1)
					continue
				}
				local = append(local, time.Since(t0))
				atomic.AddUint64(&totalOK, 1)
			}
			mu.Lock()
			latencies = append(latencies, local...)
			mu.Unlock()
		}(int64(w) + 1)
	}
	wg.Wait()
	elapsed := time.Since(start)

	hitsAfter, missesAfter := scrapeCacheCounters(*metricsURL)

	report(elapsed, atomic.LoadUint64(&totalOK), atomic.LoadUint64(&totalErr), latencies,
		hitsAfter-hitsBefore, missesAfter-missesBefore, *metricsURL != "")
}

// buildQuery encodes a minimal DNS query for an A record (no EDNS).
func buildQuery(id uint16, name string) []byte {
	msg := make([]byte, 12, 12+len(name)+6)
	binary.BigEndian.PutUint16(msg[0:2], id)
	binary.BigEndian.PutUint16(msg[2:4], 0x0100) // RD set
	binary.BigEndian.PutUint16(msg[4:6], 1)      // QDCOUNT
	for _, label := range strings.Split(name, ".") {
		msg = append(msg, byte(len(label)))
		msg = append(msg, label...)
	}
	msg = append(msg, 0)          // root label
	msg = append(msg, 0x00, 0x01) // TYPE A
	msg = append(msg, 0x00, 0x01) // CLASS IN
	return msg
}

func report(elapsed time.Duration, ok, errs uint64, lat []time.Duration, hits, misses uint64, showHitRatio bool) {
	sort.Slice(lat, func(i, j int) bool { return lat[i] < lat[j] })
	qps := float64(ok) / elapsed.Seconds()

	fmt.Printf("duration        %s\n", elapsed.Round(time.Millisecond))
	fmt.Printf("queries ok      %d\n", ok)
	fmt.Printf("queries failed  %d\n", errs)
	fmt.Printf("throughput      %.0f qps\n", qps)
	if len(lat) > 0 {
		fmt.Printf("latency p50     %s\n", pctl(lat, 0.50).Round(time.Microsecond))
		fmt.Printf("latency p95     %s\n", pctl(lat, 0.95).Round(time.Microsecond))
		fmt.Printf("latency p99     %s\n", pctl(lat, 0.99).Round(time.Microsecond))
		fmt.Printf("latency max     %s\n", lat[len(lat)-1].Round(time.Microsecond))
	}
	if showHitRatio {
		total := hits + misses
		ratio := 0.0
		if total > 0 {
			ratio = float64(hits) / float64(total)
		}
		fmt.Printf("cache hits      %d\n", hits)
		fmt.Printf("cache misses    %d\n", misses)
		fmt.Printf("hit ratio       %.1f%%\n", ratio*100)
	}
}

// pctl returns the p-quantile of a pre-sorted slice (nearest-rank method).
func pctl(sorted []time.Duration, p float64) time.Duration {
	if len(sorted) == 0 {
		return 0
	}
	idx := int(p * float64(len(sorted)))
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}

// scrapeCacheCounters reads dns_cache_hits_total and dns_cache_misses_total from
// a Prometheus /metrics endpoint. Returns zeros if url is empty or unreachable.
func scrapeCacheCounters(url string) (hits, misses uint64) {
	if url == "" {
		return 0, 0
	}
	resp, err := http.Get(url)
	if err != nil {
		log.Printf("metrics scrape error: %v", err)
		return 0, 0
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	for _, line := range strings.Split(string(body), "\n") {
		switch {
		case strings.HasPrefix(line, "dns_cache_hits_total "):
			hits = parseCounter(line)
		case strings.HasPrefix(line, "dns_cache_misses_total "):
			misses = parseCounter(line)
		}
	}
	return hits, misses
}

func parseCounter(line string) uint64 {
	fields := strings.Fields(line)
	if len(fields) < 2 {
		return 0
	}
	f, err := strconv.ParseFloat(fields[1], 64)
	if err != nil {
		return 0
	}
	return uint64(f)
}
