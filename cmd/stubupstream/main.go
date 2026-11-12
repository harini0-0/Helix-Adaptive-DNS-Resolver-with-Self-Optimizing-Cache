// Command stubupstream is a minimal, controlled DNS upstream used for
// reproducible benchmarking. It answers every A-record query with a single
// fixed answer after a configurable artificial delay that stands in for real
// upstream round-trip time. Using a controlled upstream (instead of a public
// resolver whose latency drifts) is what makes the cache-on vs cache-off
// comparison in BENCHMARKS.md reproducible and defensible.
package main

import (
	"encoding/binary"
	"flag"
	"log"
	"net"
	"time"
)

func main() {
	listen := flag.String("listen", ":8054", "UDP address to serve stub answers on")
	delay := flag.Duration("delay", 20*time.Millisecond, "simulated upstream RTT per query")
	ttl := flag.Uint("ttl", 60, "TTL in seconds for every answer")
	flag.Parse()

	addr, err := net.ResolveUDPAddr("udp", *listen)
	if err != nil {
		log.Fatal(err)
	}
	conn, err := net.ListenUDP("udp", addr)
	if err != nil {
		log.Fatal(err)
	}
	defer conn.Close()
	log.Printf("stub upstream on %s, delay=%s ttl=%ds", *listen, *delay, *ttl)

	buf := make([]byte, 512)
	for {
		n, client, err := conn.ReadFromUDP(buf)
		if err != nil {
			log.Printf("read error: %v", err)
			continue
		}
		query := make([]byte, n)
		copy(query, buf[:n])
		go func() {
			if *delay > 0 {
				time.Sleep(*delay)
			}
			resp, ok := buildResponse(query, uint32(*ttl))
			if !ok {
				return
			}
			if _, err := conn.WriteToUDP(resp, client); err != nil {
				log.Printf("write error: %v", err)
			}
		}()
	}
}

// questionEnd returns the offset just past the question section (one question),
// i.e. after the name's terminating zero byte plus the 4-byte type and class.
func questionEnd(msg []byte) (int, bool) {
	pos := 12 // skip the fixed header
	for {
		if pos >= len(msg) {
			return 0, false
		}
		b := msg[pos]
		if b == 0 {
			pos++ // consume the zero length octet
			break
		}
		if b&0xC0 == 0xC0 {
			return 0, false // queries should not carry compression pointers
		}
		pos += 1 + int(b)
	}
	if pos+4 > len(msg) {
		return 0, false
	}
	return pos + 4, true
}

// buildResponse turns a query into a well-formed single-answer A response that
// the resolver's parser can walk (ExtractTTL reads the answer's TTL field).
func buildResponse(query []byte, ttl uint32) ([]byte, bool) {
	if len(query) < 12 {
		return nil, false
	}
	qEnd, ok := questionEnd(query)
	if !ok {
		return nil, false
	}

	resp := make([]byte, 0, qEnd+16)
	resp = append(resp, query[:qEnd]...) // header + question, copied verbatim

	// Header rewrite: keep ID, set response flags, one question, one answer.
	binary.BigEndian.PutUint16(resp[2:4], 0x8180) // QR=1, RD=1, RA=1, rcode=0
	binary.BigEndian.PutUint16(resp[4:6], 1)      // QDCOUNT
	binary.BigEndian.PutUint16(resp[6:8], 1)      // ANCOUNT
	binary.BigEndian.PutUint16(resp[8:10], 0)     // NSCOUNT
	binary.BigEndian.PutUint16(resp[10:12], 0)    // ARCOUNT

	// Answer record: name is a pointer to the question name at offset 12.
	answer := make([]byte, 16)
	answer[0], answer[1] = 0xC0, 0x0C          // pointer to offset 12
	binary.BigEndian.PutUint16(answer[2:4], 1) // TYPE A
	binary.BigEndian.PutUint16(answer[4:6], 1) // CLASS IN
	binary.BigEndian.PutUint32(answer[6:10], ttl)
	binary.BigEndian.PutUint16(answer[10:12], 4) // RDLENGTH
	answer[12], answer[13], answer[14], answer[15] = 93, 184, 216, 34

	return append(resp, answer...), true
}
