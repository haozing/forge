package organization

// invitation.go — the single OrganizationInvitation aggregate
// (organization.member_invitations + organization.invitation_workspace_grants).
// A workspace admin may create the same aggregate but only with
// authority_scope=workspace, organization_role=member and exactly one grant
// for the workspace they govern. Raw tokens live only in the encrypted email
// delivery payload.

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"agentchunzhi/internal/auth"
	"agentchunzhi/internal/authz"
	"agentchunzhi/internal/eventing"
	"agentchunzhi/internal/notification"
	"agentchunzhi/internal/store"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const (
	AuthorityOrganization = "organization"
	AuthorityWorkspace    = "workspace"

	InvitationPending  = "pending"
	InvitationAccepted = "accepted"
	InvitationRevoked  = "revoked"
	InvitationExpired  = "expired"
)

type Grant struct {
	WorkspaceID string `json:"workspace_id"`
	Role        string `json:"role"`
}

type Invitation struct {
	ID               string     `json:"id"`
	Email            string     `json:"email"`
	DisplayName      string     `json:"display_name,omitempty"`
	OrganizationRole string     `json:"organization_role"`
	AuthorityScope   string     `json:"authority_scope"`
	ScopeWorkspace   string     `json:"scope_workspace_id,omitempty"`
	Status           string     `json:"status"`
	InvitedBy        string     `json:"invited_by"`
	ExpiresAt        time.Time  `json:"expires_at"`
	Grants           []Grant    `json:"grants"`
	CreatedAt        time.Time  `json:"created_at"`
	AcceptedAt       *time.Time `json:"accepted_at,omitempty"`
	Revision         int64      `json:"revision"`
	ETag             string     `json:"etag"`
}

type InvitationService struct {
	Store  *store.Store
	Events *eventing.EventStore
	// Mail enqueues must happen through the domain transaction; the cipher
	// and key version are provided by composition root.
	Cipher           *notification.Cipher
	KeyVersion       int32
	BaseURL          string
	OrganizationName string
}

type CreateInput struct {
	Email            string
	DisplayName      string
	OrganizationRole string
	Grants           []Grant
	ExpiresInHours   int
}

var emailPattern = regexp.MustCompile(`^[^@\s]+@[^@\s\.]+(\.[^@\s\.]+)+$`)

// NormalizeEmail canonicalizes an address: trim, case fold, no provider
// special cases.
func NormalizeEmail(value string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" || len(value) > 254 || !emailPattern.MatchString(value) {
		return "", ErrInvalidInput
	}
	return value, nil
}

