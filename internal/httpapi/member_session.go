package httpapi

// member_session.go — shared member-session and workspace error mapping
// helpers still used by the legacy routes scheduled for retirement in phases
// 2-4. The phase 1 identity/workspace handlers they served are retired; see
// docs/route-retirement-ledger.md.

import (
	"errors"
	"log"
	"net/http"

	"agentchunzhi/internal/auth"
	"agentchunzhi/internal/workspace"
)

func requireMemberSession(w http.ResponseWriter, r *http.Request, deps Dependencies) (auth.Principal, bool) {
	principal, err := deps.SessionService.Authenticate(r.Context(), r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return auth.Principal{}, false
	}
	if principal.UserType != "member" {
		writeError(w, http.StatusForbidden, "member_required")
		return auth.Principal{}, false
	}
	return principal, true
}

func writeWorkspaceError(w http.ResponseWriter, err error, fallback string) {
	switch {
	case errors.Is(err, workspace.ErrInvalidInput):
		writeError(w, http.StatusUnprocessableEntity, "validation_failed")
	case errors.Is(err, workspace.ErrInvalidEmail):
		writeError(w, http.StatusUnprocessableEntity, "invalid_email")
	case errors.Is(err, workspace.ErrAmbiguousMember):
		writeError(w, http.StatusConflict, "workspace_member_ambiguous")
	case errors.Is(err, workspace.ErrForbidden):
		writeError(w, http.StatusForbidden, "workspace_access_denied")
	case errors.Is(err, workspace.ErrNotFound):
		writeError(w, http.StatusNotFound, "workspace_not_found")
	case errors.Is(err, workspace.ErrConflict):
		writeError(w, http.StatusConflict, "workspace_conflict")
	default:
		log.Printf("workspace request failed: %v", err)
		writeError(w, http.StatusInternalServerError, fallback)
	}
}

func listWorkspaceAgentApplications(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		principal, ok := requireMemberSession(w, r, deps)
		if !ok {
			return
		}
		if !requirePathUUID(w, r.PathValue("workspaceId")) || !rejectUnknownWorkspace(w, r, deps, principal) {
			return
		}
		items, err := deps.WorkspaceService.AgentApplications(r.Context(), principal, r.PathValue("workspaceId"))
		if err != nil {
			writeWorkspaceError(w, err, "workspace_agent_applications_failed")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"items": items, "has_more": false})
	}
}
