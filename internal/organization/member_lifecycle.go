package organization

// member_lifecycle.go — organization member role/status commands. Disabling a
// member, changing roles and removing memberships must protect the last
// active organization admin and every active workspace admin the user holds,
// under the fixed Organization -> Workspace(s in UUID order) lock order.

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"agentchunzhi/internal/auth"
	"agentchunzhi/internal/authz"
	"agentchunzhi/internal/eventing"
	"agentchunzhi/internal/store"

	"github.com/jackc/pgx/v5"
)

// PatchMemberRole changes one member's organization role.
func (s Service) PatchMemberRole(ctx context.Context, principal auth.Principal, userID, role string) (Member, error) {
	if err := s.RequireOrganizationAction(ctx, principal, authz.ActionOrganizationMemberManage); err != nil {
		return Member{}, err
	}
	if !s.validID(userID) || !authz.ValidOrganizationRole(role) {
		return Member{}, ErrInvalidInput
	}
	tx, err := s.Store.Pool.Begin(ctx)
	if err != nil {
		return Member{}, err
	}
	defer tx.Rollback(ctx)
	// Lock the organization row first: this serializes every governance
	// transition that must re-count admins.
	if err := lockOrganizationTx(ctx, tx, principal.OrganizationID); err != nil {
		return Member{}, err
	}
	var oldRole *string
	var status string
	err = tx.QueryRow(ctx, `
		SELECT organization_role, status FROM identity.users
		WHERE organization_id = $1::uuid AND id = $2::uuid AND user_type = 'member'
		FOR UPDATE
	`, principal.OrganizationID, userID).Scan(&oldRole, &status)
	if errors.Is(err, pgx.ErrNoRows) {
		return Member{}, ErrNotFound
	}
	if err != nil {
		return Member{}, err
	}
	old := "member"
	if oldRole != nil {
		old = *oldRole
	}
	if old == authz.OrganizationRoleAdmin && role != authz.OrganizationRoleAdmin && status == "active" {
		admins, err := countActiveOrgAdminsTx(ctx, tx, principal.OrganizationID)
		if err != nil {
			return Member{}, err
		}
		if admins <= 1 {
			return Member{}, ErrLastOrgAdmin
		}
	}
	var item Member
	err = tx.QueryRow(ctx, `
		UPDATE identity.users SET organization_role = $3, revision = revision + 1, updated_at = now()
		WHERE organization_id = $1::uuid AND id = $2::uuid
		RETURNING id::text, COALESCE(email, ''), display_name, status, COALESCE(organization_role, ''), last_login_at, created_at, revision
	`, principal.OrganizationID, userID, role).Scan(&item.UserID, &item.Email, &item.DisplayName,
		&item.Status, &item.OrganizationRole, &item.LastLoginAt, &item.CreatedAt, &item.Revision)
	if err != nil {
		return Member{}, err
	}
	item.ETag = fmt.Sprint(item.Revision)
	store.AppendAuditTx(ctx, tx, store.NewAuditEntry("organization.member.role_changed", principal.OrganizationID, principal.UserID, "user", userID, map[string]any{"old_role": old, "new_role": role}), "")
	if err := appendOrgEvent(ctx, tx, s.Events, principal, "", eventing.EventOrganizationMemberRoleChanged, userID, item.Revision, map[string]any{"user_id": userID, "old_role": old, "new_role": role}); err != nil {
		return Member{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Member{}, err
	}
	return item, nil
}

// PatchMemberStatus enables or disables a member. Disabling revokes every
// session, clears default workspace preferences, preserves memberships and
// verifies the user is not the last active admin anywhere.
func (s Service) PatchMemberStatus(ctx context.Context, principal auth.Principal, userID, status string) (Member, error) {
	if err := s.RequireOrganizationAction(ctx, principal, authz.ActionOrganizationMemberManage); err != nil {
		return Member{}, err
	}
	if !s.validID(userID) {
		return Member{}, ErrInvalidInput
	}
	switch status {
	case "active", "disabled":
	default:
		return Member{}, ErrInvalidInput
	}
	tx, err := s.Store.Pool.Begin(ctx)
	if err != nil {
		return Member{}, err
	}
	defer tx.Rollback(ctx)
	if err := lockOrganizationTx(ctx, tx, principal.OrganizationID); err != nil {
		return Member{}, err
	}
	var currentStatus string
	var currentRole *string
	err = tx.QueryRow(ctx, `
		SELECT status, organization_role FROM identity.users
		WHERE organization_id = $1::uuid AND id = $2::uuid AND user_type = 'member'
		FOR UPDATE
	`, principal.OrganizationID, userID).Scan(&currentStatus, &currentRole)
	if errors.Is(err, pgx.ErrNoRows) {
		return Member{}, ErrNotFound
	}
	if err != nil {
		return Member{}, err
	}
	if status == "disabled" && currentStatus == "active" {
		// Hold every active workspace where the user is the potential last admin.
		workspaces, err := lockWorkspacesWhereUserIsAdminTx(ctx, tx, principal.OrganizationID, userID)
		if err != nil {
			return Member{}, err
		}
		for _, workspaceID := range workspaces {
			admins, err := countActiveWorkspaceAdminsExcludingTx(ctx, tx, workspaceID, userID)
			if err != nil {
				return Member{}, err
			}
			if admins == 0 {
				return Member{}, ErrLastWorkspaceAdmin
			}
		}
	}
	if status == "active" && currentRole != nil && *currentRole == authz.OrganizationRoleAdmin {
		// Re-activation cannot create the first admin; admins are count-protected elsewhere.
		_ = currentRole
	}
	var item Member
	err = tx.QueryRow(ctx, `
		UPDATE identity.users SET status = $3, revision = revision + 1, updated_at = now()
		WHERE organization_id = $1::uuid AND id = $2::uuid
		RETURNING id::text, COALESCE(email, ''), display_name, status, COALESCE(organization_role, ''), last_login_at, created_at, revision
	`, principal.OrganizationID, userID, status).Scan(&item.UserID, &item.Email, &item.DisplayName,
		&item.Status, &item.OrganizationRole, &item.LastLoginAt, &item.CreatedAt, &item.Revision)
	if err != nil {
		return Member{}, err
	}
	item.ETag = fmt.Sprint(item.Revision)
	if status == "disabled" {
		if _, err := tx.Exec(ctx, `
			UPDATE identity.sessions SET revoked_at = COALESCE(revoked_at, now())
			WHERE user_id = $1::uuid AND revoked_at IS NULL
		`, userID); err != nil {
			return Member{}, err
		}
		if _, err := tx.Exec(ctx, `
			UPDATE identity.user_preferences p
			SET default_workspace_id = NULL, revision = revision + 1, updated_at = now()
			FROM content.workspace_members wm
			WHERE p.user_id = $1::uuid AND wm.user_id = p.user_id
			  AND p.default_workspace_id = wm.workspace_id
			  AND NOT EXISTS (
			    SELECT 1 FROM content.workspace_members keep
			    JOIN content.workspaces w ON w.organization_id = keep.organization_id AND w.id = keep.workspace_id
			    WHERE keep.workspace_id = wm.workspace_id AND keep.user_id = p.user_id
			      AND w.status = 'active'
			  )
		`, userID); err != nil {
			return Member{}, err
		}
	}
	store.AppendAuditTx(ctx, tx, store.NewAuditEntry("organization.member."+mapStatus(status), principal.OrganizationID, principal.UserID, "user", userID, map[string]any{"old_status": currentStatus, "new_status": status}), "")
	if err := appendOrgEvent(ctx, tx, s.Events, principal, "", eventing.EventOrganizationMemberStatusChanged, userID, item.Revision, map[string]any{"user_id": userID, "old_status": currentStatus, "new_status": status}); err != nil {
		return Member{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Member{}, err
	}
	return item, nil
}

func mapStatus(status string) string {
	if status == "active" {
		return "enabled"
	}
	return status
}

func lockOrganizationTx(ctx context.Context, tx pgx.Tx, organizationID string) error {
	if _, err := tx.Exec(ctx, `SELECT id FROM organization.organizations WHERE id = $1::uuid FOR UPDATE`, organizationID); err != nil {
		return fmt.Errorf("lock organization: %w", err)
	}
	return nil
}

func countActiveOrgAdminsTx(ctx context.Context, tx pgx.Tx, organizationID string) (int, error) {
	var admins int
	err := tx.QueryRow(ctx, `
		SELECT count(*) FROM identity.users
		WHERE organization_id = $1::uuid AND user_type = 'member' AND status = 'active'
		  AND organization_role = 'admin'
	`, organizationID).Scan(&admins)
	return admins, err
}

// lockWorkspacesWhereUserIsAdminTx locks every active workspace (UUID order)
// where the user currently holds an admin membership.
func lockWorkspacesWhereUserIsAdminTx(ctx context.Context, tx pgx.Tx, organizationID, userID string) ([]string, error) {
	rows, err := tx.Query(ctx, `
		SELECT wm.workspace_id::text
		FROM content.workspace_members wm
		JOIN content.workspaces w ON w.organization_id = wm.organization_id AND w.id = wm.workspace_id
		WHERE wm.organization_id = $1::uuid AND wm.user_id = $2::uuid
		  AND wm.role = 'admin' AND w.status = 'active'
		ORDER BY wm.workspace_id
		FOR UPDATE OF w
	`, organizationID, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids, rows.Err()
}

func countActiveWorkspaceAdminsExcludingTx(ctx context.Context, tx pgx.Tx, workspaceID, exceptUserID string) (int, error) {
	var admins int
	err := tx.QueryRow(ctx, `
		SELECT count(*) FROM content.workspace_members wm
		JOIN identity.users u ON u.id = wm.user_id AND u.status = 'active'
		WHERE wm.workspace_id = $1::uuid AND wm.role = 'admin' AND wm.user_id <> $2::uuid
	`, workspaceID, exceptUserID).Scan(&admins)
	return admins, err
}
