package organization

// invitation_accept.go — anonymous token resolve/accept. The accept
// transaction locks Organization -> granted Workspaces (UUID order) ->
// Invitation, re-verifies every grant and creates the User plus all
// memberships atomically. The session is created in a second transaction:
// activation survives a session failure.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"agentchunzhi/internal/auth"
	"agentchunzhi/internal/eventing"
	"agentchunzhi/internal/notification"
	"agentchunzhi/internal/store"

	"github.com/jackc/pgx/v5"
)

type ResolvedInvitation struct {
	OrganizationName string    `json:"organization_name"`
	MaskedEmail      string    `json:"email"`
	InviterName      string    `json:"inviter_display_name"`
	Workspaces       []string  `json:"workspaces"`
	ExpiresAt        time.Time `json:"expires_at"`
}

type AcceptInput struct {
	Token       string
	DisplayName string
	Password    string
}

type AcceptResult struct {
	UserID         string   `json:"user_id"`
	Email          string   `json:"email"`
	OrganizationID string   `json:"organization_id"`
	WorkspaceIDs   []string `json:"workspace_ids"`
}

// Resolve turns a token into the minimum anonymous-facing display facts; it
// never returns internal IDs or the token hash.
func (s InvitationService) Resolve(ctx context.Context, token string) (ResolvedInvitation, error) {
	if token == "" {
		return ResolvedInvitation{}, ErrInvitationInvalid
	}
	var result ResolvedInvitation
	var workspaceNames []byte
	err := s.Store.Pool.QueryRow(ctx, `
		SELECT o.name, i.email, inviter.display_name, i.expires_at,
		       COALESCE((SELECT json_agg(w.name ORDER BY w.name)
		         FROM organization.invitation_workspace_grants g
		         JOIN content.workspaces w ON w.id = g.workspace_id
		         WHERE g.invitation_id = i.id), '[]'::jsonb)
		FROM organization.member_invitations i
		JOIN organization.organizations o ON o.id = i.organization_id
		JOIN identity.users inviter ON inviter.id = i.invited_by
		WHERE i.token_hash = $1 AND i.status = 'pending' AND i.expires_at > now()
	`, hashToken(token)).Scan(&result.OrganizationName, &result.MaskedEmail, &result.InviterName,
		&result.ExpiresAt, &workspaceNames)
	if errors.Is(err, pgx.ErrNoRows) {
		return ResolvedInvitation{}, ErrInvitationInvalid
	}
	if err != nil {
		return ResolvedInvitation{}, err
	}
	_ = json.Unmarshal(workspaceNames, &result.Workspaces)
	result.MaskedEmail = maskEmail(result.MaskedEmail)
	return result, nil
}

func maskEmail(email string) string {
	at := -1
	for index, ch := range email {
		if ch == '@' {
			at = index
			break
		}
	}
	if at <= 0 {
		return "***"
	}
	local := email[:at]
	domain := email[at:]
	visible := 1
	if len(local) < visible {
		visible = len(local)
	}
	return local[:visible] + "***" + domain
}

