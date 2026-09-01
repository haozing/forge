package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	adminservice "agentchunzhi/internal/admin"
	"agentchunzhi/internal/agentapp"
	agentquery "agentchunzhi/internal/query"

	"github.com/jackc/pgx/v5"
)

func writeAgentApplicationError(w http.ResponseWriter, err error, fallback string) {
	switch {
	case errors.Is(err, adminservice.ErrInvalidInput),
		errors.Is(err, adminservice.ErrApplicationListInvalidInput), errors.Is(err, adminservice.ErrApplicationStatusInvalidInput), errors.Is(err, adminservice.ErrApplicationUpdateInvalidInput):
		writeError(w, http.StatusUnprocessableEntity, "validation_failed")
	case errors.Is(err, adminservice.ErrApplicationNotFound):
		writeError(w, http.StatusNotFound, "agent_application_not_found")
	case errors.Is(err, adminservice.ErrConflict):
		writeError(w, http.StatusConflict, "agent_application_conflict")
	case errors.Is(err, agentapp.ErrInvalidInput):
		writeError(w, http.StatusUnprocessableEntity, "validation_failed")
	case errors.Is(err, agentapp.ErrNotFound):
		writeError(w, http.StatusNotFound, "agent_application_not_found")
	default:
		writeError(w, http.StatusInternalServerError, fallback)
	}
}

func agentApplications(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		principal, ok := requireMemberSession(w, r, deps)
		if !ok {
			return
		}
		items, err := deps.AdminService.ListAgentApplications(r.Context(), principal, 100)
		if err != nil {
			writeAgentApplicationError(w, err, "agent_application_list_failed")
			return
		}
		writeJSON(w, http.StatusOK, items)
	}
}

func workspaceAgentApplications(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			listWorkspaceAgentApplications(deps)(w, r)
			return
		}
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		principal, ok := requireMemberSession(w, r, deps)
		if !ok {
			return
		}
		if _, err := deps.WorkspacePolicy.Require(r.Context(), principal, r.PathValue("workspaceId"), "", "workspace.manage"); err != nil {
			writeError(w, http.StatusForbidden, "workspace_access_denied")
			return
		}
		key, ok := requestIdempotencyKey(w, r)
		if !ok {
			writeError(w, http.StatusUnprocessableEntity, "idempotency_key_invalid")
			return
		}
		var input registerAgentRequest
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64*1024))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&input); err != nil {
			writeError(w, http.StatusUnprocessableEntity, "validation_failed")
			return
		}
		var expiresAt *time.Time
		if input.ExpiresAt != nil && strings.TrimSpace(*input.ExpiresAt) != "" {
			parsed, err := time.Parse(time.RFC3339, strings.TrimSpace(*input.ExpiresAt))
			if err != nil {
				writeError(w, http.StatusUnprocessableEntity, "validation_failed")
				return
			}
			expiresAt = &parsed
		}
		created, err := deps.AdminService.RegisterAgent(r.Context(), principal, adminservice.RegisterAgentInput{
			DisplayName: input.DisplayName, ApiKeyName: input.ApiKeyName, ApplicationName: input.ApplicationName,
			ModelEndpointID: input.ModelEndpointID, RuntimeMode: input.RuntimeMode, WorkflowKey: input.WorkflowKey,
			Capabilities: input.Capabilities, AnswerPosture: input.AnswerPosture,
			ExpiresAt:    expiresAt, IdempotencyKey: key,
		})
		if err != nil {
			writeAgentApplicationError(w, err, "agent_application_create_failed")
			return
		}
		if _, err := deps.Store.Pool.Exec(r.Context(), `
                        INSERT INTO content.workspace_agent_applications (organization_id, workspace_id, agent_application_id, created_by)
                        VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid)
                        ON CONFLICT (workspace_id, agent_application_id) DO UPDATE SET enabled = true
                `, principal.OrganizationID, r.PathValue("workspaceId"), created.AgentApplicationID, principal.UserID); err != nil {
			writeError(w, http.StatusInternalServerError, "workspace_agent_application_link_failed")
			return
		}
		// A workspace-created application must be usable immediately. Grant its
		// bound identity read AND query.execute on the workspace's default
		// resource model, scoped to the workspace itself (never org-wide): the
		// RAG retrieval scope requires query.execute, so a read-only grant
		// would make every chat fail with query_scope_forbidden. Owners can
		// still narrow or replace this policy through admin controls.
		if _, err := deps.Store.Pool.Exec(r.Context(), `
                        INSERT INTO content.agent_access_policies
                                (organization_id, workspace_id, agent_user_id, resource_model_id, actions, created_by)
                        SELECT w.organization_id, w.id, $3::uuid, w.default_resource_model_id,
                               ARRAY['read', 'query.execute']::text[], $4::uuid
                        FROM content.workspaces w
                        WHERE w.organization_id = $1::uuid AND w.id = $2::uuid
                          AND w.default_resource_model_id IS NOT NULL
                        ON CONFLICT (organization_id, agent_user_id, workspace_id, resource_model_id)
                        DO UPDATE SET actions = ARRAY['read', 'query.execute']::text[], updated_at = now()
		`, principal.OrganizationID, r.PathValue("workspaceId"), created.AgentUserID, principal.UserID); err != nil {
			writeError(w, http.StatusInternalServerError, "workspace_agent_application_policy_failed")
			return
		}
		if _, err := deps.Store.Pool.Exec(r.Context(), `
			INSERT INTO "authorization".policy_revisions (organization_id, revision, updated_at)
			VALUES ($1::uuid, 2, now())
			ON CONFLICT (organization_id) DO UPDATE
			SET revision = "authorization".policy_revisions.revision + 1, updated_at = now()
		`, principal.OrganizationID); err != nil {
			writeError(w, http.StatusInternalServerError, "workspace_agent_application_policy_revision_failed")
			return
		}
		item, err := deps.AdminService.GetAgentApplication(r.Context(), principal, created.AgentApplicationID)
		if err != nil {
			writeAgentApplicationError(w, err, "agent_application_load_failed")
			return
		}
		// The raw API key exists exactly once, at registration; merge it into
		// the re-read application view so the caller can actually use the
		// credential. It is never returned again afterwards.
		payload := map[string]any{}
		if raw, err := json.Marshal(item); err == nil {
			_ = json.Unmarshal(raw, &payload)
		}
		payload["api_key"] = created.ApiKey
		payload["api_key_prefix"] = created.ApiKeyPrefix
		writeJSON(w, http.StatusCreated, payload)
	}
}

