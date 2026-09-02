// Package delivery — the server-rendered HTML face of the public sites
// (docs/公开站点SSR投递与样式参数空间设计方案.md). ViewModel whitelist,
// html/template rendering, the L1 StyleEngine, the page-level cache and the
// invalidation queue endpoints of the design.
package delivery

import (
	"strings"
	"sync"
	"time"
)

// Page-cache constants (design doc §6.1): one key per (site, release, tier,
// route), a hard TTL ceiling as the missed-invalidation backstop and a
// bounded entry count with insertion-order eviction.
const (
	CacheKeyPrefix = "page"
	DefaultCacheTTL      = 300 * time.Second
	DefaultCacheCapacity = 10000
)

// CacheEntry is one rendered page representation.
type CacheEntry struct {
	Body         []byte
	ETag         string
	ContentType  string
	CacheControl string
	NoIndex      bool
	expiresAt    time.Time
}

// PageCache is the process-local page store: a plain map with TTL, a
// capacity bound and insertion-order eviction. No LRU library, no byte
// quotas — capacity tuning waits for real traffic (design doc §6.1).
type PageCache struct {
	mu        sync.Mutex
	entries   map[string]CacheEntry
	order     []string
	capacity  int
	ttl       time.Duration
	hits      uint64
	misses    uint64
	evictions uint64
}

// NewPageCache builds a cache with the default TTL and capacity unless the
// caller supplies non-positive overrides.
func NewPageCache(capacity int, ttl time.Duration) *PageCache {
	if capacity <= 0 {
		capacity = DefaultCacheCapacity
	}
	if ttl <= 0 {
		ttl = DefaultCacheTTL
	}
	return &PageCache{entries: make(map[string]CacheEntry), capacity: capacity, ttl: ttl}
}

// PageKey renders the canonical cache key of one page.
func PageKey(siteID, revision, tier, routePath string) string {
	return CacheKeyPrefix + ":" + siteID + ":" + revision + ":" + tier + ":" + routePath
}

// Get answers a live entry and counts the hit/miss.
func (c *PageCache) Get(key string) (CacheEntry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[key]
	if !ok {
		c.misses++
		return CacheEntry{}, false
	}
	if time.Now().After(entry.expiresAt) {
		delete(c.entries, key)
		c.misses++
		return CacheEntry{}, false
	}
	c.hits++
	return entry, true
}

// Set stores one entry and evicts by insertion order past the capacity.
func (c *PageCache) Set(key string, entry CacheEntry) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, exists := c.entries[key]; !exists {
		c.order = append(c.order, key)
	}
	entry.expiresAt = time.Now().Add(c.ttl)
	c.entries[key] = entry
	for len(c.order) > c.capacity {
		oldest := c.order[0]
		c.order = c.order[1:]
		delete(c.entries, oldest)
		c.evictions++
	}
}

// InvalidatePrefix drops every key of one site (optionally one tier and one
// route prefix; empty matches all). Keys carry the release revision, which
// invalidation deliberately ignores: a late invalidation must clear every
// generation, not just the newest.
func (c *PageCache) InvalidatePrefix(siteID, tier, routePrefix string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	removed := 0
	kept := c.order[:0]
	for _, key := range c.order {
		parts := strings.SplitN(key, ":", 5)
		if len(parts) == 5 && parts[0] == CacheKeyPrefix && parts[1] == siteID &&
			(tier == "" || parts[3] == tier) &&
			(routePrefix == "" || strings.HasPrefix(parts[4], routePrefix)) {
			delete(c.entries, key)
			removed++
			continue
		}
		kept = append(kept, key)
	}
	c.order = kept
	return removed
}

// Stats reports the hit/miss/eviction counters (log-pointed observability,
// design doc §6.3).
func (c *PageCache) Stats() (hits, misses, evictions uint64, size int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.hits, c.misses, c.evictions, len(c.entries)
}
