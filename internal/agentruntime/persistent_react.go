package agentruntime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	runtimecheckpoint "agentchunzhi/internal/agentruntime/checkpoint"
	runtimetools "agentchunzhi/internal/agentruntime/tools"
	"agentchunzhi/internal/automation"
	"agentchunzhi/internal/modelendpoint"
	"agentchunzhi/internal/store"

	"github.com/jackc/pgx/v5"
)

type ReActToolScope struct {
	OrganizationID     string
	WorkspaceID        string
	RunID              string
	SessionID          string
	PrincipalID        string
	AgentUserID        string
	AgentApplicationID string
}

type ReActToolFactory interface {
	Build(context.Context, ReActToolScope, map[string]any) (*runtimetools.Registry, runtimetools.Policy, error)
}

type PersistentReActService struct {
	Store       *store.Store
	Cipher      *modelendpoint.CredentialCipher
	Models      ModelResolver
	ToolFactory ReActToolFactory
	Coordinator Coordinator
}

// Process executes one claimed ReAct attempt. waiting is true when Eino saved
// a checkpoint and the attempt was atomically moved to an interaction state.
func (s PersistentReActService) Process(ctx context.Context, claimed automation.ClaimedRun) (waiting bool, err error) {
	if s.Store == nil || s.Store.Pool == nil || s.Cipher == nil || s.Models == nil || s.ToolFactory == nil {
		return false, errors.New("persistent ReAct service is not initialized")
	}
	run, err := s.loadRun(ctx, claimed.Run.ID)
	if err != nil {
		return false, err
	}
	if run.RuntimeMode != "react" || run.Status != "running" {
		return false, ErrInvalidReActRequest
	}
	registry, policy, err := s.ToolFactory.Build(ctx, run.Scope, run.ToolPolicy)
	if err != nil {
		return false, fmt.Errorf("build ReAct tools: %w", err)
	}
	policy.UsedCalls = run.ToolCallCount
	request := ReActRequest{
		OrganizationID: run.Scope.OrganizationID, RunID: run.Scope.RunID,
		AgentApplicationID: run.Scope.AgentApplicationID, ModelEndpointID: run.ModelEndpointID,
		ModelRevision: run.ModelRevision, CheckpointID: run.Scope.RunID, Instruction: run.Instruction,
		Query: run.Query, History: run.History, ToolPolicy: policy,
		CheckPointStore: runtimecheckpoint.PostgresStore{
			Store: s.Store, Cipher: s.Cipher, OrganizationID: run.Scope.OrganizationID, RunID: run.Scope.RunID,
		},
	}
	executor := ReActExecutor{Models: s.Models, Tools: registry}
	emit := func(event ReActEvent) error { return s.persistEvent(ctx, run.Scope, event) }
	var result ReActResult
	if run.Resume == nil {
		result, err = executor.Execute(ctx, request, emit)
	} else {
		decision := runtimetools.ApprovalDecision{
			Approved: run.Resume.Status == "approved", Reason: stringValue(run.Resume.Response["reason"]),
		}
		result, err = executor.ResumeApproval(ctx, request, run.Resume.InterruptID, decision, emit)
	}
	if err != nil {
		return false, err
	}
	if run.Resume != nil {
		if _, err := s.Store.Pool.Exec(ctx, `
			UPDATE automation.interactions SET resume_consumed_at = now()
			WHERE id = $1::uuid AND resume_consumed_at IS NULL
		`, run.Resume.ID); err != nil {
			return false, fmt.Errorf("consume ReAct interaction: %w", err)
		}
	}
	if result.Interrupted {
		root, ok := rootInterrupt(result.Interrupts)
		if !ok {
			return false, errors.New("Eino interrupt has no root-cause address")
		}
		kind, prompt, display, resumeSchema := interactionFromInterrupt(root)
		coordinator := s.Coordinator
		if coordinator.Store == nil {
			coordinator.Store = s.Store
		}
		workerID := ""
		if claimed.Attempt.ClaimedBy != nil {
			workerID = *claimed.Attempt.ClaimedBy
		}
		if _, err := coordinator.SuspendForInteraction(ctx, claimed.Attempt.ID, workerID,
			run.Scope.OrganizationID, run.Scope.RunID, kind, root.ID, prompt, display, resumeSchema); err != nil {
			return false, err
		}
		return true, nil
	}
	output, _ := json.Marshal(map[string]any{
		"answer": result.Answer, "model_endpoint_id": result.ModelEndpointID,
		"model_endpoint_revision": result.ModelEndpointRevision,
	})
	if _, err := s.Store.Pool.Exec(ctx, `
		UPDATE automation.runs SET output_snapshot = $2::jsonb, input_tokens = $3, output_tokens = $4,
			current_node = 'complete'
		WHERE id = $1::uuid AND status = 'running'
	`, run.Scope.RunID, output, result.Usage.InputTokens, result.Usage.OutputTokens); err != nil {
		return false, fmt.Errorf("persist ReAct result: %w", err)
	}
	return false, nil
}

