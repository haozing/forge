package agenttask

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
	"agentchunzhi/internal/query"
	"agentchunzhi/internal/store"

	"github.com/jackc/pgx/v5"
)

var (
	ErrInvalidInput         = errors.New("invalid agent task input")
	ErrNotFound             = errors.New("agent task not found")
	ErrConflict             = errors.New("agent task conflict")
	ErrIdempotencyConflict  = errors.New("agent task idempotency conflict")
	ErrUnsupportedOperation = errors.New("agent task operation is not supported")
)

const PrepareAsset = "prepare_asset"

// MaxTaskAssets caps one prepare task so a single workflow run stays bounded
// (doc §5: agenttask accepts 1..20 input assets).
const MaxTaskAssets = 20

type Service struct {
	Store *store.Store
}

type CreateInput struct {
	AgentApplicationID string
	Operation          string
	InputAssetIDs      []string
	IdempotencyKey     string
}

type TaskResult struct {
	ID                 string     `json:"id"`
	Status             string     `json:"status"`
	Operation          string     `json:"operation"`
	RunID              string     `json:"run_id,omitempty"`
	CandidateVersionID *string    `json:"candidate_version_id,omitempty"`
	ErrorCode          string     `json:"error_code,omitempty"`
	AttemptCount       int        `json:"attempt_count"`
	CreatedAt          time.Time  `json:"created_at"`
	CompletedAt        *time.Time `json:"completed_at,omitempty"`
}

