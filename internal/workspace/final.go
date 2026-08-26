package workspace

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"agentchunzhi/internal/auth"

	"github.com/jackc/pgx/v5"
)

type CreateInput struct {
	Name                   string `json:"name"`
	Description            string `json:"description"`
	DefaultVisibility      string `json:"default_visibility"`
	DefaultResourceModelID string `json:"default_resource_model_id"`
}

type MemberDetail struct {
	ID          string     `json:"id"`
	DisplayName string     `json:"display_name"`
	LoginName   string     `json:"login_name"`
	Role        string     `json:"role"`
	Status      string     `json:"status"`
	JoinedAt    time.Time  `json:"joined_at"`
	LastSeenAt  *time.Time `json:"last_seen_at,omitempty"`
}

type Invitation struct {
	ID          string     `json:"id"`
	WorkspaceID string     `json:"workspace_id"`
	Email       string     `json:"email"`
	Role        string     `json:"role"`
	Status      string     `json:"status"`
	InvitedBy   string     `json:"invited_by"`
	ExpiresAt   time.Time  `json:"expires_at"`
	CreatedAt   time.Time  `json:"created_at"`
	AcceptedAt  *time.Time `json:"accepted_at,omitempty"`
}

type InviteInput struct {
	Email          string `json:"email"`
	Role           string `json:"role"`
	ExpiresInHours int    `json:"expires_in_hours"`
}

