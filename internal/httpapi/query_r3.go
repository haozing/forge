package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	agentquery "agentchunzhi/internal/query"
)

func unifiedQueryR3(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		principal, err := deps.Authenticator.Authenticate(r.Context(), r)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		if !requireAgentCapability(w, principal, "query.read") {
			return
		}
		var input unifiedQueryRequest
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64*1024))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&input); err != nil {
			writeError(w, http.StatusUnprocessableEntity, "invalid_query_request")
			return
		}
		queryText := strings.TrimSpace(input.Query)
		allowed, err := deps.ScopeResolver.AllowedModelIDs(r.Context(), principal, "asset.read")
		if err != nil {
			writeError(w, http.StatusInternalServerError, "authorization_scope_failed")
			return
		}
		modelIDs := append([]string(nil), input.ModelIDs...)
		result, err := deps.QueryService.Query(r.Context(), principal, agentquery.QueryRequest{Mode: input.Mode, Query: queryText, ModelIDs: modelIDs, TopK: input.TopK, Cursor: input.Cursor, Filters: input.Filters}, allowed)
		if errors.Is(err, agentquery.ErrModelAccessDenied) {
			writeError(w, http.StatusForbidden, "model_access_denied")
			return
		}
		if errors.Is(err, agentquery.ErrCursorInvalid) {
			writeError(w, http.StatusUnprocessableEntity, "invalid_cursor")
			return
		}
		if errors.Is(err, agentquery.ErrInvalidQuery) {
			writeError(w, http.StatusUnprocessableEntity, "invalid_query_request")
			return
		}
		if errors.Is(err, agentquery.ErrVectorUnavailable) {
			writeError(w, http.StatusServiceUnavailable, "vector_retrieval_unavailable")
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "query_failed")
			return
		}
		writeJSON(w, http.StatusOK, result)
	}
}