type persistentRun struct {
	Scope           ReActToolScope
	RuntimeMode     string
	Status          string
	ModelEndpointID string
	ModelRevision   int64
	Instruction     string
	Query           string
	History         []ChatMessage
	ToolPolicy      map[string]any
	ToolCallCount   int
	Resume          *persistentResume
}

type persistentResume struct {
	ID          string
	InterruptID string
	Status      string
	Response    map[string]any
}

func (s PersistentReActService) loadRun(ctx context.Context, runID string) (persistentRun, error) {
	var result persistentRun
	var input, toolPolicy []byte
	err := s.Store.Pool.QueryRow(ctx, `
		SELECT r.organization_id::text, r.workspace_id::text, r.id::text,
		       COALESCE(r.session_id::text, ''), r.principal_id::text, COALESCE(r.agent_user_id::text, ''),
		       r.agent_application_id::text, r.runtime_mode, r.status, r.model_endpoint_id::text,
		       r.model_endpoint_revision, aa.instruction, r.input_snapshot, aa.tool_policy, r.tool_call_count
		FROM automation.runs r
		JOIN integration.agent_applications aa ON aa.id = r.agent_application_id
		WHERE r.id = $1::uuid AND aa.status = 'active'
	`, runID).Scan(
		&result.Scope.OrganizationID, &result.Scope.WorkspaceID, &result.Scope.RunID,
		&result.Scope.SessionID, &result.Scope.PrincipalID, &result.Scope.AgentUserID,
		&result.Scope.AgentApplicationID, &result.RuntimeMode, &result.Status, &result.ModelEndpointID,
		&result.ModelRevision, &result.Instruction, &input, &toolPolicy, &result.ToolCallCount,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return persistentRun{}, ErrRunNotFound
	}
	if err != nil {
		return persistentRun{}, fmt.Errorf("load persistent ReAct run: %w", err)
	}
	payload := map[string]any{}
	if err := json.Unmarshal(input, &payload); err != nil {
		return persistentRun{}, fmt.Errorf("decode ReAct input snapshot: %w", err)
	}
	result.Query = strings.TrimSpace(stringValue(payload["query"]))
	result.History = decodeChatHistory(payload["history"])
	result.ToolPolicy = map[string]any{}
	if len(toolPolicy) > 0 {
		_ = json.Unmarshal(toolPolicy, &result.ToolPolicy)
	}
	var resume persistentResume
	var response []byte
	err = s.Store.Pool.QueryRow(ctx, `
		SELECT id::text, interrupt_id, status, COALESCE(response, '{}'::jsonb)
		FROM automation.interactions
		WHERE run_id = $1::uuid AND status IN ('approved', 'rejected') AND resume_consumed_at IS NULL
		ORDER BY responded_at DESC NULLS LAST LIMIT 1
	`, runID).Scan(&resume.ID, &resume.InterruptID, &resume.Status, &response)
	if err == nil {
		resume.Response = map[string]any{}
		_ = json.Unmarshal(response, &resume.Response)
		result.Resume = &resume
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return persistentRun{}, fmt.Errorf("load ReAct resume interaction: %w", err)
	}
	return result, nil
}

