package httpapi

import (
	"encoding/json"
	"net/http"
	"strings"

	"agentchunzhi/internal/deletion"
	"agentchunzhi/internal/workspace"
)

func workspaceCollectionFinal(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			listWorkspaces(deps)(w, r)
			return
		}
		if r.Method != http.MethodPost {
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
		var input workspace.CreateInput
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64*1024))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&input); err != nil {
			writeError(w, http.StatusUnprocessableEntity, "validation_failed")
			return
		}
		result, err := deps.WorkspaceService.Create(r.Context(), principal, input)
		if err != nil {
			writeWorkspaceError(w, err, "workspace_create_failed")
			return
		}
		writeETag(w, representationETag(result.ID, result.UpdatedAt.String()))
		writeJSON(w, http.StatusCreated, result)
	}
}

type finalWorkspacePatch struct {
	Name                   *string `json:"name"`
	Description            *string `json:"description"`
	AvatarURL              *string `json:"avatar_url"`
	Status                 *string `json:"status"`
	DefaultVisibility      *string `json:"default_visibility"`
	DefaultResourceModelID *string `json:"default_resource_model_id"`
}

func workspaceResourceFinal(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			getWorkspace(deps)(w, r)
			return
		}
		principal, ok := requireMemberSession(w, r, deps)
		if !ok {
			return
		}
		key, ok := requestIdempotencyKey(w, r)
		if !ok {
			writeError(w, http.StatusUnprocessableEntity, "idempotency_key_invalid")
			return
		}
		workspaceID := r.PathValue("workspaceId")
		scope, err := deps.WorkspacePolicy.Require(r.Context(), principal, workspaceID, "", "workspace.manage")
		if err != nil || (scope.Role != "owner" && scope.Role != "admin") {
			writeError(w, http.StatusForbidden, "workspace_access_denied")
			return
		}
		if r.Method == http.MethodDelete {
			if scope.Role != "owner" {
				writeError(w, http.StatusForbidden, "workspace_access_denied")
				return
			}
			job, err := (deletion.Service{Store: deps.Store}).Enqueue(r.Context(), principal, workspaceID, "workspace", workspaceID, key)
			if err != nil {
				writeError(w, http.StatusInternalServerError, "workspace_delete_failed")
				return
			}
			writeJSON(w, http.StatusAccepted, job)
			return
		}
		if r.Method != http.MethodPatch {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		var input finalWorkspacePatch
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64*1024))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&input); err != nil {
			writeError(w, http.StatusUnprocessableEntity, "validation_failed")
			return
		}
		set := make([]string, 0, 5)
		args := []any{principal.OrganizationID, workspaceID}
		arg := func(value any) string { args = append(args, value); return "$" + string(rune('0'+len(args))) }
		if input.Name != nil {
			value := strings.TrimSpace(*input.Name)
			if value == "" {
				writeError(w, http.StatusUnprocessableEntity, "validation_failed")
				return
			}
			set = append(set, "name = "+arg(value))
		}
		if input.Description != nil {
			set = append(set, "description = "+arg(*input.Description))
		}
		if input.AvatarURL != nil {
			set = append(set, "avatar_url = "+arg(*input.AvatarURL))
		}
		if input.Status != nil {
			value := strings.TrimSpace(*input.Status)
			if value != "active" && value != "archived" {
				writeError(w, http.StatusUnprocessableEntity, "validation_failed")
				return
			}
			set = append(set, "status = "+arg(value))
		}
		if input.DefaultVisibility != nil {
			value := strings.TrimSpace(*input.DefaultVisibility)
			if value != "public" && value != "login" && value != "private" && value != "workspace" && value != "internal" {
				writeError(w, http.StatusUnprocessableEntity, "validation_failed")
				return
			}
			set = append(set, "default_visibility = "+arg(value))
		}
		if input.DefaultResourceModelID != nil {
			set = append(set, "default_resource_model_id = NULLIF("+arg(strings.TrimSpace(*input.DefaultResourceModelID))+", '')::uuid")
		}
		if len(set) == 0 {
			writeError(w, http.StatusUnprocessableEntity, "validation_failed")
			return
		}
		set = append(set, "updated_at = now()")
		query := "UPDATE content.workspaces SET " + strings.Join(set, ", ") + " WHERE organization_id = $1::uuid AND id = $2::uuid"
		if _, err := deps.Store.Pool.Exec(r.Context(), query, args...); err != nil {
			writeError(w, http.StatusInternalServerError, "workspace_update_failed")
			return
		}
		item, err := deps.WorkspaceService.Get(r.Context(), principal, workspaceID)
		if err != nil {
			writeWorkspaceError(w, err, "workspace_load_failed")
			return
		}
		writeETag(w, representationETag(item.ID, item.UpdatedAt.String()))
		writeJSON(w, http.StatusOK, item)
	}
}

func listWorkspaceMembersFinal(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		principal, ok := requireMemberSession(w, r, deps)
		if !ok {
			return
		}
		items, err := deps.WorkspaceService.ListMembers(r.Context(), principal, r.PathValue("workspaceId"))
		if err != nil {
			writeWorkspaceError(w, err, "workspace_members_failed")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"items": items, "has_more": false})
	}
}

