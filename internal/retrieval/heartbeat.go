package retrieval

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"time"

	"agentchunzhi/internal/store"
)

// HeartbeatInterval is the worker lease renewal cadence; a lease older than
// twice the interval means the worker is not ready (doc §13.4).
const HeartbeatInterval = 10 * time.Second

// Heartbeat registers a retrieval worker in system.worker_heartbeats so the
// API /readyz can verify a live worker registered the handlers required by
// the active profile, with a matching manifest fingerprint.
type Heartbeat struct {
	Store *store.Store
	// WorkerID identifies the process (hostname+random by default).
	WorkerID string
	// Role is "retrieval".
	Role string
	// Fingerprint is the embedding manifest fingerprint.
	Fingerprint string
	// Handlers lists the River job kinds registered by this worker.
	Handlers []string
	// Logf receives lifecycle warnings; defaults to the std logger.
	Logf func(string, ...any)
}

// NewHeartbeat builds a heartbeat with defaults.
func NewHeartbeat(st *store.Store, workerID, fingerprint string) *Heartbeat {
	return &Heartbeat{
		Store:       st,
		WorkerID:    workerID,
		Role:        "retrieval",
		Fingerprint: fingerprint,
		Handlers:    HandlerManifest(),
	}
}

// Renew upserts the heartbeat row once.
func (h *Heartbeat) Renew(ctx context.Context) error {
	if h.Store == nil || h.Store.Pool == nil {
		return errors.New("database store is not initialized")
	}
	handlers, err := json.Marshal(h.Handlers)
	if err != nil {
		handlers = []byte("[]")
	}
	if _, err := h.Store.Pool.Exec(ctx, `
		INSERT INTO system.worker_heartbeats
			(worker_id, role, manifest_fingerprint, handler_manifest, status, started_at, last_seen_at)
		VALUES ($1, $2, $3, $4::jsonb, 'running', now(), now())
		ON CONFLICT (worker_id, role) DO UPDATE
		SET manifest_fingerprint = EXCLUDED.manifest_fingerprint,
		    handler_manifest = EXCLUDED.handler_manifest,
		    status = 'running',
		    last_seen_at = now()
	`, h.WorkerID, h.Role, h.Fingerprint, string(handlers)); err != nil {
		return fmt.Errorf("renew retrieval worker heartbeat: %w", err)
	}
	return nil
}

// Run renews the heartbeat until ctx is cancelled, then removes the row.
func (h *Heartbeat) Run(ctx context.Context) {
	logf := h.Logf
	if logf == nil {
		logf = log.Printf
	}
	if err := h.Renew(ctx); err != nil {
		logf("retrieval heartbeat registration failed: %v", err)
	}
	ticker := time.NewTicker(HeartbeatInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			h.remove(context.WithoutCancel(ctx))
			return
		case <-ticker.C:
			renewCtx, cancel := context.WithTimeout(ctx, HeartbeatInterval)
			err := h.Renew(renewCtx)
			cancel()
			if err != nil {
				logf("retrieval heartbeat renewal failed: %v", err)
			}
		}
	}
}

func (h *Heartbeat) remove(ctx context.Context) {
	if h.Store == nil || h.Store.Pool == nil {
		return
	}
	if _, err := h.Store.Pool.Exec(ctx, `
		DELETE FROM system.worker_heartbeats WHERE worker_id = $1 AND role = $2
	`, h.WorkerID, h.Role); err != nil {
		if h.Logf != nil {
			h.Logf("retrieval heartbeat cleanup failed: %v", err)
		}
	}
}
