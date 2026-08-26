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
	ID               string    `json:"id"`
	AgentUserID      string    `json:"agent_user_id"`
	AgentDisplayName string    `json:"agent_display_name"`
	AgentStatus      string    `json:"agent_status"`
	ModelEndpointID  string    `json:"model_endpoint_id"`
	ModelEndpointName string   `json:"model_endpoint_name"`
	ModelRevision    int64     `json:"model_endpoint_revision"`
	ProviderType     string    `json:"provider_type"`
	ModelName        string    `json:"model_name"`
	RuntimeMode      string    `json:"runtime_mode"`
	WorkflowKey      string    `json:"workflow_key,omitempty"`
	Name             string    `json:"name"`
	Status           string    `json:"status"`
	Capabilities     []string  `json:"capabilities"`
	APIKeyActive     bool      `json:"api_key_active"`
	Ready            bool      `json:"ready"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`
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
	return result, nil
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
		&item.Name,
		&item.Status,
		&capabilities,
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
	return item, nil
}
