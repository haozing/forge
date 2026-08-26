package agentruntime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"agentchunzhi/internal/automation"
	"agentchunzhi/internal/store"

	"github.com/jackc/pgx/v5"
)

var (
	ErrInvalidRunRequest = errors.New("invalid agent run request")
	ErrRunNotFound       = errors.New("agent run not found")
	ErrInteractionState  = errors.New("interaction is not pending")
)

type RunRequest struct {
	OrganizationID     string
	WorkspaceID        string
	PrincipalID        string
	AgentUserID        string
	AgentApplicationID string
	SessionID          string
	ModelEndpointID    string
	ModelRevision      int64
	RuntimeMode        string
	WorkflowKey        string
	WorkflowCodeVer    int64
	Source             string
	Input              map[string]any
	ExecutionOptions   map[string]any
	PolicyRevision     int64
	IdempotencyKey     string
}

type Run struct {
	ID                   string
	OrganizationID       string
	WorkspaceID          string
	AgentApplicationID   string
	SessionID            string
	ModelEndpointID      string
	ModelRevision        int64
	RuntimeMode          string
	WorkflowKey          string
	WorkflowCodeVersion  int64
	Status               string
	CurrentNode          string
	CheckpointID         string
	CheckpointSequence   int64
	WaitingInteractionID string
	CreatedAt            time.Time
}

type Interaction struct {
	ID          string
	RunID       string
	Type        string
	Status      string
	Prompt      string
	Metadata    map[string]any
	Response    map[string]any
	RequestedAt time.Time
	RespondedAt *time.Time
	RespondedBy string
}

type Coordinator struct {
	Store *store.Store
}

func (c Coordinator) Create(ctx context.Context, req RunRequest) (Run, error) {
	if c.Store == nil || c.Store.Pool == nil || !validRunRequest(req) {
		return Run{}, ErrInvalidRunRequest
	}
	input := nonNilMap(req.Input)
	options := nonNilMap(req.ExecutionOptions)
	inputBytes, _ := json.Marshal(input)
	checksum := sha256.Sum256(inputBytes)
	var runID string
	err := c.Store.Pool.QueryRow(ctx, `
		INSERT INTO automation.runs
			(organization_id, workspace_id, source, operation, status, created_by,
			 principal_id, agent_user_id, agent_application_id, session_id,
			 model_endpoint_id, model_endpoint_revision, runtime_mode, workflow_key,
			 workflow_code_version, input_snapshot, execution_options, input_checksum,
			 policy_revision, idempotency_key)
		VALUES ($1::uuid, $2::uuid, $3, $4, 'queued', $5::uuid,
			 $5::uuid, NULLIF($6, '')::uuid, $7::uuid, NULLIF($8, '')::uuid,
			 NULLIF($9, '')::uuid, NULLIF($10, 0), $11, NULLIF($12, ''),
			 NULLIF($13, 0), $14::jsonb, $15::jsonb, $16, NULLIF($17, 0), $18)
		ON CONFLICT (organization_id, workspace_id, created_by, idempotency_key)
		WHERE idempotency_key IS NOT NULL DO NOTHING
		RETURNING id::text
	`, req.OrganizationID, req.WorkspaceID, req.Source, runOperation(req), req.PrincipalID,
		req.AgentUserID, req.AgentApplicationID, req.SessionID, req.ModelEndpointID, req.ModelRevision,
		req.RuntimeMode, req.WorkflowKey, req.WorkflowCodeVer, string(inputBytes), mustJSON(options),
		hex.EncodeToString(checksum[:]), req.PolicyRevision, req.IdempotencyKey).Scan(&runID)
	if errors.Is(err, pgx.ErrNoRows) {
		err = c.Store.Pool.QueryRow(ctx, `SELECT id::text FROM automation.runs WHERE organization_id = $1::uuid AND workspace_id = $2::uuid AND created_by = $3::uuid AND idempotency_key = $4`, req.OrganizationID, req.WorkspaceID, req.PrincipalID, req.IdempotencyKey).Scan(&runID)
	}
	if err != nil {
		return Run{}, fmt.Errorf("create persistent agent run: %w", err)
	}
	if _, err := c.Store.Pool.Exec(ctx, `
		INSERT INTO automation.run_events (organization_id, run_id, event_type, payload)
		VALUES ($1::uuid, $2::uuid, 'run.queued', $3::jsonb)
	`, req.OrganizationID, runID, mustJSON(map[string]any{"runtime_mode": req.RuntimeMode, "workflow_key": req.WorkflowKey})); err != nil {
		return Run{}, fmt.Errorf("record persistent run event: %w", err)
	}
	return c.Get(ctx, req.OrganizationID, runID)
}