func agentApplicationResource(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		principal, ok := requireMemberSession(w, r, deps)
		if !ok {
			return
		}
		item, err := deps.AdminService.GetAgentApplication(r.Context(), principal, r.PathValue("applicationId"))
		if err != nil {
			writeAgentApplicationError(w, err, "agent_application_load_failed")
			return
		}
		if r.Method == http.MethodGet {
			writeJSON(w, http.StatusOK, item)
			return
		}
		if r.Method != http.MethodPatch {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		key, ok := requestIdempotencyKey(w, r)
		if !ok {
			writeError(w, http.StatusUnprocessableEntity, "idempotency_key_invalid")
			return
		}
		var patch struct {
			Name            *string   `json:"name"`
			ModelEndpointID *string   `json:"model_endpoint_id"`
			RuntimeMode     *string   `json:"runtime_mode"`
			WorkflowKey     *string   `json:"workflow_key"`
			Capabilities    *[]string `json:"capabilities"`
			AnswerPosture   *string   `json:"answer_posture"`
		}
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32*1024))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&patch); err != nil || (patch.Name == nil && patch.ModelEndpointID == nil && patch.RuntimeMode == nil && patch.WorkflowKey == nil && patch.Capabilities == nil && patch.AnswerPosture == nil) {
			writeError(w, http.StatusUnprocessableEntity, "validation_failed")
			return
		}
		if _, err := deps.AdminService.UpdateAgentApplication(r.Context(), principal, adminservice.UpdateAgentApplicationInput{
			ApplicationID: item.ID, Name: patch.Name, ModelEndpointID: patch.ModelEndpointID,
			RuntimeMode: patch.RuntimeMode, WorkflowKey: patch.WorkflowKey,
			Capabilities: patch.Capabilities, AnswerPosture: patch.AnswerPosture, IdempotencyKey: key,
		}); err != nil {
			writeAgentApplicationError(w, err, "agent_application_update_failed")
			return
		}
		item, err = deps.AdminService.GetAgentApplication(r.Context(), principal, item.ID)
		if err != nil {
			writeAgentApplicationError(w, err, "agent_application_load_failed")
			return
		}
		writeJSON(w, http.StatusOK, item)
	}
}

