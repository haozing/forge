package admin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"agentchunzhi/internal/auth"

	"github.com/jackc/pgx/v5"
)

var ErrApplicationUpdateInvalidInput = errors.New("invalid agent application update input")

type UpdateAgentApplicationInput struct {
	ApplicationID   string
	Name            *string
	ModelEndpointID *string
	RuntimeMode     *string
	WorkflowKey     *string
	Capabilities    *[]string
	AnswerPosture   *string
	IdempotencyKey  string
}

type UpdateAgentApplicationResult struct {
	ApplicationID   string    `json:"application_id"`
	ModelEndpointID string    `json:"model_endpoint_id"`
	RuntimeMode     string    `json:"runtime_mode"`
	WorkflowKey     string    `json:"workflow_key,omitempty"`
	AnswerPosture   string    `json:"answer_posture"`
	UpdatedAt       time.Time `json:"updated_at"`
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
	var currentCapabilitiesJSON []byte
	err = tx.QueryRow(ctx, `
		SELECT name, model_endpoint_id::text, runtime_mode, COALESCE(workflow_key, ''), capabilities, answer_posture
		FROM integration.agent_applications
		WHERE id = $1::uuid AND organization_id = $2::uuid
		FOR UPDATE
	`, input.ApplicationID, principal.OrganizationID).Scan(
		&currentName, &currentEndpointID, &currentRuntimeMode, &currentWorkflowKey, &currentCapabilitiesJSON, &currentAnswerPosture,
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
		    updated_at = now()
		WHERE id = $1::uuid AND organization_id = $2::uuid
		RETURNING id::text, model_endpoint_id::text, runtime_mode, COALESCE(workflow_key, ''), answer_posture, updated_at
	`, input.ApplicationID, principal.OrganizationID, name, endpointID, runtimeMode, nullableText(workflowKey), string(capabilitiesJSON), answerPosture).Scan(
		&result.ApplicationID, &result.ModelEndpointID, &result.RuntimeMode, &result.WorkflowKey, &result.AnswerPosture, &result.UpdatedAt,
	)
	if err != nil {
		return UpdateAgentApplicationResult{}, fmt.Errorf("update agent application: %w", err)
	}
	metadata, _ := json.Marshal(map[string]any{
		"application_id":             result.ApplicationID,
		"previous_model_endpoint_id": currentEndpointID,
		"model_endpoint_id":          result.ModelEndpointID,
		"previous_runtime_mode":      currentRuntimeMode,
		"runtime_mode":               result.RuntimeMode,
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
	return input.Name != nil || input.ModelEndpointID != nil || input.RuntimeMode != nil || input.WorkflowKey != nil || input.Capabilities != nil || input.AnswerPosture != nil
}

func applicationUpdateRequestHash(input UpdateAgentApplicationInput) (string, error) {
	requestBytes, err := json.Marshal(input)
	if err != nil {
		return "", fmt.Errorf("encode agent application update request: %w", err)
	}
	hash := sha256.Sum256(requestBytes)
	return hex.EncodeToString(hash[:]), nil
}
