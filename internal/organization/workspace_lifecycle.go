package organization

// workspace_lifecycle.go — organization-governed workspace lifecycle:
// create (org admin only, creator receives explicit admin membership),
// archive (revokes workspace-scope invitations, cancels unsent deliveries,
// clears default workspace preferences) and restore (requires an active
// admin membership). Workspace admin membership management endpoints share
// the same transaction pattern.

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"agentchunzhi/internal/auth"
	"agentchunzhi/internal/authz"
	"agentchunzhi/internal/eventing"
	"agentchunzhi/internal/store"

	"github.com/jackc/pgx/v5"
)

type Workspace struct {
	ID          string `json:"id"`
	Slug        string `json:"slug"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Status      string `json:"status"`
	Revision    int64  `json:"revision"`
	CreatedBy   string `json:"created_by"`
	ETag        string `json:"etag"`
}

// CreateWorkspace is reserved to organization admins (organization.create
// governance action); content permissions still require explicit membership,
// which the creator receives as workspace admin.
func (s Service) CreateWorkspace(ctx context.Context, principal auth.Principal, name, description, defaultResourceModelID string) (Workspace, error) {
	if err := s.RequireOrganizationAction(ctx, principal, authz.ActionWorkspaceCreate); err != nil {
		return Workspace{}, err
	}
	name = strings.TrimSpace(name)
	if name == "" || len([]rune(name)) > 100 {
		return Workspace{}, ErrInvalidInput
	}
	tx, err := s.Store.Pool.Begin(ctx)
	if err != nil {
		return Workspace{}, err
	}
	defer tx.Rollback(ctx)
	var id, slug string
	err = tx.QueryRow(ctx, `
		INSERT INTO content.workspaces
			(organization_id, slug, name, description, default_resource_model_id, created_by)
		VALUES ($1::uuid, 'ws-' || replace(gen_random_uuid()::text, '-', ''), $2, $3, NULLIF($4, '')::uuid, $5::uuid)
		RETURNING id::text, slug
	`, principal.OrganizationID, name, description, defaultResourceModelID, principal.UserID).Scan(&id, &slug)
	if err != nil {
		return Workspace{}, fmt.Errorf("insert workspace: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO content.workspace_members (organization_id, workspace_id, user_id, role, granted_by)
		VALUES ($1::uuid, $2::uuid, $3::uuid, 'admin', $3::uuid)
	`, principal.OrganizationID, id, principal.UserID); err != nil {
		return Workspace{}, fmt.Errorf("insert creator membership: %w", err)
	}
	workspace := Workspace{ID: id, Slug: slug, Name: name, Description: description, Status: "active", CreatedBy: principal.UserID, ETag: "1"}
	store.AppendAuditTx(ctx, tx, store.NewAuditEntry("workspace.created", principal.OrganizationID, principal.UserID, "workspace", id, map[string]any{"workspace_id": id}), id)
	if err := appendOrgEvent(ctx, tx, s.Events, principal, id, eventing.EventWorkspaceCreated, id, 1, map[string]any{"workspace_id": id}); err != nil {
		return Workspace{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Workspace{}, err
	}
	return workspace, nil
}

