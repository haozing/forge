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
	if principal.UserType != "member" || action != "agent.manage" {
		return []string{}, nil
	}
	var admin bool
	err := r.Store.Pool.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM identity.users
		WHERE id = $1::uuid AND organization_id = $2::uuid AND user_type = 'member'
		  AND status = 'active' AND member_role = 'admin')
	`, principal.UserID, principal.OrganizationID).Scan(&admin)
	if err != nil {
		return nil, fmt.Errorf("resolve organization administrator: %w", err)
	}
	if !admin {
		return []string{}, nil
	}
	return []string{"system:agent-users", "system:agent-applications"}, nil
}

func (r ScopeResolver) AllowedModelIDs(ctx context.Context, principal auth.Principal, action string) ([]string, error) {
	if r.Store == nil || r.Store.Pool == nil {
		return nil, fmt.Errorf("database store is not initialized")
	}
	if principal.UserType == "agent" {
		action = strings.TrimPrefix(strings.TrimSpace(action), "asset.")
		rows, err := r.Store.Pool.Query(ctx, `
			SELECT DISTINCT resource_model_id::text FROM content.agent_access_policies
			WHERE organization_id = $1::uuid AND agent_user_id = $2::uuid
			  AND workspace_id IS NULL AND $3 = ANY(actions)
		`, principal.OrganizationID, principal.UserID, action)
		if err != nil {
			return nil, fmt.Errorf("resolve agent access policy: %w", err)
		}
		return collectIDs(rows)
	}
	if principal.UserType != "member" {
		return []string{}, nil
	}
	rows, err := r.Store.Pool.Query(ctx, `
		SELECT DISTINCT rm.id::text
		FROM model.resource_models rm
		JOIN identity.users u ON u.id = $2::uuid AND u.organization_id = rm.organization_id
		LEFT JOIN content.workspaces w ON w.organization_id = rm.organization_id
		 AND (w.id = rm.workspace_id OR w.default_resource_model_id = rm.id)
		LEFT JOIN content.workspace_members wm ON wm.workspace_id = w.id AND wm.user_id = u.id
		WHERE rm.organization_id = $1::uuid AND rm.status = 'active'
		  AND u.user_type = 'member' AND u.status = 'active'
		  AND (u.member_role = 'admin' OR wm.user_id IS NOT NULL)
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
	if principal.UserType != "member" || action != "agent.use" {
		return []string{}, nil
	}
	rows, err := r.Store.Pool.Query(ctx, `
		SELECT DISTINCT wa.agent_application_id::text
		FROM content.workspace_agent_applications wa
		JOIN content.workspace_members wm ON wm.workspace_id = wa.workspace_id AND wm.user_id = $2::uuid
		JOIN integration.agent_applications aa ON aa.id = wa.agent_application_id
		WHERE wa.organization_id = $1::uuid AND wa.enabled = true AND aa.status = 'active'
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
