package workspace

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"agentchunzhi/internal/auth"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
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

const auditResourceWorkspaceInvitation = "workspace_invitation"
const auditResourceWorkspaceMember = "workspace_member"

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
	// Invitations are unusable if the invitee can never present a matching
	// identity, so obvious non-addresses are rejected outright.
	if !ValidEmail(input.Email) {
		return Invitation{}, ErrInvalidEmail
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
	s.writeAuditAsync(NewAuditEntry(AuditInvitationCreate, "", principal.OrganizationID, principal.UserID,
		auditResourceWorkspaceInvitation, item.ID, map[string]any{
			"workspace_id":  workspaceID,
			"invitation_id": item.ID,
			"email":         item.Email,
			"role":          item.Role,
		}))
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

// usersEmailColumnState caches whether identity.users carries an email column.
// The invitations are matched against the email column when it exists and fall
// back to login_name matching otherwise, so the probe runs once per process.
var usersEmailColumnState struct {
	sync.Mutex
	known   bool
	present bool
}

func identityUsersHasEmailColumn(ctx context.Context, pool *pgxpool.Pool) bool {
	usersEmailColumnState.Lock()
	defer usersEmailColumnState.Unlock()
	if !usersEmailColumnState.known && pool != nil {
		var count int
		err := pool.QueryRow(ctx, `
			SELECT count(*) FROM information_schema.columns
			WHERE table_schema = 'identity' AND table_name = 'users' AND column_name = 'email'
		`).Scan(&count)
		if err == nil {
			usersEmailColumnState.known = true
			usersEmailColumnState.present = count > 0
		}
	}
	return usersEmailColumnState.present
}

// userMatchesInvitedEmail reports whether the acting user presents the invited
// address. The email column wins when present; otherwise either login_name or
// (future) email may satisfy the invitation.
func (s Service) userMatchesInvitedEmail(ctx context.Context, principal auth.Principal, invitedEmail string) (bool, error) {
	invited := strings.ToLower(strings.TrimSpace(invitedEmail))
	predicate := "lower(btrim(COALESCE(u.login_name, ''))) = $3"
	if identityUsersHasEmailColumn(ctx, s.Store.Pool) {
		predicate = "(lower(btrim(COALESCE(u.email, ''))) = $3 OR lower(btrim(COALESCE(u.login_name, ''))) = $3)"
	}
	var count int
	err := s.Store.Pool.QueryRow(ctx, `
		SELECT count(*) FROM identity.users u
		WHERE u.organization_id = $1::uuid AND u.id = $2::uuid AND u.status = 'active'
		  AND `+predicate+`
	`, principal.OrganizationID, principal.UserID, invited).Scan(&count)
	if err != nil {
		return false, fmt.Errorf("match invited email: %w", err)
	}
	return count > 0, nil
}

func (s Service) AcceptInvitation(ctx context.Context, principal auth.Principal, invitationID string) (MemberDetail, error) {
	if !validateID(invitationID) {
		return MemberDetail{}, ErrInvalidInput
	}
	if err := s.validatePrincipal(principal); err != nil {
		return MemberDetail{}, ErrForbidden
	}
	var workspaceID, email, role, status string
	err := s.Store.Pool.QueryRow(ctx, `
		SELECT workspace_id::text, email, role, status
		FROM content.workspace_invitations
		WHERE id = $1::uuid AND organization_id = $2::uuid AND expires_at > now()
	`, invitationID, principal.OrganizationID).Scan(&workspaceID, &email, &role, &status)
	if errors.Is(err, pgx.ErrNoRows) {
		return MemberDetail{}, ErrNotFound
	}
	if err != nil {
		return MemberDetail{}, err
	}
	// Accepting is idempotent: an already-accepted invitation answers with the
	// caller's current membership state instead of failing. Non-members keep
	// the previous "not found" answer so state is not disclosed across
	// workspace boundaries.
	if status == "accepted" {
		detail, err := s.MemberDetail(ctx, principal, workspaceID, principal.UserID)
		if errors.Is(err, ErrForbidden) {
			return MemberDetail{}, ErrNotFound
		}
		return detail, err
	}
	if status != "pending" {
		return MemberDetail{}, ErrNotFound
	}
	matches, err := s.userMatchesInvitedEmail(ctx, principal, email)
	if err != nil {
		return MemberDetail{}, err
	}
	if !matches {
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
	s.writeAuditAsync(NewAuditEntry(AuditInvitationAccept, "", principal.OrganizationID, principal.UserID,
		auditResourceWorkspaceInvitation, invitationID, map[string]any{
			"workspace_id":  workspaceID,
			"invitation_id": invitationID,
			"role":          role,
		}))
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
	s.writeAuditAsync(NewAuditEntry(AuditInvitationRevoke, "", principal.OrganizationID, principal.UserID,
		auditResourceWorkspaceInvitation, invitationID, map[string]any{
			"workspace_id":  workspaceID,
			"invitation_id": invitationID,
		}))
	return nil
}

// memberRecord is one candidate content.workspace_members row for a
// {memberId} reference.
type memberRecord struct {
	RowID       string
	WorkspaceID string
	UserID      string
	Role        string
}

// selectMemberRecord turns the candidate membership rows into the single row a
// member reference addresses, or an error. workspaceHint is the optional
// ?workspace_id= disambiguator callers may send when one user id maps to
// several workspace memberships.
func selectMemberRecord(rows []memberRecord, workspaceHint string) (memberRecord, error) {
	if strings.TrimSpace(workspaceHint) != "" {
		if !validateID(workspaceHint) {
			return memberRecord{}, ErrInvalidInput
		}
		filtered := make([]memberRecord, 0, len(rows))
		for _, row := range rows {
			if row.WorkspaceID == workspaceHint {
				filtered = append(filtered, row)
			}
		}
		rows = filtered
	}
	switch len(rows) {
	case 0:
		return memberRecord{}, ErrNotFound
	case 1:
		return rows[0], nil
	default:
		return memberRecord{}, ErrAmbiguousMember
	}
}

func collectMemberRecords(ctx context.Context, rows pgx.Rows) ([]memberRecord, error) {
	defer rows.Close()
	records := make([]memberRecord, 0)
	for rows.Next() {
		var record memberRecord
		if err := rows.Scan(&record.RowID, &record.WorkspaceID, &record.UserID, &record.Role); err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	return records, rows.Err()
}

// resolveMemberRecord supports both addressing schemes of
// /api/frontend/workspace-members/{memberId}: the internal
// content.workspace_members.id and the user id that every list endpoint
// exposes. The caller still has to hold owner/admin rights in exactly the
// workspace the resolved row belongs to — the checks in UpdateMember /
// RemoveMember operate on record.WorkspaceID only.
func (s Service) resolveMemberRecord(ctx context.Context, principal auth.Principal, memberID, workspaceHint string) (memberRecord, error) {
	records, err := s.resolveMemberRecords(ctx, principal, memberID)
	if err != nil {
		return memberRecord{}, err
	}
	return selectMemberRecord(records, workspaceHint)
}

func (s Service) resolveMemberRecords(ctx context.Context, principal auth.Principal, memberID string) ([]memberRecord, error) {
	if !validateID(memberID) {
		return nil, ErrInvalidInput
	}
	rows, err := s.Store.Pool.Query(ctx, `
		SELECT id::text, workspace_id::text, user_id::text, role
		FROM content.workspace_members
		WHERE organization_id = $1::uuid AND id = $2::uuid
	`, principal.OrganizationID, memberID)
	if err != nil {
		return nil, fmt.Errorf("resolve workspace member by row id: %w", err)
	}
	records, err := collectMemberRecords(ctx, rows)
	if err != nil || len(records) > 0 {
		return records, err
	}
	rows, err = s.Store.Pool.Query(ctx, `
		SELECT id::text, workspace_id::text, user_id::text, role
		FROM content.workspace_members
		WHERE organization_id = $1::uuid AND user_id = $2::uuid
		ORDER BY workspace_id
	`, principal.OrganizationID, memberID)
	if err != nil {
		return nil, fmt.Errorf("resolve workspace member by user id: %w", err)
	}
	return collectMemberRecords(ctx, rows)
}

func validMemberRole(role string) bool {
	switch role {
	case "admin", "editor", "reviewer", "viewer", "member", "owner":
		return true
	}
	return false
}

func (s Service) UpdateMember(ctx context.Context, principal auth.Principal, memberID, role, workspaceHint string) (MemberDetail, error) {
	role = strings.TrimSpace(role)
	if !validMemberRole(role) {
		return MemberDetail{}, ErrInvalidInput
	}
	record, err := s.resolveMemberRecord(ctx, principal, memberID, workspaceHint)
	if err != nil {
		return MemberDetail{}, err
	}
	actorRole, err := s.membership(ctx, principal, record.WorkspaceID)
	if err != nil {
		return MemberDetail{}, err
	}
	if actorRole != "owner" && actorRole != "admin" {
		return MemberDetail{}, ErrForbidden
	}
	if role == "owner" && actorRole != "owner" {
		return MemberDetail{}, ErrForbidden
	}
	if _, err := s.Store.Pool.Exec(ctx, `UPDATE content.workspace_members SET role = $3 WHERE organization_id = $1::uuid AND id = $2::uuid`, principal.OrganizationID, record.RowID, role); err != nil {
		return MemberDetail{}, err
	}
	s.writeAuditAsync(NewAuditEntry(AuditMemberRoleChange, "", principal.OrganizationID, principal.UserID,
		auditResourceWorkspaceMember, record.RowID, map[string]any{
			"workspace_id":   record.WorkspaceID,
			"target_user_id": record.UserID,
			"old_role":       record.Role,
			"new_role":       role,
		}))
	return s.MemberDetail(ctx, principal, record.WorkspaceID, record.UserID)
}

func (s Service) RemoveMember(ctx context.Context, principal auth.Principal, memberID, workspaceHint string) error {
	record, err := s.resolveMemberRecord(ctx, principal, memberID, workspaceHint)
	if err != nil {
		return err
	}
	actorRole, err := s.membership(ctx, principal, record.WorkspaceID)
	if err != nil {
		return err
	}
	if actorRole != "owner" && actorRole != "admin" {
		return ErrForbidden
	}
	if record.Role == "owner" {
		return ErrConflict
	}
	if record.UserID == principal.UserID {
		return ErrConflict
	}
	if _, err := s.Store.Pool.Exec(ctx, `DELETE FROM content.workspace_members WHERE organization_id = $1::uuid AND id = $2::uuid`, principal.OrganizationID, record.RowID); err != nil {
		return err
	}
	s.writeAuditAsync(NewAuditEntry(AuditMemberRemove, "", principal.OrganizationID, principal.UserID,
		auditResourceWorkspaceMember, record.RowID, map[string]any{
			"workspace_id":    record.WorkspaceID,
			"removed_user_id": record.UserID,
			"old_role":        record.Role,
		}))
	return nil
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
