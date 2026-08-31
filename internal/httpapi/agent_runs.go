package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"agentchunzhi/internal/agentruntime"
	"agentchunzhi/internal/auth"
)

type createAgentRunRequest struct {
	WorkspaceID string                     `json:"workspace_id"`
	Query       string                     `json:"query"`
	History     []agentruntime.ChatMessage `json:"history,omitempty"`
}

type resumeAgentRunRequest struct {
	InteractionID string `json:"interaction_id"`
	Approved      bool   `json:"approved"`
	Reason        string `json:"reason,omitempty"`
}

// agentRunPayload maps agentruntime.Run onto the site-wide snake_case JSON
// contract; the coordinator struct itself has no json tags and would otherwise
// serialize PascalCase field names.
type agentRunPayload struct {
	ID                   string    `json:"id"`
	OrganizationID       string    `json:"organization_id"`
	WorkspaceID          string    `json:"workspace_id"`
	AgentApplicationID   string    `json:"agent_application_id"`
	SessionID            string    `json:"session_id"`
	ModelEndpointID      string    `json:"model_endpoint_id"`
	ModelRevision        int64     `json:"model_revision"`
	RuntimeMode          string    `json:"runtime_mode"`
	WorkflowKey          string    `json:"workflow_key,omitempty"`
	WorkflowCodeVersion  int64     `json:"workflow_code_version"`
	Status               string    `json:"status"`
	CurrentNode          string    `json:"current_node,omitempty"`
	CheckpointID         string    `json:"checkpoint_id,omitempty"`
	CheckpointSequence   int64     `json:"checkpoint_sequence"`
	WaitingInteractionID string    `json:"waiting_interaction_id,omitempty"`
	CreatedAt            time.Time `json:"created_at"`
}

func agentRunJSON(run agentruntime.Run) agentRunPayload {
	return agentRunPayload{
		ID: run.ID, OrganizationID: run.OrganizationID, WorkspaceID: run.WorkspaceID,
		AgentApplicationID: run.AgentApplicationID, SessionID: run.SessionID,
		ModelEndpointID: run.ModelEndpointID, ModelRevision: run.ModelRevision,
		RuntimeMode: run.RuntimeMode, WorkflowKey: run.WorkflowKey,
		WorkflowCodeVersion: run.WorkflowCodeVersion, Status: run.Status,
		CurrentNode: run.CurrentNode, CheckpointID: run.CheckpointID,
		CheckpointSequence: run.CheckpointSequence, WaitingInteractionID: run.WaitingInteractionID,
		CreatedAt: run.CreatedAt,
	}
}

func agentSessionRuns(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		principal, ok := requireMemberSession(w, r, deps)
		if !ok {
			return
		}
		idempotencyKey, ok := requestIdempotencyKey(w, r)
		if !ok {
			writeError(w, http.StatusUnprocessableEntity, "idempotency_key_invalid")
			return
		}
		if !requirePathUUID(w, r.PathValue("sessionId")) {
			return
		}
		var input createAgentRunRequest
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 128*1024))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&input) != nil || strings.TrimSpace(input.Query) == "" || len([]rune(input.Query)) > 10000 || len(input.History) > 20 {
			writeError(w, http.StatusUnprocessableEntity, "invalid_agent_run_request")
			return
		}
		for _, message := range input.History {
			if (message.Role != "user" && message.Role != "assistant") || strings.TrimSpace(message.Content) == "" || len([]rune(message.Content)) > 10000 {
				writeError(w, http.StatusUnprocessableEntity, "invalid_agent_run_history")
				return
			}
		}
		var organizationID, applicationID, agentUserID, endpointID string
		var revision int64
		err := deps.Store.Pool.QueryRow(r.Context(), `
			SELECT s.organization_id::text, s.agent_application_id::text, s.bound_agent_user_id::text,
			       aa.model_endpoint_id::text, e.current_revision
			FROM integration.agent_sessions s
			JOIN integration.agent_applications aa ON aa.id = s.agent_application_id
			JOIN integration.model_endpoints e ON e.id = aa.model_endpoint_id AND e.organization_id = aa.organization_id
			JOIN content.workspace_agent_applications wa ON wa.organization_id = s.organization_id
			  AND wa.workspace_id = $4::uuid AND wa.agent_application_id = aa.id AND wa.enabled = true
			JOIN content.workspace_members wm ON wm.workspace_id = wa.workspace_id AND wm.user_id = $3::uuid
			WHERE s.id = $1::uuid AND s.organization_id = $2::uuid AND s.initiator_user_id = $3::uuid
			  AND s.status = 'active' AND s.expires_at > now() AND aa.status = 'active'
			  AND aa.runtime_mode = 'react' AND e.status = 'active'
		`, r.PathValue("sessionId"), principal.OrganizationID, principal.UserID, input.WorkspaceID).Scan(
			&organizationID, &applicationID, &agentUserID, &endpointID, &revision,
		)
		if err != nil {
			writeError(w, http.StatusForbidden, "agent_react_run_not_allowed")
			return
		}
		coordinator := agentruntime.Coordinator{Store: deps.Store}
		run, err := coordinator.Create(r.Context(), agentruntime.RunRequest{
			OrganizationID: organizationID, WorkspaceID: input.WorkspaceID, PrincipalID: principal.UserID,
			AgentUserID: agentUserID, AgentApplicationID: applicationID, SessionID: r.PathValue("sessionId"),
			ModelEndpointID: endpointID, ModelRevision: revision, RuntimeMode: "react", Source: "chat",
			Input:            map[string]any{"query": strings.TrimSpace(input.Query), "history": input.History},
			ExecutionOptions: map[string]any{"streaming": true}, IdempotencyKey: idempotencyKey,
		})
		if errors.Is(err, agentruntime.ErrInvalidRunRequest) {
			writeError(w, http.StatusUnprocessableEntity, "invalid_agent_run_request")
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "agent_run_create_failed")
			return
		}
		writeJSON(w, http.StatusAccepted, agentRunJSON(run))
	}
}

