package store

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// AuditEntry is a single audit_log row awaiting persistence. Result must be
// one of the values allowed by the audit_log check constraint
// (allowed / denied / error); an empty result defaults to "allowed".
type AuditEntry struct {
	OrganizationID  string
	ActorUserID     string
	InitiatorUserID string
	Action          string
	ResourceType    string
	ResourceID      string
	RequestID       string
	Result          string
	Metadata        map[string]any
}

// validAuditResults mirrors the audit_log_result_check constraint.
var validAuditResults = map[string]bool{
	"allowed": true,
	"denied":  true,
	"error":   true,
}

// NewAuditEntry assembles one audit record with the default result "allowed".
// Result is validated again by RecordAudit.
func NewAuditEntry(action, organizationID, actorUserID, resourceType, resourceID string, metadata map[string]any) AuditEntry {
	if metadata == nil {
		metadata = map[string]any{}
	}
	return AuditEntry{
		OrganizationID:  organizationID,
		ActorUserID:     actorUserID,
		InitiatorUserID: actorUserID,
		Action:          action,
		ResourceType:    resourceType,
		ResourceID:      resourceID,
		Result:          "allowed",
		Metadata:        metadata,
	}
}

// AppendAuditTx persists one audit entry inside the caller's business
// transaction. Governance and content writes must use this variant so audit
// cannot be lost after the business commit.
func AppendAuditTx(ctx context.Context, tx pgx.Tx, entry AuditEntry, workspaceID string) error {
	if tx == nil {
		return fmt.Errorf("audit transaction is required")
	}
	if entry.Action == "" {
		return fmt.Errorf("audit entry requires an action")
	}
	result := entry.Result
	if result == "" {
		result = "allowed"
	}
	if !validAuditResults[result] {
		return fmt.Errorf("invalid audit result %q", result)
	}
	metadataJSON, err := json.Marshal(entry.Metadata)
	if err != nil {
		return fmt.Errorf("encode audit metadata: %w", err)
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO audit.audit_log
			(organization_id, workspace_id, actor_user_id, initiator_user_id, action, resource_type, resource_id, request_id, result, metadata)
		VALUES (NULLIF($1,'')::uuid, NULLIF($2,'')::uuid, NULLIF($3,'')::uuid, NULLIF($4,'')::uuid, $5,
			NULLIF($6,''), NULLIF($7,'')::uuid, NULLIF($8,''), $9, $10::jsonb)
	`, entry.OrganizationID, workspaceID, entry.ActorUserID, entry.InitiatorUserID, entry.Action,
		entry.ResourceType, entry.ResourceID, entry.RequestID, result, string(metadataJSON))
	return err
}

// RecordAudit persists one generic audit entry. It is the shared write path
// for session audit events; governance and content writes use AppendAuditTx.
func (s *Store) RecordAudit(ctx context.Context, entry AuditEntry) error {
	if s == nil || s.Pool == nil {
		return fmt.Errorf("database store is not initialized")
	}
	if entry.Action == "" {
		return fmt.Errorf("audit entry requires an action")
	}
	result := entry.Result
	if result == "" {
		result = "allowed"
	}
	if !validAuditResults[result] {
		return fmt.Errorf("invalid audit result %q", result)
	}
	metadataJSON, err := json.Marshal(entry.Metadata)
	if err != nil {
		return fmt.Errorf("encode audit metadata: %w", err)
	}
	_, err = s.Pool.Exec(ctx, `
		INSERT INTO audit.audit_log
			(organization_id, actor_user_id, initiator_user_id, action, resource_type, resource_id, request_id, result, metadata)
		VALUES (NULLIF($1,'')::uuid, NULLIF($2,'')::uuid, NULLIF($3,'')::uuid, $4,
			NULLIF($5,''), NULLIF($6,'')::uuid, NULLIF($7,''), $8, $9::jsonb)
	`, entry.OrganizationID, entry.ActorUserID, entry.InitiatorUserID, entry.Action,
		entry.ResourceType, entry.ResourceID, entry.RequestID, result, string(metadataJSON))
	return err
}

func (s *Store) RecordAgentChatAudit(ctx context.Context, organizationID, actorUserID, initiatorUserID, applicationID, sessionID, result string, metadata map[string]any) error {
	if s == nil || s.Pool == nil {
		return fmt.Errorf("database store is not initialized")
	}
	metadataJSON, err := json.Marshal(metadata)
	if err != nil {
		return fmt.Errorf("encode agent chat audit metadata: %w", err)
	}
	_, err = s.Pool.Exec(ctx, `
		INSERT INTO audit.audit_log
			(organization_id, actor_user_id, initiator_user_id, agent_application_id,
			 action, resource_type, resource_id, result, metadata)
		VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid,
			 'agent.chat', 'agent_session', $5::uuid, $6, $7::jsonb)
	`, organizationID, actorUserID, initiatorUserID, applicationID, sessionID, result, string(metadataJSON))
	return err
}
