package httpapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"agentchunzhi/internal/agentapp"
	"agentchunzhi/internal/content"
)

func conversationChat(deps Dependencies, stream bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		member, ok := requireMemberSession(w, r, deps)
		if !ok {
			return
		}
		key, ok := requiredIdempotencyKey(r)
		if !ok {
			writeError(w, http.StatusUnprocessableEntity, "idempotency_key_required")
			return
		}
		conversationID := r.PathValue("conversationId")
		conversation, err := deps.ConversationService.GetConversation(r.Context(), member, conversationID)
		if errors.Is(err, content.ErrNotFound) {
			writeError(w, http.StatusNotFound, "conversation_not_found")
			return
		}
		if err != nil {
			writeError(w, http.StatusUnprocessableEntity, "invalid_conversation_id")
			return
		}
		allowed, err := deps.ScopeResolver.AllowedAgentApplicationIDs(r.Context(), member, "agent.use")
		if err != nil {
			writeError(w, http.StatusInternalServerError, "authorization_scope_failed")
			return
		}
		if conversation.AgentApplicationID == "" {
			writeError(w, http.StatusConflict, "conversation_agent_application_missing")
			return
		}
		session, err := deps.AgentAppService.Start(r.Context(), member, allowed, conversation.AgentApplicationID, key)
		if errors.Is(err, agentapp.ErrNotFound) {
			writeError(w, http.StatusForbidden, "agent_application_not_allowed")
			return
		}
		if err != nil {
			writeError(w, http.StatusConflict, "agent_session_start_failed")
			return
		}
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 128*1024))
		if err != nil {
			writeError(w, http.StatusUnprocessableEntity, "invalid_agent_chat_request")
			return
		}
		var payload map[string]any
		if err := json.Unmarshal(body, &payload); err != nil {
			writeError(w, http.StatusUnprocessableEntity, "invalid_agent_chat_request")
			return
		}
		payload["conversation_id"] = conversationID
		encoded, _ := json.Marshal(payload)
		r.Body = io.NopCloser(bytes.NewReader(encoded))
		r.ContentLength = int64(len(encoded))
		r.SetPathValue("sessionId", session.SessionID)
		if stream {
			streamAgentSession(deps)(w, r)
			return
		}
		chatAgentSession(deps)(w, r)
	}
}
