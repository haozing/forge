package admin

// agent_users_list.go — 管理台"Agent 用户"列表（2026-09-21）：发钥 UI 的
// 数据面。聚合 identity.users(agent) × api_keys × agent_applications ×
// agent_access_policies，一次拉全量（组织内 agent 用户量级小）。

import (
	"context"
	"encoding/json"
	"time"

	"agentchunzhi/internal/auth"
)

type AgentKeyItem struct {
	KeyID        string     `json:"key_id"`
	Name         string     `json:"name"`
	Prefix       string     `json:"prefix"`
	ExpiresAt    *time.Time `json:"expires_at,omitempty"`
	Revoked      bool       `json:"revoked"`
	Capabilities []string   `json:"capabilities"`
}

type AgentPolicyItem struct {
	ResourceModelID string   `json:"resource_model_id"`
	Actions         []string `json:"actions"`
	DataScope       string   `json:"data_scope"`
}

type AgentUserListItem struct {
	AgentUserID     string            `json:"agent_user_id"`
	DisplayName     string            `json:"display_name"`
	Status          string            `json:"status"`
	CreatedAt       time.Time         `json:"created_at"`
	ApplicationName string            `json:"application_name"`
	RuntimeMode     string            `json:"runtime_mode"`
	Keys            []AgentKeyItem    `json:"keys"`
	Policies        []AgentPolicyItem `json:"policies"`
}

type AgentUserListResult struct {
	Items []AgentUserListItem `json:"items"`
}

// ListAgentUsers 枚举本组织的 Agent 用户及其密钥、应用与模型级授权。
func (s Service) ListAgentUsers(ctx context.Context, principal auth.Principal) (AgentUserListResult, error) {
	if principal.UserType != "member" {
		return AgentUserListResult{}, ErrInvalidInput
	}
	if s.Store == nil || s.Store.Pool == nil {
		return AgentUserListResult{}, ErrInvalidInput
	}
	result := AgentUserListResult{Items: []AgentUserListItem{}}
	rows, err := s.Store.Pool.Query(ctx, `
		SELECT u.id::text, u.display_name, u.status, u.created_at,
		       COALESCE(app.name, ''), COALESCE(app.runtime_mode, '')
		FROM identity.users u
		LEFT JOIN integration.agent_applications app
		  ON app.organization_id = u.organization_id AND app.bound_agent_user_id = u.id
		WHERE u.organization_id = $1::uuid AND u.user_type = 'agent'
		ORDER BY u.created_at DESC
	`, principal.OrganizationID)
	if err != nil {
		return AgentUserListResult{}, err
	}
	defer rows.Close()
	index := map[string]int{}
	for rows.Next() {
		var item AgentUserListItem
		if err := rows.Scan(&item.AgentUserID, &item.DisplayName, &item.Status, &item.CreatedAt, &item.ApplicationName, &item.RuntimeMode); err != nil {
			return AgentUserListResult{}, err
		}
		item.Keys = []AgentKeyItem{}
		item.Policies = []AgentPolicyItem{}
		index[item.AgentUserID] = len(result.Items)
		result.Items = append(result.Items, item)
	}
	rows.Close()

	keyRows, err := s.Store.Pool.Query(ctx, `
		SELECT k.user_id::text, k.id::text, k.name, k.key_prefix,
		       COALESCE(k.revoked_at IS NOT NULL, false), k.expires_at, COALESCE(k.capabilities, '[]'::jsonb)
		FROM identity.api_keys k
		JOIN identity.users u ON u.organization_id = k.organization_id AND u.id = k.user_id
		WHERE u.organization_id = $1::uuid AND u.user_type = 'agent'
		ORDER BY k.created_at DESC
	`, principal.OrganizationID)
	if err != nil {
		return AgentUserListResult{}, err
	}
	defer keyRows.Close()
	for keyRows.Next() {
		var userID string
		var item AgentKeyItem
		var capabilities []byte
		if err := keyRows.Scan(&userID, &item.KeyID, &item.Name, &item.Prefix, &item.Revoked, &item.ExpiresAt, &capabilities); err != nil {
			return AgentUserListResult{}, err
		}
		item.Capabilities = []string{}
		_ = jsonUnmarshalStringsInto(capabilities, &item.Capabilities)
		if idx, ok := index[userID]; ok {
			result.Items[idx].Keys = append(result.Items[idx].Keys, item)
		}
	}

	policyRows, err := s.Store.Pool.Query(ctx, `
		SELECT p.agent_user_id::text, p.resource_model_id::text, p.actions, COALESCE(p.draft_scope, '')
		FROM content.agent_access_policies p
		JOIN identity.users u ON u.organization_id = p.organization_id AND u.id = p.agent_user_id
		WHERE p.organization_id = $1::uuid AND u.user_type = 'agent'
		ORDER BY p.resource_model_id
	`, principal.OrganizationID)
	if err != nil {
		return AgentUserListResult{}, err
	}
	defer policyRows.Close()
	for policyRows.Next() {
		var userID string
		var item AgentPolicyItem
		if err := policyRows.Scan(&userID, &item.ResourceModelID, &item.Actions, &item.DataScope); err != nil {
			return AgentUserListResult{}, err
		}
		if idx, ok := index[userID]; ok {
			result.Items[idx].Policies = append(result.Items[idx].Policies, item)
		}
	}
	return result, nil
}

// jsonUnmarshalStringsInto 宽松解析 string 数组（失败返回 false，out 保持原值）。
func jsonUnmarshalStringsInto(raw []byte, out *[]string) bool {
	var parsed []string
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return false
	}
	*out = parsed
	return true
}
