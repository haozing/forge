package admin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	runtimetools "agentchunzhi/internal/agentruntime/tools"
	"agentchunzhi/internal/auth"

	"github.com/jackc/pgx/v5"
)

var ErrApplicationUpdateInvalidInput = errors.New("invalid agent application update input")

// ErrKnowledgeBaseNotReady means the target knowledge base (workspace) has no
// active default resource model, so an agent moved there would have nothing
// to retrieve from.
var ErrKnowledgeBaseNotReady = errors.New("knowledge base workspace has no default resource model")

// ToolPolicyPatch is the admin-facing subset of tool_policy the update
// endpoint may change. allow_high_write stays SQL-only by design: it widens
// the approval surface and has no console channel yet.
type ToolPolicyPatch struct {
	AllowedCapabilities *[]string `json:"allowed_capabilities"`
	ApproveLowWrite     *bool     `json:"approve_low_write"`
	// AllowLowWrite toggles the low-write tool class itself (G3/G6/G8 low
	// writes: cover alt, pattern save); without it those tools never run.
	AllowLowWrite *bool `json:"allow_low_write"`
}

type UpdateAgentApplicationInput struct {
	ApplicationID   string
	Name            *string
	ModelEndpointID *string
	RuntimeMode     *string
	WorkflowKey     *string
	Capabilities    *[]string
	AnswerPosture   *string
	ToolPolicy      *ToolPolicyPatch
	// KnowledgeBaseWorkspaceID moves the application to another knowledge
	// base (workspace): disable old enablement rows, enable the target one
	// and replace the agent identity's retrieval grant with the target
	// workspace's default resource model, all in the update transaction.
	KnowledgeBaseWorkspaceID *string
	IdempotencyKey           string
}

type UpdateAgentApplicationResult struct {
	ApplicationID   string `json:"application_id"`
	ModelEndpointID string `json:"model_endpoint_id"`
	RuntimeMode     string `json:"runtime_mode"`
	WorkflowKey     string `json:"workflow_key,omitempty"`
	AnswerPosture   string `json:"answer_posture"`
	// Knowledge base move outcome; empty when the patch did not move the
	// application.
	KnowledgeBaseWorkspaceID         string    `json:"knowledge_base_workspace_id,omitempty"`
	PreviousKnowledgeBaseWorkspaceID string    `json:"previous_knowledge_base_workspace_id,omitempty"`
	UpdatedAt                        time.Time `json:"updated_at"`
}