func hashToken(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

// orgName resolves the organization display name at send time so invitation
// mail never carries a stale composition-root copy. The OrganizationName
// field stays as a fallback for stores that cannot answer (unit tests).
func (s InvitationService) orgName(ctx context.Context, organizationID string) string {
	if s.Store != nil && s.Store.Pool != nil {
		var name string
		err := s.Store.Pool.QueryRow(ctx,
			`SELECT name FROM organization.organizations WHERE id = $1::uuid`, organizationID).Scan(&name)
		if err == nil && strings.TrimSpace(name) != "" {
			return name
		}
	}
	return s.OrganizationName
}

// Create issues a new invitation plus workspace grants in one transaction.
func (s InvitationService) Create(ctx context.Context, principal auth.Principal, input CreateInput) (Invitation, string, error) {
	svc := Service{Store: s.Store}
	if err := svc.RequireOrganizationAction(ctx, principal, authz.ActionOrganizationInvitationMng); err != nil {
		return Invitation{}, "", err
	}
	email, err := NormalizeEmail(input.Email)
	if err != nil {
		return Invitation{}, "", ErrInvalidInput
	}
	if input.OrganizationRole == "" {
		input.OrganizationRole = authz.OrganizationRoleMember
	}
	if !authz.ValidOrganizationRole(input.OrganizationRole) {
		return Invitation{}, "", ErrInvalidInput
	}
	if len(input.Grants) > 50 {
		return Invitation{}, "", ErrInvalidInput
	}
	for _, grant := range input.Grants {
		if !svc.validID(grant.WorkspaceID) || !authz.ValidWorkspaceRole(grant.Role) {
			return Invitation{}, "", ErrInvalidInput
		}
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
	// Lock the organization row to serialize invitations and grants.
	if err := lockOrganizationTx(ctx, tx, principal.OrganizationID); err != nil {
		return Invitation{}, "", err
	}
	// The invitee must not already be a member.
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
	// At most one pending invitation per email globally.
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
		VALUES ($1::uuid, $2, $3, $4, 'organization', NULL, $5, 'pending', now() + make_interval(hours => $6), $7::uuid)
		RETURNING id::text, revision
	`, principal.OrganizationID, email, strings.TrimSpace(input.DisplayName), input.OrganizationRole,
		hashToken(rawToken), input.ExpiresInHours, principal.UserID).Scan(&invitationID, &revision)
	if err != nil {
		return Invitation{}, "", fmt.Errorf("insert invitation: %w", err)
	}
	grants := make([]Grant, 0, len(input.Grants))
	for _, grant := range input.Grants {
		var workspaceName string
		err := tx.QueryRow(ctx, `
			SELECT name FROM content.workspaces
			WHERE organization_id = $1::uuid AND id = $2::uuid AND status = 'active'
		`, principal.OrganizationID, grant.WorkspaceID).Scan(&workspaceName)
		if errors.Is(err, pgx.ErrNoRows) {
			return Invitation{}, "", ErrInvalidInput
		}
		if err != nil {
			return Invitation{}, "", err
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO organization.invitation_workspace_grants (invitation_id, organization_id, workspace_id, role)
			VALUES ($1::uuid, $4::uuid, $2::uuid, $3)
			ON CONFLICT (invitation_id, workspace_id) DO UPDATE SET role = EXCLUDED.role
		`, invitationID, grant.WorkspaceID, grant.Role, principal.OrganizationID); err != nil {
			return Invitation{}, "", err
		}
		grants = append(grants, grant)
	}
	// Encrypted delivery in the same transaction; the raw token only exists
	// here in memory and inside the encrypted payload.
	link, err := notification.JoinBaseURL(s.BaseURL, "/invite/accept?token="+rawToken)
	if err != nil {
		return Invitation{}, "", err
	}
	payload, err := json.Marshal(map[string]any{
		"token": rawToken, "link": link, "organization_name": s.orgName(ctx, principal.OrganizationID),
		"email": email, "display_name": strings.TrimSpace(input.DisplayName),
	})
	if err != nil {
		return Invitation{}, "", err
	}
	// The ciphertext is bound to the delivery row id, so the id is generated
	// here and written explicitly by Enqueue; the worker re-derives the same
	// associated data from the claimed row.
	deliveryID := uuid.NewString()
	_, ciphertext, err := s.Cipher.Encrypt(deliveryID, notification.TemplateOrganizationInvitation, payload)
	if err != nil {
		return Invitation{}, "", err
	}
	if _, err := notification.Enqueue(ctx, tx, deliveryID, principal.OrganizationID, notification.TemplateOrganizationInvitation, email, s.KeyVersion, ciphertext); err != nil {
		return Invitation{}, "", err
	}
	invitation := Invitation{
		ID: invitationID, Email: email, DisplayName: strings.TrimSpace(input.DisplayName),
		OrganizationRole: input.OrganizationRole, AuthorityScope: AuthorityOrganization,
		Status: InvitationPending, InvitedBy: principal.UserID,
		ExpiresAt: time.Now().UTC().Add(time.Duration(input.ExpiresInHours) * time.Hour),
		Grants:    grants, Revision: revision, ETag: fmt.Sprint(revision),
	}
	store.AppendAuditTx(ctx, tx, store.NewAuditEntry("organization.member.invited", principal.OrganizationID, principal.UserID, "invitation", invitationID, map[string]any{
		"authority_scope": AuthorityOrganization, "email_domain": emailDomain(email),
	}), "")
	if err := appendOrgEvent(ctx, tx, s.Events, principal, "", eventing.EventOrganizationMemberInvited, invitationID, revision, map[string]any{
		"invitation_id": invitationID, "authority_scope": AuthorityOrganization,
	}); err != nil {
		return Invitation{}, "", err
	}
	if err := tx.Commit(ctx); err != nil {
		return Invitation{}, "", err
	}
	return invitation, rawToken, nil
}

func emailDomain(email string) string {
	parts := strings.SplitN(email, "@", 2)
	if len(parts) != 2 {
		return ""
	}
	return parts[1]
}

func newInvitationToken() string {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		panic(fmt.Sprintf("csprng unavailable: %v", err))
	}
	return hex.EncodeToString(raw)
}

