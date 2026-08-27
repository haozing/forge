package httpapi

import (
	"context"
	"log"
	"time"

	"agentchunzhi/internal/store"
)

// recordAuditAsync best-effort persists an audit entry in the background.
// Audit must never block or fail the served request: the write runs detached
// with its own timeout and only logs failures.
func recordAuditAsync(deps Dependencies, entry store.AuditEntry) {
	if deps.Store == nil || deps.Store.Pool == nil {
		return
	}
	go func() {
		defer func() { _ = recover() }()
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := deps.Store.RecordAudit(ctx, entry); err != nil {
			log.Printf("audit %s failed: %v", entry.Action, err)
		}
	}()
}
