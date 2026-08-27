package admin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"agentchunzhi/internal/auth"

	"github.com/jackc/pgx/v5"
)

// ErrAgentNotAllowed marks an Agent user that exists but cannot receive an
// onboarding package right now (disabled/revoked), distinct from "not found".
var ErrAgentNotAllowed = errors.New("agent user is not allowed to onboard")

const (
	onboardingOpenAPIPath    = "/openapi-open-v1.yaml"
	onboardingAuthFormatTmpl = "Authorization: Bearer <agent-api-key>"
)

type AgentOnboardingAuth struct {
	Type   string `json:"type"`
	Header string `json:"header"`
	Format string `json:"format"`
}

type AgentOnboardingResourceModel struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	Actions []string `json:"actions"`
}

type AgentAllowedOperation struct {
	Operation string `json:"operation"`
	Allowed   bool   `json:"allowed"`
}

// AgentOnboarding mirrors product doc §6.8.3: everything an external developer
// needs to integrate one Agent user. Structure is always returned, even when
// the Agent currently has no key or no access policy (then every operation is
// reported as denied instead of hiding the object).
type AgentOnboarding struct {
	BaseURL           string                         `json:"base_url"`
	AgentUserID       string                         `json:"agent_user_id"`
	ApiKeyPrefix      *string                        `json:"api_key_prefix,omitempty"`
	Auth              AgentOnboardingAuth            `json:"auth"`
	OpenAPIURL        string                         `json:"openapi_url"`
	RuntimeMode       string                         `json:"runtime_mode"`
	WorkflowKey       string                         `json:"workflow_key,omitempty"`
	Capabilities      []string                       `json:"capabilities"`
	ResourceModels    []AgentOnboardingResourceModel `json:"resource_models"`
	AllowedOperations []AgentAllowedOperation        `json:"allowed_operations"`
	SampleCurl        string                         `json:"sample_curl"`
}

// GetAgentOnboarding assembles the §6.8.3 package for one Agent user owned by
// the caller's organization. baseURL comes from the admin HTTP request so the
// sample stays valid behind proxies.
func (s Service) GetAgentOnboarding(ctx context.Context, principal auth.Principal, agentUserID, baseURL string) (AgentOnboarding, error) {
	agentUserID = strings.TrimSpace(agentUserID)
	if principal.UserType != "member" || !validUUID(agentUserID) {
		return AgentOnboarding{}, ErrInvalidInput
	}
	if s.Store == nil || s.Store.Pool == nil {
		return AgentOnboarding{}, errors.New("database store is not initialized")
	}
	var status string
	err := s.Store.Pool.QueryRow(ctx, `
		SELECT u.id::text, u.status
		FROM identity.users u
		WHERE u.id = $1::uuid AND u.organization_id = $2::uuid AND u.user_type = 'agent'
	`, agentUserID, principal.OrganizationID).Scan(new(string), &status)
	if errors.Is(err, pgx.ErrNoRows) {
		return AgentOnboarding{}, ErrAgentNotFound
	}
	if err != nil {
		return AgentOnboarding{}, fmt.Errorf("load agent user for onboarding: %w", err)
	}
	if status != "active" {
		return AgentOnboarding{}, ErrAgentNotAllowed
	}

	pack := AgentOnboarding{
		BaseURL:     strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		AgentUserID: agentUserID,
		Auth: AgentOnboardingAuth{
			Type:   "Bearer",
			Header: "Authorization",
			Format: onboardingAuthFormatTmpl,
		},
		OpenAPIURL:     onboardingOpenAPIPath,
		RuntimeMode:    "rag",
		Capabilities:   []string{},
		ResourceModels: []AgentOnboardingResourceModel{},
	}
	if pack.BaseURL == "" {
		return AgentOnboarding{}, ErrInvalidInput
	}
	var keyPrefix *string
	if err := s.Store.Pool.QueryRow(ctx, `
		SELECT key_prefix FROM identity.api_keys
		WHERE user_id = $1::uuid AND status = 'active'
		ORDER BY created_at DESC LIMIT 1
	`, agentUserID).Scan(&keyPrefix); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return AgentOnboarding{}, fmt.Errorf("load agent api key prefix for onboarding: %w", err)
	}
	pack.ApiKeyPrefix = keyPrefix

	var workflowKey *string
	var capabilitiesJSON []byte
	err = s.Store.Pool.QueryRow(ctx, `
		SELECT COALESCE(runtime_mode, 'rag'), COALESCE(workflow_key, ''), capabilities
		FROM integration.agent_applications
		WHERE organization_id = $1::uuid AND bound_agent_user_id = $2::uuid
		ORDER BY created_at DESC LIMIT 1
	`, principal.OrganizationID, agentUserID).Scan(&pack.RuntimeMode, &workflowKey, &capabilitiesJSON)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		// Registration always creates an application; fall back to defaults so
		// the contract keeps returning the full structure regardless.
	default:
		if err != nil {
			return AgentOnboarding{}, fmt.Errorf("load agent application for onboarding: %w", err)
		}
		if workflowKey != nil {
			pack.WorkflowKey = *workflowKey
		}
		capabilities := []string{}
		if json.Unmarshal(capabilitiesJSON, &capabilities) == nil {
			sort.Strings(capabilities)
			pack.Capabilities = capabilities
		}
	}

	rows, err := s.Store.Pool.Query(ctx, `
		SELECT rm.id::text, rm.name, p.actions
		FROM content.agent_access_policies p
		JOIN model.resource_models rm
		  ON rm.id = p.resource_model_id AND rm.organization_id = p.organization_id
		WHERE p.organization_id = $1::uuid AND p.agent_user_id = $2::uuid
		  AND rm.status = 'active'
		ORDER BY rm.name, rm.id
	`, principal.OrganizationID, agentUserID)
	if err != nil {
		return AgentOnboarding{}, fmt.Errorf("list agent onboarding resource models: %w", err)
	}
	defer rows.Close()
	var policyRows []onboardingPolicyRow
	for rows.Next() {
		var row onboardingPolicyRow
		if err := rows.Scan(&row.ID, &row.Name, &row.Actions); err != nil {
			return AgentOnboarding{}, err
		}
		policyRows = append(policyRows, row)
	}
	if err := rows.Err(); err != nil {
		return AgentOnboarding{}, fmt.Errorf("iterate agent onboarding resource models: %w", err)
	}
	pack.ResourceModels = mergeOnboardingPolicyRows(policyRows)
	pack.AllowedOperations = deriveOnboardingOperations(pack.Capabilities, pack.ResourceModels)
	pack.SampleCurl = buildOnboardingSampleCurl(pack.BaseURL)
	return pack, nil
}