// Resend invalidates the old token, issues a new one and cancels the pending
// delivery in favor of a fresh one. Organization admins only.
func (s InvitationService) Resend(ctx context.Context, principal auth.Principal, invitationID string) (Invitation, error) {
	svc := Service{Store: s.Store}
	if err := svc.RequireOrganizationAction(ctx, principal, authz.ActionOrganizationInvitationMng); err != nil {
		return Invitation{}, err
	}
	if !svc.validID(invitationID) {
		return Invitation{}, ErrInvalidInput
	}
	return s.resendInvitation(ctx, principal, invitationID)
}

// resendInvitation is the shared token-rotation body for the organization and
// workspace authority paths. The caller owns the authorization gate.
func (s InvitationService) resendInvitation(ctx context.Context, principal auth.Principal, invitationID string) (Invitation, error) {
	tx, err := s.Store.Pool.Begin(ctx)
	if err != nil {
		return Invitation{}, err
	}
	defer tx.Rollback(ctx)
	var email, status, authorityScope string
	var scopeWorkspace *string
	var revision int64
	err = tx.QueryRow(ctx, `
		SELECT email, status, authority_scope, scope_workspace_id, revision FROM organization.member_invitations
		WHERE organization_id = $1::uuid AND id = $2::uuid FOR UPDATE
	`, principal.OrganizationID, invitationID).Scan(&email, &status, &authorityScope, &scopeWorkspace, &revision)
	if errors.Is(err, pgx.ErrNoRows) {
		return Invitation{}, ErrNotFound
	}
	if err != nil {
		return Invitation{}, err
	}
	if status != InvitationPending {
		return Invitation{}, ErrConflict
	}
	rawToken := newInvitationToken()
	if _, err := tx.Exec(ctx, `
		UPDATE organization.member_invitations
		SET token_hash = $3, expires_at = now() + interval '168 hours', revision = revision + 1, updated_at = now()
		WHERE organization_id = $1::uuid AND id = $2::uuid
	`, principal.OrganizationID, invitationID, hashToken(rawToken)); err != nil {
		return Invitation{}, err
	}
	if err := notification.CancelPending(ctx, tx, principal.OrganizationID, notification.TemplateOrganizationInvitation, email); err != nil {
		return Invitation{}, err
	}
	link, err := notification.JoinBaseURL(s.BaseURL, "/invite/accept?token="+rawToken)
	if err != nil {
		return Invitation{}, err
	}
	payload, _ := json.Marshal(map[string]any{
		"token": rawToken, "link": link, "organization_name": s.orgName(ctx, principal.OrganizationID),
		"email": email, "workspace_name": s.workspaceNameTx(ctx, tx, principal.OrganizationID, scopeWorkspace),
	})
	deliveryID := uuid.NewString()
	_, ciphertext, err := s.Cipher.Encrypt(deliveryID, notification.TemplateOrganizationInvitation, payload)
	if err != nil {
		return Invitation{}, err
	}
	if _, err := notification.Enqueue(ctx, tx, deliveryID, principal.OrganizationID, notification.TemplateOrganizationInvitation, email, s.KeyVersion, ciphertext); err != nil {
		return Invitation{}, err
	}
	store.AppendAuditTx(ctx, tx, store.NewAuditEntry("organization.invitation.resent", principal.OrganizationID, principal.UserID, "invitation", invitationID, nil), "")
	if err := appendOrgEvent(ctx, tx, s.Events, principal, "", eventing.EventOrganizationInvitationResent, invitationID, revision+1, map[string]any{"invitation_id": invitationID}); err != nil {
		return Invitation{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Invitation{}, err
	}
	return s.getInvitation(ctx, principal.OrganizationID, invitationID)
}

func (s InvitationService) workspaceNameTx(ctx context.Context, tx pgx.Tx, organizationID string, scopeWorkspace *string) string {
	if scopeWorkspace == nil {
		return ""
	}
	var name string
	if err := tx.QueryRow(ctx, `
		SELECT name FROM content.workspaces
		WHERE organization_id = $1::uuid AND id = $2::uuid
	`, organizationID, *scopeWorkspace).Scan(&name); err != nil {
		return ""
	}
	return name
}

func (s InvitationService) getInvitation(ctx context.Context, organizationID, invitationID string) (Invitation, error) {
	var item Invitation
	var scopeWorkspace *string
	var displayName *string
	err := s.Store.Pool.QueryRow(ctx, `
		SELECT id::text, email, display_name, organization_role, authority_scope, scope_workspace_id::text,
		       status, invited_by::text, expires_at, created_at, accepted_at, revision
		FROM organization.member_invitations
		WHERE organization_id = $1::uuid AND id = $2::uuid
	`, organizationID, invitationID).Scan(&item.ID, &item.Email, &displayName, &item.OrganizationRole,
		&item.AuthorityScope, &scopeWorkspace, &item.Status, &item.InvitedBy, &item.ExpiresAt,
		&item.CreatedAt, &item.AcceptedAt, &item.Revision)
	if errors.Is(err, pgx.ErrNoRows) {
		return Invitation{}, ErrNotFound
	}
	if err != nil {
		return Invitation{}, err
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
		return Invitation{}, err
	}
	item.Grants = grants
	return item, nil
}

func (s InvitationService) loadGrants(ctx context.Context, invitationID string) ([]Grant, error) {
	rows, err := s.Store.Pool.Query(ctx, `
		SELECT workspace_id::text, role FROM organization.invitation_workspace_grants
		WHERE invitation_id = $1::uuid ORDER BY workspace_id
	`, invitationID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	grants := []Grant{}
	for rows.Next() {
		var grant Grant
		if err := rows.Scan(&grant.WorkspaceID, &grant.Role); err != nil {
			return nil, err
		}
		grants = append(grants, grant)
	}
	return grants, rows.Err()
}

// Revoke cancels a pending invitation and its unsent delivery. Organization
// admins only; workspace admins use RevokeWorkspaceScoped.
func (s InvitationService) Revoke(ctx context.Context, principal auth.Principal, invitationID string) error {
	svc := Service{Store: s.Store}
	if err := svc.RequireOrganizationAction(ctx, principal, authz.ActionOrganizationInvitationMng); err != nil {
		return err
	}
	if !svc.validID(invitationID) {
		return ErrInvalidInput
	}
	return s.revokeInvitation(ctx, principal, invitationID)
}

// revokeInvitation is the shared cancellation body. The caller owns the
// authorization gate.
func (s InvitationService) revokeInvitation(ctx context.Context, principal auth.Principal, invitationID string) error {
	tx, err := s.Store.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var status, email string
	var revision int64
	err = tx.QueryRow(ctx, `
		SELECT status, email, revision FROM organization.member_invitations
		WHERE organization_id = $1::uuid AND id = $2::uuid FOR UPDATE
	`, principal.OrganizationID, invitationID).Scan(&status, &email, &revision)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if status != InvitationPending {
		return ErrConflict
	}
	commandTag, err := tx.Exec(ctx, `
		UPDATE organization.member_invitations SET status = 'revoked', revoked_at = now(), revision = revision + 1, updated_at = now()
		WHERE organization_id = $1::uuid AND id = $2::uuid AND status = 'pending'
	`, principal.OrganizationID, invitationID)
	if err != nil {
		return err
	}
	if commandTag.RowsAffected() == 0 {
		return ErrConflict
	}
	if err := notification.CancelPending(ctx, tx, principal.OrganizationID, notification.TemplateOrganizationInvitation, email); err != nil {
		return err
	}
	store.AppendAuditTx(ctx, tx, store.NewAuditEntry("organization.invitation.revoked", principal.OrganizationID, principal.UserID, "invitation", invitationID, nil), "")
	if err := appendOrgEvent(ctx, tx, s.Events, principal, "", eventing.EventOrganizationInvitationRevoked, invitationID, revision+1, map[string]any{"invitation_id": invitationID}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// ListInvitations returns the organization's invitation log.
func (s InvitationService) ListInvitations(ctx context.Context, principal auth.Principal, status string) ([]Invitation, error) {
	svc := Service{Store: s.Store}
	if err := svc.RequireOrganizationAction(ctx, principal, authz.ActionOrganizationInvitationMng); err != nil {
		return nil, err
	}
	rows, err := s.Store.Pool.Query(ctx, `
		SELECT id::text, email, COALESCE(display_name, ''), organization_role, authority_scope,
		       COALESCE(scope_workspace_id::text, ''), status, invited_by::text, expires_at, created_at, accepted_at, revision
		FROM organization.member_invitations
		WHERE organization_id = $1::uuid AND ($2 = '' OR status = $2)
		ORDER BY created_at DESC, id
		LIMIT 100
	`, principal.OrganizationID, status)
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
		items = append(items, item)
	}
	return items, rows.Err()
}
