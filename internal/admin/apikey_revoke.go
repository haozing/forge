package admin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"agentchunzhi/internal/auth"

	"github.com/jackc/pgx/v5"
)

// RevokeAllAPIKeysInput targets one Agent user; no replacement key is issued.
type RevokeAllAPIKeysInput struct {
	AgentUserID    string
	IdempotencyKey string
}

type RevokeAllAPIKeysResult struct {
	AgentUserID  string `json:"agent_user_id"`
	RevokedCount int    `json:"revoked_count"`
}

// RevokeAllAgentAPIKeys flips every active key of an Agent user to revoked
// without creating a replacement, so access dies immediately until an explicit
// rotate or re-registration issues a fresh key. Revocation also works for
// disabled Agent users because it is exactly what disabling should imply.
func (s Service) RevokeAllAgentAPIKeys(ctx context.Context, principal auth.Principal, input RevokeAllAPIKeysInput) (RevokeAllAPIKeysResult, error) {
	input.AgentUserID = strings.TrimSpace(input.AgentUserID)
	input.IdempotencyKey = strings.TrimSpace(input.IdempotencyKey)
	if principal.UserType != "member" || !validUUID(input.AgentUserID) || !validIdempotencyKey(input.IdempotencyKey) {
		return RevokeAllAPIKeysResult{}, ErrInvalidInput
	}
	if s.Store == nil || s.Store.Pool == nil {
		return RevokeAllAPIKeysResult{}, errors.New("database store is not initialized")
	}
	tx, err := s.Store.Pool.Begin(ctx)
	if err != nil {
		return RevokeAllAPIKeysResult{}, fmt.Errorf("begin agent api key revocation: %w", err)
	}
	defer tx.Rollback(ctx)
	requestBytes, _ := json.Marshal(struct {
		AgentUserID string `json:"agent_user_id"`
	}{input.AgentUserID})
	hash := sha256.Sum256(requestBytes)
	requestHash := hex.EncodeToString(hash[:])
	reserved, err := tx.Exec(ctx, `
		INSERT INTO system.idempotency_keys
			(organization_id, subject_id, operation, idempotency_key, request_hash, expires_at)
		VALUES ($1::uuid, $2::uuid, 'agent.api_key.revoke_all', $3, $4, now() + interval '24 hours')
		ON CONFLICT (organization_id, subject_id, operation, idempotency_key) DO NOTHING
	`, principal.OrganizationID, principal.UserID, input.IdempotencyKey, requestHash)
	if err != nil {
		return RevokeAllAPIKeysResult{}, fmt.Errorf("reserve agent api key revocation idempotency: %w", err)
	}
	if reserved.RowsAffected() != 1 {
		return RevokeAllAPIKeysResult{}, ErrConflict
	}
	var found string
	err = tx.QueryRow(ctx, `
		SELECT u.id::text
		FROM identity.users u
		WHERE u.id = $1::uuid AND u.organization_id = $2::uuid AND u.user_type = 'agent'
	`, input.AgentUserID, principal.OrganizationID).Scan(&found)
	if errors.Is(err, pgx.ErrNoRows) {
		return RevokeAllAPIKeysResult{}, ErrAgentNotFound
	}
	if err != nil {
		return RevokeAllAPIKeysResult{}, fmt.Errorf("load agent user for api key revocation: %w", err)
	}
	result := RevokeAllAPIKeysResult{AgentUserID: input.AgentUserID}
	if err := tx.QueryRow(ctx, `
		WITH revoked AS (
			UPDATE identity.api_keys k
			SET status = 'revoked', revoked_at = now()
			FROM identity.users u
			WHERE k.user_id = u.id
			  AND u.id = $1::uuid AND u.organization_id = $2::uuid
			  AND k.status = 'active'
			RETURNING k.id
		)
		SELECT count(*) FROM revoked
	`, input.AgentUserID, principal.OrganizationID).Scan(&result.RevokedCount); err != nil {
		return RevokeAllAPIKeysResult{}, fmt.Errorf("revoke agent api keys: %w", err)
	}
	metadataBytes, _ := json.Marshal(map[string]any{
		"agent_user_id": input.AgentUserID,
		"revoked_count": result.RevokedCount,
	})
	if _, err := tx.Exec(ctx, `
		INSERT INTO audit.audit_log
			(organization_id, actor_user_id, initiator_user_id, action, resource_type, resource_id, result, metadata)
		VALUES ($1::uuid, $2::uuid, $2::uuid, 'agent.api_key.revoke_all', 'agent_user', $3::uuid, 'allowed', $4::jsonb)
	`, principal.OrganizationID, principal.UserID, input.AgentUserID, string(metadataBytes)); err != nil {
		return RevokeAllAPIKeysResult{}, fmt.Errorf("record agent api key revocation audit: %w", err)
	}
	responseBytes, _ := json.Marshal(result)
	if _, err := tx.Exec(ctx, `
		UPDATE system.idempotency_keys
		SET response_status = 200, response_body = $5::jsonb
		WHERE organization_id = $1::uuid AND subject_id = $2::uuid
		  AND operation = 'agent.api_key.revoke_all' AND idempotency_key = $3 AND request_hash = $4
	`, principal.OrganizationID, principal.UserID, input.IdempotencyKey, requestHash, string(responseBytes)); err != nil {
		return RevokeAllAPIKeysResult{}, fmt.Errorf("save agent api key revocation idempotency: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return RevokeAllAPIKeysResult{}, fmt.Errorf("commit agent api key revocation: %w", err)
	}
	return result, nil
}