func (s PersistentReActService) persistEvent(ctx context.Context, scope ReActToolScope, event ReActEvent) error {
	if event.Type == "waiting" {
		return nil
	}
	payload := map[string]any{}
	switch event.Type {
	case "delta":
		payload["delta"] = event.Delta
	case "tool_started":
		payload["tool_name"], payload["tool_call_id"], payload["arguments_hash"] = event.ToolName, event.ToolCallID, event.ArgumentsHash
		result, err := s.Store.Pool.Exec(ctx, `
			WITH inserted AS (
				INSERT INTO integration.agent_tool_calls
					(organization_id, run_id, session_id, tool_call_id, tool_name, arguments_summary, status)
				VALUES ($1::uuid, $2::uuid, NULLIF($3, '')::uuid, $4, $5,
				        jsonb_build_object('sha256', $6::text), 'started')
				ON CONFLICT (run_id, tool_call_id) DO NOTHING RETURNING 1
			)
			UPDATE automation.runs SET tool_call_count = tool_call_count + (SELECT count(*) FROM inserted)
			WHERE id = $2::uuid
		`, scope.OrganizationID, scope.RunID, scope.SessionID, event.ToolCallID, event.ToolName, event.ArgumentsHash)
		if err != nil {
			return fmt.Errorf("record ReAct tool start: %w", err)
		}
		_ = result
	case "tool_finished":
		payload["tool_name"], payload["tool_call_id"] = event.ToolName, event.ToolCallID
		if _, err := s.Store.Pool.Exec(ctx, `
			UPDATE integration.agent_tool_calls
			SET status = CASE WHEN $3 LIKE '%\"ok\":false%' THEN 'failed' ELSE 'succeeded' END,
				result_summary = jsonb_build_object('summary', $3::text),
				duration_ms = (EXTRACT(EPOCH FROM (now() - created_at)) * 1000)::bigint,
				completed_at = now()
			WHERE run_id = $1::uuid AND tool_call_id = $2
		`, scope.RunID, event.ToolCallID, event.ResultSummary); err != nil {
			return fmt.Errorf("record ReAct tool finish: %w", err)
		}
	case "complete":
	default:
		return nil
	}
	if _, err := s.Store.Pool.Exec(ctx, `
		INSERT INTO automation.run_events (organization_id, run_id, event_type, payload)
		VALUES ($1::uuid, $2::uuid, $3, $4::jsonb)
	`, scope.OrganizationID, scope.RunID, event.Type, mustJSON(payload)); err != nil {
		return fmt.Errorf("persist ReAct event: %w", err)
	}
	return nil
}

func rootInterrupt(interrupts []ReActInterrupt) (ReActInterrupt, bool) {
	for _, item := range interrupts {
		if item.IsRootCause && strings.TrimSpace(item.ID) != "" {
			return item, true
		}
	}
	return ReActInterrupt{}, false
}

func interactionFromInterrupt(interrupt ReActInterrupt) (string, string, map[string]any, map[string]any) {
	display := map[string]any{"interrupt_id": interrupt.ID}
	kind, prompt := "input", "Additional input is required"
	if request, ok := interrupt.Data.(*runtimetools.ApprovalRequest); ok && request != nil {
		kind, prompt = "approval", "Approve the requested tool action"
		display["tool_name"] = request.ToolName
		display["tool_call_id"] = request.ToolCallID
		display["risk"] = request.Risk
		display["arguments_digest"] = request.ArgumentsDigest
	} else if body, err := json.Marshal(interrupt.Data); err == nil {
		var safe map[string]any
		if json.Unmarshal(body, &safe) == nil {
			display["request"] = safe
		}
	}
	resumeSchema := map[string]any{
		"type": "object", "properties": map[string]any{
			"approved": map[string]any{"type": "boolean"}, "reason": map[string]any{"type": "string", "maxLength": 500},
		}, "required": []string{"approved"}, "additionalProperties": false,
	}
	return kind, prompt, display, resumeSchema
}

func decodeChatHistory(value any) []ChatMessage {
	items, _ := value.([]any)
	result := make([]ChatMessage, 0, len(items))
	for _, item := range items {
		entry, _ := item.(map[string]any)
		role, content := stringValue(entry["role"]), stringValue(entry["content"])
		if (role == "user" || role == "assistant") && strings.TrimSpace(content) != "" {
			result = append(result, ChatMessage{Role: role, Content: content})
		}
	}
	return result
}

func stringValue(value any) string {
	text, _ := value.(string)
	return strings.TrimSpace(text)
}
