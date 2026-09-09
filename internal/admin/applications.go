package admin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"agentchunzhi/internal/auth"

	"github.com/jackc/pgx/v5"
)

var (
	ErrApplicationListInvalidInput = errors.New("invalid agent application list input")
	ErrApplicationNotFound         = errors.New("agent application not found")
)

type AgentApplicationSummary struct {
	ID                string                `json:"id"`
	AgentUserID       string                `json:"agent_user_id"`
	AgentDisplayName  string                `json:"agent_display_name"`
	AgentStatus       string                `json:"agent_status"`
	ModelEndpointID   string                `json:"model_endpoint_id"`
	ModelEndpointName string                `json:"model_endpoint_name"`
	ModelRevision     int64                 `json:"model_endpoint_revision"`
	ProviderType      string                `json:"provider_type"`
	ModelName         string                `json:"model_name"`
	RuntimeMode       string                `json:"runtime_mode"`
	WorkflowKey       string                `json:"workflow_key,omitempty"`
	AnswerPosture     string                `json:"answer_posture"`
	Name              string                `json:"name"`
	Status            string                `json:"status"`
	Capabilities      []string              `json:"capabilities"`
	ToolPolicy        any                   `json:"tool_policy,omitempty"`
	KnowledgeScopes   []AgentKnowledgeScope `json:"knowledge_scopes"`
	APIKeyActive      bool                  `json:"api_key_active"`
	Ready             bool                  `json:"ready"`
	CreatedAt         time.Time             `json:"created_at"`
	UpdatedAt         time.Time             `json:"updated_at"`
}

// AgentKnowledgeScope is one retrieval grant of the bound agent identity:
// the knowledge base (workspace) it lives in and the resource model it can
// query. Direction A exposes the scope read-only here; editing moves the
// whole application via PATCH knowledge_base_workspace_id.
type AgentKnowledgeScope struct {
	WorkspaceID       string `json:"workspace_id"`
	WorkspaceName     string `json:"workspace_name"`
	ResourceModelID   string `json:"resource_model_id"`
	ResourceModelName string `json:"resource_model_name"`
	DataScope         string `json:"data_scope"`
}

type AgentApplicationList struct {
	Items []AgentApplicationSummary `json:"items"`
	Limit int                       `json:"limit"`
}

