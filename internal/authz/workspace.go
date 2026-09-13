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
// operations that carry a workspace. Transport handlers must not duplicate
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
	if principal.UserType == auth.UserTypeMember {
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
		allowed := MemberRoleActions(role)
		if !containsAction(allowed, action) {
			return Scope{}, ErrWorkspaceForbidden
		}
		return Scope{WorkspaceID: workspaceID, ResourceModelID: resourceModelID, Role: role, AllowedActions: allowed}, nil
	}
	if principal.UserType == auth.UserTypeAgent {
		// 统一方案 A/B：agent 与人共用成员表 —— 必须持有 agent 成员行，
		// 权限 = 角色预设 ± 成员覆写，再减去 human_only 动作（C/I/J）。
		// 能力清单（key/应用）与模型级策略行仍是工具/模型层的附加授予源。
		var role string
		var granted, revoked []string
		err := p.Store.Pool.QueryRow(ctx, `
			SELECT wm.role, wm.granted_actions, wm.revoked_actions
			FROM content.workspace_members wm
			JOIN content.workspaces w ON w.organization_id = wm.organization_id AND w.id = wm.workspace_id
			WHERE wm.organization_id = $1::uuid AND wm.workspace_id = $2::uuid AND wm.user_id = $3::uuid
			  AND wm.principal_type = 'agent' AND w.status = 'active'
		`, principal.OrganizationID, workspaceID, principal.UserID).Scan(&role, &granted, &revoked)
		if errors.Is(err, pgx.ErrNoRows) {
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
			return Scope{}, fmt.Errorf("load agent membership: %w", err)
		}
		effective := EffectiveMemberActions(role, granted, revoked, principal.UserType)
		if !containsAction(effective, action) {
			// human_only 或预设/覆写未授予：单一答案来源，直接拒绝。
			return Scope{}, ErrWorkspaceForbidden
		}
		// 模型级策略行（workspace 级优先于 org 级）与能力清单作为附加授予源
		// ——保留既有 C11 语义；角色基线不满足时上面已经拒绝。
		var policyActions []string
		if resourceModelID != "" {
			_ = p.Store.Pool.QueryRow(ctx, `
				SELECT COALESCE(ap.actions, '{}'::text[])
				FROM content.agent_access_policies ap
				WHERE ap.organization_id = $1::uuid
				  AND (ap.workspace_id = $2::uuid OR ap.workspace_id IS NULL)
				  AND ap.agent_user_id = $3::uuid
				  AND ap.resource_model_id = NULLIF($4, '')::uuid
				ORDER BY ap.workspace_id NULLS LAST
				LIMIT 1
			`, principal.OrganizationID, workspaceID, principal.UserID, resourceModelID).Scan(&policyActions)
		}
		if !containsAction(policyActions, action) && !containsAction(principal.Capabilities, action) {
			return Scope{}, ErrWorkspaceForbidden
		}
		return Scope{WorkspaceID: workspaceID, ResourceModelID: resourceModelID, Role: role, AllowedActions: effective}, nil
	}
	return Scope{}, ErrWorkspaceForbidden
}

func containsAction(values []string, target string) bool {
	for _, value := range values {
		if value == target || value == "system:*" {
			return true
		}
	}
	return false
}