func (c Coordinator) Get(ctx context.Context, organizationID, runID string) (Run, error) {
	if c.Store == nil || c.Store.Pool == nil || !validID(organizationID) || !validID(runID) {
		return Run{}, ErrInvalidRunRequest
	}
	var run Run
	err := c.Store.Pool.QueryRow(ctx, `
		SELECT id::text, organization_id::text, workspace_id::text,
		       COALESCE(agent_application_id::text, ''), COALESCE(session_id::text, ''),
		       COALESCE(model_endpoint_id::text, ''), COALESCE(model_endpoint_revision, 0),
		       COALESCE(runtime_mode, ''), COALESCE(workflow_key, ''), COALESCE(workflow_code_version, 0),
		       status, COALESCE(current_node, ''), COALESCE(eino_checkpoint_id::text, ''),
		       checkpoint_sequence, COALESCE(waiting_interaction_id::text, ''), created_at
		FROM automation.runs WHERE organization_id = $1::uuid AND id = $2::uuid
	`, organizationID, runID).Scan(
		&run.ID, &run.OrganizationID, &run.WorkspaceID, &run.AgentApplicationID, &run.SessionID,
		&run.ModelEndpointID, &run.ModelRevision, &run.RuntimeMode, &run.WorkflowKey, &run.WorkflowCodeVersion,
		&run.Status, &run.CurrentNode, &run.CheckpointID, &run.CheckpointSequence, &run.WaitingInteractionID, &run.CreatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return Run{}, ErrRunNotFound
	}
	if err != nil {
		return Run{}, fmt.Errorf("load persistent agent run: %w", err)
	}
	return run, nil
}

func (c Coordinator) Claim(ctx context.Context, workerID string, lease time.Duration) (automation.ClaimedRun, error) {
	return (automation.Service{Store: c.Store}).ClaimNextRun(ctx, workerID, lease)
}

func (c Coordinator) Renew(ctx context.Context, attemptID, workerID string, lease time.Duration) (automation.Attempt, error) {
	return (automation.Service{Store: c.Store}).RenewAttempt(ctx, attemptID, workerID, lease)
}

func (c Coordinator) Finish(ctx context.Context, attemptID, workerID string, success bool, errorCode, errorSummary string) (automation.Run, error) {
	return (automation.Service{Store: c.Store}).FinishAttempt(ctx, attemptID, workerID, success, errorCode, errorSummary)
}