// ArchiveWorkspace revokes workspace-scope invitations, cancels their unsent
// deliveries and clears member default-workspace preferences.
func (s Service) ArchiveWorkspace(ctx context.Context, principal auth.Principal, workspaceID string) (Workspace, error) {
	if err := s.RequireOrganizationAction(ctx, principal, authz.ActionWorkspaceArchive); err != nil {
		return Workspace{}, err
	}
	if !s.validID(workspaceID) {
		return Workspace{}, ErrInvalidInput
	}
	tx, err := s.Store.Pool.Begin(ctx)
	if err != nil {
		return Workspace{}, err
	}
	defer tx.Rollback(ctx)
	if err := lockOrganizationTx(ctx, tx, principal.OrganizationID); err != nil {
		return Workspace{}, err
	}
	var status string
	var revision int64
	err = tx.QueryRow(ctx, `
		SELECT status, revision FROM content.workspaces
		WHERE organization_id = $1::uuid AND id = $2::uuid FOR UPDATE
	`, principal.OrganizationID, workspaceID).Scan(&status, &revision)
	if errors.Is(err, pgx.ErrNoRows) {
		return Workspace{}, ErrNotFound
	}
	if err != nil {
		return Workspace{}, err
	}
	if status == "archived" {
		return Workspace{}, ErrConflict
	}
	if _, err := tx.Exec(ctx, `
		UPDATE content.workspaces
		SET status = 'archived', archived_at = now(), archived_by = $3::uuid, revision = revision + 1, updated_at = now()
		WHERE organization_id = $1::uuid AND id = $2::uuid
	`, principal.OrganizationID, workspaceID, principal.UserID); err != nil {
		return Workspace{}, err
	}
	// Revoke workspace-scope pending invitations and cancel unsent deliveries.
	rows, err := tx.Query(ctx, `
		UPDATE organization.member_invitations
		SET status = 'revoked', revoked_at = now(), revision = revision + 1, updated_at = now()
		WHERE organization_id = $1::uuid AND scope_workspace_id = $2::uuid AND status = 'pending'
		RETURNING email
	`, principal.OrganizationID, workspaceID)
	if err != nil {
		return Workspace{}, err
	}
	emails := []string{}
	for rows.Next() {
		var email string
		if err := rows.Scan(&email); err != nil {
			rows.Close()
			return Workspace{}, err
		}
		emails = append(emails, email)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return Workspace{}, err
	}
	for _, email := range emails {
		if err := cancelDeliveryTx(ctx, tx, principal.OrganizationID, email); err != nil {
			return Workspace{}, err
		}
	}
	// Clear default-workspace preferences pointing at the archived workspace.
	if _, err := tx.Exec(ctx, `
		UPDATE identity.user_preferences
		SET default_workspace_id = NULL, revision = revision + 1, updated_at = now()
		WHERE default_workspace_id = $2::uuid
		  AND EXISTS (SELECT 1 FROM identity.users u WHERE u.id = user_preferences.user_id AND u.organization_id = $1::uuid)
	`, principal.OrganizationID, workspaceID); err != nil {
		return Workspace{}, err
	}
	store.AppendAuditTx(ctx, tx, store.NewAuditEntry("workspace.archived", principal.OrganizationID, principal.UserID, "workspace", workspaceID, map[string]any{"workspace_id": workspaceID}), workspaceID)
	if err := appendOrgEvent(ctx, tx, s.Events, principal, workspaceID, eventing.EventWorkspaceArchived, workspaceID, revision+1, map[string]any{"workspace_id": workspaceID}); err != nil {
		return Workspace{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Workspace{}, err
	}
	return s.GetWorkspace(ctx, principal, workspaceID)
}

func cancelDeliveryTx(ctx context.Context, tx pgx.Tx, organizationID, email string) error {
	_, err := tx.Exec(ctx, `
		UPDATE notification.email_deliveries
		SET status = 'cancelled', encrypted_payload = '', updated_at = now()
		WHERE organization_id = $1::uuid AND recipient_email = $2 AND status IN ('pending', 'sending')
	`, organizationID, email)
	return err
}

// RestoreWorkspace re-enables an archived workspace; at least one active user
// must already hold an admin membership.
func (s Service) RestoreWorkspace(ctx context.Context, principal auth.Principal, workspaceID string) (Workspace, error) {
	if err := s.RequireOrganizationAction(ctx, principal, authz.ActionWorkspaceRestore); err != nil {
		return Workspace{}, err
	}
	if !s.validID(workspaceID) {
		return Workspace{}, ErrInvalidInput
	}
	tx, err := s.Store.Pool.Begin(ctx)
	if err != nil {
		return Workspace{}, err
	}
	defer tx.Rollback(ctx)
	if err := lockOrganizationTx(ctx, tx, principal.OrganizationID); err != nil {
		return Workspace{}, err
	}
	var status string
	var revision int64
	err = tx.QueryRow(ctx, `
		SELECT status, revision FROM content.workspaces
		WHERE organization_id = $1::uuid AND id = $2::uuid FOR UPDATE
	`, principal.OrganizationID, workspaceID).Scan(&status, &revision)
	if errors.Is(err, pgx.ErrNoRows) {
		return Workspace{}, ErrNotFound
	}
	if err != nil {
		return Workspace{}, err
	}
	if status != "archived" {
		return Workspace{}, ErrConflict
	}
	admins, err := countActiveWorkspaceAdminsExcludingTx(ctx, tx, workspaceID, "")
	if err != nil {
		return Workspace{}, err
	}
	if admins == 0 {
		return Workspace{}, ErrLastWorkspaceAdmin
	}
	if _, err := tx.Exec(ctx, `
		UPDATE content.workspaces
		SET status = 'active', archived_at = NULL, archived_by = NULL, revision = revision + 1, updated_at = now()
		WHERE organization_id = $1::uuid AND id = $2::uuid
	`, principal.OrganizationID, workspaceID); err != nil {
		return Workspace{}, err
	}
	store.AppendAuditTx(ctx, tx, store.NewAuditEntry("workspace.restored", principal.OrganizationID, principal.UserID, "workspace", workspaceID, nil), workspaceID)
	if err := appendOrgEvent(ctx, tx, s.Events, principal, workspaceID, eventing.EventWorkspaceRestored, workspaceID, revision+1, map[string]any{"workspace_id": workspaceID}); err != nil {
		return Workspace{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Workspace{}, err
	}
	return s.GetWorkspace(ctx, principal, workspaceID)
}

// GetWorkspace returns governance metadata (no content permissions implied).
func (s Service) GetWorkspace(ctx context.Context, principal auth.Principal, workspaceID string) (Workspace, error) {
	if err := s.RequireOrganizationAction(ctx, principal, authz.ActionOrganizationManage); err != nil {
		return Workspace{}, err
	}
	if !s.validID(workspaceID) {
		return Workspace{}, ErrInvalidInput
	}
	var item Workspace
	err := s.Store.Pool.QueryRow(ctx, `
		SELECT id::text, slug, name, description, status, revision, created_by::text
		FROM content.workspaces
		WHERE organization_id = $1::uuid AND id = $2::uuid
	`, principal.OrganizationID, workspaceID).Scan(&item.ID, &item.Slug, &item.Name, &item.Description,
		&item.Status, &item.Revision, &item.CreatedBy)
	if errors.Is(err, pgx.ErrNoRows) {
		return Workspace{}, ErrNotFound
	}
	item.ETag = fmt.Sprint(item.Revision)
	return item, err
}

// ListWorkspaces is the organization governance view over all workspaces.
func (s Service) ListWorkspaces(ctx context.Context, principal auth.Principal) ([]Workspace, error) {
	if err := s.RequireOrganizationAction(ctx, principal, authz.ActionOrganizationManage); err != nil {
		return nil, err
	}
	rows, err := s.Store.Pool.Query(ctx, `
		SELECT id::text, slug, name, description, status, revision, created_by::text
		FROM content.workspaces WHERE organization_id = $1::uuid
		ORDER BY created_at DESC, id
	`, principal.OrganizationID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]Workspace, 0)
	for rows.Next() {
		var item Workspace
		if err := rows.Scan(&item.ID, &item.Slug, &item.Name, &item.Description, &item.Status, &item.Revision, &item.CreatedBy); err != nil {
			return nil, err
		}
		item.ETag = fmt.Sprint(item.Revision)
		items = append(items, item)
	}
	return items, rows.Err()
}

// GrantWorkspaceMembership lets an organization admin grant an explicit
// membership without holding one themselves; governance is not a content
// permission.
func (s Service) GrantWorkspaceMembership(ctx context.Context, principal auth.Principal, workspaceID, userID, role string) error {
	if err := s.RequireOrganizationAction(ctx, principal, authz.ActionOrganizationMemberManage); err != nil {
		return err
	}
	if !s.validID(workspaceID) || !s.validID(userID) || !authz.ValidWorkspaceRole(role) {
		return ErrInvalidInput
	}
	tx, err := s.Store.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err := lockOrganizationTx(ctx, tx, principal.OrganizationID); err != nil {
		return err
	}
	commandTag, err := tx.Exec(ctx, `
		INSERT INTO content.workspace_members (organization_id, workspace_id, user_id, role, granted_by)
		SELECT $1::uuid, $2::uuid, u.id, $4, $3::uuid
		FROM identity.users u
		WHERE u.organization_id = $1::uuid AND u.id = $5::uuid AND u.user_type = 'member' AND u.status = 'active'
		  AND EXISTS (SELECT 1 FROM content.workspaces w WHERE w.organization_id = $1::uuid AND w.id = $2::uuid)
		ON CONFLICT (workspace_id, user_id) DO NOTHING
	`, principal.OrganizationID, workspaceID, principal.UserID, role, userID)
	if err != nil {
		return err
	}
	if commandTag.RowsAffected() == 0 {
		return ErrMembershipExists
	}
	store.AppendAuditTx(ctx, tx, store.NewAuditEntry("workspace.member.granted", principal.OrganizationID, principal.UserID, "membership", workspaceID, map[string]any{
		"workspace_id": workspaceID, "user_id": userID, "role": role,
	}), workspaceID)
	if err := appendOrgEvent(ctx, tx, s.Events, principal, workspaceID, eventing.EventWorkspaceMembershipChanged, workspaceID, 1, map[string]any{
		"workspace_id": workspaceID, "user_id": userID, "operation": "granted", "role": role,
	}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// RevokeWorkspaceMembership removes one membership with last-admin
// protection; organization governance may revoke its own membership too.
func (s Service) RevokeWorkspaceMembership(ctx context.Context, principal auth.Principal, workspaceID, membershipID string) error {
	if err := s.RequireOrganizationAction(ctx, principal, authz.ActionOrganizationMemberManage); err != nil {
		return err
	}
	if !s.validID(workspaceID) || !s.validID(membershipID) {
		return ErrInvalidInput
	}
	tx, err := s.Store.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err := lockOrganizationTx(ctx, tx, principal.OrganizationID); err != nil {
		return err
	}
	var role, userID, workspaceStatus string
	err = tx.QueryRow(ctx, `
		SELECT wm.role, wm.user_id::text, w.status
		FROM content.workspace_members wm
		JOIN content.workspaces w ON w.organization_id = wm.organization_id AND w.id = wm.workspace_id
		WHERE wm.organization_id = $1::uuid AND wm.id = $2::uuid AND wm.workspace_id = $3::uuid
		FOR UPDATE OF wm
	`, principal.OrganizationID, membershipID, workspaceID).Scan(&role, &userID, &workspaceStatus)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if role == authz.WorkspaceRoleAdmin && workspaceStatus == "active" {
		admins, err := countActiveWorkspaceAdminsExcludingTx(ctx, tx, workspaceID, userID)
		if err != nil {
			return err
		}
		if admins == 0 {
			return ErrLastWorkspaceAdmin
		}
	}
	commandTag, err := tx.Exec(ctx, `
		DELETE FROM content.workspace_members
		WHERE organization_id = $1::uuid AND id = $2::uuid AND workspace_id = $3::uuid
	`, principal.OrganizationID, membershipID, workspaceID)
	if err != nil {
		return err
	}
	if commandTag.RowsAffected() == 0 {
		return ErrNotFound
	}
	store.AppendAuditTx(ctx, tx, store.NewAuditEntry("workspace.member.revoked", principal.OrganizationID, principal.UserID, "membership", membershipID, map[string]any{
		"workspace_id": workspaceID, "user_id": userID, "old_role": role,
	}), workspaceID)
	if err := appendOrgEvent(ctx, tx, s.Events, principal, workspaceID, eventing.EventWorkspaceMembershipChanged, workspaceID, 1, map[string]any{
		"workspace_id": workspaceID, "user_id": userID, "operation": "revoked", "old_role": role,
	}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// PatchWorkspaceMembership changes one membership's role from the governance
// surface. Organization admins may act without holding a membership; the
// last active admin of an active workspace is protected.
func (s Service) PatchWorkspaceMembership(ctx context.Context, principal auth.Principal, workspaceID, membershipID, role string) (Workspace, string, error) {
	if err := s.RequireOrganizationAction(ctx, principal, authz.ActionOrganizationMemberManage); err != nil {
		return Workspace{}, "", err
	}
	if !s.validID(workspaceID) || !s.validID(membershipID) || !authz.ValidWorkspaceRole(role) {
		return Workspace{}, "", ErrInvalidInput
	}
	tx, err := s.Store.Pool.Begin(ctx)
	if err != nil {
		return Workspace{}, "", err
	}
	defer tx.Rollback(ctx)
	if err := lockOrganizationTx(ctx, tx, principal.OrganizationID); err != nil {
		return Workspace{}, "", err
	}
	var oldRole, userID, workspaceStatus string
	err = tx.QueryRow(ctx, `
		SELECT wm.role, wm.user_id::text, w.status
		FROM content.workspace_members wm
		JOIN content.workspaces w ON w.organization_id = wm.organization_id AND w.id = wm.workspace_id
		WHERE wm.organization_id = $1::uuid AND wm.id = $2::uuid AND wm.workspace_id = $3::uuid
		FOR UPDATE OF wm
	`, principal.OrganizationID, membershipID, workspaceID).Scan(&oldRole, &userID, &workspaceStatus)
	if errors.Is(err, pgx.ErrNoRows) {
		return Workspace{}, "", ErrNotFound
	}
	if err != nil {
		return Workspace{}, "", err
	}
	if oldRole == authz.WorkspaceRoleAdmin && role != authz.WorkspaceRoleAdmin && workspaceStatus == "active" {
		admins, err := countActiveWorkspaceAdminsExcludingTx(ctx, tx, workspaceID, userID)
		if err != nil {
			return Workspace{}, "", err
		}
		if admins == 0 {
			return Workspace{}, "", ErrLastWorkspaceAdmin
		}
	}
	if _, err := tx.Exec(ctx, `
		UPDATE content.workspace_members
		SET role = $3, revision = revision + 1, updated_at = now()
		WHERE organization_id = $1::uuid AND id = $2::uuid
	`, principal.OrganizationID, membershipID, role); err != nil {
		return Workspace{}, "", err
	}
	store.AppendAuditTx(ctx, tx, store.NewAuditEntry("workspace.member.role_changed", principal.OrganizationID, principal.UserID, "membership", membershipID, map[string]any{
		"workspace_id": workspaceID, "user_id": userID, "old_role": oldRole, "new_role": role,
	}), workspaceID)
	if err := appendOrgEvent(ctx, tx, s.Events, principal, workspaceID, eventing.EventWorkspaceMembershipChanged, workspaceID, 1, map[string]any{
		"workspace_id": workspaceID, "user_id": userID, "operation": "role_changed", "old_role": oldRole, "new_role": role,
	}); err != nil {
		return Workspace{}, "", err
	}
	if err := tx.Commit(ctx); err != nil {
		return Workspace{}, "", err
	}
	workspace, err := s.GetWorkspace(ctx, principal, workspaceID)
	if err != nil {
		return Workspace{}, "", err
	}
	return workspace, userID, nil
}
