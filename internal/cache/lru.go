// Package cache implements a TTL-aware LRU cache, safe for concurrent use.
package cache

import (
	"container/list"
	"math"
	"sync"
	"time"
)

type entry struct {
	key       string
	value     []byte
	expiresAt time.Time
}

// Cache is a fixed-capacity, least-recently-used cache where each entry also
// expires after its own TTL. Reads and writes are safe for concurrent use.
type Cache struct {
	mu       sync.Mutex
	capacity int
	ll       *list.List // front = most recently used, back = least
	items    map[string]*list.Element

	// Optional adaptive eviction. When scorer is set, eviction samples the
	// sampleSize least-recently-used entries and drops the one with the lowest
	// predicted retention score instead of the strict LRU tail. If scorer
	// returns ok=false for every sampled key, it falls back to strict LRU, so
	// correctness never depends on the model.
	scorer     func(key string) (score float64, ok bool)
	sampleSize int
}

// New creates a Cache holding at most capacity entries.
func New(capacity int) *Cache {
	if capacity <= 0 {
		capacity = 1
	}
	return &Cache{
		capacity: capacity,
		ll:       list.New(),
		items:    make(map[string]*list.Element),
	}
}

// Get returns the value stored for key and its remaining TTL. ok is false if
// the key is absent or has expired; an expired entry is evicted on access.
func (c *Cache) Get(key string) (value []byte, remaining time.Duration, ok bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	el, found := c.items[key]
	if !found {
		return nil, 0, false
	}
	e := el.Value.(*entry)
	remaining = time.Until(e.expiresAt)
	if remaining <= 0 {
		c.removeElement(el)
		return nil, 0, false
	}
	c.ll.MoveToFront(el)
	return e.value, remaining, true
}

// Put inserts or updates key with value, expiring after ttl. If the cache is
// over capacity afterward, the least-recently-used entry is evicted.
func (c *Cache) Put(key string, value []byte, ttl time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()

	expiresAt := time.Now().Add(ttl)
	if el, found := c.items[key]; found {
		e := el.Value.(*entry)
		e.value = value
		e.expiresAt = expiresAt
		c.ll.MoveToFront(el)
		return
	}

	el := c.ll.PushFront(&entry{key: key, value: value, expiresAt: expiresAt})
	c.items[key] = el

	if c.ll.Len() > c.capacity {
		c.evict()
	}
}

// SetAdaptiveEviction enables model-driven eviction: when the cache is full it
// samples the sampleSize least-recently-used entries and evicts the one scorer
// ranks lowest (least likely to be requested again). A nil scorer or
// sampleSize <= 1 keeps strict LRU. Configure this once at startup, before
// serving; it is not meant to change concurrently with use.
func (c *Cache) SetAdaptiveEviction(scorer func(key string) (float64, bool), sampleSize int) {
	c.scorer = scorer
	c.sampleSize = sampleSize
}

// evict removes one entry. Callers must hold c.mu. With adaptive eviction it
// scores the LRU-tail sample and drops the lowest; otherwise it drops the tail.
func (c *Cache) evict() {
	if c.scorer == nil || c.sampleSize <= 1 {
		c.removeElement(c.ll.Back())
		return
	}
	var worst *list.Element
	worstScore := math.Inf(1)
	found := false
	for el, i := c.ll.Back(), 0; el != nil && i < c.sampleSize; el, i = el.Prev(), i+1 {
		key := el.Value.(*entry).key
		if s, ok := c.scorer(key); ok && (!found || s < worstScore) {
			worstScore, worst, found = s, el, true
		}
	}
	if !found {
		c.removeElement(c.ll.Back()) // no scores available -> strict LRU
		return
	}
	c.removeElement(worst)
}

// removeElement unlinks el from both the list and the map. Callers must hold c.mu.
func (c *Cache) removeElement(el *list.Element) {
	c.ll.Remove(el)
	delete(c.items, el.Value.(*entry).key)
}

// Len reports the number of entries currently stored.
func (c *Cache) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.ll.Len()
}