func (c Coordinator) RequestCancel(ctx context.Context, organizationID, runID, reason string) error {
	if c.Store == nil || c.Store.Pool == nil || !validID(organizationID) || !validID(runID) {
		return ErrInvalidRunRequest
	}
	tx, err := c.Store.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	result, err := tx.Exec(ctx, `
		UPDATE automation.runs SET status = 'cancel_requested', cancel_requested = true,
			error_code = 'cancel_requested', error_summary = NULLIF($3, '')
		WHERE organization_id = $1::uuid AND id = $2::uuid AND status IN ('queued', 'running', 'waiting_input', 'waiting_approval')
	`, organizationID, runID, strings.TrimSpace(reason))
	if err != nil {
		return fmt.Errorf("request run cancellation: %w", err)
	}
	if result.RowsAffected() != 1 {
		return ErrRunNotFound
	}
	if _, err := tx.Exec(ctx, `INSERT INTO automation.run_events (organization_id, run_id, event_type, payload) VALUES ($1::uuid, $2::uuid, 'run.cancel_requested', $3::jsonb)`, organizationID, runID, mustJSON(map[string]any{"reason": reason})); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (c Coordinator) WaitForInteraction(ctx context.Context, organizationID, runID, interactionType, prompt string, metadata map[string]any, idempotencyKey string) (Interaction, error) {
	if c.Store == nil || c.Store.Pool == nil || !validID(organizationID) || !validID(runID) || (interactionType != "input" && interactionType != "approval") || strings.TrimSpace(prompt) == "" {
		return Interaction{}, ErrInvalidRunRequest
	}
	tx, err := c.Store.Pool.Begin(ctx)
	if err != nil {
		return Interaction{}, err
	}
	defer tx.Rollback(ctx)
	var interaction Interaction
	err = tx.QueryRow(ctx, `
		INSERT INTO automation.interactions (organization_id, run_id, interaction_type, prompt, metadata, idempotency_key)
		VALUES ($1::uuid, $2::uuid, $3, $4, $5::jsonb, NULLIF($6, ''))
		ON CONFLICT (run_id, idempotency_key) DO UPDATE SET id = automation.interactions.id
		RETURNING id::text, run_id::text, interaction_type, status, prompt, metadata, response, requested_at, responded_at, COALESCE(responded_by::text, '')
	`, organizationID, runID, interactionType, prompt, mustJSON(nonNilMap(metadata)), idempotencyKey).Scan(
		&interaction.ID, &interaction.RunID, &interaction.Type, &interaction.Status, &interaction.Prompt,
		&metadataJSON{target: &interaction.Metadata}, &metadataJSON{target: &interaction.Response}, &interaction.RequestedAt,
		&interaction.RespondedAt, &interaction.RespondedBy,
	)
	if err != nil {
		return Interaction{}, fmt.Errorf("create run interaction: %w", err)
	}
	result, err := tx.Exec(ctx, `UPDATE automation.runs SET status = CASE WHEN $3 = 'approval' THEN 'waiting_approval' ELSE 'waiting_input' END, waiting_interaction_id = $2::uuid WHERE organization_id = $1::uuid AND id = $4::uuid AND status IN ('queued', 'running', 'waiting_input', 'waiting_approval')`, organizationID, interaction.ID, interaction.Type, runID)
	if err != nil {
		return Interaction{}, err
	}
	if result.RowsAffected() != 1 {
		return Interaction{}, ErrRunNotFound
	}
	if err := tx.Commit(ctx); err != nil {
		return Interaction{}, err
	}
	return interaction, nil
}

func (c Coordinator) SuspendForInteraction(ctx context.Context, attemptID, workerID, organizationID, runID, interactionType, interruptID, prompt string, displayPayload, resumeSchema map[string]any) (Interaction, error) {
	if c.Store == nil || c.Store.Pool == nil || !validID(attemptID) || !validID(organizationID) || !validID(runID) ||
		strings.TrimSpace(workerID) == "" || (interactionType != "input" && interactionType != "approval") ||
		strings.TrimSpace(interruptID) == "" || strings.TrimSpace(prompt) == "" {
		return Interaction{}, ErrInvalidRunRequest
	}
	tx, err := c.Store.Pool.Begin(ctx)
	if err != nil {
		return Interaction{}, err
	}
	defer tx.Rollback(ctx)
	var currentStatus, claimedBy string
	if err := tx.QueryRow(ctx, `
		SELECT r.status, COALESCE(a.claimed_by, '')
		FROM automation.runs r JOIN automation.attempts a ON a.run_id = r.id
		WHERE r.organization_id = $1::uuid AND r.id = $2::uuid AND a.id = $3::uuid
		FOR UPDATE OF r, a
	`, organizationID, runID, attemptID).Scan(&currentStatus, &claimedBy); errors.Is(err, pgx.ErrNoRows) {
		return Interaction{}, ErrRunNotFound
	} else if err != nil {
		return Interaction{}, err
	}
	if currentStatus != "running" || claimedBy != strings.TrimSpace(workerID) {
		return Interaction{}, ErrInteractionState
	}
	var interaction Interaction
	metadata := map[string]any{
		"interrupt_id": interruptID, "display_payload": nonNilMap(displayPayload), "resume_schema": nonNilMap(resumeSchema),
	}
	err = tx.QueryRow(ctx, `
		INSERT INTO automation.interactions
			(organization_id, run_id, interaction_type, interrupt_id, prompt, metadata, display_payload, resume_schema, idempotency_key)
		VALUES ($1::uuid, $2::uuid, $3, $4, $5, $6::jsonb, $7::jsonb, $8::jsonb, $4)
		ON CONFLICT (run_id, interrupt_id) WHERE interrupt_id IS NOT NULL
		DO UPDATE SET interrupt_id = EXCLUDED.interrupt_id
		RETURNING id::text, run_id::text, interaction_type, status, prompt, metadata, response,
		          requested_at, responded_at, COALESCE(responded_by::text, '')
	`, organizationID, runID, interactionType, interruptID, prompt, mustJSON(metadata),
		mustJSON(nonNilMap(displayPayload)), mustJSON(nonNilMap(resumeSchema))).Scan(
		&interaction.ID, &interaction.RunID, &interaction.Type, &interaction.Status, &interaction.Prompt,
		&metadataJSON{target: &interaction.Metadata}, &metadataJSON{target: &interaction.Response},
		&interaction.RequestedAt, &interaction.RespondedAt, &interaction.RespondedBy,
	)
	if err != nil {
		return Interaction{}, fmt.Errorf("persist Eino interaction: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE automation.attempts
		SET status = 'waiting', lease_expires_at = NULL, completed_at = now(), updated_at = now()
		WHERE id = $1::uuid AND status = 'started' AND claimed_by = $2
	`, attemptID, workerID); err != nil {
		return Interaction{}, err
	}
	nextStatus := "waiting_input"
	if interactionType == "approval" {
		nextStatus = "waiting_approval"
	}
	if _, err := tx.Exec(ctx, `
		UPDATE automation.runs SET status = $3, waiting_interaction_id = $2::uuid,
			current_node = 'eino_interrupt'
		WHERE organization_id = $1::uuid AND id = $4::uuid AND status = 'running'
	`, organizationID, interaction.ID, nextStatus, runID); err != nil {
		return Interaction{}, err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO automation.run_events (organization_id, run_id, event_type, payload)
		VALUES ($1::uuid, $2::uuid, 'waiting', $3::jsonb)
	`, organizationID, runID, mustJSON(map[string]any{
		"interaction_id": interaction.ID, "interaction_type": interactionType, "interrupt_id": interruptID,
	})); err != nil {
		return Interaction{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Interaction{}, err
	}
	return interaction, nil
}

func (c Coordinator) ResolveInteraction(ctx context.Context, organizationID, runID, interactionID, responderID, status string, response map[string]any) (Run, error) {
	if c.Store == nil || c.Store.Pool == nil || !validID(organizationID) || !validID(runID) || !validID(interactionID) || !validID(responderID) || (status != "approved" && status != "rejected") {
		return Run{}, ErrInvalidRunRequest
	}
	tx, err := c.Store.Pool.Begin(ctx)
	if err != nil {
		return Run{}, err
	}
	defer tx.Rollback(ctx)
	if err := tx.QueryRow(ctx, `
		UPDATE automation.interactions i
		SET status = $4, response = $5::jsonb, responded_at = now(), responded_by = $3::uuid
		FROM automation.runs r
		WHERE i.organization_id = $1::uuid AND i.run_id = $2::uuid AND i.id = $6::uuid
		  AND i.status = 'pending' AND r.id = i.run_id
		  AND r.organization_id = i.organization_id
		  AND r.status IN ('waiting_input', 'waiting_approval')
		  AND r.waiting_interaction_id = i.id
		RETURNING i.run_id::text
	`, organizationID, runID, responderID, status, mustJSON(nonNilMap(response)), interactionID).Scan(&runID); errors.Is(err, pgx.ErrNoRows) {
		return Run{}, ErrInteractionState
	} else if err != nil {
		return Run{}, err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE automation.runs SET status = 'queued', waiting_interaction_id = NULL,
			error_code = NULL, error_summary = NULL, completed_at = NULL, next_attempt_at = now()
		WHERE organization_id = $1::uuid AND id = $2::uuid
		  AND status IN ('waiting_input', 'waiting_approval')
	`, organizationID, runID); err != nil {
		return Run{}, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO automation.run_events (organization_id, run_id, event_type, payload) VALUES ($1::uuid, $2::uuid, 'run.queued', $3::jsonb)`, organizationID, runID, mustJSON(map[string]any{"interaction_id": interactionID, "status": status})); err != nil {
		return Run{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Run{}, err
	}
	return c.Get(ctx, organizationID, runID)
}

type metadataJSON struct{ target *map[string]any }

func (m *metadataJSON) Scan(value any) error {
	if value == nil {
		*m.target = map[string]any{}
		return nil
	}
	var raw []byte
	switch v := value.(type) {
	case []byte:
		raw = v
	case string:
		raw = []byte(v)
	default:
		return fmt.Errorf("decode JSON metadata from %T", value)
	}
	*m.target = map[string]any{}
	return json.Unmarshal(raw, m.target)
}

func validRunRequest(req RunRequest) bool {
	if !validID(req.OrganizationID) || !validID(req.WorkspaceID) || !validID(req.PrincipalID) || !validID(req.AgentApplicationID) || !validID(req.ModelEndpointID) || req.ModelRevision <= 0 || len(strings.TrimSpace(req.IdempotencyKey)) < 16 || strings.TrimSpace(req.RuntimeMode) == "" {
		return false
	}
	if req.AgentUserID != "" && !validID(req.AgentUserID) || req.SessionID != "" && !validID(req.SessionID) {
		return false
	}
	if req.RuntimeMode == "workflow" && strings.TrimSpace(req.WorkflowKey) == "" || req.RuntimeMode == "react" && strings.TrimSpace(req.WorkflowKey) != "" {
		return false
	}
	if req.Source != "automation" && req.Source != "manual" && req.Source != "agent" && req.Source != "chat" {
		return false
	}
	return req.RuntimeMode == "workflow" || req.RuntimeMode == "react"
}

func runOperation(req RunRequest) string {
	if req.WorkflowKey != "" {
		return req.WorkflowKey
	}
	return req.RuntimeMode
}

func nonNilMap(value map[string]any) map[string]any {
	if value == nil {
		return map[string]any{}
	}
	return value
}

func mustJSON(value any) []byte {
	result, _ := json.Marshal(value)
	return result
}

func validID(value string) bool {
	value = strings.TrimSpace(value)
	if len(value) != 36 {
		return false
	}
	for index, char := range value {
		if index == 8 || index == 13 || index == 18 || index == 23 {
			if char != '-' {
				return false
			}
			continue
		}
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') && (char < 'A' || char > 'F') {
			return false
		}
	}
	return true
}
