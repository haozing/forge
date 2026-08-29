package workspace

// events.go — membership facts ride the same transaction as the membership
// change: business write, audit and outbox commit together (phase 1 §10.1).

import (
	"context"
	"errors"

	"agentchunzhi/internal/auth"
	"agentchunzhi/internal/eventing"
	"agentchunzhi/internal/store"

	"github.com/jackc/pgx/v5"
)

// appendMembershipEventTx emits one workspace.membership_changed fact inside
// the caller's transaction.
func (s Service) appendMembershipEventTx(ctx context.Context, tx pgx.Tx, principal auth.Principal, workspaceID string, payload eventing.WorkspaceMembershipChangedPayload) error {
	if s.Events == nil {
		return errors.New("event store is not initialized: workspace membership facts would be lost")
	}
	raw, err := eventing.EncodePayload(payload)
	if err != nil {
		return err
	}
	_, err = s.Events.AppendTx(ctx, tx, eventing.Event{
		OrganizationID:   principal.OrganizationID,
		WorkspaceID:      workspaceID,
		EventType:        eventing.EventWorkspaceMembershipChanged,
		AggregateType:    "workspace",
		AggregateID:      workspaceID,
		AggregateVersion: 1,
		PayloadVersion:   eventing.PayloadVersionV1,
		Actor:            eventing.ActorFromPrincipal(principal),
		Payload:          raw,
	})
	return err
}

// appendMembershipAuditTx writes the membership audit row synchronously in
// the caller's transaction.
func appendMembershipAuditTx(ctx context.Context, tx pgx.Tx, principal auth.Principal, action, workspaceID, resourceID string, metadata map[string]any) error {
	entry := NewAuditEntry(action, "", principal.OrganizationID, principal.UserID, auditResourceWorkspaceMember, resourceID, metadata)
	return store.AppendAuditTx(ctx, tx, entry, workspaceID)
}