func workspaceInvitationsFinal(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		principal, ok := requireMemberSession(w, r, deps)
		if !ok {
			return
		}
		if r.Method == http.MethodGet {
			items, err := deps.WorkspaceService.ListInvitations(r.Context(), principal, r.PathValue("workspaceId"))
			if err != nil {
				writeWorkspaceError(w, err, "workspace_invitations_failed")
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"items": items, "has_more": false})
			return
		}
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		if _, ok := requestIdempotencyKey(w, r); !ok {
			writeError(w, http.StatusUnprocessableEntity, "idempotency_key_invalid")
			return
		}
		var input workspace.InviteInput
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32*1024))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&input); err != nil {
			writeError(w, http.StatusUnprocessableEntity, "validation_failed")
			return
		}
		item, err := deps.WorkspaceService.Invite(r.Context(), principal, r.PathValue("workspaceId"), input)
		if err != nil {
			writeWorkspaceError(w, err, "workspace_invitation_failed")
			return
		}
		writeJSON(w, http.StatusCreated, item)
	}
}

func acceptWorkspaceInvitationFinal(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
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
		item, err := deps.WorkspaceService.AcceptInvitation(r.Context(), principal, r.PathValue("invitationId"))
		if err != nil {
			writeWorkspaceError(w, err, "invitation_accept_failed")
			return
		}
		writeJSON(w, http.StatusOK, item)
	}
}

func revokeWorkspaceInvitationFinal(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
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
		if err := deps.WorkspaceService.RevokeInvitation(r.Context(), principal, r.PathValue("invitationId")); err != nil {
			writeWorkspaceError(w, err, "invitation_revoke_failed")
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

type finalMemberPatch struct {
	Role string `json:"role"`
}

func workspaceMemberResourceFinal(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		principal, ok := requireMemberSession(w, r, deps)
		if !ok {
			return
		}
		if _, ok := requestIdempotencyKey(w, r); !ok {
			writeError(w, http.StatusUnprocessableEntity, "idempotency_key_invalid")
			return
		}
		switch r.Method {
		case http.MethodPatch:
			var input finalMemberPatch
			decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16*1024))
			decoder.DisallowUnknownFields()
			if err := decoder.Decode(&input); err != nil {
				writeError(w, http.StatusUnprocessableEntity, "validation_failed")
				return
			}
			// {memberId} accepts both the workspace_members row id and the
			// user id exposed by list endpoints; workspace_id disambiguates a
			// user that belongs to several workspaces.
			item, err := deps.WorkspaceService.UpdateMember(r.Context(), principal, r.PathValue("memberId"), input.Role, r.URL.Query().Get("workspace_id"))
			if err != nil {
				writeWorkspaceError(w, err, "workspace_member_update_failed")
				return
			}
			writeJSON(w, http.StatusOK, item)
		case http.MethodDelete:
			if err := deps.WorkspaceService.RemoveMember(r.Context(), principal, r.PathValue("memberId"), r.URL.Query().Get("workspace_id")); err != nil {
				writeWorkspaceError(w, err, "workspace_member_remove_failed")
				return
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
		}
	}
}

func currentUserProfileFinal(deps Dependencies) http.HandlerFunc {
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
		var input struct {
			DisplayName *string `json:"display_name"`
			AvatarURL   *string `json:"avatar_url"`
		}
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16*1024))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&input); err != nil {
			writeError(w, http.StatusUnprocessableEntity, "validation_failed")
			return
		}
		sets := make([]string, 0, 2)
		args := []any{principal.OrganizationID, principal.UserID}
		next := func(v any) string { args = append(args, v); return "$" + string(rune('0'+len(args))) }
		if input.DisplayName != nil {
			value := strings.TrimSpace(*input.DisplayName)
			if value == "" {
				writeError(w, http.StatusUnprocessableEntity, "validation_failed")
				return
			}
			sets = append(sets, "display_name = "+next(value))
		}
		if input.AvatarURL != nil {
			sets = append(sets, "avatar_url = "+next(strings.TrimSpace(*input.AvatarURL)))
		}
		if len(sets) == 0 {
			writeError(w, http.StatusUnprocessableEntity, "validation_failed")
			return
		}
		sets = append(sets, "updated_at = now()")
		if _, err := deps.Store.Pool.Exec(r.Context(), "UPDATE identity.users SET "+strings.Join(sets, ", ")+" WHERE organization_id = $1::uuid AND id = $2::uuid", args...); err != nil {
			writeError(w, http.StatusInternalServerError, "profile_update_failed")
			return
		}
		var id, org, typ, display, login, avatar string
		if err := deps.Store.Pool.QueryRow(r.Context(), `SELECT id::text, organization_id::text, user_type, display_name, COALESCE(login_name, ''), COALESCE(avatar_url, '') FROM identity.users WHERE id = $1::uuid`, principal.UserID).Scan(&id, &org, &typ, &display, &login, &avatar); err != nil {
			writeError(w, http.StatusInternalServerError, "profile_load_failed")
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"user_id": id, "organization_id": org, "user_type": typ, "display_name": display, "login_name": login, "avatar_url": avatar})
	}
}
