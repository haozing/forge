package workspace

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"agentchunzhi/internal/auth"
	"agentchunzhi/internal/authz"
	"agentchunzhi/internal/eventing"

	"github.com/jackc/pgx/v5"
)

type CreateInput struct {
	Name                   string `json:"name"`
	Description            string `json:"description"`
	DefaultResourceModelID string `json:"default_resource_model_id"`
}

type MemberDetail struct {
	ID          string     `json:"id"`
	DisplayName string     `json:"display_name"`
	Email       string     `json:"email"`
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

// Create is reserved to organization admins. The creator receives an explicit
// workspace admin membership; there is no workspace owner role.
func (s Service) Create(ctx context.Context, principal auth.Principal, input CreateInput) (Summary, error) {
	if err := s.validatePrincipal(principal); err != nil {
		return Summary{}, ErrForbidden
	}
	input.Name = strings.TrimSpace(input.Name)
	if input.Name == "" {
		return Summary{}, ErrInvalidInput
	}
	var admin bool
	if err := s.Store.Pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM identity.users
			WHERE organization_id = $1::uuid AND id = $2::uuid
			  AND user_type = 'member' AND status = 'active' AND organization_role = 'admin'
		)
	`, principal.OrganizationID, principal.UserID).Scan(&admin); err != nil {
		return Summary{}, fmt.Errorf("check organization admin: %w", err)
	}
	if !admin {
		return Summary{}, ErrForbidden
	}
	tx, err := s.Store.Pool.Begin(ctx)
	if err != nil {
		return Summary{}, fmt.Errorf("begin workspace create: %w", err)
	}
	defer tx.Rollback(ctx)
	var id string
	err = tx.QueryRow(ctx, `
		INSERT INTO content.workspaces
			(organization_id, slug, name, description, default_resource_model_id, created_by)
		VALUES ($1::uuid, 'ws-' || replace(gen_random_uuid()::text, '-', ''), $2, $3, NULLIF($4, '')::uuid, $5::uuid)
		RETURNING id::text
	`, principal.OrganizationID, input.Name, input.Description, input.DefaultResourceModelID, principal.UserID).Scan(&id)
	if err != nil {
		return Summary{}, fmt.Errorf("insert workspace: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO content.workspace_members (organization_id, workspace_id, user_id, role, granted_by)
		VALUES ($1::uuid, $2::uuid, $3::uuid, 'admin', $3::uuid)
	`, principal.OrganizationID, id, principal.UserID); err != nil {
		return Summary{}, fmt.Errorf("insert workspace admin membership: %w", err)
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
		SELECT u.id::text, u.display_name, COALESCE(u.email, ''), wm.role, u.status, wm.created_at
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
		if err := rows.Scan(&item.ID, &item.DisplayName, &item.Email, &item.Role, &item.Status, &item.JoinedAt); err != nil {
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
	if role != authz.WorkspaceRoleAdmin {
		return Invitation{}, ErrForbidden
	}
	input.Email = strings.TrimSpace(strings.ToLower(input.Email))
	if input.Email == "" {
		return Invitation{}, ErrInvalidInput
	}
	if !ValidEmail(input.Email) {
		return Invitation{}, ErrInvalidEmail
	}
	if !authz.ValidWorkspaceRole(input.Role) {
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
	if role != authz.WorkspaceRoleAdmin {
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

// userMatchesInvitedEmail reports whether the acting user presents the invited
// address. Members own exactly one organization-scoped identity with a
// normalized email column; the legacy login_name fallback is retired.
func (s Service) userMatchesInvitedEmail(ctx context.Context, principal auth.Principal, invitedEmail string) (bool, error) {
	invited := strings.ToLower(strings.TrimSpace(invitedEmail))
	var count int
	err := s.Store.Pool.QueryRow(ctx, `
		SELECT count(*) FROM identity.users u
		WHERE u.organization_id = $1::uuid AND u.id = $2::uuid AND u.status = 'active'
		  AND lower(btrim(COALESCE(u.email, ''))) = $3
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
		INSERT INTO content.workspace_members (organization_id, workspace_id, user_id, role, granted_by)
		VALUES ($1::uuid, $2::uuid, $3::uuid, $4, $3::uuid)
		ON CONFLICT (workspace_id, user_id) DO NOTHING
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
	if role != authz.WorkspaceRoleAdmin {
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
// /api/workspace-members/{memberId}: the internal
// content.workspace_members.id and the user id that every list endpoint
// exposes. The caller still has to hold admin rights in exactly the
// workspace the resolved row belongs to.
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
	return authz.ValidWorkspaceRole(role)
}

// otherActiveAdmins counts active admins besides the target user.
func (s Service) otherActiveAdmins(ctx context.Context, workspaceID, exceptUserID string) (int, error) {
	var count int
	err := s.Store.Pool.QueryRow(ctx, `
		SELECT count(*)
		FROM content.workspace_members wm
		JOIN identity.users u ON u.id = wm.user_id AND u.status = 'active'
		WHERE wm.workspace_id = $1::uuid AND wm.role = 'admin' AND wm.user_id <> $2::uuid
	`, workspaceID, exceptUserID).Scan(&count)
	return count, err
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
	if actorRole != authz.WorkspaceRoleAdmin {
		return MemberDetail{}, ErrForbidden
	}
	if record.Role == authz.WorkspaceRoleAdmin && role != authz.WorkspaceRoleAdmin {
		admins, err := s.otherActiveAdmins(ctx, record.WorkspaceID, record.UserID)
		if err != nil {
			return MemberDetail{}, err
		}
		if admins == 0 {
			return MemberDetail{}, ErrLastAdminRequired
		}
	}
	// Business write, audit and the membership fact commit together; the
	// workspace row lock serializes the admin re-count against concurrent
	// demotions and disables.
	tx, err := s.Store.Pool.Begin(ctx)
	if err != nil {
		return MemberDetail{}, fmt.Errorf("begin member role change: %w", err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `
		SELECT id FROM content.workspaces
		WHERE organization_id = $1::uuid AND id = $2::uuid
		FOR UPDATE
	`, principal.OrganizationID, record.WorkspaceID); err != nil {
		return MemberDetail{}, fmt.Errorf("lock workspace for role change: %w", err)
	}
	if record.Role == authz.WorkspaceRoleAdmin && role != authz.WorkspaceRoleAdmin {
		var admins int
		if err := tx.QueryRow(ctx, `
			SELECT count(*) FROM content.workspace_members wm
			JOIN identity.users u ON u.id = wm.user_id AND u.status = 'active'
			WHERE wm.workspace_id = $1::uuid AND wm.role = 'admin' AND wm.user_id <> $2::uuid
		`, record.WorkspaceID, record.UserID).Scan(&admins); err != nil {
			return MemberDetail{}, err
		}
		if admins == 0 {
			return MemberDetail{}, ErrLastAdminRequired
		}
	}
	if _, err := tx.Exec(ctx, `
		UPDATE content.workspace_members
		SET role = $3, revision = revision + 1, updated_at = now()
		WHERE organization_id = $1::uuid AND id = $2::uuid
	`, principal.OrganizationID, record.RowID, role); err != nil {
		return MemberDetail{}, err
	}
	if err := appendMembershipAuditTx(ctx, tx, principal, AuditMemberRoleChange, record.WorkspaceID, record.RowID, map[string]any{
		"workspace_id":   record.WorkspaceID,
		"target_user_id": record.UserID,
		"old_role":       record.Role,
		"new_role":       role,
	}); err != nil {
		return MemberDetail{}, err
	}
	if err := s.appendMembershipEventTx(ctx, tx, principal, record.WorkspaceID, eventing.WorkspaceMembershipChangedPayload{
		WorkspaceID: record.WorkspaceID,
		UserID:      record.UserID,
		Role:        role,
		OldRole:     record.Role,
		NewRole:     role,
		Operation:   "role_changed",
	}); err != nil {
		return MemberDetail{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return MemberDetail{}, fmt.Errorf("commit member role change: %w", err)
	}
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
	if actorRole != authz.WorkspaceRoleAdmin {
		return ErrForbidden
	}
	if record.Role == authz.WorkspaceRoleAdmin {
		admins, err := s.otherActiveAdmins(ctx, record.WorkspaceID, record.UserID)
		if err != nil {
			return err
		}
		if admins == 0 {
			return ErrLastAdminRequired
		}
	}
	if record.UserID == principal.UserID {
		return ErrConflict
	}
	tx, err := s.Store.Pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin member removal: %w", err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `
		SELECT id FROM content.workspaces
		WHERE organization_id = $1::uuid AND id = $2::uuid
		FOR UPDATE
	`, principal.OrganizationID, record.WorkspaceID); err != nil {
		return fmt.Errorf("lock workspace for removal: %w", err)
	}
	if record.Role == authz.WorkspaceRoleAdmin {
		var admins int
		if err := tx.QueryRow(ctx, `
			SELECT count(*) FROM content.workspace_members wm
			JOIN identity.users u ON u.id = wm.user_id AND u.status = 'active'
			WHERE wm.workspace_id = $1::uuid AND wm.role = 'admin' AND wm.user_id <> $2::uuid
		`, record.WorkspaceID, record.UserID).Scan(&admins); err != nil {
			return err
		}
		if admins == 0 {
			return ErrLastAdminRequired
		}
	}
	commandTag, err := tx.Exec(ctx, `DELETE FROM content.workspace_members WHERE organization_id = $1::uuid AND id = $2::uuid`, principal.OrganizationID, record.RowID)
	if err != nil {
		return err
	}
	if commandTag.RowsAffected() == 0 {
		return ErrNotFound
	}
	if err := appendMembershipAuditTx(ctx, tx, principal, AuditMemberRemove, record.WorkspaceID, record.RowID, map[string]any{
		"workspace_id":    record.WorkspaceID,
		"removed_user_id": record.UserID,
		"old_role":        record.Role,
	}); err != nil {
		return err
	}
	if err := s.appendMembershipEventTx(ctx, tx, principal, record.WorkspaceID, eventing.WorkspaceMembershipChangedPayload{
		WorkspaceID: record.WorkspaceID,
		UserID:      record.UserID,
		OldRole:     record.Role,
		Operation:   "revoked",
	}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s Service) MemberDetail(ctx context.Context, principal auth.Principal, workspaceID, userID string) (MemberDetail, error) {
	if _, err := s.membership(ctx, principal, workspaceID); err != nil {
		return MemberDetail{}, err
	}
	var item MemberDetail
	err := s.Store.Pool.QueryRow(ctx, `
		SELECT u.id::text, u.display_name, COALESCE(u.email, ''), wm.role, u.status, wm.created_at
		FROM content.workspace_members wm JOIN identity.users u ON u.id = wm.user_id
		WHERE wm.organization_id = $1::uuid AND wm.workspace_id = $2::uuid AND wm.user_id = $3::uuid
	`, principal.OrganizationID, workspaceID, userID).Scan(&item.ID, &item.DisplayName, &item.Email, &item.Role, &item.Status, &item.JoinedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return MemberDetail{}, ErrNotFound
	}
	return item, err
}