func agentApplicationStatus(deps Dependencies, status string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		principal, ok := requireMemberSession(w, r, deps)
		if !ok {
			return
		}
		key, ok := requestIdempotencyKey(w, r)
		if !ok {
			writeError(w, http.StatusUnprocessableEntity, "idempotency_key_invalid")
			return
		}
		result, err := deps.AdminService.SetAgentApplicationStatus(r.Context(), principal, adminservice.SetApplicationStatusInput{ApplicationID: r.PathValue("applicationId"), Status: status, IdempotencyKey: key})
		if err != nil {
			writeAgentApplicationError(w, err, "agent_application_status_failed")
			return
		}
		writeJSON(w, http.StatusOK, result)
	}
}

func agentSessionResource(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		member, ok := requireMemberSession(w, r, deps)
		if !ok {
			return
		}
		if r.Method == http.MethodGet {
			if !agentquery.ValidUUID(r.PathValue("sessionId")) {
				writeError(w, http.StatusUnprocessableEntity, "invalid_session_id")
				return
			}
			if deps.Store == nil || deps.Store.Pool == nil {
				writeError(w, http.StatusInternalServerError, "agent_session_load_failed")
				return
			}
			var sessionID, applicationID, status, modelEndpointID, providerType, modelName, runtimeMode string
			var modelRevision int64
			var expiresAt, createdAt time.Time
			var completedAt *time.Time
			err := deps.Store.Pool.QueryRow(r.Context(), `
				SELECT s.id::text, s.agent_application_id::text, s.status,
				       aa.model_endpoint_id::text, me.current_revision, mer.provider_type,
				       mer.model_name, aa.runtime_mode,
				       s.expires_at, s.created_at, s.completed_at
				FROM integration.agent_sessions s
				JOIN integration.agent_applications aa ON aa.id = s.agent_application_id
				JOIN integration.model_endpoints me ON me.id = aa.model_endpoint_id
				JOIN integration.model_endpoint_revisions mer ON mer.model_endpoint_id = me.id AND mer.revision = me.current_revision
				WHERE s.id = $1::uuid AND s.organization_id = $2::uuid AND s.initiator_user_id = $3::uuid
			`, r.PathValue("sessionId"), member.OrganizationID, member.UserID).Scan(
				&sessionID, &applicationID, &status, &modelEndpointID, &modelRevision,
				&providerType, &modelName, &runtimeMode, &expiresAt, &createdAt, &completedAt)
			if errors.Is(err, pgx.ErrNoRows) {
				writeError(w, http.StatusNotFound, "agent_session_not_found")
				return
			}
			if err != nil {
				writeError(w, http.StatusInternalServerError, "agent_session_load_failed")
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{
				"session_id": sessionID, "agent_application_id": applicationID, "status": status,
				"model_endpoint_id": modelEndpointID, "model_endpoint_revision": modelRevision,
				"provider_type": providerType, "model_name": modelName, "runtime_mode": runtimeMode,
				"expires_at": expiresAt, "created_at": createdAt, "completed_at": completedAt,
			})
			return
		}
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
	}
}

func cancelAgentSession(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		member, ok := requireMemberSession(w, r, deps)
		if !ok {
			return
		}
		if _, ok := requestIdempotencyKey(w, r); !ok {
			writeError(w, http.StatusUnprocessableEntity, "idempotency_key_invalid")
			return
		}
		if !agentquery.ValidUUID(r.PathValue("sessionId")) {
			writeError(w, http.StatusUnprocessableEntity, "invalid_session_id")
			return
		}
		if deps.Store == nil || deps.Store.Pool == nil {
			writeError(w, http.StatusInternalServerError, "agent_session_cancel_failed")
			return
		}
		result, err := deps.Store.Pool.Exec(r.Context(), `UPDATE integration.agent_sessions SET status = 'revoked', completed_at = now() WHERE id = $1::uuid AND organization_id = $2::uuid AND initiator_user_id = $3::uuid AND status = 'active'`, r.PathValue("sessionId"), member.OrganizationID, member.UserID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "agent_session_cancel_failed")
			return
		}
		if result.RowsAffected() == 0 {
			writeError(w, http.StatusNotFound, "agent_session_not_found")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"session_id": r.PathValue("sessionId"), "status": "revoked"})
	}
}