func (s Service) ListAgentApplications(ctx context.Context, principal auth.Principal, limit int) (AgentApplicationList, error) {
	if principal.UserType != "member" || limit < 1 || limit > 100 {
		return AgentApplicationList{}, ErrApplicationListInvalidInput
	}
	if s.Store == nil || s.Store.Pool == nil {
		return AgentApplicationList{}, errors.New("database store is not initialized")
	}
	rows, err := s.Store.Pool.Query(ctx, `
		SELECT aa.id::text,
		       aa.bound_agent_user_id::text,
		       au.display_name,
		       au.status,
		       aa.model_endpoint_id::text,
		       me.name,
		       me.current_revision,
		       mer.provider_type,
		       mer.model_name,
		       aa.runtime_mode,
		       COALESCE(aa.workflow_key, ''),
		       aa.answer_posture,
		       aa.name,
		       aa.status,
		       aa.capabilities,
		       EXISTS (
		           SELECT 1
		           FROM identity.api_keys ak
		           WHERE ak.user_id = aa.bound_agent_user_id
		             AND ak.status = 'active'
		             AND (ak.expires_at IS NULL OR ak.expires_at > now())
		       ) AS api_key_active,
		       (aa.status = 'active' AND au.status = 'active' AND me.status = 'active' AND EXISTS (
		           SELECT 1
		           FROM identity.api_keys ak_ready
		           WHERE ak_ready.user_id = aa.bound_agent_user_id
		             AND ak_ready.status = 'active'
		             AND (ak_ready.expires_at IS NULL OR ak_ready.expires_at > now())
		       )) AS ready,
		       aa.created_at,
		       aa.updated_at
		FROM integration.agent_applications aa
		JOIN identity.users au ON au.id = aa.bound_agent_user_id
		JOIN integration.model_endpoints me ON me.id = aa.model_endpoint_id
		JOIN integration.model_endpoint_revisions mer
		  ON mer.model_endpoint_id = me.id AND mer.revision = me.current_revision
		WHERE aa.organization_id = $1::uuid
		  AND au.organization_id = aa.organization_id
		ORDER BY aa.created_at DESC, aa.id DESC
		LIMIT $2
	`, principal.OrganizationID, limit)
	if err != nil {
		return AgentApplicationList{}, fmt.Errorf("list agent applications: %w", err)
	}
	defer rows.Close()
	result := AgentApplicationList{Items: make([]AgentApplicationSummary, 0), Limit: limit}
	for rows.Next() {
		var item AgentApplicationSummary
		var capabilities []byte
		if err := rows.Scan(
			&item.ID,
			&item.AgentUserID,
			&item.AgentDisplayName,
			&item.AgentStatus,
			&item.ModelEndpointID,
			&item.ModelEndpointName,
			&item.ModelRevision,
			&item.ProviderType,
			&item.ModelName,
			&item.RuntimeMode,
			&item.WorkflowKey,
			&item.AnswerPosture,
			&item.Name,
			&item.Status,
			&capabilities,
			&item.APIKeyActive,
			&item.Ready,
			&item.CreatedAt,
			&item.UpdatedAt,
		); err != nil {
			return AgentApplicationList{}, fmt.Errorf("scan agent application: %w", err)
		}
		item.Capabilities = []string{}
		if len(capabilities) > 0 {
			if err := json.Unmarshal(capabilities, &item.Capabilities); err != nil {
				return AgentApplicationList{}, fmt.Errorf("decode agent application capabilities: %w", err)
			}
		}
		result.Items = append(result.Items, item)
	}
	if err := rows.Err(); err != nil {
		return AgentApplicationList{}, fmt.Errorf("iterate agent applications: %w", err)
	}
	if err := s.attachKnowledgeScopes(ctx, principal.OrganizationID, result.Items); err != nil {
		return AgentApplicationList{}, err
	}
	return result, nil
}

// attachKnowledgeScopes fills KnowledgeScopes for every item from
// content.agent_access_policies, the single source of truth for what a bound
// agent identity may retrieve. One application usually carries exactly one
// scope (its knowledge base), but the table is many-to-many by design, so the
// field is an array and stays honest about reality.
func (s Service) attachKnowledgeScopes(ctx context.Context, organizationID string, items []AgentApplicationSummary) error {
	if len(items) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(items))
	agentUserIDs := make([]string, 0, len(items))
	for _, item := range items {
		if !seen[item.AgentUserID] {
			seen[item.AgentUserID] = true
			agentUserIDs = append(agentUserIDs, item.AgentUserID)
		}
	}
	rows, err := s.Store.Pool.Query(ctx, `
		SELECT p.agent_user_id::text,
		       COALESCE(p.workspace_id::text, ''),
		       COALESCE(w.name, ''),
		       p.resource_model_id::text,
		       COALESCE(rm.name, ''),
		       p.data_scope
		FROM content.agent_access_policies p
		LEFT JOIN content.workspaces w
		  ON w.organization_id = p.organization_id AND w.id = p.workspace_id
		LEFT JOIN model.resource_models rm
		  ON rm.organization_id = p.organization_id AND rm.id = p.resource_model_id
		WHERE p.organization_id = $1::uuid
		  AND p.agent_user_id::text = ANY($2::text[])
		ORDER BY p.created_at, p.id
	`, organizationID, agentUserIDs)
	if err != nil {
		return fmt.Errorf("list agent knowledge scopes: %w", err)
	}
	defer rows.Close()
	scopesByAgent := make(map[string][]AgentKnowledgeScope)
	for rows.Next() {
		var agentUserID string
		var scope AgentKnowledgeScope
		if err := rows.Scan(&agentUserID, &scope.WorkspaceID, &scope.WorkspaceName, &scope.ResourceModelID, &scope.ResourceModelName, &scope.DataScope); err != nil {
			return fmt.Errorf("scan agent knowledge scope: %w", err)
		}
		scopesByAgent[agentUserID] = append(scopesByAgent[agentUserID], scope)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate agent knowledge scopes: %w", err)
	}
	for i := range items {
		items[i].KnowledgeScopes = scopesByAgent[items[i].AgentUserID]
		if items[i].KnowledgeScopes == nil {
			items[i].KnowledgeScopes = []AgentKnowledgeScope{}
		}
	}
	return nil
}

