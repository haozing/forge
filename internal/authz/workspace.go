package authz

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"agentchunzhi/internal/auth"
	"agentchunzhi/internal/store"

	"github.com/jackc/pgx/v5"
)

var (
	ErrWorkspaceForbidden = errors.New("workspace action forbidden")
	ErrWorkspaceNotFound  = errors.New("workspace scope not found")
)

type Scope struct {
	WorkspaceID     string   `json:"workspace_id"`
	ResourceModelID string   `json:"resource_model_id,omitempty"`
	Role            string   `json:"role"`
	AllowedActions  []string `json:"allowed_actions"`
}

// WorkspacePolicy is the single authorization boundary for member and agent
// operations that carry a workspace. Transport handlers should not duplicate
// role checks or infer permissions from organization-level membership.
type WorkspacePolicy interface {
	Require(ctx context.Context, principal auth.Principal, workspaceID, resourceModelID, action string) (Scope, error)
}

type WorkspacePolicyService struct {
	Store *store.Store
}

func (p WorkspacePolicyService) Require(ctx context.Context, principal auth.Principal, workspaceID, resourceModelID, action string) (Scope, error) {
	if p.Store == nil || p.Store.Pool == nil {
		return Scope{}, errors.New("database store is not initialized")
	}
	workspaceID = strings.TrimSpace(workspaceID)
	resourceModelID = strings.TrimSpace(resourceModelID)
	action = strings.TrimSpace(action)
	if workspaceID == "" || action == "" {
		return Scope{}, ErrWorkspaceNotFound
	}
	if principal.UserType == "member" {
		var role string
		err := p.Store.Pool.QueryRow(ctx, `
			SELECT wm.role
			FROM content.workspace_members wm
			JOIN content.workspaces w ON w.organization_id = wm.organization_id AND w.id = wm.workspace_id
			WHERE wm.organization_id = $1::uuid AND wm.workspace_id = $2::uuid AND wm.user_id = $3::uuid
			  AND w.status = 'active'
		`, principal.OrganizationID, workspaceID, principal.UserID).Scan(&role)
		if errors.Is(err, pgx.ErrNoRows) {
			// Distinguish "workspace does not exist in this organization" (safe
			// to surface as 404 upstream) from "exists but caller is not a
			// member" (403 without leaking existence).
			var exists bool
			if probeErr := p.Store.Pool.QueryRow(ctx, `
				SELECT EXISTS (
					SELECT 1 FROM content.workspaces
					WHERE organization_id = $1::uuid AND id = $2::uuid AND status = 'active'
				)
			`, principal.OrganizationID, workspaceID).Scan(&exists); probeErr == nil && !exists {
				return Scope{}, ErrWorkspaceNotFound
			}
			return Scope{}, ErrWorkspaceForbidden
		}
		if err != nil {
			return Scope{}, fmt.Errorf("load workspace policy: %w", err)
		}
		allowed := memberActions(role)
		if !containsAction(allowed, action) {
			return Scope{}, ErrWorkspaceForbidden
		}
		return Scope{WorkspaceID: workspaceID, ResourceModelID: resourceModelID, Role: role, AllowedActions: allowed}, nil
	}
	if principal.UserType == "agent" {
		var allowed []string
		err := p.Store.Pool.QueryRow(ctx, `
			SELECT COALESCE(ap.actions, '{}'::text[])
			FROM content.agent_access_policies ap
			WHERE ap.organization_id = $1::uuid AND ap.workspace_id = $2::uuid
			  AND ap.agent_user_id = $3::uuid
			  AND ($4 = '' OR ap.resource_model_id = NULLIF($4, '')::uuid)
			ORDER BY ap.resource_model_id NULLS LAST
			LIMIT 1
		`, principal.OrganizationID, workspaceID, principal.UserID, resourceModelID).Scan(&allowed)
		if errors.Is(err, pgx.ErrNoRows) || (!containsAction(allowed, action) && !containsAction(principal.Capabilities, action)) {
			return Scope{}, ErrWorkspaceForbidden
		}
		return Scope{WorkspaceID: workspaceID, ResourceModelID: resourceModelID, Role: "agent", AllowedActions: allowed}, nil
	}
	return Scope{}, ErrWorkspaceForbidden
}

func memberActions(role string) []string {
	switch role {
	case "owner", "admin":
		return []string{"workspace.read", "workspace.manage", "model.read", "model.manage", "asset.read", "asset.write", "asset.review", "asset.publish", "asset.archive", "container.manage", "conversation.use", "automation.read", "automation.write", "automation.run", "attachment.read", "attachment.write", "stats.read", "audit.read"}
	case "reviewer":
		return []string{"workspace.read", "model.read", "asset.read", "asset.review", "asset.publish", "container.manage", "conversation.use", "attachment.read", "attachment.write", "stats.read"}
	case "editor":
		return []string{"workspace.read", "model.read", "asset.read", "asset.write", "container.manage", "conversation.use", "attachment.read", "attachment.write", "stats.read", "audit.read"}
	case "member":
		return []string{"workspace.read", "model.read", "asset.read", "asset.write", "conversation.use", "attachment.read", "attachment.write", "stats.read"}
	default:
		return []string{"workspace.read", "model.read", "asset.read", "conversation.use", "attachment.read", "stats.read"}
	}
}

func containsAction(values []string, target string) bool {
	for _, value := range values {
		if value == target || value == "system:*" {
			return true
		}
	}
	return false
}
