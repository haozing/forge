package httpapi

// activity.go — the read surface for the audit trail every governance and
// content write already records (audit.audit_log used to be write-only:
// appended in invitation/member/export/deletion flows, never queried).
// The workspace service has had a cursor-paginated Activity query since the
// phase 1 build; this handler finally wires it to HTTP behind audit.read,
// the same admin gate the query-execution audit uses.

import (
	"net/http"
	"strconv"

	"agentchunzhi/internal/authz"
)

// WorkspaceActivity serves GET /api/workspaces/{workspaceId}/audit. The
// name is deliberately fresh: the legacy /activity and /audit-logs paths
// were retired with the unversioned-tree cleanup and stay pinned dead.
func WorkspaceActivity(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		principal, _, ok := requireWorkspaceSession(w, r, deps)
		if !ok {
			return
		}
		workspaceID := r.PathValue("workspaceId")
		if !requirePathUUID(w, workspaceID) {
			return
		}
		if !requireWorkspaceAction(w, r, deps, principal, workspaceID, authz.ActionAuditRead) {
			return
		}
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		page, err := deps.WorkspaceService.Activity(r.Context(), principal, workspaceID, r.URL.Query().Get("cursor"), limit)
		if err != nil {
			DomainError(w, err)
			return
		}
		writeData(w, r, http.StatusOK, page)
	}
}
