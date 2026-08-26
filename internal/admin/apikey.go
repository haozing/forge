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

var ErrAgentNotFound = errors.New("agent user not found")

type RotateAPIKeyInput struct {
	AgentUserID    string
	Name           string
	ExpiresAt      *time.Time
	IdempotencyKey string
}

type RotateAPIKeyResult struct {
	AgentUserID  string     `json:"agent_user_id"`
	Name         string     `json:"name"`
	ApiKey       string     `json:"api_key"`
	ApiKeyPrefix string     `json:"api_key_prefix"`
	ExpiresAt    *time.Time `json:"expires_at,omitempty"`
}

// RotateAgentAPIKey revokes all currently active keys for an Agent user and
// creates one replacement key. The plaintext replacement is returned only
// from this call; idempotency records contain metadata, never the key.
func (s Service) RotateAgentAPIKey(ctx context.Context, principal auth.Principal, input RotateAPIKeyInput) (RotateAPIKeyResult, error) {
	input.AgentUserID = strings.TrimSpace(input.AgentUserID)
	input.Name = strings.TrimSpace(input.Name)
	input.IdempotencyKey = strings.TrimSpace(input.IdempotencyKey)
	if principal.UserType != "member" || !validUUID(input.AgentUserID) || !validText(input.Name, 1, 100) || !validIdempotencyKey(input.IdempotencyKey) {
		return RotateAPIKeyResult{}, ErrInvalidInput
	}
	if !validExpiry(input.ExpiresAt) {
		return RotateAPIKeyResult{}, ErrInvalidInput
	}
	if s.Store == nil || s.Store.Pool == nil {
		return RotateAPIKeyResult{}, errors.New("database store is not initialized")
	}
	rawKey, err := newAPIKey()
	if err != nil {
		return RotateAPIKeyResult{}, fmt.Errorf("generate rotated agent api key: %w", err)
	}
	keyPrefix := rawKey[:12]
	tx, err := s.Store.Pool.Begin(ctx)
	if err != nil {
		return RotateAPIKeyResult{}, fmt.Errorf("begin agent api key rotation: %w", err)
	}
	defer tx.Rollback(ctx)

	requestBytes, _ := json.Marshal(struct {
		AgentUserID string     `json:"agent_user_id"`
		Name        string     `json:"name"`
		ExpiresAt   *time.Time `json:"expires_at,omitempty"`
	}{input.AgentUserID, input.Name, input.ExpiresAt})
	hash := sha256.Sum256(requestBytes)
	requestHash := hex.EncodeToString(hash[:])
	reserved, err := tx.Exec(ctx, `
		INSERT INTO system.idempotency_keys
			(organization_id, subject_id, operation, idempotency_key, request_hash, expires_at)
		VALUES ($1::uuid, $2::uuid, 'agent.api_key.rotate', $3, $4, now() + interval '24 hours')
		ON CONFLICT (organization_id, subject_id, operation, idempotency_key) DO NOTHING
	`, principal.OrganizationID, principal.UserID, input.IdempotencyKey, requestHash)
	if err != nil {
		return RotateAPIKeyResult{}, fmt.Errorf("reserve agent api key rotation idempotency: %w", err)
	}
	if reserved.RowsAffected() != 1 {
		return RotateAPIKeyResult{}, ErrConflict
	}

	var found string
	var capabilitiesJSON []byte
	err = tx.QueryRow(ctx, `
		SELECT u.id::text, COALESCE((
			SELECT k.capabilities FROM identity.api_keys k
			WHERE k.user_id = u.id AND k.status = 'active'
			ORDER BY k.created_at DESC LIMIT 1
		), '[]'::jsonb)
		FROM identity.users u
		WHERE u.id = $1::uuid AND u.organization_id = $2::uuid
		  AND u.user_type = 'agent' AND u.status = 'active'
	`, input.AgentUserID, principal.OrganizationID).Scan(&found, &capabilitiesJSON)
	if errors.Is(err, pgx.ErrNoRows) {
		return RotateAPIKeyResult{}, ErrAgentNotFound
	}
	if err != nil {
		return RotateAPIKeyResult{}, fmt.Errorf("load agent user for api key rotation: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE identity.api_keys k
		SET status = 'revoked', revoked_at = now()
		FROM identity.users u
		WHERE k.user_id = u.id
		  AND u.id = $1::uuid AND u.organization_id = $2::uuid
		  AND k.status = 'active'
	`, input.AgentUserID, principal.OrganizationID); err != nil {
		return RotateAPIKeyResult{}, fmt.Errorf("revoke previous agent api keys: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO identity.api_keys (user_id, name, key_prefix, key_hash, expires_at, capabilities)
		VALUES ($1::uuid, $2, $3, $4, $5, $6::jsonb)
	`, input.AgentUserID, input.Name, keyPrefix, auth.HashAPIKey(rawKey), input.ExpiresAt, string(capabilitiesJSON)); err != nil {
		return RotateAPIKeyResult{}, fmt.Errorf("create rotated agent api key: %w", err)
	}

	result := RotateAPIKeyResult{
		AgentUserID:  input.AgentUserID,
		Name:         input.Name,
		ApiKey:       rawKey,
		ApiKeyPrefix: keyPrefix,
		ExpiresAt:    input.ExpiresAt,
	}
	metadataBytes, _ := json.Marshal(map[string]any{
		"agent_user_id": input.AgentUserID,
		"key_prefix":    keyPrefix,
		"expires_at":    input.ExpiresAt,
	})
	if _, err := tx.Exec(ctx, `
		INSERT INTO audit.audit_log
			(organization_id, actor_user_id, initiator_user_id, action, resource_type, resource_id, result, metadata)
		VALUES ($1::uuid, $2::uuid, $2::uuid, 'agent.api_key.rotate', 'agent_user', $3::uuid, 'allowed', $4::jsonb)
	`, principal.OrganizationID, principal.UserID, input.AgentUserID, string(metadataBytes)); err != nil {
		return RotateAPIKeyResult{}, fmt.Errorf("record agent api key rotation audit: %w", err)
	}
	responseBytes, _ := json.Marshal(map[string]any{
		"agent_user_id":  input.AgentUserID,
		"name":           input.Name,
		"api_key_prefix": keyPrefix,
		"expires_at":     input.ExpiresAt,
	})
	if _, err := tx.Exec(ctx, `
		UPDATE system.idempotency_keys
		SET response_status = 200, response_body = $5::jsonb
		WHERE organization_id = $1::uuid AND subject_id = $2::uuid
		  AND operation = 'agent.api_key.rotate' AND idempotency_key = $3 AND request_hash = $4
	`, principal.OrganizationID, principal.UserID, input.IdempotencyKey, requestHash, string(responseBytes)); err != nil {
		return RotateAPIKeyResult{}, fmt.Errorf("save agent api key rotation idempotency: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return RotateAPIKeyResult{}, fmt.Errorf("commit agent api key rotation: %w", err)
	}
	return result, nil
}

func validExpiry(value *time.Time) bool {
	return value == nil || (!value.IsZero() && value.After(time.Now().UTC()))
}
