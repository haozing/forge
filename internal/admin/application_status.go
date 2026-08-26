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

var ErrApplicationStatusInvalidInput = errors.New("invalid agent application status input")

type SetApplicationStatusInput struct {
	ApplicationID  string
	Status         string
	IdempotencyKey string
}

type ApplicationStatusResult struct {
	ApplicationID string    `json:"application_id"`
	Status        string    `json:"status"`
	UpdatedAt     time.Time `json:"updated_at"`
}

func (s Service) SetAgentApplicationStatus(ctx context.Context, principal auth.Principal, input SetApplicationStatusInput) (ApplicationStatusResult, error) {
	input.ApplicationID = strings.TrimSpace(input.ApplicationID)
	input.Status = strings.ToLower(strings.TrimSpace(input.Status))
	input.IdempotencyKey = strings.TrimSpace(input.IdempotencyKey)
	if principal.UserType != "member" || !validUUID(input.ApplicationID) || !validApplicationStatus(input.Status) || !validIdempotencyKey(input.IdempotencyKey) {
		return ApplicationStatusResult{}, ErrApplicationStatusInvalidInput
	}
	if s.Store == nil || s.Store.Pool == nil {
		return ApplicationStatusResult{}, errors.New("database store is not initialized")
	}
	tx, err := s.Store.Pool.Begin(ctx)
	if err != nil {
		return ApplicationStatusResult{}, fmt.Errorf("begin agent application status update: %w", err)
	}
	defer tx.Rollback(ctx)
	requestBytes, _ := json.Marshal(struct {
		ApplicationID string `json:"application_id"`
		Status        string `json:"status"`
	}{input.ApplicationID, input.Status})
	hash := sha256.Sum256(requestBytes)
	requestHash := hex.EncodeToString(hash[:])
	reserved, err := tx.Exec(ctx, `
		INSERT INTO system.idempotency_keys
			(organization_id, subject_id, operation, idempotency_key, request_hash, expires_at)
		VALUES ($1::uuid, $2::uuid, 'agent.application.status', $3, $4, now() + interval '24 hours')
		ON CONFLICT (organization_id, subject_id, operation, idempotency_key) DO NOTHING
	`, principal.OrganizationID, principal.UserID, input.IdempotencyKey, requestHash)
	if err != nil {
		return ApplicationStatusResult{}, fmt.Errorf("reserve agent application status idempotency: %w", err)
	}
	if reserved.RowsAffected() != 1 {
		return ApplicationStatusResult{}, ErrConflict
	}
	var previousStatus, modelEndpointID, runtimeMode string
	err = tx.QueryRow(ctx, `
		SELECT aa.status, aa.model_endpoint_id::text, aa.runtime_mode
		FROM integration.agent_applications aa
		JOIN identity.users au ON au.id = aa.bound_agent_user_id
		WHERE aa.id = $1::uuid
		  AND aa.organization_id = $2::uuid
		  AND au.organization_id = aa.organization_id
		FOR UPDATE OF aa
	`, input.ApplicationID, principal.OrganizationID).Scan(&previousStatus, &modelEndpointID, &runtimeMode)
	if errors.Is(err, pgx.ErrNoRows) {
		return ApplicationStatusResult{}, ErrApplicationNotFound
	}
	if err != nil {
		return ApplicationStatusResult{}, fmt.Errorf("load agent application for status update: %w", err)
	}
	if input.Status == "active" {
		if err := requireModelEndpointForRuntime(ctx, tx, principal.OrganizationID, modelEndpointID, runtimeMode); err != nil {
			if errors.Is(err, ErrInvalidInput) {
				return ApplicationStatusResult{}, ErrApplicationStatusInvalidInput
			}
			return ApplicationStatusResult{}, err
		}
	}
	var result ApplicationStatusResult
	err = tx.QueryRow(ctx, `
		UPDATE integration.agent_applications
		SET status = $3, updated_at = now()
		WHERE id = $1::uuid AND organization_id = $2::uuid
		RETURNING id::text, status, updated_at
	`, input.ApplicationID, principal.OrganizationID, input.Status).Scan(&result.ApplicationID, &result.Status, &result.UpdatedAt)
	if err != nil {
		return ApplicationStatusResult{}, fmt.Errorf("update agent application status: %w", err)
	}
	metadata, _ := json.Marshal(map[string]string{
		"application_id":  input.ApplicationID,
		"previous_status": previousStatus,
		"status":          input.Status,
	})
	if _, err := tx.Exec(ctx, `
		INSERT INTO audit.audit_log
			(organization_id, actor_user_id, initiator_user_id, agent_application_id,
			 action, resource_type, resource_id, result, metadata)
		VALUES ($1::uuid, $2::uuid, $2::uuid, $3::uuid,
			 'agent.application.status', 'agent_application', $3::uuid, 'allowed', $4::jsonb)
	`, principal.OrganizationID, principal.UserID, input.ApplicationID, string(metadata)); err != nil {
		return ApplicationStatusResult{}, fmt.Errorf("record agent application status audit: %w", err)
	}
	responseBytes, _ := json.Marshal(result)
	if _, err := tx.Exec(ctx, `
		UPDATE system.idempotency_keys
		SET response_status = 200, response_body = $5::jsonb
		WHERE organization_id = $1::uuid AND subject_id = $2::uuid
		  AND operation = 'agent.application.status' AND idempotency_key = $3 AND request_hash = $4
	`, principal.OrganizationID, principal.UserID, input.IdempotencyKey, requestHash, string(responseBytes)); err != nil {
		return ApplicationStatusResult{}, fmt.Errorf("save agent application status idempotency: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return ApplicationStatusResult{}, fmt.Errorf("commit agent application status: %w", err)
	}
	return result, nil
}

func validApplicationStatus(value string) bool {
	return value == "active" || value == "disabled"
}
