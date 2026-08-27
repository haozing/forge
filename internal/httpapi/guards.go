package httpapi

import (
	"context"
	"net/http"
	"strings"

	"agentchunzhi/internal/auth"
	agentquery "agentchunzhi/internal/query"
)

// rejectBlankText answers 422 blank_content when an optional text field is
// supplied but contains only whitespace after trimming.
func rejectBlankText(w http.ResponseWriter, values ...*string) bool {
	for _, value := range values {
		if value != nil && strings.TrimSpace(*value) == "" {
			writeError(w, http.StatusUnprocessableEntity, "blank_content")
			return false
		}
	}
	return true
}

// requirePathUUID rejects malformed path identifiers with a deterministic
// 400 invalid_identifier before they reach SQL ::uuid casts. Without this
// guard a bad identifier previously surfaced as 500 for handlers that feed
// path values straight into typed queries.
func requirePathUUID(w http.ResponseWriter, values ...string) bool {
	for _, value := range values {
		if !agentquery.ValidUUID(value) {
			writeError(w, http.StatusBadRequest, "invalid_identifier")
			return false
		}
	}
	return true
}

// workspaceExists reports whether workspaceID names an active workspace inside
// the caller's organization. Infrastructure failures deliberately report true
// so downstream services keep their canonical error mapping; only a definitive
// miss yields false.
func workspaceExists(ctx context.Context, deps Dependencies, organizationID, workspaceID string) bool {
	if deps.Store == nil || deps.Store.Pool == nil {
		return true
	}
	var exists bool
	err := deps.Store.Pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM content.workspaces
			WHERE organization_id = $1::uuid AND id = $2::uuid AND status = 'active'
		)`, organizationID, workspaceID).Scan(&exists)
	return err == nil && exists
}

// rejectUnknownWorkspace answers 404 workspace_not_found for identifiers that
// do not exist in the caller's organization, keeping permission denials (403)
// distinguishable from probing a nonexistent workspace.
func rejectUnknownWorkspace(w http.ResponseWriter, r *http.Request, deps Dependencies, principal auth.Principal) bool {
	if workspaceExists(r.Context(), deps, principal.OrganizationID, r.PathValue("workspaceId")) {
		return true
	}
	writeError(w, http.StatusNotFound, "workspace_not_found")
	return false
}
