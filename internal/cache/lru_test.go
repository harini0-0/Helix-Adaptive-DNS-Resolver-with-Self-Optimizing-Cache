package cache

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestGetMiss(t *testing.T) {
	c := New(2)
	if _, _, ok := c.Get("missing"); ok {
		t.Fatal("expected miss on empty cache")
	}
}

func TestPutThenGet(t *testing.T) {
	c := New(2)
	c.Put("a", []byte("1"), time.Minute)

	val, remaining, ok := c.Get("a")
	if !ok {
		t.Fatal("expected hit after put")
	}
	if string(val) != "1" {
		t.Fatalf("got value %q, want %q", val, "1")
	}
	if remaining <= 0 || remaining > time.Minute {
		t.Fatalf("remaining TTL out of range: %v", remaining)
	}
}

func TestEvictsLeastRecentlyUsedAtCapacity(t *testing.T) {
	c := New(2)
	c.Put("a", []byte("1"), time.Minute)
	c.Put("b", []byte("2"), time.Minute)
	c.Put("c", []byte("3"), time.Minute) // capacity 2: "a" should be evicted

	if _, _, ok := c.Get("a"); ok {
		t.Fatal("expected \"a\" to be evicted")
	}
	if _, _, ok := c.Get("b"); !ok {
		t.Fatal("expected \"b\" to still be present")
	}
	if _, _, ok := c.Get("c"); !ok {
		t.Fatal("expected \"c\" to still be present")
	}
}

func TestGetMovesToFront(t *testing.T) {
	c := New(2)
	c.Put("a", []byte("1"), time.Minute)
	c.Put("b", []byte("2"), time.Minute)

	// Touch "a" so "b" becomes the least-recently-used entry.
	if _, _, ok := c.Get("a"); !ok {
		t.Fatal("expected hit on \"a\"")
	}
	c.Put("c", []byte("3"), time.Minute) // capacity 2: "b" should be evicted, not "a"

	if _, _, ok := c.Get("b"); ok {
		t.Fatal("expected \"b\" to be evicted after \"a\" was accessed")
	}
	if _, _, ok := c.Get("a"); !ok {
		t.Fatal("expected \"a\" to survive since it was recently accessed")
	}
}

func TestExpiredEntryIsMiss(t *testing.T) {
	c := New(2)
	c.Put("a", []byte("1"), 10*time.Millisecond)
	time.Sleep(20 * time.Millisecond)

	if _, _, ok := c.Get("a"); ok {
		t.Fatal("expected expired entry to be a miss")
	}
	if c.Len() != 0 {
		t.Fatalf("expected expired entry to be evicted from Len(), got %d", c.Len())
	}
}

// TestConcurrentAccessIsRaceFree hammers the cache from many goroutines doing
// interleaved Get, Put, and Len on overlapping keys. Its job is not to assert a
// particular value but to give the race detector something to catch: run it with
// `go test -race` and a clean pass is the actual proof of concurrency safety.
// It also asserts the capacity bound is never exceeded under concurrent writes.
func TestConcurrentAccessIsRaceFree(t *testing.T) {
	const (
		capacity   = 64
		goroutines = 32
		opsEach    = 2000
		keySpace   = 128 // > capacity, so evictions happen constantly
	)
	c := New(capacity)

	var wg sync.WaitGroup
	wg.Add(goroutines)
	for g := 0; g < goroutines; g++ {
		go func(g int) {
			defer wg.Done()
			for i := 0; i < opsEach; i++ {
				key := fmt.Sprintf("k%d", (g*7+i)%keySpace)
				switch i % 3 {
				case 0:
					c.Put(key, []byte("v"), time.Minute)
				case 1:
					c.Get(key)
				default:
					if n := c.Len(); n > capacity {
						t.Errorf("cache exceeded capacity: Len()=%d > %d", n, capacity)
					}
				}
			}
		}(g)
	}
	wg.Wait()

	if n := c.Len(); n > capacity {
		t.Fatalf("final Len()=%d exceeds capacity %d", n, capacity)
	}
}

func TestAdaptiveEvictionDropsLowestScoreNotLRU(t *testing.T) {
	c := New(2)
	// Score "a" high (keep) and "b" low (drop). Strict LRU would evict "a"
	// (the tail); adaptive eviction should instead evict "b".
	c.SetAdaptiveEviction(func(key string) (float64, bool) {
		switch key {
		case "a":
			return 10, true
		case "b":
			return 1, true
		default:
			return 0, true
		}
	}, 2)

	c.Put("a", []byte("1"), time.Minute)
	c.Put("b", []byte("2"), time.Minute)
	c.Put("c", []byte("3"), time.Minute) // full: sample {b,a}, evict lowest = "b"

	if _, _, ok := c.Get("b"); ok {
		t.Fatal("expected adaptive eviction to drop low-scoring \"b\"")
	}
	if _, _, ok := c.Get("a"); !ok {
		t.Fatal("expected high-scoring \"a\" to survive despite being LRU tail")
	}
	if _, _, ok := c.Get("c"); !ok {
		t.Fatal("expected freshly inserted \"c\" to be present")
	}
}

func TestAdaptiveEvictionFallsBackToLRUWhenUnscorable(t *testing.T) {
	c := New(2)
	// scorer never has data -> must fall back to strict LRU (evict the tail).
	c.SetAdaptiveEviction(func(string) (float64, bool) { return 0, false }, 4)

	c.Put("a", []byte("1"), time.Minute)
	c.Put("b", []byte("2"), time.Minute)
	c.Put("c", []byte("3"), time.Minute) // strict LRU: "a" (tail) evicted

	if _, _, ok := c.Get("a"); ok {
		t.Fatal("expected strict-LRU fallback to evict tail \"a\"")
	}
	if _, _, ok := c.Get("b"); !ok {
		t.Fatal("expected \"b\" to survive under LRU fallback")
	}
}

func TestPutUpdatesExistingKey(t *testing.T) {
	c := New(2)
	c.Put("a", []byte("1"), time.Minute)
	c.Put("a", []byte("2"), time.Minute)

	val, _, ok := c.Get("a")
	if !ok || string(val) != "2" {
		t.Fatalf("expected updated value \"2\", got %q (ok=%v)", val, ok)
	}
	if c.Len() != 1 {
		t.Fatalf("expected 1 entry after update, got %d", c.Len())
	}
}
