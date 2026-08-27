package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	agentquery "agentchunzhi/internal/query"
)

// unionStrings merges ID lists from doc-style (data_models) and internal-style
// params, preserving order and dropping duplicates.
func unionStrings(lists ...[]string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0)
	for _, list := range lists {
		for _, value := range list {
			if value == "" {
				continue
			}
			if _, ok := seen[value]; ok {
				continue
			}
			seen[value] = struct{}{}
			out = append(out, value)
		}
	}
	return out
}

type memberQueryRequest struct {
	Mode              string         `json:"mode"`
	Query             string         `json:"query"`
	ResourceModelIDs  []string       `json:"resource_model_ids"`
	DataModels        []string       `json:"data_models"`
	Visibility        []string       `json:"visibility"`
	PublicationStatus []string       `json:"publication_status"`
	Filters           map[string]any `json:"filters"`
	TopK              int            `json:"top_k"`
	Cursor            string         `json:"cursor"`
}

func memberQuery(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		principal, ok := requireMemberSession(w, r, deps)
		if !ok {
			return
		}
		started := time.Now()
		var input memberQueryRequest
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 128*1024))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&input); err != nil {
			writeError(w, http.StatusUnprocessableEntity, "validation_failed")
			return
		}
		result, err := deps.QueryService.QueryMember(r.Context(), principal, agentquery.MemberQueryRequest{
			WorkspaceID:       r.PathValue("workspaceId"),
			Mode:              input.Mode,
			Query:             input.Query,
			ModelIDs:          unionStrings(input.ResourceModelIDs, input.DataModels),
			Visibility:        input.Visibility,
			PublicationStatus: input.PublicationStatus,
			Filters:           input.Filters,
			TopK:              input.TopK,
			Cursor:            input.Cursor,
		})
		queryHash := hashQuery(input.Mode + "\x00" + input.Query + "\x00" + r.PathValue("workspaceId"))
		if err != nil {
			if deps.Store != nil {
				_ = deps.Store.RecordQueryLog(r.Context(), principal.OrganizationID, principal.UserID, "/api/frontend/workspaces/{workspaceId}/query", queryHash, 0, int(time.Since(started).Milliseconds()), "failed")
			}
		} else if deps.Store != nil {
			_ = deps.Store.RecordQueryLog(r.Context(), principal.OrganizationID, principal.UserID, "/api/frontend/workspaces/{workspaceId}/query", queryHash, len(result.Items), int(time.Since(started).Milliseconds()), "succeeded")
		}
		switch {
		case errors.Is(err, agentquery.ErrWorkspaceMissing):
			writeError(w, http.StatusNotFound, "workspace_not_found")
			return
		case errors.Is(err, agentquery.ErrModelAccessDenied):
			writeError(w, http.StatusForbidden, "workspace_or_model_access_denied")
			return
		case errors.Is(err, agentquery.ErrCursorInvalid):
			writeError(w, http.StatusUnprocessableEntity, "invalid_cursor")
			return
		case errors.Is(err, agentquery.ErrInvalidQuery):
			writeError(w, http.StatusUnprocessableEntity, "validation_failed")
			return
		case errors.Is(err, agentquery.ErrVectorUnavailable):
			writeError(w, http.StatusServiceUnavailable, "vector_retrieval_unavailable")
			return
		case err != nil:
			writeError(w, http.StatusInternalServerError, "query_failed")
			return
		}
		writeJSON(w, http.StatusOK, result)
	}
}
