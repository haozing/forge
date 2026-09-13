package authz

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"agentchunzhi/internal/auth"
	"agentchunzhi/internal/store"
)

type ScopeResolver struct{ Store *store.Store }

func (r ScopeResolver) AllowedSystemResourceIDs(ctx context.Context, principal auth.Principal, action string) ([]string, error) {
	if r.Store == nil || r.Store.Pool == nil {
		return nil, fmt.Errorf("database store is not initialized")
	}
	if principal.UserType != auth.UserTypeMember || action != "agent.manage" {
		return []string{}, nil
	}
	var admin bool
	err := r.Store.Pool.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM identity.users
		WHERE id = $1::uuid AND organization_id = $2::uuid AND user_type = 'member'
		  AND status = 'active' AND organization_role = 'admin')
	`, principal.UserID, principal.OrganizationID).Scan(&admin)
	if err != nil {
		return nil, fmt.Errorf("resolve organization administrator: %w", err)
	}
	if !admin {
		return []string{}, nil
	}
	return []string{"system:agent-users", "system:agent-applications"}, nil
}

// AllowedModelIDs resolves the model scope for agent technical identities and
// workspace members. Organization-level governance grants nothing here: a
// member sees models bound to workspaces where they hold an explicit
// membership. Builtin models carry a NULL workspace and follow the caller's
// workspace reach.
func (r ScopeResolver) AllowedModelIDs(ctx context.Context, principal auth.Principal, action string) ([]string, error) {
	if r.Store == nil || r.Store.Pool == nil {
		return nil, fmt.Errorf("database store is not initialized")
	}
	if principal.UserType == auth.UserTypeAgent {
		// A4：human_only 动作（如 asset.publish）对 agent 永远解析出空范围，
		// 与角色/覆写/能力无关。
		if HumanOnlyAction(strings.TrimSpace(action)) {
			return []string{}, nil
		}
		// A4：无成员行即无权限——create/update/insert 走本路径而非
		// WorkspacePolicy.Require，成员基线在这里补判。
		var member bool
		if err := r.Store.Pool.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1 FROM content.workspace_members wm
				JOIN content.workspaces w ON w.organization_id = wm.organization_id AND w.id = wm.workspace_id
				WHERE wm.organization_id = $1::uuid AND wm.user_id = $2::uuid
				  AND wm.principal_type = 'agent' AND w.status = 'active'
			)
		`, principal.OrganizationID, principal.UserID).Scan(&member); err != nil {
			return nil, fmt.Errorf("resolve agent membership: %w", err)
		}
		if !member {
			return []string{}, nil
		}
		action = strings.TrimPrefix(strings.TrimSpace(action), "asset.")
		// Workspace-scoped grants (the ones workspace agent-app registration
		// writes) and org-wide grants both count; the retrieval funnel then
		// intersects them with the channel policy in ForAgent.
		rows, err := r.Store.Pool.Query(ctx, `
			SELECT DISTINCT resource_model_id::text FROM content.agent_access_policies
			WHERE organization_id = $1::uuid AND agent_user_id = $2::uuid
			  AND $3 = ANY(actions)
		`, principal.OrganizationID, principal.UserID, action)
		if err != nil {
			return nil, fmt.Errorf("resolve agent access policy: %w", err)
		}
		return collectIDs(rows)
	}
	if principal.UserType != auth.UserTypeMember {
		return []string{}, nil
	}
	rows, err := r.Store.Pool.Query(ctx, `
		SELECT DISTINCT rm.id::text
		FROM model.resource_models rm
		JOIN identity.users u ON u.id = $2::uuid AND u.organization_id = rm.organization_id
		LEFT JOIN content.workspaces w ON w.organization_id = rm.organization_id
		 AND (w.id = rm.workspace_id OR w.default_resource_model_id = rm.id)
		JOIN content.workspace_members wm ON wm.workspace_id = w.id AND wm.user_id = u.id
		WHERE rm.organization_id = $1::uuid AND rm.status = 'active'
		  AND u.user_type = 'member' AND u.status = 'active'
	`, principal.OrganizationID, principal.UserID)
	if err != nil {
		return nil, fmt.Errorf("resolve member model scope: %w", err)
	}
	return collectIDs(rows)
}

func (r ScopeResolver) AllowedAgentApplicationIDs(ctx context.Context, principal auth.Principal, action string) ([]string, error) {
	if r.Store == nil || r.Store.Pool == nil {
		return nil, fmt.Errorf("database store is not initialized")
	}
	if principal.UserType != auth.UserTypeMember || action != "agent.use" {
		return []string{}, nil
	}
	rows, err := r.Store.Pool.Query(ctx, `
		SELECT DISTINCT aa.id::text
		FROM content.workspace_members wm_me
		JOIN content.workspace_members wm_agent
		  ON wm_agent.workspace_id = wm_me.workspace_id AND wm_agent.principal_type = 'agent'
		JOIN integration.agent_applications aa
		  ON aa.organization_id = wm_me.organization_id AND aa.bound_agent_user_id = wm_agent.user_id
		WHERE wm_me.organization_id = $1::uuid AND wm_me.user_id = $2::uuid AND aa.status = 'active'
	`, principal.OrganizationID, principal.UserID)
	if err != nil {
		return nil, fmt.Errorf("resolve member agent applications: %w", err)
	}
	return collectIDs(rows)
}

type idRows interface {
	Next() bool
	Scan(...any) error
	Err() error
	Close()
}

func collectIDs(rows idRows) ([]string, error) {
	defer rows.Close()
	ids := make([]string, 0)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan permission id: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate permission ids: %w", err)
	}
	sort.Strings(ids)
	return ids, nil
}