func (s Service) Create(ctx context.Context, principal auth.Principal, input CreateInput) (Summary, error) {
	if err := s.validatePrincipal(principal); err != nil {
		return Summary{}, ErrForbidden
	}
	input.Name = strings.TrimSpace(input.Name)
	input.DefaultVisibility = strings.TrimSpace(input.DefaultVisibility)
	if input.Name == "" {
		return Summary{}, ErrInvalidInput
	}
	if input.DefaultVisibility == "" {
		input.DefaultVisibility = "workspace"
	}
	if input.DefaultVisibility != "public" && input.DefaultVisibility != "login" && input.DefaultVisibility != "private" && input.DefaultVisibility != "workspace" && input.DefaultVisibility != "internal" {
		return Summary{}, ErrInvalidInput
	}
	tx, err := s.Store.Pool.Begin(ctx)
	if err != nil {
		return Summary{}, fmt.Errorf("begin workspace create: %w", err)
	}
	defer tx.Rollback(ctx)
	var id string
	err = tx.QueryRow(ctx, `
		INSERT INTO content.workspaces
			(organization_id, name, description, default_visibility, default_resource_model_id, created_by)
		VALUES ($1::uuid, $2, $3, $4, NULLIF($5, '')::uuid, $6::uuid)
		RETURNING id::text
	`, principal.OrganizationID, input.Name, input.Description, input.DefaultVisibility, input.DefaultResourceModelID, principal.UserID).Scan(&id)
	if err != nil {
		return Summary{}, fmt.Errorf("insert workspace: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO content.workspace_members (organization_id, workspace_id, user_id, role)
		VALUES ($1::uuid, $2::uuid, $3::uuid, 'owner')
	`, principal.OrganizationID, id, principal.UserID); err != nil {
		return Summary{}, fmt.Errorf("insert workspace owner: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return Summary{}, fmt.Errorf("commit workspace create: %w", err)
	}
	return s.Get(ctx, principal, id)
}

func (s Service) ListMembers(ctx context.Context, principal auth.Principal, workspaceID string) ([]MemberDetail, error) {
	if _, err := s.membership(ctx, principal, workspaceID); err != nil {
		return nil, err
	}
	rows, err := s.Store.Pool.Query(ctx, `
		SELECT u.id::text, u.display_name, COALESCE(u.login_name, ''), wm.role, u.status, wm.created_at
		FROM content.workspace_members wm
		JOIN identity.users u ON u.id = wm.user_id
		WHERE wm.organization_id = $1::uuid AND wm.workspace_id = $2::uuid
		ORDER BY wm.created_at, u.id
	`, principal.OrganizationID, workspaceID)
	if err != nil {
		return nil, fmt.Errorf("list workspace members: %w", err)
	}
	defer rows.Close()
	items := make([]MemberDetail, 0)
	for rows.Next() {
		var item MemberDetail
		if err := rows.Scan(&item.ID, &item.DisplayName, &item.LoginName, &item.Role, &item.Status, &item.JoinedAt); err != nil {
			return nil, fmt.Errorf("scan workspace member: %w", err)
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s Service) Invite(ctx context.Context, principal auth.Principal, workspaceID string, input InviteInput) (Invitation, error) {
	role, err := s.membership(ctx, principal, workspaceID)
	if err != nil {
		return Invitation{}, err
	}
	if role != "owner" && role != "admin" {
		return Invitation{}, ErrForbidden
	}
	input.Email = strings.TrimSpace(strings.ToLower(input.Email))
	if input.Email == "" {
		return Invitation{}, ErrInvalidInput
	}
	if input.Role == "" || input.Role != "admin" && input.Role != "editor" && input.Role != "reviewer" && input.Role != "viewer" && input.Role != "member" {
		return Invitation{}, ErrInvalidInput
	}
	if input.ExpiresInHours <= 0 || input.ExpiresInHours > 720 {
		input.ExpiresInHours = 168
	}
	var item Invitation
	err = s.Store.Pool.QueryRow(ctx, `
		INSERT INTO content.workspace_invitations
			(organization_id, workspace_id, email, role, invited_by, expires_at, token_hash)
		VALUES ($1::uuid, $2::uuid, $3, $4, $5::uuid, now() + make_interval(hours => $6), encode(digest(gen_random_uuid()::text, 'sha256'), 'hex'))
		RETURNING id::text, workspace_id::text, email, role, status, invited_by::text, expires_at, created_at, accepted_at
	`, principal.OrganizationID, workspaceID, input.Email, input.Role, principal.UserID, input.ExpiresInHours).Scan(
		&item.ID, &item.WorkspaceID, &item.Email, &item.Role, &item.Status, &item.InvitedBy, &item.ExpiresAt, &item.CreatedAt, &item.AcceptedAt)
	if err != nil {
		return Invitation{}, fmt.Errorf("create workspace invitation: %w", err)
	}
	return item, nil
}

func (s Service) ListInvitations(ctx context.Context, principal auth.Principal, workspaceID string) ([]Invitation, error) {
	role, err := s.membership(ctx, principal, workspaceID)
	if err != nil {
		return nil, err
	}
	if role != "owner" && role != "admin" {
		return nil, ErrForbidden
	}
	rows, err := s.Store.Pool.Query(ctx, `
		SELECT id::text, workspace_id::text, email, role, status, invited_by::text, expires_at, created_at, accepted_at
		FROM content.workspace_invitations
		WHERE organization_id = $1::uuid AND workspace_id = $2::uuid
		ORDER BY created_at DESC, id
	`, principal.OrganizationID, workspaceID)
	if err != nil {
		return nil, fmt.Errorf("list workspace invitations: %w", err)
	}
	defer rows.Close()
	items := make([]Invitation, 0)
	for rows.Next() {
		var item Invitation
		if err := rows.Scan(&item.ID, &item.WorkspaceID, &item.Email, &item.Role, &item.Status, &item.InvitedBy, &item.ExpiresAt, &item.CreatedAt, &item.AcceptedAt); err != nil {
			return nil, fmt.Errorf("scan workspace invitation: %w", err)
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s Service) AcceptInvitation(ctx context.Context, principal auth.Principal, invitationID string) (MemberDetail, error) {
	if !validateID(invitationID) {
		return MemberDetail{}, ErrInvalidInput
	}
	var workspaceID, email, role string
	err := s.Store.Pool.QueryRow(ctx, `
		SELECT workspace_id::text, email, role
		FROM content.workspace_invitations
		WHERE id = $1::uuid AND organization_id = $2::uuid AND status = 'pending' AND expires_at > now()
	`, invitationID, principal.OrganizationID).Scan(&workspaceID, &email, &role)
	if errors.Is(err, pgx.ErrNoRows) {
		return MemberDetail{}, ErrNotFound
	}
	if err != nil {
		return MemberDetail{}, err
	}
	var loginName string
	if err := s.Store.Pool.QueryRow(ctx, `SELECT COALESCE(login_name, '') FROM identity.users WHERE id = $1::uuid`, principal.UserID).Scan(&loginName); err != nil {
		return MemberDetail{}, err
	}
	if !strings.EqualFold(strings.TrimSpace(email), strings.TrimSpace(loginName)) {
		return MemberDetail{}, ErrForbidden
	}
	tx, err := s.Store.Pool.Begin(ctx)
	if err != nil {
		return MemberDetail{}, err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `
		INSERT INTO content.workspace_members (organization_id, workspace_id, user_id, role)
		VALUES ($1::uuid, $2::uuid, $3::uuid, $4)
		ON CONFLICT (workspace_id, user_id) DO UPDATE SET role = EXCLUDED.role
	`, principal.OrganizationID, workspaceID, principal.UserID, role); err != nil {
		return MemberDetail{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE content.workspace_invitations SET status = 'accepted', accepted_at = now() WHERE id = $1::uuid`, invitationID); err != nil {
		return MemberDetail{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return MemberDetail{}, err
	}
	return s.MemberDetail(ctx, principal, workspaceID, principal.UserID)
}

func (s Service) RevokeInvitation(ctx context.Context, principal auth.Principal, invitationID string) error {
	if !validateID(invitationID) {
		return ErrInvalidInput
	}
	var workspaceID string
	if err := s.Store.Pool.QueryRow(ctx, `SELECT workspace_id::text FROM content.workspace_invitations WHERE id = $1::uuid AND organization_id = $2::uuid`, invitationID, principal.OrganizationID).Scan(&workspaceID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	role, err := s.membership(ctx, principal, workspaceID)
	if err != nil {
		return err
	}
	if role != "owner" && role != "admin" {
		return ErrForbidden
	}
	result, err := s.Store.Pool.Exec(ctx, `UPDATE content.workspace_invitations SET status = 'revoked' WHERE id = $1::uuid AND status = 'pending'`, invitationID)
	if err != nil {
		return err
	}
	if result.RowsAffected() == 0 {
		return ErrConflict
	}
	return nil
}

func (s Service) UpdateMember(ctx context.Context, principal auth.Principal, memberID, role string) (MemberDetail, error) {
	if !validateID(memberID) {
		return MemberDetail{}, ErrInvalidInput
	}
	role = strings.TrimSpace(role)
	if role != "admin" && role != "editor" && role != "reviewer" && role != "viewer" && role != "member" && role != "owner" {
		return MemberDetail{}, ErrInvalidInput
	}
	var workspaceID string
	if err := s.Store.Pool.QueryRow(ctx, `SELECT workspace_id::text FROM content.workspace_members WHERE organization_id = $1::uuid AND id = $2::uuid`, principal.OrganizationID, memberID).Scan(&workspaceID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return MemberDetail{}, ErrNotFound
		}
		return MemberDetail{}, err
	}
	actorRole, err := s.membership(ctx, principal, workspaceID)
	if err != nil {
		return MemberDetail{}, err
	}
	if actorRole != "owner" && actorRole != "admin" {
		return MemberDetail{}, ErrForbidden
	}
	if role == "owner" && actorRole != "owner" {
		return MemberDetail{}, ErrForbidden
	}
	if _, err := s.Store.Pool.Exec(ctx, `UPDATE content.workspace_members SET role = $3 WHERE organization_id = $1::uuid AND id = $2::uuid`, principal.OrganizationID, memberID, role); err != nil {
		return MemberDetail{}, err
	}
	var userID string
	if err := s.Store.Pool.QueryRow(ctx, `SELECT user_id::text FROM content.workspace_members WHERE organization_id = $1::uuid AND id = $2::uuid`, principal.OrganizationID, memberID).Scan(&userID); err != nil {
		return MemberDetail{}, err
	}
	return s.MemberDetail(ctx, principal, workspaceID, userID)
}

func (s Service) RemoveMember(ctx context.Context, principal auth.Principal, memberID string) error {
	if !validateID(memberID) {
		return ErrInvalidInput
	}
	var workspaceID, memberUserID, memberRole string
	if err := s.Store.Pool.QueryRow(ctx, `SELECT workspace_id::text, user_id::text, role FROM content.workspace_members WHERE organization_id = $1::uuid AND id = $2::uuid`, principal.OrganizationID, memberID).Scan(&workspaceID, &memberUserID, &memberRole); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	actorRole, err := s.membership(ctx, principal, workspaceID)
	if err != nil {
		return err
	}
	if actorRole != "owner" && actorRole != "admin" {
		return ErrForbidden
	}
	if memberRole == "owner" {
		return ErrConflict
	}
	if memberUserID == principal.UserID {
		return ErrConflict
	}
	_, err = s.Store.Pool.Exec(ctx, `DELETE FROM content.workspace_members WHERE organization_id = $1::uuid AND id = $2::uuid`, principal.OrganizationID, memberID)
	return err
}

func (s Service) MemberDetail(ctx context.Context, principal auth.Principal, workspaceID, userID string) (MemberDetail, error) {
	if _, err := s.membership(ctx, principal, workspaceID); err != nil {
		return MemberDetail{}, err
	}
	var item MemberDetail
	err := s.Store.Pool.QueryRow(ctx, `
		SELECT u.id::text, u.display_name, COALESCE(u.login_name, ''), wm.role, u.status, wm.created_at
		FROM content.workspace_members wm JOIN identity.users u ON u.id = wm.user_id
		WHERE wm.organization_id = $1::uuid AND wm.workspace_id = $2::uuid AND wm.user_id = $3::uuid
	`, principal.OrganizationID, workspaceID, userID).Scan(&item.ID, &item.DisplayName, &item.LoginName, &item.Role, &item.Status, &item.JoinedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return MemberDetail{}, ErrNotFound
	}
	return item, err
}
