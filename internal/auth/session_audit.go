package auth

import (
	"context"
	"log"
	"time"

	"agentchunzhi/internal/store"
)

// Session audit actions follow the audit_log <domain>.<object> vocabulary.
// The result column carries the outcome: "allowed" for successful logins,
// "denied" for rejected credentials.
const (
	SessionLogin  = "session.login"
	SessionLogout = "session.logout"
)

// Machine-readable failure reasons for session audit metadata. The vocabulary
// is fixed so audit rows can never grow free-form credential-shaped content.
const (
	ReasonUnknownLoginName    = "unknown_login_name"
	ReasonInvalidCredentials  = "invalid_credentials"
	ReasonSessionCreateFailed = "session_create_failed"
)

// SessionAuditEvent describes one session lifecycle moment. It deliberately
// has no password field: failed logins are audited with the attempted login
// name only, never with credentials.
type SessionAuditEvent struct {
	Action         string
	Result         string
	OrganizationID string
	UserID         string
	LoginName      string
	Reason         string
}

// BuildSessionAuditEntry maps an event onto a generic audit record. Failed
// logins often have no known organization, which is fine: organization_id is
// nullable and those rows stay globally queryable instead of disappearing.
func BuildSessionAuditEntry(event SessionAuditEvent) store.AuditEntry {
	result := event.Result
	if result == "" {
		result = "allowed"
	}
	metadata := map[string]any{}
	if event.LoginName != "" {
		metadata["login_name"] = event.LoginName
	}
	if event.Reason != "" {
		metadata["reason"] = event.Reason
	}
	return store.AuditEntry{
		OrganizationID:  event.OrganizationID,
		ActorUserID:     event.UserID,
		InitiatorUserID: event.UserID,
		Action:          event.Action,
		ResourceType:    "session",
		ResourceID:      "",
		RequestID:       "",
		Result:          result,
		Metadata:        metadata,
	}
}

// reportSessionEvent emits an audit event through the configured hook or, by
// default, asynchronously through the shared store write path. Audit must
// never break authentication, so hook panics and store failures are swallowed
// after logging.
func (s SessionService) reportSessionEvent(event SessionAuditEvent) {
	if s.AuditHook != nil {
		s.emitThroughHook(event)
		return
	}
	s.writeSessionAuditDefault(event)
}

func (s SessionService) emitThroughHook(event SessionAuditEvent) {
	defer func() { _ = recover() }()
	s.AuditHook(event)
}

func (s SessionService) writeSessionAuditDefault(event SessionAuditEvent) {
	if s.Store == nil || s.Store.Pool == nil {
		return
	}
	go func() {
		defer func() { _ = recover() }()
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := s.Store.RecordAudit(ctx, BuildSessionAuditEntry(event)); err != nil {
			log.Printf("auth audit %s failed: %v", event.Action, err)
		}
	}()
}
