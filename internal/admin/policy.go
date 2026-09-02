package admin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"agentchunzhi/internal/auth"

	"github.com/jackc/pgx/v5"
)

var ErrPolicyNotFound = errors.New("agent or resource model not found")

type ReplacePolicyInput struct {
	AgentUserID     string
	ResourceModelID string
	Actions         []string
	DataScope       string
	IdempotencyKey  string
}

type PolicyResult struct {
	AgentUserID     string   `json:"agent_user_id"`
	ResourceModelID string   `json:"resource_model_id"`
	Actions         []string `json:"actions"`
	DataScope       string   `json:"data_scope"`
}

var supportedAgentActions = map[string]struct{}{
	"read":    {},
	"create":  {},
	"edit":    {},
	"publish": {},
	"archive": {},
}

var supportedAgentDataScopes = map[string]struct{}{
	"public":       {},
	"organization": {},
	"workspace":    {},
}

func (s Service) ReplaceAgentModelPolicy(ctx context.Context, principal auth.Principal, input ReplacePolicyInput) (PolicyResult, error) {
	input.AgentUserID = strings.TrimSpace(input.AgentUserID)
	input.ResourceModelID = strings.TrimSpace(input.ResourceModelID)
	input.IdempotencyKey = strings.TrimSpace(input.IdempotencyKey)
	input.DataScope = strings.TrimSpace(input.DataScope)
	if input.DataScope == "" {
		// 0017 default: the pre-existing organization band.
		input.DataScope = "organization"
	}
	actions, ok := normalizeActions(input.Actions)
	if principal.UserType != "member" || !validUUID(input.AgentUserID) || !validUUID(input.ResourceModelID) || !ok || !validIdempotencyKey(input.IdempotencyKey) {
		return PolicyResult{}, ErrInvalidInput
	}
	if _, known := supportedAgentDataScopes[input.DataScope]; !known {
		return PolicyResult{}, ErrInvalidInput
	}
	if s.Store == nil || s.Store.Pool == nil {
		return PolicyResult{}, errors.New("database store is not initialized")
	}
	tx, err := s.Store.Pool.Begin(ctx)
	if err != nil {
		return PolicyResult{}, fmt.Errorf("begin agent policy update: %w", err)
	}
	defer tx.Rollback(ctx)
	requestBytes, _ := json.Marshal(struct {
		AgentUserID     string   `json:"agent_user_id"`
		ResourceModelID string   `json:"resource_model_id"`
		Actions         []string `json:"actions"`
		DataScope       string   `json:"data_scope"`
	}{input.AgentUserID, input.ResourceModelID, actions, input.DataScope})
	hash := sha256.Sum256(requestBytes)
	requestHash := hex.EncodeToString(hash[:])
	reserved, err := tx.Exec(ctx, `
		INSERT INTO system.idempotency_keys
			(organization_id, subject_id, operation, idempotency_key, request_hash, expires_at)
		VALUES ($1::uuid, $2::uuid, 'agent.policy.replace', $3, $4, now() + interval '24 hours')
		ON CONFLICT (organization_id, subject_id, operation, idempotency_key) DO NOTHING
	`, principal.OrganizationID, principal.UserID, input.IdempotencyKey, requestHash)
	if err != nil {
		return PolicyResult{}, fmt.Errorf("reserve agent policy idempotency: %w", err)
	}
	if reserved.RowsAffected() != 1 {
		return PolicyResult{}, ErrConflict
	}
	var found string
	err = tx.QueryRow(ctx, `
		SELECT u.id::text
		FROM identity.users u
		WHERE u.id = $1::uuid AND u.organization_id = $2::uuid
		  AND u.user_type = 'agent' AND u.status = 'active'
	`, input.AgentUserID, principal.OrganizationID).Scan(&found)
	if errors.Is(err, pgx.ErrNoRows) {
		return PolicyResult{}, ErrPolicyNotFound
	}
	if err != nil {
		return PolicyResult{}, fmt.Errorf("load agent user for policy: %w", err)
	}
	err = tx.QueryRow(ctx, `
		SELECT id::text
		FROM model.resource_models
		WHERE id = $1::uuid AND organization_id = $2::uuid AND status = 'active'
	`, input.ResourceModelID, principal.OrganizationID).Scan(&found)
	if errors.Is(err, pgx.ErrNoRows) {
		return PolicyResult{}, ErrPolicyNotFound
	}
	if err != nil {
		return PolicyResult{}, fmt.Errorf("load resource model for policy: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		DELETE FROM content.agent_access_policies
		WHERE organization_id = $1::uuid AND agent_user_id = $2::uuid
		  AND resource_model_id = $3::uuid AND workspace_id IS NULL
	`, principal.OrganizationID, input.AgentUserID, input.ResourceModelID); err != nil {
		return PolicyResult{}, fmt.Errorf("clear agent model policy: %w", err)
	}
	if len(actions) > 0 {
		if _, err := tx.Exec(ctx, `
			INSERT INTO content.agent_access_policies
				(organization_id, workspace_id, agent_user_id, resource_model_id, actions, data_scope, created_by)
			VALUES ($1::uuid, NULL, $2::uuid, $3::uuid, $4::text[], $5, $6::uuid)
		`, principal.OrganizationID, input.AgentUserID, input.ResourceModelID, actions, input.DataScope, principal.UserID); err != nil {
			return PolicyResult{}, fmt.Errorf("grant agent model policy: %w", err)
		}
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO "authorization".policy_revisions (organization_id, revision, updated_at)
		VALUES ($1::uuid, 2, now())
		ON CONFLICT (organization_id) DO UPDATE
		SET revision = "authorization".policy_revisions.revision + 1,
		    updated_at = now()
	`, principal.OrganizationID); err != nil {
		return PolicyResult{}, fmt.Errorf("bump authorization policy revision: %w", err)
	}
	result := PolicyResult{AgentUserID: input.AgentUserID, ResourceModelID: input.ResourceModelID, Actions: actions, DataScope: input.DataScope}
	responseBytes, _ := json.Marshal(result)
	metadataBytes, _ := json.Marshal(result)
	if _, err := tx.Exec(ctx, `
		INSERT INTO audit.audit_log
			(organization_id, actor_user_id, initiator_user_id, action, resource_type, resource_id, result, metadata)
		VALUES ($1::uuid, $2::uuid, $2::uuid, 'agent.policy.replace', 'resource_model', $3::uuid, 'allowed', $4::jsonb)
	`, principal.OrganizationID, principal.UserID, input.ResourceModelID, string(metadataBytes)); err != nil {
		return PolicyResult{}, fmt.Errorf("record agent policy audit: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE system.idempotency_keys
		SET response_status = 200, response_body = $5::jsonb
		WHERE organization_id = $1::uuid AND subject_id = $2::uuid
		  AND operation = 'agent.policy.replace' AND idempotency_key = $3 AND request_hash = $4
	`, principal.OrganizationID, principal.UserID, input.IdempotencyKey, requestHash, string(responseBytes)); err != nil {
		return PolicyResult{}, fmt.Errorf("save agent policy idempotency: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return PolicyResult{}, fmt.Errorf("commit agent policy update: %w", err)
	}
	return result, nil
}

func normalizeActions(values []string) ([]string, bool) {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if _, ok := supportedAgentActions[value]; !ok {
			return nil, false
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Strings(result)
	return result, true
}

func validUUID(value string) bool {
	return uuidPattern.MatchString(value)
}

var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[1-5][0-9a-fA-F]{3}-[89abAB][0-9a-fA-F]{3}-[0-9a-fA-F]{12}$`)
