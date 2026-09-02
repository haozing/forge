package delivery

// poller.go — the api-side consumer of delivery.cache_invalidations (design
// doc §6.2): a background goroutine claims pending rows in a short
// transaction (FOR UPDATE SKIP LOCKED), applies the prefix deletes to the
// page cache and stamps processed_at. Unprocessed rows survive restarts;
// the 300s cache TTL is the missed-invalidation backstop. Stalled queues
// older than the alert threshold are logged once per minute (§6.3).

import (
	"context"
	"log"
	"time"

	"agentchunzhi/internal/store"
)

// Poller tunes.
const (
	DefaultPollInterval = 2 * time.Second
	pollBatchSize       = 500
	staleThreshold      = 60 * time.Second
	staleLogInterval    = time.Minute
	statsLogInterval    = 5 * time.Minute
)

// Poller drains the invalidation queue into the page cache.
type Poller struct {
	Store     *store.Store
	Cache     *PageCache
	Interval  time.Duration
	Logf      func(string, ...any)
	lastStale time.Time
	lastStats time.Time
}

// NewPoller wires the default poller.
func NewPoller(database *store.Store, cache *PageCache, logf func(string, ...any)) *Poller {
	if logf == nil {
		logf = log.Printf
	}
	return &Poller{Store: database, Cache: cache, Interval: DefaultPollInterval, Logf: logf}
}

// Run loops until the context ends; every error is logged and retried on the
// next tick (the queue stays consistent — rows are only stamped after the
// cache mutation of their transaction commits).
func (p *Poller) Run(ctx context.Context) {
	if p.Store == nil || p.Store.Pool == nil || p.Cache == nil {
		p.Logf("delivery poller not wired; cache invalidation rides on TTL only")
		return
	}
	interval := p.Interval
	if interval <= 0 {
		interval = DefaultPollInterval
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := p.PollOnce(ctx); err != nil {
				p.Logf("delivery cache poll failed: %v", err)
			}
			p.observe(ctx)
		}
	}
}

// PollOnce applies one batch of pending invalidations.
func (p *Poller) PollOnce(ctx context.Context) error {
	tx, err := p.Store.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	rows, err := tx.Query(ctx, `
		SELECT id, site_id::text, tier, route_prefix
		FROM delivery.cache_invalidations
		WHERE processed_at IS NULL
		ORDER BY id
		LIMIT $1::int
		FOR UPDATE SKIP LOCKED
	`, pollBatchSize)
	if err != nil {
		return err
	}
	type pending struct {
		ID      int64
		SiteID  string
		Tier    string
		Prefix  string
	}
	batch := make([]pending, 0, pollBatchSize)
	for rows.Next() {
		var item pending
		if err := rows.Scan(&item.ID, &item.SiteID, &item.Tier, &item.Prefix); err != nil {
			rows.Close()
			return err
		}
		batch = append(batch, item)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	if len(batch) == 0 {
		return nil
	}
	ids := make([]int64, 0, len(batch))
	for _, item := range batch {
		p.Cache.InvalidatePrefix(item.SiteID, item.Tier, item.Prefix)
		ids = append(ids, item.ID)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE delivery.cache_invalidations
		SET processed_at = now()
		WHERE id = ANY($1::bigint[])
	`, ids); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	return nil
}

// observe reports queue staleness and cache hit-rate counters (structured
// log points stand in for a metrics stack, design doc §6.3/§11).
func (p *Poller) observe(ctx context.Context) {
	now := time.Now()
	var oldest *time.Time
	if err := p.Store.Pool.QueryRow(ctx, `
		SELECT min(created_at) FROM delivery.cache_invalidations WHERE processed_at IS NULL
	`).Scan(&oldest); err == nil && oldest != nil && now.Sub(*oldest) > staleThreshold {
		if now.Sub(p.lastStale) >= staleLogInterval {
			p.lastStale = now
			p.Logf("delivery cache invalidation queue stale: oldest unprocessed row %s old", now.Sub(*oldest).Round(time.Second))
		}
	}
	if now.Sub(p.lastStats) >= statsLogInterval {
		p.lastStats = now
		hits, misses, evictions, size := p.Cache.Stats()
		if hits+misses > 0 {
			p.Logf("delivery cache stats hits=%d misses=%d hit_rate=%.3f evictions=%d size=%d",
				hits, misses, float64(hits)/float64(hits+misses), evictions, size)
		}
	}
}
