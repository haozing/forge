package admin

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"agentchunzhi/internal/auth"
	"agentchunzhi/internal/modelendpoint"
	"agentchunzhi/internal/store"

	"github.com/jackc/pgx/v5"
)

var (
	ErrInvalidInput = errors.New("invalid agent registration input")
	ErrConflict     = errors.New("agent registration conflict")
)

type Service struct {
	Store *store.Store
}

type RegisterAgentInput struct {
	DisplayName     string
	ApiKeyName      string
	ApplicationName string
	ModelEndpointID string
	RuntimeMode     string
	WorkflowKey     string
	Capabilities    []string
	ExpiresAt       *time.Time
	IdempotencyKey  string
}

type RegisterAgentResult struct {
	AgentUserID        string     `json:"agent_user_id"`
	AgentApplicationID string     `json:"agent_application_id"`
	ApiKey             string     `json:"api_key"`
	ApiKeyPrefix       string     `json:"api_key_prefix"`
	ModelEndpointID    string     `json:"model_endpoint_id"`
	RuntimeMode        string     `json:"runtime_mode"`
	WorkflowKey        string     `json:"workflow_key,omitempty"`
	Capabilities       []string   `json:"capabilities"`
	ExpiresAt          *time.Time `json:"expires_at,omitempty"`
}

