package workspace

import (
	"context"
	"log"
	"time"

	"agentchunzhi/internal/store"
)

// Audit actions written by the workspace package. Names follow the existing
// audit_log vocabulary (<domain>.<object>.<verb>) so they read naturally next
// to asset.create / agent.register / workspace.delete.
const (
	AuditInvitationCreate = "workspace.invitation.create"
	AuditInvitationAccept = "workspace.invitation.accept"
	AuditInvitationRevoke = "workspace.invitation.revoke"
	AuditMemberAdd        = "workspace.member.add"
	AuditMemberRoleChange = "workspace.member.role_change"
	AuditMemberRemove     = "workspace.member.remove"
	AuditMemberLeft       = "workspace.member.left"
	AuditSettingsUpdate   = "workspace.settings.update"
)

const AuditResourceWorkspace = "workspace"

// auditSink is the narrow slice of *store.Store the workspace service needs
// for auditing. It exists so tests can inject an in-memory recorder without a
// database.
type auditSink interface {
	RecordAudit(ctx context.Context, entry store.AuditEntry) error
}

// NewAuditEntry assembles one permission-change audit record. Every entry
// carries metadata.workspace_id because workspace-scoped audit views filter on
// metadata->>'workspace_id'.
func NewAuditEntry(action, result, organizationID, actorUserID, resourceType, resourceID string, metadata map[string]any) store.AuditEntry {
	if metadata == nil {
		metadata = map[string]any{}
	}
	entry := store.NewAuditEntry(action, organizationID, actorUserID, resourceType, resourceID, metadata)
	entry.Result = result
	if entry.Result == "" {
		entry.Result = "allowed"
	}
	return entry
}

// WriteAuditEvent persists a single entry through sink. Errors are returned
// for the caller to log; the dispatch helpers below treat audit failures as
// non-fatal by contract.
func WriteAuditEvent(ctx context.Context, sink auditSink, entry store.AuditEntry) error {
	if sink == nil {
		return nil
	}
	return sink.RecordAudit(ctx, entry)
}

// writeAuditAsync records a workspace audit event in the background. Audit
// must never block or fail the served request, so the write runs detached with
// its own timeout and only logs failures.
func (s Service) writeAuditAsync(entry store.AuditEntry) {
	sink := s.auditable()
	if sink == nil {
		return
	}
	go func() {
		defer func() { _ = recover() }()
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := WriteAuditEvent(ctx, sink, entry); err != nil {
			log.Printf("workspace audit %s failed: %v", entry.Action, err)
		}
	}()
}

// auditable returns the usable audit sink, or nil when auditing is disabled
// (nil store — e.g. unit tests).
func (s Service) auditable() auditSink {
	if s.Store == nil || s.Store.Pool == nil {
		return nil
	}
	return s.Store
}
