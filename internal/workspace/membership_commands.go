package workspace

// membership_commands.go — phase 1 explicit membership commands: add, leave
// and the eligible-member directory. Leave holds the workspace row lock while
// re-counting admins so a concurrent demotion cannot leave the workspace
// without an active admin. Business writes, audit and membership facts commit
// in one transaction.

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"agentchunzhi/internal/auth"
	"agentchunzhi/internal/authz"
	"agentchunzhi/internal/eventing"

	"github.com/jackc/pgx/v5"
)

// EligibleMember is one same-organization active user that does not belong to
// the workspace yet. The email is masked: eligible-member results feed
// autocomplete boxes, never bulk email harvesting.
type EligibleMember struct {
	UserID      string `json:"user_id"`
	DisplayName string `json:"display_name"`
	Email       string `json:"email"`
}

// AddMember grants an explicit membership. The caller must govern the
// workspace; the target must be an active member of the same organization and
// must not hold a membership already.
func (s Service) AddMember(ctx context.Context, principal auth.Principal, workspaceID, userID, role string) (MemberDetail, error) {
	role = strings.TrimSpace(role)
	if !validMemberRole(role) {
		return MemberDetail{}, ErrInvalidInput
	}
	if !validateID(userID) {
		return MemberDetail{}, ErrInvalidInput
	}
	actorRole, err := s.membership(ctx, principal, workspaceID)
	if err != nil {
		return MemberDetail{}, err
	}
	if actorRole != authz.WorkspaceRoleAdmin {
		return MemberDetail{}, ErrForbidden
	}
	var inserted bool
	var detail MemberDetail
	tx, err := s.Store.Pool.Begin(ctx)
	if err != nil {
		return MemberDetail{}, fmt.Errorf("begin add workspace member: %w", err)
	}
	defer tx.Rollback(ctx)
	err = tx.QueryRow(ctx, `
		WITH target AS (
			SELECT u.id FROM identity.users u
			WHERE u.organization_id = $1::uuid AND u.id = $4::uuid
			  AND u.user_type = 'member' AND u.status = 'active'
		), upsert AS (
			INSERT INTO content.workspace_members (organization_id, workspace_id, user_id, role, granted_by)
			SELECT $1::uuid, $2::uuid, target.id, $3, $5::uuid
			FROM target
			ON CONFLICT (workspace_id, user_id) DO NOTHING
			RETURNING id, user_id
		)
		SELECT EXISTS (SELECT 1 FROM upsert),
		       COALESCE((SELECT u.id::text FROM identity.users u WHERE u.id = $4::uuid), ''),
		       COALESCE((SELECT u.display_name FROM identity.users u WHERE u.id = $4::uuid), ''),
		       COALESCE((SELECT u.login_name FROM identity.users u WHERE u.id = $4::uuid AND u.login_name IS NOT NULL), ''),
		       $3,
		       COALESCE((SELECT u.status FROM identity.users u WHERE u.id = $4::uuid), ''),
		       COALESCE((SELECT wm.created_at FROM content.workspace_members wm WHERE wm.workspace_id = $2::uuid AND wm.user_id = $4::uuid), now())
	`, principal.OrganizationID, workspaceID, role, userID, principal.UserID).Scan(
		&inserted, &detail.ID, &detail.DisplayName, &detail.LoginName, &detail.Role, &detail.Status, &detail.JoinedAt)
	if err != nil {
		return MemberDetail{}, fmt.Errorf("add workspace member: %w", err)
	}
	if detail.ID == "" {
		return MemberDetail{}, ErrNotFound
	}
	if !inserted {
		return MemberDetail{}, ErrConflict
	}
	if err := appendMembershipAuditTx(ctx, tx, principal, AuditMemberAdd, workspaceID, detail.ID, map[string]any{
		"workspace_id":  workspaceID,
		"added_user_id": userID,
		"role":          role,
	}); err != nil {
		return MemberDetail{}, err
	}
	if err := s.appendMembershipEventTx(ctx, tx, principal, workspaceID, eventing.WorkspaceMembershipChangedPayload{
		WorkspaceID: workspaceID,
		UserID:      userID,
		Role:        role,
		NewRole:     role,
		Operation:   "granted",
	}); err != nil {
		return MemberDetail{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return MemberDetail{}, fmt.Errorf("commit add workspace member: %w", err)
	}
	return detail, nil
}

// Leave removes the caller's own membership. The last active admin of an
// active workspace cannot leave: the workspace row is locked first so the
// admin re-count is serialized against every other membership transition.
func (s Service) Leave(ctx context.Context, principal auth.Principal, workspaceID string) error {
	if _, err := s.membership(ctx, principal, workspaceID); err != nil {
		return err
	}
	tx, err := s.Store.Pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin workspace leave: %w", err)
	}
	defer tx.Rollback(ctx)
	// Lock the workspace row: every last-admin decision in the organization
	// package takes the same lock (Organization -> Workspace order), so the
	// count below cannot race a concurrent demotion or disable.
	var status string
	if err := tx.QueryRow(ctx, `
		SELECT status FROM content.workspaces
		WHERE organization_id = $1::uuid AND id = $2::uuid
		FOR UPDATE
	`, principal.OrganizationID, workspaceID).Scan(&status); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return fmt.Errorf("lock workspace for leave: %w", err)
	}
	var role string
	err = tx.QueryRow(ctx, `
		SELECT role FROM content.workspace_members
		WHERE organization_id = $1::uuid AND workspace_id = $2::uuid AND user_id = $3::uuid
		FOR UPDATE
	`, principal.OrganizationID, workspaceID, principal.UserID).Scan(&role)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("lock workspace membership for leave: %w", err)
	}
	if role == authz.WorkspaceRoleAdmin && status == "active" {
		var admins int
		if err := tx.QueryRow(ctx, `
			SELECT count(*) FROM content.workspace_members wm
			JOIN identity.users u ON u.id = wm.user_id AND u.status = 'active'
			WHERE wm.workspace_id = $1::uuid AND wm.role = 'admin' AND wm.user_id <> $2::uuid
		`, workspaceID, principal.UserID).Scan(&admins); err != nil {
			return fmt.Errorf("count workspace admins for leave: %w", err)
		}
		if admins == 0 {
			return ErrLastAdminRequired
		}
	}
	commandTag, err := tx.Exec(ctx, `
		DELETE FROM content.workspace_members
		WHERE organization_id = $1::uuid AND workspace_id = $2::uuid AND user_id = $3::uuid
	`, principal.OrganizationID, workspaceID, principal.UserID)
	if err != nil {
		return fmt.Errorf("delete workspace membership: %w", err)
	}
	if commandTag.RowsAffected() == 0 {
		return ErrNotFound
	}
	if err := appendMembershipAuditTx(ctx, tx, principal, AuditMemberLeft, workspaceID, principal.UserID, map[string]any{
		"workspace_id": workspaceID,
		"left_user_id": principal.UserID,
		"old_role":     role,
	}); err != nil {
		return err
	}
	if err := s.appendMembershipEventTx(ctx, tx, principal, workspaceID, eventing.WorkspaceMembershipChangedPayload{
		WorkspaceID: workspaceID,
		UserID:      principal.UserID,
		Role:        role,
		OldRole:     role,
		Operation:   "left",
	}); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit workspace leave: %w", err)
	}
	return nil
}