func (s Service) GetAgentApplication(ctx context.Context, principal auth.Principal, applicationID string) (AgentApplicationSummary, error) {
	if principal.UserType != "member" || !validUUID(applicationID) {
		return AgentApplicationSummary{}, ErrApplicationListInvalidInput
	}
	if s.Store == nil || s.Store.Pool == nil {
		return AgentApplicationSummary{}, errors.New("database store is not initialized")
	}
	var item AgentApplicationSummary
	var capabilities []byte
	var toolPolicy []byte
	err := s.Store.Pool.QueryRow(ctx, `
		SELECT aa.id::text,
		       aa.bound_agent_user_id::text,
		       au.display_name,
		       au.status,
		       aa.model_endpoint_id::text,
		       me.name,
		       me.current_revision,
		       mer.provider_type,
		       mer.model_name,
		       aa.runtime_mode,
		       COALESCE(aa.workflow_key, ''),
		       aa.answer_posture,
		       aa.name,
		       aa.status,
		       aa.capabilities,
		       COALESCE(aa.tool_policy, '{}'::jsonb),
		       EXISTS (
		           SELECT 1
		           FROM identity.api_keys ak
		           WHERE ak.user_id = aa.bound_agent_user_id
		             AND ak.status = 'active'
		             AND (ak.expires_at IS NULL OR ak.expires_at > now())
		       ) AS api_key_active,
		       (aa.status = 'active' AND au.status = 'active' AND me.status = 'active' AND EXISTS (
		           SELECT 1
		           FROM identity.api_keys ak_ready
		           WHERE ak_ready.user_id = aa.bound_agent_user_id
		             AND ak_ready.status = 'active'
		             AND (ak_ready.expires_at IS NULL OR ak_ready.expires_at > now())
		       )) AS ready,
		       aa.created_at,
		       aa.updated_at
		FROM integration.agent_applications aa
		JOIN identity.users au ON au.id = aa.bound_agent_user_id
		JOIN integration.model_endpoints me ON me.id = aa.model_endpoint_id
		JOIN integration.model_endpoint_revisions mer
		  ON mer.model_endpoint_id = me.id AND mer.revision = me.current_revision
		WHERE aa.organization_id = $1::uuid
		  AND au.organization_id = aa.organization_id
		  AND aa.id = $2::uuid
	`, principal.OrganizationID, applicationID).Scan(
		&item.ID,
		&item.AgentUserID,
		&item.AgentDisplayName,
		&item.AgentStatus,
		&item.ModelEndpointID,
		&item.ModelEndpointName,
		&item.ModelRevision,
		&item.ProviderType,
		&item.ModelName,
		&item.RuntimeMode,
		&item.WorkflowKey,
		&item.AnswerPosture,
		&item.Name,
		&item.Status,
		&capabilities,
		&toolPolicy,
		&item.APIKeyActive,
		&item.Ready,
		&item.CreatedAt,
		&item.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return AgentApplicationSummary{}, ErrApplicationNotFound
	}
	if err != nil {
		return AgentApplicationSummary{}, fmt.Errorf("get agent application: %w", err)
	}
	item.Capabilities = []string{}
	if len(capabilities) > 0 {
		if err := json.Unmarshal(capabilities, &item.Capabilities); err != nil {
			return AgentApplicationSummary{}, fmt.Errorf("decode agent application capabilities: %w", err)
		}
	}
	item.ToolPolicy = map[string]any{}
	if len(toolPolicy) > 0 {
		decoded := map[string]any{}
		if err := json.Unmarshal(toolPolicy, &decoded); err != nil {
			return AgentApplicationSummary{}, fmt.Errorf("decode agent application tool policy: %w", err)
		}
		item.ToolPolicy = decoded
	}
	items := []AgentApplicationSummary{item}
	if err := s.attachKnowledgeScopes(ctx, principal.OrganizationID, items); err != nil {
		return AgentApplicationSummary{}, err
	}
	return items[0], nil
}
