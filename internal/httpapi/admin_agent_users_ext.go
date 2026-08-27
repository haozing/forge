package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"agentchunzhi/internal/admin"
	agentquery "agentchunzhi/internal/query"
)

// POST /api/admin/agent-users/{agentUserId}/api-keys/revoke-all
// Revokes every active key of the Agent user without issuing a replacement.
func revokeAllAgentAPIKeys(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		principal, ok := requireAgentManagement(w, r, deps)
		if !ok {
			return
		}
		agentUserID := r.PathValue("agentUserId")
		if !agentquery.ValidUUID(agentUserID) {
			writeError(w, http.StatusUnprocessableEntity, "invalid_agent_user_id")
			return
		}
		idempotencyKey, ok := requestIdempotencyKey(w, r)
		if !ok {
			writeError(w, http.StatusUnprocessableEntity, "idempotency_key_invalid")
			return
		}
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 16*1024))
		if err != nil {
			writeError(w, http.StatusUnprocessableEntity, "invalid_agent_api_key_revoke_request")
			return
		}
		if len(strings.TrimSpace(string(body))) > 0 {
			var input struct{}
			decoder := json.NewDecoder(strings.NewReader(string(body)))
			decoder.DisallowUnknownFields()
			if err := decoder.Decode(&input); err != nil {
				writeError(w, http.StatusUnprocessableEntity, "invalid_agent_api_key_revoke_request")
				return
			}
		}
		result, err := deps.AdminService.RevokeAllAgentAPIKeys(r.Context(), principal, admin.RevokeAllAPIKeysInput{
			AgentUserID:    agentUserID,
			IdempotencyKey: idempotencyKey,
		})
		if errors.Is(err, admin.ErrInvalidInput) {
			writeError(w, http.StatusUnprocessableEntity, "invalid_agent_api_key_revoke_request")
			return
		}
		if errors.Is(err, admin.ErrAgentNotFound) {
			writeError(w, http.StatusNotFound, "agent_user_not_found")
			return
		}
		if errors.Is(err, admin.ErrConflict) {
			writeError(w, http.StatusConflict, "agent_api_key_revocation_conflict")
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "agent_api_key_revocation_failed")
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, http.StatusOK, result)
	}
}

// GET /api/admin/agent-users/{agentUserId}/onboarding
// Returns the §6.8.3 integration package: base URL, identity, auth scheme,
// OpenAPI location, runtime mode, capabilities, reachable resource models,
// annotated operation catalog and a runnable sample request.
func getAgentUserOnboarding(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		principal, ok := requireAgentManagement(w, r, deps)
		if !ok {
			return
		}
		agentUserID := r.PathValue("agentUserId")
		if !agentquery.ValidUUID(agentUserID) {
			writeError(w, http.StatusUnprocessableEntity, "invalid_agent_user_id")
			return
		}
		result, err := deps.AdminService.GetAgentOnboarding(r.Context(), principal, agentUserID, requestBaseURL(r))
		if errors.Is(err, admin.ErrInvalidInput) {
			writeError(w, http.StatusUnprocessableEntity, "invalid_agent_user_id")
			return
		}
		if errors.Is(err, admin.ErrAgentNotFound) {
			writeError(w, http.StatusNotFound, "agent_user_not_found")
			return
		}
		if errors.Is(err, admin.ErrAgentNotAllowed) {
			writeError(w, http.StatusForbidden, "agent_user_not_allowed")
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "agent_onboarding_failed")
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, http.StatusOK, result)
	}
}

func requestBaseURL(r *http.Request) string {
	scheme := "http"
	if forwarded := strings.TrimSpace(r.Header.Get("X-Forwarded-Proto")); forwarded != "" {
		scheme = forwarded
	} else if r.TLS != nil {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}