// RegisterAgent creates the identity, one API key, and its bound application
// in one transaction. The plaintext key is deliberately returned only here.
func (s Service) RegisterAgent(ctx context.Context, principal auth.Principal, input RegisterAgentInput) (RegisterAgentResult, error) {
	input.DisplayName = strings.TrimSpace(input.DisplayName)
	input.ApiKeyName = strings.TrimSpace(input.ApiKeyName)
	input.ApplicationName = strings.TrimSpace(input.ApplicationName)
	input.ModelEndpointID = strings.TrimSpace(input.ModelEndpointID)
	input.RuntimeMode = strings.TrimSpace(input.RuntimeMode)
	input.WorkflowKey = strings.TrimSpace(input.WorkflowKey)
	input.IdempotencyKey = strings.TrimSpace(input.IdempotencyKey)
	if principal.UserType != "member" || !validInput(input) || !validExpiry(input.ExpiresAt) || !validIdempotencyKey(input.IdempotencyKey) {
		return RegisterAgentResult{}, ErrInvalidInput
	}
	if s.Store == nil || s.Store.Pool == nil {
		return RegisterAgentResult{}, errors.New("database store is not initialized")
	}
	rawKey, err := newAPIKey()
	if err != nil {
		return RegisterAgentResult{}, fmt.Errorf("generate agent api key: %w", err)
	}
	keyPrefix := rawKey[:12]
	capabilities := normalizeCapabilities(input.Capabilities)
	capabilitiesJSON, err := json.Marshal(capabilities)
	if err != nil {
		return RegisterAgentResult{}, fmt.Errorf("encode agent capabilities: %w", err)
	}
	tx, err := s.Store.Pool.Begin(ctx)
	if err != nil {
		return RegisterAgentResult{}, fmt.Errorf("begin agent registration: %w", err)
	}
	defer tx.Rollback(ctx)
	requestBytes, _ := json.Marshal(struct {
		DisplayName     string     `json:"display_name"`
		ApiKeyName      string     `json:"api_key_name"`
		ApplicationName string     `json:"application_name"`
		ModelEndpointID string     `json:"model_endpoint_id"`
		RuntimeMode     string     `json:"runtime_mode"`
		WorkflowKey     string     `json:"workflow_key,omitempty"`
		Capabilities    []string   `json:"capabilities"`
		ExpiresAt       *time.Time `json:"expires_at,omitempty"`
	}{input.DisplayName, input.ApiKeyName, input.ApplicationName, input.ModelEndpointID, input.RuntimeMode, input.WorkflowKey, capabilities, input.ExpiresAt})
	requestHashBytes := sha256.Sum256(requestBytes)
	requestHash := hex.EncodeToString(requestHashBytes[:])
	reserved, err := tx.Exec(ctx, `
		INSERT INTO system.idempotency_keys
			(organization_id, subject_id, operation, idempotency_key, request_hash, expires_at)
		VALUES ($1::uuid, $2::uuid, 'agent.register', $3, $4, now() + interval '24 hours')
		ON CONFLICT (organization_id, subject_id, operation, idempotency_key) DO NOTHING
	`, principal.OrganizationID, principal.UserID, input.IdempotencyKey, requestHash)
	if err != nil {
		return RegisterAgentResult{}, fmt.Errorf("reserve agent registration idempotency: %w", err)
	}
	if reserved.RowsAffected() != 1 {
		return RegisterAgentResult{}, ErrConflict
	}
	if err := requireModelEndpointForRuntime(ctx, tx, principal.OrganizationID, input.ModelEndpointID, input.RuntimeMode); err != nil {
		return RegisterAgentResult{}, err
	}

	var agentUserID, applicationID string
	if err := tx.QueryRow(ctx, `
		INSERT INTO identity.users
			(organization_id, user_type, display_name, status)
		VALUES ($1::uuid, 'agent', $2, 'active')
		RETURNING id::text
	`, principal.OrganizationID, input.DisplayName).Scan(&agentUserID); err != nil {
		return RegisterAgentResult{}, fmt.Errorf("create agent user: %w", err)
	}
	if err := tx.QueryRow(ctx, `
		INSERT INTO identity.api_keys (organization_id, user_id, name, key_prefix, key_hash, expires_at, capabilities)
		VALUES ($1::uuid, $2::uuid, $3, $4, $5, $6, $7::jsonb)
		RETURNING id::text
	`, principal.OrganizationID, agentUserID, input.ApiKeyName, keyPrefix, auth.HashAPIKey(rawKey), input.ExpiresAt, string(capabilitiesJSON)).Scan(new(string)); err != nil {
		return RegisterAgentResult{}, fmt.Errorf("create agent api key: %w", err)
	}
	if err := tx.QueryRow(ctx, `
		INSERT INTO integration.agent_applications
				(organization_id, bound_agent_user_id, model_endpoint_id, runtime_mode, workflow_key, name, capabilities)
			VALUES ($1::uuid, $2::uuid, $3::uuid, $4, $5, $6, $7::jsonb)
			RETURNING id::text
		`, principal.OrganizationID, agentUserID, input.ModelEndpointID, input.RuntimeMode, nullableText(input.WorkflowKey), input.ApplicationName, string(capabilitiesJSON)).Scan(&applicationID); err != nil {
		return RegisterAgentResult{}, fmt.Errorf("create agent application: %w", err)
	}
	metadataBytes, _ := json.Marshal(map[string]any{
		"agent_user_id":        agentUserID,
		"agent_application_id": applicationID,
		"key_prefix":           keyPrefix,
		"model_endpoint_id":    input.ModelEndpointID,
		"runtime_mode":         input.RuntimeMode,
		"expires_at":           input.ExpiresAt,
	})
	if _, err := tx.Exec(ctx, `
		INSERT INTO audit.audit_log
			(organization_id, actor_user_id, initiator_user_id, agent_application_id,
			 action, resource_type, resource_id, result, metadata)
		VALUES ($1::uuid, $2::uuid, $2::uuid, $3::uuid,
			 'agent.register', 'agent_application', $3::uuid, 'allowed', $4::jsonb)
	`, principal.OrganizationID, principal.UserID, applicationID, string(metadataBytes)); err != nil {
		return RegisterAgentResult{}, fmt.Errorf("record agent registration audit: %w", err)
	}
	responseBytes, _ := json.Marshal(map[string]any{
		"agent_user_id":        agentUserID,
		"agent_application_id": applicationID,
		"api_key_prefix":       keyPrefix,
		"model_endpoint_id":    input.ModelEndpointID,
		"runtime_mode":         input.RuntimeMode,
		"expires_at":           input.ExpiresAt,
	})
	if _, err := tx.Exec(ctx, `
		UPDATE system.idempotency_keys
		SET response_status = 201, response_body = $5::jsonb
		WHERE organization_id = $1::uuid AND subject_id = $2::uuid
		  AND operation = 'agent.register' AND idempotency_key = $3 AND request_hash = $4
	`, principal.OrganizationID, principal.UserID, input.IdempotencyKey, requestHash, string(responseBytes)); err != nil {
		return RegisterAgentResult{}, fmt.Errorf("save agent registration idempotency: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return RegisterAgentResult{}, fmt.Errorf("commit agent registration: %w", err)
	}
	return RegisterAgentResult{
		AgentUserID:        agentUserID,
		AgentApplicationID: applicationID,
		ApiKey:             rawKey,
		ApiKeyPrefix:       keyPrefix,
		ModelEndpointID:    input.ModelEndpointID,
		RuntimeMode:        input.RuntimeMode,
		WorkflowKey:        input.WorkflowKey,
		Capabilities:       capabilities,
		ExpiresAt:          input.ExpiresAt,
	}, nil
}

func validInput(input RegisterAgentInput) bool {
	return validCapabilities(input.Capabilities) &&
		validText(input.DisplayName, 1, 200) &&
		validText(input.ApiKeyName, 1, 100) &&
		validText(input.ApplicationName, 1, 200) &&
		validUUID(input.ModelEndpointID) &&
		validRuntimeMode(input.RuntimeMode, input.WorkflowKey)
}

type modelEndpointQuerier interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func requireModelEndpointForRuntime(ctx context.Context, db modelEndpointQuerier, organizationID, endpointID, runtimeMode string) error {
	var endpointCapabilitiesJSON []byte
	err := db.QueryRow(ctx, `
		SELECT r.capabilities
		FROM integration.model_endpoints e
		JOIN integration.model_endpoint_revisions r
		  ON r.model_endpoint_id = e.id AND r.revision = e.current_revision
		WHERE e.id = $1::uuid AND e.organization_id = $2::uuid
		  AND e.status = 'active' AND r.revoked_at IS NULL
	`, endpointID, organizationID).Scan(&endpointCapabilitiesJSON)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrInvalidInput
	}
	if err != nil {
		return fmt.Errorf("load model endpoint for agent application: %w", err)
	}
	var endpointCapabilities modelendpoint.Capabilities
	if json.Unmarshal(endpointCapabilitiesJSON, &endpointCapabilities) != nil ||
		!endpointCapabilities.Generate ||
		(runtimeMode == "react" && !endpointCapabilities.ToolCalling) ||
		(runtimeMode == "workflow" && !endpointCapabilities.StructuredOutput) {
		return ErrInvalidInput
	}
	return nil
}