func (s Service) Create(ctx context.Context, principal auth.Principal, input CreateInput, readableModelIDs, editableModelIDs []string) (TaskResult, error) {
	input.AgentApplicationID = strings.TrimSpace(input.AgentApplicationID)
	input.Operation = strings.TrimSpace(input.Operation)
	input.IdempotencyKey = strings.TrimSpace(input.IdempotencyKey)
	// One task may batch up to MaxTaskAssets assets of a single workspace;
	// duplicates collapse before hashing so idempotent replays agree.
	input.InputAssetIDs = dedupeAssetIDs(input.InputAssetIDs)
	if principal.UserType != "agent" || !query.ValidUUID(input.AgentApplicationID) || !validOperation(input.Operation) || !validIdempotencyKey(input.IdempotencyKey) || len(input.InputAssetIDs) == 0 || len(input.InputAssetIDs) > MaxTaskAssets || len(readableModelIDs) == 0 || len(editableModelIDs) == 0 {
		return TaskResult{}, ErrInvalidInput
	}
	if input.Operation != PrepareAsset {
		return TaskResult{}, ErrUnsupportedOperation
	}
	for _, assetID := range input.InputAssetIDs {
		if !query.ValidUUID(assetID) {
			return TaskResult{}, ErrInvalidInput
		}
	}
	if s.Store == nil || s.Store.Pool == nil {
		return TaskResult{}, errors.New("database store is not initialized")
	}
	tx, err := s.Store.Pool.Begin(ctx)
	if err != nil {
		return TaskResult{}, fmt.Errorf("begin agent task: %w", err)
	}
	defer tx.Rollback(ctx)

	requestHash := hashRequest(input)
	state, err := beginIdempotency(ctx, tx, principal, requestHash, input.IdempotencyKey)
	if err != nil {
		return TaskResult{}, err
	}
	if state.Replay {
		var result TaskResult
		if err := json.Unmarshal(state.Body, &result); err != nil {
			return TaskResult{}, fmt.Errorf("decode agent task idempotent response: %w", err)
		}
		return result, nil
	}

	var boundAgentUserID, modelEndpointID string
	var modelEndpointRevision int64
	err = tx.QueryRow(ctx, `
		SELECT aa.bound_agent_user_id::text, aa.model_endpoint_id::text, me.current_revision
		FROM integration.agent_applications aa
		JOIN integration.model_endpoints me ON me.id = aa.model_endpoint_id AND me.organization_id = aa.organization_id
		JOIN integration.model_endpoint_revisions mer
		  ON mer.model_endpoint_id = me.id AND mer.revision = me.current_revision
		WHERE aa.id = $1::uuid
		  AND aa.organization_id = $2::uuid
		  AND aa.bound_agent_user_id = $3::uuid
		  AND aa.status = 'active'
		  AND aa.runtime_mode = 'workflow' AND aa.workflow_key = 'asset_prepare'
		  AND me.status = 'active' AND mer.revoked_at IS NULL
	`, input.AgentApplicationID, principal.OrganizationID, principal.UserID).Scan(&boundAgentUserID, &modelEndpointID, &modelEndpointRevision)
	if errors.Is(err, pgx.ErrNoRows) {
		return TaskResult{}, ErrNotFound
	}
	if err != nil {
		return TaskResult{}, fmt.Errorf("load agent application for task: %w", err)
	}

	// Every input asset must be editable through the agent's model scope and
	// carry a working version; the whole task shares one workspace because the
	// workflow run row records exactly one. The lock order is the id order so
	// concurrent multi-asset tasks cannot deadlock.
	rows, err := tx.Query(ctx, `
		SELECT a.id::text, a.current_working_version_id::text, a.resource_model_id::text, a.workspace_id::text
		FROM asset.assets a
		WHERE a.id = ANY($1::uuid[])
		  AND a.organization_id = $2::uuid
		  AND a.resource_model_id::text = ANY($3::text[])
		  AND a.resource_model_id::text = ANY($4::text[])
		  AND a.current_working_version_id IS NOT NULL
		  AND a.deleted_at IS NULL
		ORDER BY a.id
		FOR UPDATE OF a
	`, input.InputAssetIDs, principal.OrganizationID, readableModelIDs, editableModelIDs)
	if err != nil {
		return TaskResult{}, fmt.Errorf("load assets for agent task: %w", err)
	}
	defer rows.Close()
	var workspaceID string
	assetIDs := make([]string, 0, len(input.InputAssetIDs))
	versionIDs := make([]string, 0, len(input.InputAssetIDs))
	modelIDs := make([]string, 0, len(input.InputAssetIDs))
	for rows.Next() {
		var assetID, versionID, modelID, assetWorkspace string
		if err := rows.Scan(&assetID, &versionID, &modelID, &assetWorkspace); err != nil {
			return TaskResult{}, fmt.Errorf("scan agent task asset: %w", err)
		}
		if workspaceID == "" {
			workspaceID = assetWorkspace
		} else if workspaceID != assetWorkspace {
			return TaskResult{}, ErrInvalidInput
		}
		assetIDs = append(assetIDs, assetID)
		versionIDs = append(versionIDs, versionID)
		modelIDs = append(modelIDs, modelID)
	}
	if err := rows.Err(); err != nil {
		return TaskResult{}, fmt.Errorf("iterate agent task assets: %w", err)
	}
	if len(assetIDs) != len(input.InputAssetIDs) {
		return TaskResult{}, ErrNotFound
	}

	var result TaskResult
	err = tx.QueryRow(ctx, `
		INSERT INTO integration.agent_tasks
			(organization_id, agent_application_id, agent_user_id, operation, status, input_asset_ids, idempotency_key)
		VALUES ($1::uuid, $2::uuid, $3::uuid, $4, 'queued', $5::uuid[], $6)
		RETURNING id::text, status, operation, created_at
	`, principal.OrganizationID, input.AgentApplicationID, boundAgentUserID, input.Operation, assetIDs, input.IdempotencyKey).Scan(&result.ID, &result.Status, &result.Operation, &result.CreatedAt)
	if err != nil {
		return TaskResult{}, fmt.Errorf("create agent task: %w", err)
	}
	// The run carries the full asset list; the single-asset shape keeps the
	// pinned asset_version_id so retries read the exact version the task was
	// created against.
	runInput := map[string]any{"asset_ids": assetIDs}
	if len(assetIDs) == 1 {
		runInput["asset_version_id"] = versionIDs[0]
	}
	runInputJSON, _ := json.Marshal(runInput)
	runInputChecksum := sha256.Sum256(runInputJSON)
	var runID string
	if err := tx.QueryRow(ctx, `
		INSERT INTO automation.runs
			(organization_id, workspace_id, source, operation, status, input_scope, created_by,
			 idempotency_key, principal_id, agent_user_id, agent_application_id,
			 model_endpoint_id, model_endpoint_revision, runtime_mode, workflow_key,
			 workflow_code_version, input_snapshot, execution_options, input_checksum,
			 policy_revision, agent_task_id)
		VALUES ($1::uuid, $2::uuid, 'agent', 'prepare_asset', 'queued', $3::jsonb, $4::uuid,
			$5, $4::uuid, $4::uuid, $6::uuid,
			$7::uuid, $8, 'workflow', 'asset_prepare',
			1, $3::jsonb, '{}'::jsonb, $9, 1, $10::uuid)
		RETURNING id::text
	`, principal.OrganizationID, workspaceID, runInputJSON, principal.UserID,
		"agent-task-run:"+input.IdempotencyKey, input.AgentApplicationID,
		modelEndpointID, modelEndpointRevision, hex.EncodeToString(runInputChecksum[:]), result.ID).Scan(&runID); err != nil {
		return TaskResult{}, fmt.Errorf("create agent task workflow run: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO automation.run_events (organization_id, run_id, event_type, payload)
		VALUES ($1::uuid, $2::uuid, 'run.queued', jsonb_build_object(
			'runtime_mode', 'workflow', 'workflow_key', 'asset_prepare', 'agent_task_id', $3::text))
	`, principal.OrganizationID, runID, result.ID); err != nil {
		return TaskResult{}, fmt.Errorf("record agent task workflow run: %w", err)
	}
	result.RunID = runID
	metadata, _ := json.Marshal(map[string]any{
		"agent_application_id": input.AgentApplicationID,
		"agent_user_id":        principal.UserID,
		"asset_ids":            assetIDs,
		"asset_version_ids":    versionIDs,
		"resource_model_ids":   modelIDs,
		"operation":            input.Operation,
		"run_id":               runID,
	})
	if _, err := tx.Exec(ctx, `
		INSERT INTO audit.audit_log
			(organization_id, actor_user_id, initiator_user_id, agent_application_id, action, resource_type, resource_id, result, metadata)
		VALUES ($1::uuid, $2::uuid, $2::uuid, $3::uuid, 'agent.task.create', 'agent_task', $4::uuid, 'allowed', $5::jsonb)
	`, principal.OrganizationID, principal.UserID, input.AgentApplicationID, result.ID, string(metadata)); err != nil {
		return TaskResult{}, fmt.Errorf("record agent task audit: %w", err)
	}
	if err := saveIdempotency(ctx, tx, principal, input.IdempotencyKey, requestHash, result); err != nil {
		return TaskResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return TaskResult{}, fmt.Errorf("commit agent task: %w", err)
	}
	return result, nil
}

func (s Service) Get(ctx context.Context, principal auth.Principal, taskID string) (TaskResult, error) {
	if principal.UserType != "agent" || !query.ValidUUID(taskID) {
		return TaskResult{}, ErrInvalidInput
	}
	if s.Store == nil || s.Store.Pool == nil {
		return TaskResult{}, errors.New("database store is not initialized")
	}
	var result TaskResult
	err := s.Store.Pool.QueryRow(ctx, `
                SELECT t.id::text, t.status, t.operation, t.candidate_version_id::text, COALESCE(t.error_code, ''),
		       COALESCE((SELECT MAX(attempt_count) FROM automation.runs r WHERE r.agent_task_id = t.id), 0),
		       COALESCE((SELECT r.id::text FROM automation.runs r
		                 WHERE r.agent_task_id = t.id ORDER BY r.created_at DESC LIMIT 1), ''),
		       t.created_at, t.completed_at
                FROM integration.agent_tasks t
                WHERE t.id = $1::uuid AND t.organization_id = $2::uuid AND t.agent_user_id = $3::uuid
        `, taskID, principal.OrganizationID, principal.UserID).Scan(&result.ID, &result.Status, &result.Operation, &result.CandidateVersionID, &result.ErrorCode, &result.AttemptCount, &result.RunID, &result.CreatedAt, &result.CompletedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return TaskResult{}, ErrNotFound
	}
	if err != nil {
		return TaskResult{}, fmt.Errorf("load agent task: %w", err)
	}
	return result, nil
}

type idempotencyState struct {
	Replay bool
	Body   []byte
}

func beginIdempotency(ctx context.Context, tx pgx.Tx, principal auth.Principal, requestHash, key string) (idempotencyState, error) {
	// Expired reservations are reusable; otherwise a 24-hour key would remain
	// permanently bound by the unique constraint.
	if _, err := tx.Exec(ctx, `
		DELETE FROM system.idempotency_keys
		WHERE organization_id = $1::uuid AND subject_id = $2::uuid
		  AND operation = 'agent.task.create' AND idempotency_key = $3
		  AND expires_at <= now()
	`, principal.OrganizationID, principal.UserID, key); err != nil {
		return idempotencyState{}, fmt.Errorf("release expired agent task idempotency: %w", err)
	}
	var id string
	err := tx.QueryRow(ctx, `
		INSERT INTO system.idempotency_keys
			(organization_id, subject_id, operation, idempotency_key, request_hash, expires_at)
		VALUES ($1::uuid, $2::uuid, 'agent.task.create', $3, $4, now() + interval '24 hours')
		ON CONFLICT (organization_id, subject_id, operation, idempotency_key) DO NOTHING
		RETURNING id::text
	`, principal.OrganizationID, principal.UserID, key, requestHash).Scan(&id)
	if err == nil {
		return idempotencyState{}, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return idempotencyState{}, fmt.Errorf("reserve agent task idempotency: %w", err)
	}
	var storedHash string
	var body []byte
	err = tx.QueryRow(ctx, `
		SELECT request_hash, response_body
		FROM system.idempotency_keys
		WHERE organization_id = $1::uuid AND subject_id = $2::uuid
		  AND operation = 'agent.task.create' AND idempotency_key = $3
		FOR UPDATE
	`, principal.OrganizationID, principal.UserID, key).Scan(&storedHash, &body)
	if errors.Is(err, pgx.ErrNoRows) {
		return idempotencyState{}, ErrConflict
	}
	if err != nil {
		return idempotencyState{}, fmt.Errorf("load agent task idempotency: %w", err)
	}
	if storedHash != requestHash {
		return idempotencyState{}, ErrIdempotencyConflict
	}
	if len(body) == 0 {
		return idempotencyState{}, ErrConflict
	}
	return idempotencyState{Replay: true, Body: body}, nil
}

func saveIdempotency(ctx context.Context, tx pgx.Tx, principal auth.Principal, key, requestHash string, result TaskResult) error {
	body, err := json.Marshal(result)
	if err != nil {
		return fmt.Errorf("encode agent task idempotency: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE system.idempotency_keys
		SET response_status = 202, response_body = $5::jsonb
		WHERE organization_id = $1::uuid AND subject_id = $2::uuid
		  AND operation = 'agent.task.create' AND idempotency_key = $3 AND request_hash = $4
	`, principal.OrganizationID, principal.UserID, key, requestHash, string(body)); err != nil {
		return fmt.Errorf("save agent task idempotency: %w", err)
	}
	return nil
}

func hashRequest(input CreateInput) string {
	body, _ := json.Marshal(struct {
		ApplicationID string   `json:"agent_application_id"`
		Operation     string   `json:"operation"`
		AssetIDs      []string `json:"input_asset_ids"`
	}{input.AgentApplicationID, input.Operation, input.InputAssetIDs})
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

func validOperation(value string) bool {
	return strings.TrimSpace(value) != ""
}

// dedupeAssetIDs trims, drops empties and removes duplicates while preserving
// the first-seen order, so the idempotency hash of a repeated asset list is
// stable.
func dedupeAssetIDs(ids []string) []string {
	seen := make(map[string]bool, len(ids))
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	return out
}

func validIdempotencyKey(value string) bool {
	return len(value) >= 16 && len(value) <= 200 && !strings.ContainsRune(value, '\x00')
}
