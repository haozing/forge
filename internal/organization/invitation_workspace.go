package organization

// invitation_workspace.go — the workspace-admin slice of the single
// OrganizationInvitation aggregate. A workspace admin may create
// authority_scope=workspace invitations only: organization_role is fixed to
// member, exactly one grant for the workspace they govern, and resend/revoke
// re-verify that the invitation still belongs to that workspace.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"agentchunzhi/internal/auth"
	"agentchunzhi/internal/authz"
	"agentchunzhi/internal/eventing"
	"agentchunzhi/internal/notification"
	"agentchunzhi/internal/store"

	"github.com/jackc/pgx/v5"
)

// WorkspaceInviteInput is the workspace-admin create command. Role is the
// single workspace grant role.
type WorkspaceInviteInput struct {
	Email          string
	DisplayName    string
	Role           string
	ExpiresInHours int
}

// requireWorkspaceAdmin is the workspace governance gate for the invitation
// commands. It intentionally reads workspace_members directly: the workspace
// package owns content permissions, while invitations are an organization
// aggregate that workspace admins may narrow to their own workspace.
func (s InvitationService) requireWorkspaceAdmin(ctx context.Context, principal auth.Principal, workspaceID string) error {
	if principal.UserType != auth.UserTypeMember || s.Store == nil || s.Store.Pool == nil {
		return ErrForbidden
	}
	svc := Service{Store: s.Store}
	if !svc.validID(workspaceID) {
		return ErrInvalidInput
	}
	var admin bool
	err := s.Store.Pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM content.workspace_members wm
			JOIN content.workspaces w ON w.organization_id = wm.organization_id AND w.id = wm.workspace_id
			WHERE wm.organization_id = $1::uuid AND wm.workspace_id = $2::uuid
			  AND wm.user_id = $3::uuid AND wm.role = $4 AND w.status = 'active'
		)
	`, principal.OrganizationID, workspaceID, principal.UserID, authz.WorkspaceRoleAdmin).Scan(&admin)
	if err != nil {
		return err
	}
	if !admin {
		return ErrForbidden
	}
	return nil
}

// CreateWorkspaceScoped issues a workspace-scoped invitation plus its single
// grant in one transaction and returns the raw token exactly once: it lives
// only in the returned value and inside the encrypted delivery payload.
func (s InvitationService) CreateWorkspaceScoped(ctx context.Context, principal auth.Principal, workspaceID string, input WorkspaceInviteInput) (Invitation, string, error) {
	if err := s.requireWorkspaceAdmin(ctx, principal, workspaceID); err != nil {
		return Invitation{}, "", err
	}
	email, err := NormalizeEmail(input.Email)
	if err != nil {
		return Invitation{}, "", ErrInvalidInput
	}
	if input.Role == "" {
		input.Role = authz.WorkspaceRoleViewer
	}
	if !authz.ValidWorkspaceRole(input.Role) {
		return Invitation{}, "", ErrInvalidInput
	}
	if input.ExpiresInHours <= 0 || input.ExpiresInHours > 168 {
		input.ExpiresInHours = 168
	}
	rawToken := newInvitationToken()
	tx, err := s.Store.Pool.Begin(ctx)
	if err != nil {
		return Invitation{}, "", err
	}
	defer tx.Rollback(ctx)
	if err := lockOrganizationTx(ctx, tx, principal.OrganizationID); err != nil {
		return Invitation{}, "", err
	}
	// The workspace must be active inside the caller's organization.
	var workspaceName string
	if err := tx.QueryRow(ctx, `
		SELECT name FROM content.workspaces
		WHERE organization_id = $1::uuid AND id = $2::uuid AND status = 'active'
	`, principal.OrganizationID, workspaceID).Scan(&workspaceName); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Invitation{}, "", ErrNotFound
		}
		return Invitation{}, "", err
	}
	// The invitee must not already be a member of the organization.
	var exists bool
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM identity.users
		WHERE organization_id = $1::uuid AND email = $2)
	`, principal.OrganizationID, email).Scan(&exists); err != nil {
		return Invitation{}, "", err
	}
	if exists {
		return Invitation{}, "", ErrEmailUnavailable
	}
	// Expire stale pending invitations, then enforce at most one pending
	// invitation per email.
	if _, err := tx.Exec(ctx, `
		UPDATE organization.member_invitations
		SET status = 'expired', updated_at = now()
		WHERE email = $1 AND status = 'pending' AND expires_at <= now()
	`, email); err != nil {
		return Invitation{}, "", err
	}
	var pending int
	if err := tx.QueryRow(ctx, `
		SELECT count(*) FROM organization.member_invitations WHERE email = $1 AND status = 'pending'
	`, email).Scan(&pending); err != nil {
		return Invitation{}, "", err
	}
	if pending > 0 {
		return Invitation{}, "", ErrInvitationExists
	}
	var invitationID string
	var revision int64
	err = tx.QueryRow(ctx, `
		INSERT INTO organization.member_invitations
			(organization_id, email, display_name, organization_role, authority_scope, scope_workspace_id,
			 token_hash, status, expires_at, invited_by)
		VALUES ($1::uuid, $2, $3, $4, $5, $6::uuid, $7, 'pending', now() + make_interval(hours => $8), $9::uuid)
		RETURNING id::text, revision
	`, principal.OrganizationID, email, strings.TrimSpace(input.DisplayName), authz.OrganizationRoleMember,
		AuthorityWorkspace, workspaceID, hashToken(rawToken), input.ExpiresInHours, principal.UserID).Scan(&invitationID, &revision)
	if err != nil {
		return Invitation{}, "", err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO organization.invitation_workspace_grants (invitation_id, workspace_id, role)
		VALUES ($1::uuid, $2::uuid, $3)
	`, invitationID, workspaceID, input.Role); err != nil {
		return Invitation{}, "", err
	}
	// Encrypted delivery in the same transaction; the raw token only exists
	// here in memory and inside the encrypted payload.
	link, err := notification.JoinBaseURL(s.BaseURL, "/invite/accept?token="+rawToken)
	if err != nil {
		return Invitation{}, "", err
	}
	payload, err := json.Marshal(map[string]any{
		"token": rawToken, "link": link, "organization_name": s.orgName(ctx, principal.OrganizationID),
		"email": email, "display_name": strings.TrimSpace(input.DisplayName), "workspace_name": workspaceName,
	})
	if err != nil {
		return Invitation{}, "", err
	}
	_, ciphertext, err := s.Cipher.Encrypt(invitationID, notification.TemplateOrganizationInvitation, payload)
	if err != nil {
		return Invitation{}, "", err
	}
	if _, err := notification.Enqueue(ctx, tx, principal.OrganizationID, notification.TemplateOrganizationInvitation, email, s.KeyVersion, ciphertext); err != nil {
		return Invitation{}, "", err
	}
	invitation := Invitation{
		ID: invitationID, Email: email, DisplayName: strings.TrimSpace(input.DisplayName),
		OrganizationRole: authz.OrganizationRoleMember, AuthorityScope: AuthorityWorkspace,
		ScopeWorkspace: workspaceID, Status: InvitationPending, InvitedBy: principal.UserID,
		ExpiresAt: time.Now().UTC().Add(time.Duration(input.ExpiresInHours) * time.Hour),
		Grants:    []Grant{{WorkspaceID: workspaceID, Role: input.Role}},
		Revision:  revision, ETag: fmt.Sprint(revision),
	}
	store.AppendAuditTx(ctx, tx, store.NewAuditEntry("organization.member.invited", principal.OrganizationID, principal.UserID, "invitation", invitationID, map[string]any{
		"authority_scope": AuthorityWorkspace, "email_domain": emailDomain(email), "workspace_id": workspaceID,
	}), workspaceID)
	if err := appendOrgEvent(ctx, tx, s.Events, principal, workspaceID, eventing.EventOrganizationMemberInvited, invitationID, revision, map[string]any{
		"invitation_id": invitationID, "authority_scope": AuthorityWorkspace, "workspace_id": workspaceID,
	}); err != nil {
		return Invitation{}, "", err
	}
	if err := tx.Commit(ctx); err != nil {
		return Invitation{}, "", err
	}
	return invitation, rawToken, nil
}

// ListWorkspaceInvitations is the workspace admin's view over their own
// workspace-scoped invitations.
func (s InvitationService) ListWorkspaceInvitations(ctx context.Context, principal auth.Principal, workspaceID string) ([]Invitation, error) {
	if err := s.requireWorkspaceAdmin(ctx, principal, workspaceID); err != nil {
		return nil, err
	}
	rows, err := s.Store.Pool.Query(ctx, `
		SELECT id::text, email, COALESCE(display_name, ''), organization_role, authority_scope,
		       COALESCE(scope_workspace_id::text, ''), status, invited_by::text, expires_at, created_at, accepted_at, revision
		FROM organization.member_invitations
		WHERE organization_id = $1::uuid AND authority_scope = $2 AND scope_workspace_id = $3::uuid
		ORDER BY created_at DESC, id
		LIMIT 100
	`, principal.OrganizationID, AuthorityWorkspace, workspaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]Invitation, 0)
	for rows.Next() {
		var item Invitation
		var displayName, scopeWorkspace *string
		if err := rows.Scan(&item.ID, &item.Email, &displayName, &item.OrganizationRole, &item.AuthorityScope,
			&scopeWorkspace, &item.Status, &item.InvitedBy, &item.ExpiresAt, &item.CreatedAt, &item.AcceptedAt, &item.Revision); err != nil {
			return nil, err
		}
		if displayName != nil {
			item.DisplayName = *displayName
		}
		if scopeWorkspace != nil {
			item.ScopeWorkspace = *scopeWorkspace
		}
		item.ETag = fmt.Sprint(item.Revision)
		grants, err := s.loadGrants(ctx, item.ID)
		if err != nil {
			return nil, err
		}
		item.Grants = grants
		items = append(items, item)
	}
	return items, rows.Err()
}

// ResendWorkspaceScoped rotates the token of a workspace-scoped invitation.
// The caller must still govern the workspace the invitation is scoped to.
func (s InvitationService) ResendWorkspaceScoped(ctx context.Context, principal auth.Principal, workspaceID, invitationID string) (Invitation, error) {
	if err := s.requireWorkspaceAdmin(ctx, principal, workspaceID); err != nil {
		return Invitation{}, err
	}
	svc := Service{Store: s.Store}
	if !svc.validID(invitationID) {
		return Invitation{}, ErrInvalidInput
	}
	if err := s.requireInvitationInWorkspace(ctx, principal.OrganizationID, invitationID, workspaceID); err != nil {
		return Invitation{}, err
	}
	return s.resendInvitation(ctx, principal, invitationID)
}

// RevokeWorkspaceScoped cancels a pending workspace-scoped invitation and its
// unsent delivery.
func (s InvitationService) RevokeWorkspaceScoped(ctx context.Context, principal auth.Principal, workspaceID, invitationID string) error {
	if err := s.requireWorkspaceAdmin(ctx, principal, workspaceID); err != nil {
		return err
	}
	svc := Service{Store: s.Store}
	if !svc.validID(invitationID) {
		return ErrInvalidInput
	}
	if err := s.requireInvitationInWorkspace(ctx, principal.OrganizationID, invitationID, workspaceID); err != nil {
		return err
	}
	return s.revokeInvitation(ctx, principal, invitationID)
}

// requireInvitationInWorkspace verifies the invitation is a pending-scoped
// invitation of exactly this workspace before the shared mutation runs.
func (s InvitationService) requireInvitationInWorkspace(ctx context.Context, organizationID, invitationID, workspaceID string) error {
	var scopeWorkspace *string
	var authorityScope string
	err := s.Store.Pool.QueryRow(ctx, `
		SELECT authority_scope, scope_workspace_id FROM organization.member_invitations
		WHERE organization_id = $1::uuid AND id = $2::uuid
	`, organizationID, invitationID).Scan(&authorityScope, &scopeWorkspace)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if authorityScope != AuthorityWorkspace || scopeWorkspace == nil || *scopeWorkspace != workspaceID {
		return ErrNotFound
	}
	return nil
}