type onboardingPolicyRow struct {
	ID      string
	Name    string
	Actions []string
}

// mergeOnboardingPolicyRows folds workspace-scoped duplicate grants for the
// same resource model into one entry with a sorted action union.
func mergeOnboardingPolicyRows(rows []onboardingPolicyRow) []AgentOnboardingResourceModel {
	byID := map[string]*AgentOnboardingResourceModel{}
	order := make([]string, 0, len(rows))
	for _, row := range rows {
		item, ok := byID[row.ID]
		if !ok {
			item = &AgentOnboardingResourceModel{ID: row.ID, Name: row.Name, Actions: []string{}}
			byID[row.ID] = item
			order = append(order, row.ID)
		}
		for _, action := range row.Actions {
			action = strings.TrimSpace(action)
			if action == "" || containsString(item.Actions, action) {
				continue
			}
			item.Actions = append(item.Actions, action)
			sort.Strings(item.Actions)
		}
	}
	result := make([]AgentOnboardingResourceModel, 0, len(order))
	for _, id := range order {
		result = append(result, *byID[id])
	}
	sort.SliceStable(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

// onboardingOperationTable lists the open v1 surface with the key capability
// plus AccessPolicy action it requires. Empty PolicyAction means the operation
// relies on the capability alone; automation.callback additionally falls back
// to "the agent still owns at least one grant".
var onboardingOperationTable = []struct {
	Operation    string
	Capability   string
	PolicyAction string
}{
	{"query", "query.read", "read"},
	{"assets.create", "asset.create", "create"},
	{"assets.update", "asset.edit", "edit"},
	{"assets.publish", "asset.publish", "publish"},
	{"assets.archive", "asset.archive", "archive"},
	{"references", "reference.read", "read"},
	{"attachments.download", "reference.read", "read"},
	{"tasks", "agent.run", ""},
}

// deriveOnboardingOperations annotates each catalog operation with
// allowed/denied based on the Agent's current capabilities and access policy.
// An empty policy list yields all-denied while keeping the full structure.
func deriveOnboardingOperations(capabilities []string, models []AgentOnboardingResourceModel) []AgentAllowedOperation {
	grantedCaps := map[string]bool{}
	for _, capability := range capabilities {
		grantedCaps[capability] = true
	}
	grantedActions := map[string]bool{}
	totalActions := 0
	for _, model := range models {
		for _, action := range model.Actions {
			if !grantedActions[action] {
				grantedActions[action] = true
				totalActions++
			}
		}
	}
	operations := make([]AgentAllowedOperation, 0, len(onboardingOperationTable)+1)
	for _, entry := range onboardingOperationTable {
		allowed := grantedCaps[entry.Capability]
		if entry.PolicyAction != "" {
			allowed = allowed && grantedActions[entry.PolicyAction]
		} else {
			// Capability-only operations still require at least one policy
			// grant so a fully revoked agent reports everything as denied.
			allowed = allowed && totalActions > 0
		}
		operations = append(operations, AgentAllowedOperation{Operation: entry.Operation, Allowed: allowed})
	}
	// Automation callbacks ride short-lived run credentials rather than the
	// Agent API key; they remain reachable only while some authorization lives.
	operations = append(operations, AgentAllowedOperation{Operation: "automation.callback", Allowed: totalActions > 0})
	return operations
}

func buildOnboardingSampleCurl(baseURL string) string {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	return fmt.Sprintf(`curl -X POST %s/api/open/v1/query -H "%s" -H "Content-Type: application/json" -d '{"mode":"hybrid","query":"<your-question>","top_k":10}'`, baseURL, onboardingAuthFormatTmpl)
}
