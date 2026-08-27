package httpapi

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strings"

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

func listWorkspaces(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		principal, ok := requireMemberSession(w, r, deps)
		if !ok {
			return
		}
		items, err := deps.WorkspaceService.List(r.Context(), principal)
		if err != nil {
			writeWorkspaceError(w, err, "workspace_list_failed")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"items": items, "has_more": false})
	}
}

func getWorkspace(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		principal, ok := requireMemberSession(w, r, deps)
		if !ok {
			return
		}
		item, err := deps.WorkspaceService.Get(r.Context(), principal, r.PathValue("workspaceId"))
		if err != nil {
			writeWorkspaceError(w, err, "workspace_load_failed")
			return
		}
		writeETag(w, representationETag(item.ID, item.UpdatedAt.String()))
		writeJSON(w, http.StatusOK, item)
	}
}

func getWorkspaceMember(deps Dependencies) http.HandlerFunc {
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
		item, err := deps.WorkspaceService.Member(r.Context(), principal, r.PathValue("workspaceId"))
		if err != nil {
			writeWorkspaceError(w, err, "workspace_member_load_failed")
			return
		}
		writeJSON(w, http.StatusOK, item)
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

func getWorkspaceCounts(deps Dependencies) http.HandlerFunc {
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
		counts, err := deps.WorkspaceService.Counts(r.Context(), principal, r.PathValue("workspaceId"))
		if err != nil {
			writeWorkspaceError(w, err, "workspace_counts_failed")
			return
		}
		writeJSON(w, http.StatusOK, counts)
	}
}

type workspaceSettingsPatch struct {
	Name                   *string `json:"name"`
	Description            *string `json:"description"`
	DefaultVisibility      *string `json:"default_visibility"`
	DefaultResourceModelID *string `json:"default_resource_model_id"`
}

func getWorkspaceSettings(deps Dependencies) http.HandlerFunc {
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
		settings, err := deps.WorkspaceService.Settings(r.Context(), principal, r.PathValue("workspaceId"))
		if err != nil {
			writeWorkspaceError(w, err, "workspace_settings_load_failed")
			return
		}
		writeETag(w, representationETag(settings.Name, settings.Description, settings.DefaultVisibility, settings.DefaultResourceModelID))
		writeJSON(w, http.StatusOK, settings)
	}
}

func updateWorkspaceSettings(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		principal, ok := requireMemberSession(w, r, deps)
		if !ok {
			return
		}
		if _, ok := requestIdempotencyKey(w, r); !ok {
			writeError(w, http.StatusUnprocessableEntity, "idempotency_key_invalid")
			return
		}
		var patch workspaceSettingsPatch
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32*1024))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&patch); err != nil {
			writeError(w, http.StatusUnprocessableEntity, "validation_failed")
			return
		}
		current, err := deps.WorkspaceService.Settings(r.Context(), principal, r.PathValue("workspaceId"))
		if err != nil {
			writeWorkspaceError(w, err, "workspace_settings_load_failed")
			return
		}
		currentETag := representationETag(current.Name, current.Description, current.DefaultVisibility, current.DefaultResourceModelID)
		if r.Header.Get("If-Match") != "" && !ifMatchMatches(r, currentETag) {
			writeError(w, http.StatusConflict, "version_conflict")
			return
		}
		if patch.Name != nil {
			current.Name = strings.TrimSpace(*patch.Name)
		}
		if patch.Description != nil {
			current.Description = *patch.Description
		}
		if patch.DefaultVisibility != nil {
			current.DefaultVisibility = strings.TrimSpace(*patch.DefaultVisibility)
		}
		if patch.DefaultResourceModelID != nil {
			current.DefaultResourceModelID = strings.TrimSpace(*patch.DefaultResourceModelID)
		}
		result, err := deps.WorkspaceService.UpdateSettings(r.Context(), principal, r.PathValue("workspaceId"), current)
		if err != nil {
			writeWorkspaceError(w, err, "workspace_settings_update_failed")
			return
		}
		writeETag(w, representationETag(result.Name, result.Description, result.DefaultVisibility, result.DefaultResourceModelID))
		writeJSON(w, http.StatusOK, result)
	}
}

func getMemberPreferences(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		principal, ok := requireMemberSession(w, r, deps)
		if !ok {
			return
		}
		result, err := deps.WorkspaceService.Preferences(r.Context(), principal)
		if err != nil {
			writeWorkspaceError(w, err, "member_preferences_load_failed")
			return
		}
		writeJSON(w, http.StatusOK, result)
	}
}

func updateMemberPreferences(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		principal, ok := requireMemberSession(w, r, deps)
		if !ok {
			return
		}
		if _, ok := requestIdempotencyKey(w, r); !ok {
			writeError(w, http.StatusUnprocessableEntity, "idempotency_key_invalid")
			return
		}
		var input map[string]any
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32*1024))
		if err := decoder.Decode(&input); err != nil {
			writeError(w, http.StatusUnprocessableEntity, "validation_failed")
			return
		}
		result, err := deps.WorkspaceService.UpdatePreferences(r.Context(), principal, input)
		if err != nil {
			writeWorkspaceError(w, err, "member_preferences_update_failed")
			return
		}
		writeJSON(w, http.StatusOK, result)
	}
}

func workspaceSettings(deps Dependencies) http.HandlerFunc {
	get := getWorkspaceSettings(deps)
	update := updateWorkspaceSettings(deps)
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			get(w, r)
			return
		}
		update(w, r)
	}
}

func memberPreferences(deps Dependencies) http.HandlerFunc {
	get := getMemberPreferences(deps)
	update := updateMemberPreferences(deps)
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			get(w, r)
			return
		}
		update(w, r)
	}
}