func validRuntimeMode(runtimeMode, workflowKey string) bool {
	switch runtimeMode {
	case "rag", "react":
		return workflowKey == ""
	case "workflow":
		return validText(workflowKey, 1, 100)
	default:
		return false
	}
}

func nullableText(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func validIdempotencyKey(value string) bool {
	value = strings.TrimSpace(value)
	return len(value) >= 16 && len(value) <= 200 && !strings.ContainsRune(value, '\x00')
}

func validText(value string, min, max int) bool {
	value = strings.TrimSpace(value)
	return len([]rune(value)) >= min && len([]rune(value)) <= max && !strings.ContainsRune(value, '\x00')
}

func normalizeCapabilities(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if !allowedAgentCapability(value) {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

func validCapabilities(values []string) bool {
	if len(values) == 0 || len(values) > 20 {
		return false
	}
	for _, capability := range values {
		if !allowedAgentCapability(strings.TrimSpace(capability)) {
			return false
		}
	}
	return true
}

func allowedAgentCapability(value string) bool {
	switch value {
	// query.execute is required by the open-API unified query gate
	// (ForOpenAPI); without it an application can never be granted search.
	case "query.read", "query.execute", "reference.read", "asset.create", "asset.edit", "asset.publish", "asset.archive", "agent.run":
		return true
	default:
		return false
	}
}

func newAPIKey() (string, error) {
	buffer := make([]byte, 32)
	if _, err := rand.Read(buffer); err != nil {
		return "", err
	}
	return "ak_" + base64.RawURLEncoding.EncodeToString(buffer), nil
}