// EligibleMembers lists same-organization active members that are not in the
// workspace yet, optionally filtered by a display-name/email prefix. The
// result requires the workspace.manage action (workspace admin).
func (s Service) EligibleMembers(ctx context.Context, principal auth.Principal, workspaceID, q string, limit int) ([]EligibleMember, error) {
	role, err := s.membership(ctx, principal, workspaceID)
	if err != nil {
		return nil, err
	}
	if role != authz.WorkspaceRoleAdmin {
		return nil, ErrForbidden
	}
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	search := strings.TrimSpace(q)
	rows, err := s.Store.Pool.Query(ctx, `
		SELECT u.id::text, u.display_name, COALESCE(u.email, '')
		FROM identity.users u
		WHERE u.organization_id = $1::uuid AND u.user_type = 'member' AND u.status = 'active'
		  AND u.id <> $3::uuid
		  AND NOT EXISTS (
		    SELECT 1 FROM content.workspace_members wm
		    WHERE wm.workspace_id = $2::uuid AND wm.user_id = u.id
		  )
		  AND ($4 = '' OR u.display_name ILIKE $4 || '%' OR COALESCE(u.email, '') ILIKE $4 || '%')
		ORDER BY u.display_name, u.id
		LIMIT $5
	`, principal.OrganizationID, workspaceID, principal.UserID, search, limit)
	if err != nil {
		return nil, fmt.Errorf("list eligible workspace members: %w", err)
	}
	defer rows.Close()
	items := make([]EligibleMember, 0)
	for rows.Next() {
		var item EligibleMember
		var email string
		if err := rows.Scan(&item.UserID, &item.DisplayName, &email); err != nil {
			return nil, fmt.Errorf("scan eligible workspace member: %w", err)
		}
		item.Email = MaskedEmail(email)
		items = append(items, item)
	}
	return items, rows.Err()
}

// MaskedEmail keeps the first local-part character and the domain:
// "alice@example.com" becomes "a***@example.com".
func MaskedEmail(email string) string {
	at := strings.Index(email, "@")
	if at <= 0 {
		return "***"
	}
	return email[:1] + "***" + email[at:]
}