func agentRunResource(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		principal, ok := requireMemberSession(w, r, deps)
		if !ok {
			return
		}
		if !requirePathUUID(w, r.PathValue("runId")) {
			return
		}
		if !authorizeAgentRun(r, deps, principal, r.PathValue("runId")) {
			writeError(w, http.StatusNotFound, "agent_run_not_found")
			return
		}
		run, err := (agentruntime.Coordinator{Store: deps.Store}).Get(r.Context(), principal.OrganizationID, r.PathValue("runId"))
		if err != nil {
			writeError(w, http.StatusNotFound, "agent_run_not_found")
			return
		}
		var output []byte
		_ = deps.Store.Pool.QueryRow(r.Context(), `SELECT output_snapshot FROM automation.runs WHERE id = $1::uuid`, run.ID).Scan(&output)
		var outputSnapshot map[string]any
		_ = json.Unmarshal(output, &outputSnapshot)
		writeJSON(w, http.StatusOK, map[string]any{"run": agentRunJSON(run), "output": outputSnapshot})
	}
}

func resumeAgentRun(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		principal, ok := requireMemberSession(w, r, deps)
		if !ok {
			return
		}
		if !requirePathUUID(w, r.PathValue("runId")) {
			return
		}
		if !authorizeAgentRun(r, deps, principal, r.PathValue("runId")) {
			writeError(w, http.StatusNotFound, "agent_run_not_found")
			return
		}
		var input resumeAgentRunRequest
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16*1024))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&input) != nil || strings.TrimSpace(input.InteractionID) == "" || len([]rune(input.Reason)) > 500 {
			writeError(w, http.StatusUnprocessableEntity, "invalid_agent_run_resume")
			return
		}
		status := "rejected"
		if input.Approved {
			status = "approved"
		}
		run, err := (agentruntime.Coordinator{Store: deps.Store}).ResolveInteraction(r.Context(), principal.OrganizationID,
			r.PathValue("runId"), input.InteractionID, principal.UserID, status, map[string]any{"reason": strings.TrimSpace(input.Reason)})
		if errors.Is(err, agentruntime.ErrInteractionState) {
			writeError(w, http.StatusConflict, "agent_interaction_not_pending")
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "agent_run_resume_failed")
			return
		}
		writeJSON(w, http.StatusAccepted, agentRunJSON(run))
	}
}

func cancelAgentRun(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		principal, ok := requireMemberSession(w, r, deps)
		if !ok {
			return
		}
		if !requirePathUUID(w, r.PathValue("runId")) {
			return
		}
		if !authorizeAgentRun(r, deps, principal, r.PathValue("runId")) {
			writeError(w, http.StatusNotFound, "agent_run_not_found")
			return
		}
		if err := (agentruntime.Coordinator{Store: deps.Store}).RequestCancel(r.Context(), principal.OrganizationID, r.PathValue("runId"), "cancelled by user"); err != nil {
			writeError(w, http.StatusConflict, "agent_run_cancel_failed")
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func agentRunEvents(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		principal, ok := requireMemberSession(w, r, deps)
		if !ok {
			return
		}
		if !requirePathUUID(w, r.PathValue("runId")) {
			return
		}
		if !authorizeAgentRun(r, deps, principal, r.PathValue("runId")) {
			writeError(w, http.StatusNotFound, "agent_run_not_found")
			return
		}
		taskRunEventsAuthorized(deps, principal.OrganizationID, w, r)
	}
}

func authorizeAgentRun(r *http.Request, deps Dependencies, principal auth.Principal, runID string) bool {
	var allowed bool
	err := deps.Store.Pool.QueryRow(r.Context(), `
		SELECT EXISTS (
			SELECT 1 FROM automation.runs run
			JOIN integration.agent_sessions s ON s.id = run.session_id
			JOIN content.workspace_members wm ON wm.workspace_id = run.workspace_id AND wm.user_id = $2::uuid
			WHERE run.id = $1::uuid AND run.organization_id = $3::uuid AND run.runtime_mode = 'react'
			  AND s.initiator_user_id = $2::uuid
		)
	`, runID, principal.UserID, principal.OrganizationID).Scan(&allowed)
	return err == nil && allowed
}