// Accept activates the invited member inside one transaction.
func (s InvitationService) Accept(ctx context.Context, input AcceptInput) (AcceptResult, error) {
	if input.Token == "" {
		return AcceptResult{}, ErrInvitationInvalid
	}
	displayName := trimSpace(input.DisplayName)
	if displayName == "" || len([]rune(displayName)) > 80 {
		return AcceptResult{}, ErrInvalidInput
	}
	if err := auth.ValidatePassword(input.Password); err != nil {
		return AcceptResult{}, ErrInvalidInput
	}
	passwordHash, err := auth.HashPassword(input.Password)
	if err != nil {
		return AcceptResult{}, err
	}
	tx, err := s.Store.Pool.Begin(ctx)
	if err != nil {
		return AcceptResult{}, err
	}
	defer tx.Rollback(ctx)
	var invitationID, organizationID, email, organizationRole, status string
	err = tx.QueryRow(ctx, `
		UPDATE organization.member_invitations
		SET accepted_at = now(), revision = revision + 1, updated_at = now()
		WHERE id = (
			SELECT id FROM organization.member_invitations
			WHERE token_hash = $1 AND status = 'pending' AND expires_at > now()
			FOR UPDATE
		)
		RETURNING id::text, organization_id::text, email, organization_role, status
	`, hashToken(input.Token)).Scan(&invitationID, &organizationID, &email, &organizationRole, &status)
	if errors.Is(err, pgx.ErrNoRows) {
		return AcceptResult{}, ErrInvitationInvalid
	}
	if err != nil {
		return AcceptResult{}, err
	}
	// The email must still be free (accept raced with a direct registration
	// path; registration only happens through invitations in v2).
	var taken bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM identity.users WHERE email = $1)`, email).Scan(&taken); err != nil {
		return AcceptResult{}, err
	}
	if taken {
		return AcceptResult{}, ErrEmailUnavailable
	}
	if err := lockOrganizationTx(ctx, tx, organizationID); err != nil {
		return AcceptResult{}, err
	}
	// Lock granted workspaces in UUID order and re-verify they still belong.
	if _, err := tx.Exec(ctx, `
		SELECT 1 FROM organization.invitation_workspace_grants g
		JOIN content.workspaces w ON w.organization_id = g.organization_id AND w.id = g.workspace_id
		WHERE g.invitation_id = $1::uuid
		ORDER BY g.workspace_id
		FOR UPDATE OF w
	`, invitationID); err != nil {
		return AcceptResult{}, err
	}
	var userID string
	if err := tx.QueryRow(ctx, `
		INSERT INTO identity.users
			(organization_id, user_type, email, display_name, password_hash, organization_role, status)
		VALUES ($1::uuid, 'member', $2, $3, $4, $5, 'active')
		RETURNING id::text
	`, organizationID, email, displayName, passwordHash, organizationRole).Scan(&userID); err != nil {
		return AcceptResult{}, fmt.Errorf("create invited user: %w", err)
	}
	workspaceIDs := []string{}
	rows, err := tx.Query(ctx, `
		INSERT INTO content.workspace_members (organization_id, workspace_id, user_id, role, granted_by)
		SELECT g.organization_id, g.workspace_id, $2::uuid, g.role, i.invited_by
		FROM organization.invitation_workspace_grants g
		JOIN organization.member_invitations i ON i.id = g.invitation_id
		WHERE g.invitation_id = $1::uuid
		ON CONFLICT (workspace_id, user_id) DO NOTHING
		RETURNING workspace_id::text
	`, invitationID, userID)
	if err != nil {
		return AcceptResult{}, err
	}
	for rows.Next() {
		var workspaceID string
		if err := rows.Scan(&workspaceID); err != nil {
			rows.Close()
			return AcceptResult{}, err
		}
		workspaceIDs = append(workspaceIDs, workspaceID)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return AcceptResult{}, err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE organization.member_invitations SET accepted_by = $2::uuid
		WHERE id = $1::uuid
	`, invitationID, userID); err != nil {
		return AcceptResult{}, err
	}
	store.AppendAuditTx(ctx, tx, store.NewAuditEntry("organization.member.activated", organizationID, userID, "invitation", invitationID, map[string]any{
		"workspace_ids": workspaceIDs,
	}), "")
	if s.Events != nil {
		payload, _ := eventing.EncodePayload(map[string]any{
			"invitation_id": invitationID, "user_id": userID, "workspace_ids": workspaceIDs,
		})
		if _, err := s.Events.AppendTx(ctx, tx, eventing.Event{
			OrganizationID:   organizationID,
			EventType:        eventing.EventOrganizationMemberActivated,
			AggregateType:    "invitation",
			AggregateID:      invitationID,
			AggregateVersion: 1,
			PayloadVersion:   eventing.PayloadVersionV1,
			Actor:            map[string]any{"type": "invitation", "id": invitationID},
			Payload:          payload,
		}); err != nil {
			return AcceptResult{}, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return AcceptResult{}, err
	}
	return AcceptResult{UserID: userID, Email: email, OrganizationID: organizationID, WorkspaceIDs: workspaceIDs}, nil
}

// CreateSessionFor starts the second transaction: an initial session for the
// freshly accepted member. Activation survives a session failure.
func (s InvitationService) CreateSessionFor(ctx context.Context, sessions *auth.SessionService, ipPrefix, userAgent, userID string) (auth.Session, error) {
	return sessions.CreateSession(ctx, userID, ipPrefix, userAgent)
}

func trimSpace(value string) string {
	start := 0
	for start < len(value) && (value[start] == ' ' || value[start] == '\t') {
		start++
	}
	end := len(value)
	for end > start && (value[end-1] == ' ' || value[end-1] == '\t') {
		end--
	}
	return value[start:end]
}

var _ = notification.TemplatePasswordReset
var _ = store.Store{}