func (s Service) UpdateAgentApplication(ctx context.Context, principal auth.Principal, input UpdateAgentApplicationInput) (UpdateAgentApplicationResult, error) {
	input.ApplicationID = strings.TrimSpace(input.ApplicationID)
	input.IdempotencyKey = strings.TrimSpace(input.IdempotencyKey)
	if principal.UserType != "member" || !validUUID(input.ApplicationID) || !validIdempotencyKey(input.IdempotencyKey) || !hasApplicationPatch(input) {
		return UpdateAgentApplicationResult{}, ErrApplicationUpdateInvalidInput
	}
	if s.Store == nil || s.Store.Pool == nil {
		return UpdateAgentApplicationResult{}, errors.New("database store is not initialized")
	}

	requestHash, err := applicationUpdateRequestHash(input)
	if err != nil {
		return UpdateAgentApplicationResult{}, err
	}
	tx, err := s.Store.Pool.Begin(ctx)
	if err != nil {
		return UpdateAgentApplicationResult{}, fmt.Errorf("begin agent application update: %w", err)
	}
	defer tx.Rollback(ctx)
	reserved, err := tx.Exec(ctx, `
		INSERT INTO system.idempotency_keys
			(organization_id, subject_id, operation, idempotency_key, request_hash, expires_at)
		VALUES ($1::uuid, $2::uuid, 'agent.application.update', $3, $4, now() + interval '24 hours')
		ON CONFLICT (organization_id, subject_id, operation, idempotency_key) DO NOTHING
	`, principal.OrganizationID, principal.UserID, input.IdempotencyKey, requestHash)
	if err != nil {
		return UpdateAgentApplicationResult{}, fmt.Errorf("reserve agent application update idempotency: %w", err)
	}
	if reserved.RowsAffected() != 1 {
		return UpdateAgentApplicationResult{}, ErrConflict
	}

	var currentName, currentEndpointID, currentRuntimeMode, currentWorkflowKey, currentAnswerPosture string
	var currentAgentUserID string
	var currentCapabilitiesJSON []byte
	var currentToolPolicyJSON []byte
	err = tx.QueryRow(ctx, `
		SELECT name, bound_agent_user_id::text, model_endpoint_id::text, runtime_mode, COALESCE(workflow_key, ''), capabilities, answer_posture, COALESCE(tool_policy, '{}'::jsonb)
		FROM integration.agent_applications
		WHERE id = $1::uuid AND organization_id = $2::uuid
		FOR UPDATE
	`, input.ApplicationID, principal.OrganizationID).Scan(
		&currentName, &currentAgentUserID, &currentEndpointID, &currentRuntimeMode, &currentWorkflowKey, &currentCapabilitiesJSON, &currentAnswerPosture, &currentToolPolicyJSON,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return UpdateAgentApplicationResult{}, ErrApplicationNotFound
	}
	if err != nil {
		return UpdateAgentApplicationResult{}, fmt.Errorf("load agent application for update: %w", err)
	}

	name := currentName
	if input.Name != nil {
		name = strings.TrimSpace(*input.Name)
		if !validText(name, 1, 200) {
			return UpdateAgentApplicationResult{}, ErrApplicationUpdateInvalidInput
		}
	}
	endpointID := currentEndpointID
	if input.ModelEndpointID != nil {
		endpointID = strings.TrimSpace(*input.ModelEndpointID)
		if !validUUID(endpointID) {
			return UpdateAgentApplicationResult{}, ErrApplicationUpdateInvalidInput
		}
	}
	runtimeMode := currentRuntimeMode
	if input.RuntimeMode != nil {
		runtimeMode = strings.ToLower(strings.TrimSpace(*input.RuntimeMode))
	}
	workflowKey := currentWorkflowKey
	if input.WorkflowKey != nil {
		workflowKey = strings.TrimSpace(*input.WorkflowKey)
	}
	if input.RuntimeMode != nil && runtimeMode != "workflow" && input.WorkflowKey == nil {
		workflowKey = ""
	}
	if !validRuntimeMode(runtimeMode, workflowKey) {
		return UpdateAgentApplicationResult{}, ErrApplicationUpdateInvalidInput
	}

	answerPosture := currentAnswerPosture
	if input.AnswerPosture != nil {
		answerPosture = strings.TrimSpace(*input.AnswerPosture)
		if !validAnswerPosture(answerPosture) {
			return UpdateAgentApplicationResult{}, ErrApplicationUpdateInvalidInput
		}
	}

	capabilities := make([]string, 0)
	if err := json.Unmarshal(currentCapabilitiesJSON, &capabilities); err != nil {
		return UpdateAgentApplicationResult{}, fmt.Errorf("decode current agent capabilities: %w", err)
	}
	if input.Capabilities != nil {
		if !validCapabilities(*input.Capabilities) {
			return UpdateAgentApplicationResult{}, ErrApplicationUpdateInvalidInput
		}
		capabilities = normalizeCapabilities(*input.Capabilities)
	}
	capabilitiesJSON, err := json.Marshal(capabilities)
	if err != nil {
		return UpdateAgentApplicationResult{}, fmt.Errorf("encode agent application capabilities: %w", err)
	}

	// tool_policy patch: merge into the stored JSON object so keys managed
	// outside this channel (allow_high_write) survive untouched. Capability
	// values must come from the closed builtin vocabulary.
	toolPolicy := make(map[string]any)
	if len(currentToolPolicyJSON) > 0 {
		if err := json.Unmarshal(currentToolPolicyJSON, &toolPolicy); err != nil {
			return UpdateAgentApplicationResult{}, fmt.Errorf("decode current tool policy: %w", err)
		}
	}
	if input.ToolPolicy != nil {
		if input.ToolPolicy.AllowedCapabilities != nil {
			known := make(map[string]bool)
			for _, capability := range runtimetools.KnownCapabilities() {
				known[capability] = true
			}
			normalized := make([]string, 0, len(*input.ToolPolicy.AllowedCapabilities))
			seen := make(map[string]bool)
			for _, capability := range *input.ToolPolicy.AllowedCapabilities {
				capability = strings.TrimSpace(capability)
				if !known[capability] || seen[capability] {
					return UpdateAgentApplicationResult{}, ErrApplicationUpdateInvalidInput
				}
				seen[capability] = true
				normalized = append(normalized, capability)
			}
			sort.Strings(normalized)
			toolPolicy["allowed_capabilities"] = normalized
		}
		if input.ToolPolicy.ApproveLowWrite != nil {
			toolPolicy["approve_low_write"] = *input.ToolPolicy.ApproveLowWrite
		}
		if input.ToolPolicy.AllowLowWrite != nil {
			toolPolicy["allow_low_write"] = *input.ToolPolicy.AllowLowWrite
		}
	}
	toolPolicyJSON, err := json.Marshal(toolPolicy)
	if err != nil {
		return UpdateAgentApplicationResult{}, fmt.Errorf("encode agent application tool policy: %w", err)
	}
	if err := requireModelEndpointForRuntime(ctx, tx, principal.OrganizationID, endpointID, runtimeMode); err != nil {
		if errors.Is(err, ErrInvalidInput) {
			return UpdateAgentApplicationResult{}, ErrApplicationUpdateInvalidInput
		}
		return UpdateAgentApplicationResult{}, err
	}

	var result UpdateAgentApplicationResult
	err = tx.QueryRow(ctx, `
		UPDATE integration.agent_applications
		SET name = $3,
		    model_endpoint_id = $4::uuid,
		    runtime_mode = $5,
		    workflow_key = $6,
		    capabilities = $7::jsonb,
		    answer_posture = $8,
		    tool_policy = $9::jsonb,
		    updated_at = now()
		WHERE id = $1::uuid AND organization_id = $2::uuid
		RETURNING id::text, model_endpoint_id::text, runtime_mode, COALESCE(workflow_key, ''), answer_posture, updated_at
	`, input.ApplicationID, principal.OrganizationID, name, endpointID, runtimeMode, nullableText(workflowKey), string(capabilitiesJSON), answerPosture, string(toolPolicyJSON)).Scan(
		&result.ApplicationID, &result.ModelEndpointID, &result.RuntimeMode, &result.WorkflowKey, &result.AnswerPosture, &result.UpdatedAt,
	)
	if err != nil {
		return UpdateAgentApplicationResult{}, fmt.Errorf("update agent application: %w", err)
	}

	// Knowledge base move (direction A): the enablement row decides which
	// workspace's surfaces the application serves, the access policy decides
	// what its bound identity may retrieve. Both must flip together or the
	// agent would chat in one knowledge base while reading another's content.
	if input.KnowledgeBaseWorkspaceID != nil {
		targetWorkspace := strings.TrimSpace(*input.KnowledgeBaseWorkspaceID)
		if !validUUID(targetWorkspace) {
			return UpdateAgentApplicationResult{}, ErrApplicationUpdateInvalidInput
		}
		var defaultModelID string
		err = tx.QueryRow(ctx, `
			SELECT COALESCE(default_resource_model_id::text, '')
			FROM content.workspaces
			WHERE organization_id = $1::uuid AND id = $2::uuid
		`, principal.OrganizationID, targetWorkspace).Scan(&defaultModelID)
		if errors.Is(err, pgx.ErrNoRows) {
			return UpdateAgentApplicationResult{}, ErrApplicationUpdateInvalidInput
		}
		if err != nil {
			return UpdateAgentApplicationResult{}, fmt.Errorf("load knowledge base workspace: %w", err)
		}
		if defaultModelID == "" {
			return UpdateAgentApplicationResult{}, ErrKnowledgeBaseNotReady
		}
		var previousKnowledgeWorkspace string
		_ = tx.QueryRow(ctx, `
			SELECT COALESCE(workspace_id::text, '')
			FROM content.workspace_agent_applications
			WHERE organization_id = $1::uuid AND agent_application_id = $2::uuid AND enabled = true
			ORDER BY created_at
			LIMIT 1
		`, principal.OrganizationID, input.ApplicationID).Scan(&previousKnowledgeWorkspace)
		if _, err = tx.Exec(ctx, `
			UPDATE content.workspace_agent_applications
			SET enabled = false
			WHERE organization_id = $1::uuid AND agent_application_id = $2::uuid AND workspace_id <> $3::uuid
		`, principal.OrganizationID, input.ApplicationID, targetWorkspace); err != nil {
			return UpdateAgentApplicationResult{}, fmt.Errorf("disable previous knowledge base enablement: %w", err)
		}
		if _, err = tx.Exec(ctx, `
			INSERT INTO content.workspace_agent_applications (organization_id, workspace_id, agent_application_id, created_by)
			VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid)
			ON CONFLICT (workspace_id, agent_application_id) DO UPDATE SET enabled = true
		`, principal.OrganizationID, targetWorkspace, input.ApplicationID, principal.UserID); err != nil {
			return UpdateAgentApplicationResult{}, fmt.Errorf("enable target knowledge base: %w", err)
		}
		if _, err = tx.Exec(ctx, `
			DELETE FROM content.agent_access_policies
			WHERE organization_id = $1::uuid AND agent_user_id = $2::uuid
		`, principal.OrganizationID, currentAgentUserID); err != nil {
			return UpdateAgentApplicationResult{}, fmt.Errorf("clear agent knowledge grants: %w", err)
		}
		if _, err = tx.Exec(ctx, `
			INSERT INTO content.agent_access_policies
				(organization_id, workspace_id, agent_user_id, resource_model_id, actions, created_by)
			SELECT w.organization_id, w.id, $2::uuid, w.default_resource_model_id, ARRAY['read', 'query.execute']::text[], $4::uuid
			FROM content.workspaces w
			WHERE w.organization_id = $1::uuid AND w.id = $3::uuid
			  AND w.default_resource_model_id IS NOT NULL
		`, principal.OrganizationID, currentAgentUserID, targetWorkspace, principal.UserID); err != nil {
			return UpdateAgentApplicationResult{}, fmt.Errorf("grant target knowledge base scope: %w", err)
		}
		if _, err = tx.Exec(ctx, `
			INSERT INTO "authorization".policy_revisions (organization_id, revision, updated_at)
			VALUES ($1::uuid, 2, now())
			ON CONFLICT (organization_id) DO UPDATE
			SET revision = "authorization".policy_revisions.revision + 1, updated_at = now()
		`, principal.OrganizationID); err != nil {
			return UpdateAgentApplicationResult{}, fmt.Errorf("bump policy revision after knowledge base move: %w", err)
		}
		result.KnowledgeBaseWorkspaceID = targetWorkspace
		result.PreviousKnowledgeBaseWorkspaceID = previousKnowledgeWorkspace
	}
	metadata, _ := json.Marshal(map[string]any{
		"application_id":             result.ApplicationID,
		"previous_model_endpoint_id": currentEndpointID,
		"model_endpoint_id":          result.ModelEndpointID,
		"previous_runtime_mode":      currentRuntimeMode,
		"runtime_mode":               result.RuntimeMode,
		"previous_tool_policy":       json.RawMessage(currentToolPolicyJSON),
		"tool_policy":                json.RawMessage(toolPolicyJSON),
		"previous_knowledge_base":    result.PreviousKnowledgeBaseWorkspaceID,
		"knowledge_base":             result.KnowledgeBaseWorkspaceID,
	})
	if _, err := tx.Exec(ctx, `
		INSERT INTO audit.audit_log
			(organization_id, actor_user_id, initiator_user_id, agent_application_id,
			 action, resource_type, resource_id, result, metadata)
		VALUES ($1::uuid, $2::uuid, $2::uuid, $3::uuid,
			 'agent.application.update', 'agent_application', $3::uuid, 'allowed', $4::jsonb)
	`, principal.OrganizationID, principal.UserID, result.ApplicationID, string(metadata)); err != nil {
		return UpdateAgentApplicationResult{}, fmt.Errorf("record agent application update audit: %w", err)
	}
	responseBytes, _ := json.Marshal(result)
	if _, err := tx.Exec(ctx, `
		UPDATE system.idempotency_keys
		SET response_status = 200, response_body = $5::jsonb
		WHERE organization_id = $1::uuid AND subject_id = $2::uuid
		  AND operation = 'agent.application.update' AND idempotency_key = $3 AND request_hash = $4
	`, principal.OrganizationID, principal.UserID, input.IdempotencyKey, requestHash, string(responseBytes)); err != nil {
		return UpdateAgentApplicationResult{}, fmt.Errorf("save agent application update idempotency: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return UpdateAgentApplicationResult{}, fmt.Errorf("commit agent application update: %w", err)
	}
	return result, nil
}

func hasApplicationPatch(input UpdateAgentApplicationInput) bool {
	return input.Name != nil || input.ModelEndpointID != nil || input.RuntimeMode != nil || input.WorkflowKey != nil || input.Capabilities != nil || input.AnswerPosture != nil || input.ToolPolicy != nil || input.KnowledgeBaseWorkspaceID != nil
}

func applicationUpdateRequestHash(input UpdateAgentApplicationInput) (string, error) {
	requestBytes, err := json.Marshal(input)
	if err != nil {
		return "", fmt.Errorf("encode agent application update request: %w", err)
	}
	hash := sha256.Sum256(requestBytes)
	return hex.EncodeToString(hash[:]), nil
}
