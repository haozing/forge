package httpapi

import (
	"net/http"
	"strconv"
)

func workspaceStats(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		principal, ok := requireMemberSession(w, r, deps)
		if !ok {
			return
		}
		result, err := deps.WorkspaceService.Stats(r.Context(), principal, r.PathValue("workspaceId"))
		if err != nil {
			writeWorkspaceError(w, err, "workspace_stats_failed")
			return
		}
		writeJSON(w, http.StatusOK, result)
	}
}

func workspaceActivity(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		principal, ok := requireMemberSession(w, r, deps)
		if !ok {
			return
		}
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		page, err := deps.WorkspaceService.Activity(r.Context(), principal, r.PathValue("workspaceId"), r.URL.Query().Get("cursor"), limit)
		if err != nil {
			writeWorkspaceError(w, err, "workspace_activity_failed")
			return
		}
		writeJSON(w, http.StatusOK, page)
	}
}
