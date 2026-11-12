package dns

import (
	"context"
	"encoding/binary"
	"log"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/harini0-0/Adaptive-DNS-Resolver-with-Self-Optimizing-Cache/internal/cache"
	"github.com/harini0-0/Adaptive-DNS-Resolver-with-Self-Optimizing-Cache/internal/metrics"
	"github.com/harini0-0/Adaptive-DNS-Resolver-with-Self-Optimizing-Cache/internal/telemetry"
	"github.com/harini0-0/Adaptive-DNS-Resolver-with-Self-Optimizing-Cache/internal/workerpool"
)

// Server is a forwarding DNS resolver backed by a bounded worker pool and a
// TTL-aware LRU cache.
type Server struct {
	Addr      string              // e.g. ":8053"
	Upstream  string              // e.g. "1.1.1.1:53"
	Timeout   time.Duration       // per-query upstream timeout
	Workers   int                 // number of concurrent query handlers
	QueueSize int                 // pending-query buffer before Submit blocks
	Cache     *cache.Cache        // shared across all workers
	NoCache   bool                // when true, bypass the cache entirely (benchmark A/B)
	Telemetry *telemetry.Recorder // optional; nil disables feature logging
}

// cacheKey identifies a cacheable question. DNS names are case-insensitive,
// so the name is lowercased; class is omitted since IN is effectively the
// only class in use today.
func cacheKey(q Question) string {
	return strings.ToLower(q.Name) + "|" + strconv.Itoa(int(q.Type))
}

func (s *Server) ListenAndServe() error {
	addr, err := net.ResolveUDPAddr("udp", s.Addr)
	if err != nil {
		return err
	}
	conn, err := net.ListenUDP("udp", addr)
	if err != nil {
		return err
	}
	defer conn.Close()

	pool := workerpool.New(s.Workers, s.QueueSize)
	defer pool.Close()

	log.Printf("DNS listening on %s, upstream %s, workers=%d queue=%d",
		s.Addr, s.Upstream, s.Workers, s.QueueSize)

	buf := make([]byte, 512) // 512 = classic UDP DNS max; TCP/EDNS deferred
	for {
		n, client, err := conn.ReadFromUDP(buf)
		if err != nil {
			log.Printf("read error: %v", err)
			continue
		}
		// buf is reused on the next iteration, so copy this packet out
		// before handing it to a worker goroutine.
		packet := make([]byte, n)
		copy(packet, buf[:n])
		pool.Submit(func() {
			s.handle(conn, client, packet)
		})
	}
}

func (s *Server) handle(conn *net.UDPConn, client *net.UDPAddr, packet []byte) {
	metrics.QueriesTotal.Inc()
	metrics.InFlightRequests.Inc()
	defer metrics.InFlightRequests.Dec()

	start := time.Now()
	defer func() {
		metrics.ResolveDuration.Observe(time.Since(start).Seconds())
	}()

	query, err := ParseQuery(packet)
	if err != nil {
		log.Printf("malformed query from %s: %v", client, err)
		return // reject bad packets, keep serving
	}
	if len(query.Questions) == 0 {
		return // nothing to cache or answer
	}
	q := query.Questions[0]
	key := cacheKey(q)

	if !s.NoCache {
		if cached, remaining, ok := s.Cache.Get(key); ok {
			metrics.CacheHits.Inc()
			if s.Telemetry != nil {
				s.Telemetry.Record(key, true)
			}
			resp := PatchTTLs(cached, remaining)
			binary.BigEndian.PutUint16(resp[0:2], query.Header.ID)
			if _, err := conn.WriteToUDP(resp, client); err != nil {
				log.Printf("write to client error: %v", err)
			}
			log.Printf("cache hit id=%d %s %s from %s", query.Header.ID, q.Name, TypeName(q.Type), client)
			return
		}
		metrics.CacheMisses.Inc()
		if s.Telemetry != nil {
			s.Telemetry.Record(key, false)
		}
		log.Printf("cache miss id=%d %s %s from %s", query.Header.ID, q.Name, TypeName(q.Type), client)
	}

	resp, err := s.forward(packet)
	if err != nil {
		log.Printf("upstream error: %v", err)
		return
	}
	if !s.NoCache {
		if ttl, ok := ExtractTTL(resp); ok && ttl > 0 {
			s.Cache.Put(key, resp, ttl)
			metrics.CacheSize.Set(float64(s.Cache.Len()))
		}
	}
	if _, err := conn.WriteToUDP(resp, client); err != nil {
		log.Printf("write to client error: %v", err)
	}
}

// forward sends the raw query to the upstream and returns the raw response.
func (s *Server) forward(query []byte) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), s.Timeout)
	defer cancel()

	var d net.Dialer
	conn, err := d.DialContext(ctx, "udp", s.Upstream)
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
	if _, err := conn.Write(query); err != nil {
		return nil, err
	}
	resp := make([]byte, 512)
	n, err := conn.Read(resp)
	if err != nil {
		return nil, err
	}
	return resp[:n], nil
}
