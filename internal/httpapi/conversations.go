package httpapi

import (
	"errors"
	"net/http"
	"strconv"

	"agentchunzhi/internal/conversation"
)

func listConversations(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		principal, ok := requireMemberSession(w, r, deps)
		if !ok {
			return
		}
		limit := 50
		if raw := r.URL.Query().Get("limit"); raw != "" {
			parsed, parseErr := strconv.Atoi(raw)
			if parseErr != nil || parsed < 1 || parsed > 100 {
				writeError(w, http.StatusUnprocessableEntity, "invalid_limit")
				return
			}
			limit = parsed
		}
		items, hasMore, nextCursor, err := deps.ConversationService.ListPage(r.Context(), principal, r.PathValue("workspaceId"), r.URL.Query().Get("q"), limit, r.URL.Query().Get("cursor"))
		if errors.Is(err, conversation.ErrForbidden) {
			writeError(w, http.StatusForbidden, "workspace_access_denied")
			return
		}
		if errors.Is(err, conversation.ErrInvalidCursor) {
			writeError(w, http.StatusUnprocessableEntity, "invalid_cursor")
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "conversation_list_failed")
			return
		}
		response := map[string]any{"items": items, "has_more": hasMore}
		if hasMore {
			response["next_cursor"] = nextCursor
		}
		writeData(w, r, http.StatusOK, response)
	}
}

func conversationsCollection(deps Dependencies) http.HandlerFunc {
	list := listConversations(deps)
	create := createConversation(deps)
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			list(w, r)
			return
		}
		create(w, r)
	}
}

func conversationChildren(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		principal, ok := requireMemberSession(w, r, deps)
		if !ok {
			return
		}
		items, err := deps.ConversationService.ListChildren(r.Context(), principal, r.PathValue("conversationId"))
		if errors.Is(err, conversation.ErrInvalidID) {
			writeError(w, http.StatusUnprocessableEntity, "invalid_conversation_id")
			return
		}
		if errors.Is(err, conversation.ErrNotFound) {
			writeError(w, http.StatusNotFound, "conversation_not_found")
			return
		}
		if errors.Is(err, conversation.ErrForbidden) {
			writeError(w, http.StatusForbidden, "workspace_access_denied")
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "conversation_children_failed")
			return
		}
		writeData(w, r, http.StatusOK, map[string]any{"items": items})
	}
}
