package httpapi

// v2_organization.go — phase 1 organization governance and workspace
// membership surface. Organization routes gate through
// organization.Service.RequireOrganizationAction; workspace member routes
// gate through the workspace service's own membership checks.

import (
	"context"
	"net/http"
	"strconv"
	"strings"

	"agentchunzhi/internal/auth"
	"agentchunzhi/internal/organization"
)

// ---------- organization governance ----------

func v2GetOrganization(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		principal, _, ok := v2MemberSession(w, r, deps)
		if !ok {
			return
		}
		item, err := deps.OrganizationService.Get(r.Context(), principal)
		if err != nil {
			v2DomainError(w, err)
			return
		}
		writeETag(w, item.ETag)
		writeData(w, r, http.StatusOK, item)
	}
}

type v2PatchOrganizationRequest struct {
	Name string `json:"name"`
}

func v2PatchOrganization(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		principal, _, ok := v2MemberSession(w, r, deps)
		if !ok {
			return
		}
		expected, ok := requireIfMatchV2(w, r)
		if !ok {
			return
		}
		current, err := deps.OrganizationService.Get(r.Context(), principal)
		if err != nil {
			v2DomainError(w, err)
			return
		}
		// If-Match "*" only demands the organization exists (proven by the read
		// above); any concrete revision must match exactly.
		if !ifMatchWildcard(expected) && strings.Trim(expected, "\"") != strconv.FormatInt(current.Revision, 10) {
			writeError(w, http.StatusPreconditionFailed, "revision_mismatch")
			return
		}
		var input v2PatchOrganizationRequest
		if !decodeV2Body(w, r, &input, 16*1024) {
			return
		}
		item, err := deps.OrganizationService.Update(r.Context(), principal, input.Name)
		if err != nil {
			v2DomainError(w, err)
			return
		}
		writeETag(w, item.ETag)
		writeData(w, r, http.StatusOK, item)
	}
}

func v2ListOrganizationMembers(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		principal, _, ok := v2MemberSession(w, r, deps)
		if !ok {
			return
		}
		limit := atoiDefault(r.URL.Query().Get("limit"), 20)
		items, next, err := deps.OrganizationService.ListMembers(r.Context(), principal, limit, r.URL.Query().Get("cursor"))
		if err != nil {
			v2DomainError(w, err)
			return
		}
		writeData(w, r, http.StatusOK, map[string]any{"items": items, "page": pageFrom(len(items), limit, next)})
	}
}

func v2GetOrganizationMember(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		principal, _, ok := v2MemberSession(w, r, deps)
		if !ok {
			return
		}
		item, err := deps.OrganizationService.GetMember(r.Context(), principal, r.PathValue("userId"))
		if err != nil {
			v2DomainError(w, err)
			return
		}
		writeETag(w, item.ETag)
		writeData(w, r, http.StatusOK, item)
	}
}

// v2PatchOrganizationMember applies one single-command patch:
// {organization_role} or {status}.
func v2PatchOrganizationMember(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		principal, _, ok := v2MemberSession(w, r, deps)
		if !ok {
			return
		}
		expected, ok := requireIfMatchV2(w, r)
		if !ok {
			return
		}
		userID := r.PathValue("userId")
		if !requirePathUUID(w, userID) {
			return
		}
		current, err := deps.OrganizationService.GetMember(r.Context(), principal, userID)
		if err != nil {
			v2DomainError(w, err)
			return
		}
		// If-Match "*" only demands the member exists (proven by the read
		// above); any concrete revision must match exactly.
		if !ifMatchWildcard(expected) && strings.Trim(expected, "\"") != strconv.FormatInt(current.Revision, 10) {
			writeError(w, http.StatusPreconditionFailed, "revision_mismatch")
			return
		}
		var input struct {
			OrganizationRole *string `json:"organization_role"`
			Status           *string `json:"status"`
		}
		if !decodeV2Body(w, r, &input, 16*1024) {
			return
		}
		switch {
		case input.OrganizationRole != nil && input.Status != nil:
			writeError(w, http.StatusUnprocessableEntity, "validation_failed")
			return
		case input.OrganizationRole != nil:
			item, err := deps.OrganizationService.PatchMemberRole(r.Context(), principal, userID, *input.OrganizationRole)
			if err != nil {
				v2DomainError(w, err)
				return
			}
			writeETag(w, item.ETag)
			writeData(w, r, http.StatusOK, item)
		case input.Status != nil:
			item, err := deps.OrganizationService.PatchMemberStatus(r.Context(), principal, userID, *input.Status)
			if err != nil {
				v2DomainError(w, err)
				return
			}
			writeETag(w, item.ETag)
			writeData(w, r, http.StatusOK, item)
		default:
			writeError(w, http.StatusUnprocessableEntity, "validation_failed")
		}
	}
}

// ---------- organization invitations ----------

func v2ListOrganizationInvitations(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		principal, _, ok := v2MemberSession(w, r, deps)
		if !ok {
			return
		}
		items, err := deps.InvitationService.ListInvitations(r.Context(), principal, r.URL.Query().Get("status"))
		if err != nil {
			v2DomainError(w, err)
			return
		}
		writeData(w, r, http.StatusOK, map[string]any{"items": items, "page": CursorPage{NextCursor: nil, HasMore: false}})
	}
}

type v2CreateInvitationRequest struct {
	Email            string               `json:"email"`
	DisplayName      string               `json:"display_name"`
	OrganizationRole string               `json:"organization_role"`
	Grants           []organization.Grant `json:"grants"`
	ExpiresInHours   int                  `json:"expires_in_hours"`
}

// v2CreateOrganizationInvitation returns the raw invitation token exactly
// once: here in the 201 response and inside the invitation email.
func v2CreateOrganizationInvitation(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		principal, _, ok := v2MemberSession(w, r, deps)
		if !ok {
			return
		}
		if _, ok := requireIdempotencyKeyV2(w, r); !ok {
			return
		}
		var input v2CreateInvitationRequest
		if !decodeV2Body(w, r, &input, 64*1024) {
			return
		}
		invitation, token, err := deps.InvitationService.Create(r.Context(), principal, organization.CreateInput{
			Email: input.Email, DisplayName: input.DisplayName, OrganizationRole: input.OrganizationRole,
			Grants: input.Grants, ExpiresInHours: input.ExpiresInHours,
		})
		if err != nil {
			v2DomainError(w, err)
			return
		}
		writeETag(w, invitation.ETag)
		writeData(w, r, http.StatusCreated, map[string]any{"invitation": invitation, "invitation_token": token})
	}
}

func v2ResendOrganizationInvitation(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		principal, _, ok := v2MemberSession(w, r, deps)
		if !ok {
			return
		}
		if _, ok := requireIdempotencyKeyV2(w, r); !ok {
			return
		}
		item, err := deps.InvitationService.Resend(r.Context(), principal, r.PathValue("invitationId"))
		if err != nil {
			v2DomainError(w, err)
			return
		}
		writeETag(w, item.ETag)
		writeData(w, r, http.StatusOK, item)
	}
}

func v2RevokeOrganizationInvitation(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		principal, _, ok := v2MemberSession(w, r, deps)
		if !ok {
			return
		}
		if _, ok := requireIdempotencyKeyV2(w, r); !ok {
			return
		}
		if err := deps.InvitationService.Revoke(r.Context(), principal, r.PathValue("invitationId")); err != nil {
			v2DomainError(w, err)
			return
		}
		writeNoContent(w)
	}
}

// ---------- organization workspaces (governance) ----------

func v2ListOrganizationWorkspaces(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		principal, _, ok := v2MemberSession(w, r, deps)
		if !ok {
			return
		}
		items, err := deps.OrganizationService.ListWorkspaces(r.Context(), principal)
		if err != nil {
			v2DomainError(w, err)
			return
		}
		writeData(w, r, http.StatusOK, map[string]any{"items": items, "page": CursorPage{NextCursor: nil, HasMore: false}})
	}
}

type v2CreateWorkspaceRequest struct {
	Name                   string `json:"name"`
	Description            string `json:"description"`
	DefaultResourceModelID string `json:"default_resource_model_id"`
}

func v2CreateOrganizationWorkspace(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		principal, _, ok := v2MemberSession(w, r, deps)
		if !ok {
			return
		}
		if _, ok := requireIdempotencyKeyV2(w, r); !ok {
			return
		}
		var input v2CreateWorkspaceRequest
		if !decodeV2Body(w, r, &input, 32*1024) {
			return
		}
		item, err := deps.OrganizationService.CreateWorkspace(r.Context(), principal, input.Name, input.Description, input.DefaultResourceModelID)
		if err != nil {
			v2DomainError(w, err)
			return
		}
		writeETag(w, item.ETag)
		writeData(w, r, http.StatusCreated, item)
	}
}

func v2GetOrganizationWorkspace(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		principal, _, ok := v2MemberSession(w, r, deps)
		if !ok {
			return
		}
		item, err := deps.OrganizationService.GetWorkspace(r.Context(), principal, r.PathValue("workspaceId"))
		if err != nil {
			v2DomainError(w, err)
			return
		}
		writeETag(w, item.ETag)
		writeData(w, r, http.StatusOK, item)
	}
}

func v2ArchiveOrganizationWorkspace(deps Dependencies) http.HandlerFunc {
	return v2WorkspaceLifecycleCommand(deps, func(ctx context.Context, principal auth.Principal, workspaceID string) (organization.Workspace, error) {
		return deps.OrganizationService.ArchiveWorkspace(ctx, principal, workspaceID)
	})
}

func v2RestoreOrganizationWorkspace(deps Dependencies) http.HandlerFunc {
	return v2WorkspaceLifecycleCommand(deps, func(ctx context.Context, principal auth.Principal, workspaceID string) (organization.Workspace, error) {
		return deps.OrganizationService.RestoreWorkspace(ctx, principal, workspaceID)
	})
}

// v2WorkspaceLifecycleCommand is the shared archive/restore body: member
// session, Idempotency-Key, then the organization governance command.
func v2WorkspaceLifecycleCommand(deps Dependencies, command func(context.Context, auth.Principal, string) (organization.Workspace, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		principal, _, ok := v2MemberSession(w, r, deps)
		if !ok {
			return
		}
		if _, ok := requireIdempotencyKeyV2(w, r); !ok {
			return
		}
		workspaceID := r.PathValue("workspaceId")
		if !requirePathUUID(w, workspaceID) {
			return
		}
		item, err := command(r.Context(), principal, workspaceID)
		if err != nil {
			v2DomainError(w, err)
			return
		}
		writeETag(w, item.ETag)
		writeData(w, r, http.StatusOK, item)
	}
}

func v2GrantOrganizationWorkspaceMember(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		principal, _, ok := v2MemberSession(w, r, deps)
		if !ok {
			return
		}
		if _, ok := requireIdempotencyKeyV2(w, r); !ok {
			return
		}
		workspaceID := r.PathValue("workspaceId")
		if !requirePathUUID(w, workspaceID) {
			return
		}
		var input struct {
			UserID string `json:"user_id"`
			Role   string `json:"role"`
		}
		if !decodeV2Body(w, r, &input, 16*1024) {
			return
		}
		if err := deps.OrganizationService.GrantWorkspaceMembership(r.Context(), principal, workspaceID, input.UserID, input.Role); err != nil {
			v2DomainError(w, err)
			return
		}
		writeData(w, r, http.StatusCreated, map[string]any{
			"workspace_id": workspaceID, "user_id": input.UserID, "role": input.Role,
		})
	}
}

func v2PatchOrganizationWorkspaceMember(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		principal, _, ok := v2MemberSession(w, r, deps)
		if !ok {
			return
		}
		if _, ok := requireIdempotencyKeyV2(w, r); !ok {
			return
		}
		workspaceID, membershipID := r.PathValue("workspaceId"), r.PathValue("membershipId")
		if !requirePathUUID(w, workspaceID, membershipID) {
			return
		}
		var input struct {
			Role string `json:"role"`
		}
		if !decodeV2Body(w, r, &input, 16*1024) {
			return
		}
		item, _, err := deps.OrganizationService.PatchWorkspaceMembership(r.Context(), principal, workspaceID, membershipID, input.Role)
		if err != nil {
			v2DomainError(w, err)
			return
		}
		writeETag(w, item.ETag)
		writeData(w, r, http.StatusOK, item)
	}
}

func v2RevokeOrganizationWorkspaceMember(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		principal, _, ok := v2MemberSession(w, r, deps)
		if !ok {
			return
		}
		if _, ok := requireIdempotencyKeyV2(w, r); !ok {
			return
		}
		workspaceID, membershipID := r.PathValue("workspaceId"), r.PathValue("membershipId")
		if !requirePathUUID(w, workspaceID, membershipID) {
			return
		}
		if err := deps.OrganizationService.RevokeWorkspaceMembership(r.Context(), principal, workspaceID, membershipID); err != nil {
			v2DomainError(w, err)
			return
		}
		writeNoContent(w)
	}
}

// ---------- my workspaces ----------

func v2ListMyWorkspaces(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		principal, _, ok := v2MemberSession(w, r, deps)
		if !ok {
			return
		}
		items, err := deps.WorkspaceService.List(r.Context(), principal)
		if err != nil {
			v2DomainError(w, err)
			return
		}
		writeData(w, r, http.StatusOK, map[string]any{"items": items, "page": CursorPage{NextCursor: nil, HasMore: false}})
	}
}

func v2GetWorkspace(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		principal, _, ok := v2MemberSession(w, r, deps)
		if !ok {
			return
		}
		workspaceID := r.PathValue("workspaceId")
		if !requirePathUUID(w, workspaceID) {
			return
		}
		item, err := deps.WorkspaceService.Get(r.Context(), principal, workspaceID)
		if err != nil {
			v2DomainError(w, err)
			return
		}
		writeData(w, r, http.StatusOK, item)
	}
}

type v2PatchWorkspaceRequest struct {
	Name                   *string `json:"name"`
	Description            *string `json:"description"`
	DefaultResourceModelID *string `json:"default_resource_model_id"`
}

// v2PatchWorkspace updates workspace metadata through the settings command.
// The workspace domain carries no representation revision, so the command is
// protected by Idempotency-Key instead of If-Match.
func v2PatchWorkspace(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		principal, _, ok := v2MemberSession(w, r, deps)
		if !ok {
			return
		}
		if _, ok := requireIdempotencyKeyV2(w, r); !ok {
			return
		}
		workspaceID := r.PathValue("workspaceId")
		if !requirePathUUID(w, workspaceID) {
			return
		}
		var input v2PatchWorkspaceRequest
		if !decodeV2Body(w, r, &input, 32*1024) {
			return
		}
		current, err := deps.WorkspaceService.Settings(r.Context(), principal, workspaceID)
		if err != nil {
			v2DomainError(w, err)
			return
		}
		if input.Name != nil {
			current.Name = strings.TrimSpace(*input.Name)
		}
		if input.Description != nil {
			current.Description = *input.Description
		}
		if input.DefaultResourceModelID != nil {
			current.DefaultResourceModelID = strings.TrimSpace(*input.DefaultResourceModelID)
		}
		result, err := deps.WorkspaceService.UpdateSettings(r.Context(), principal, workspaceID, current)
		if err != nil {
			v2DomainError(w, err)
			return
		}
		writeData(w, r, http.StatusOK, result)
	}
}

// v2GetWorkspaceSummary folds the retired counts/stats views into one member
// summary endpoint.
func v2GetWorkspaceSummary(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		principal, _, ok := v2MemberSession(w, r, deps)
		if !ok {
			return
		}
		workspaceID := r.PathValue("workspaceId")
		if !requirePathUUID(w, workspaceID) {
			return
		}
		stats, err := deps.WorkspaceService.Stats(r.Context(), principal, workspaceID)
		if err != nil {
			v2DomainError(w, err)
			return
		}
		counts, err := deps.WorkspaceService.Counts(r.Context(), principal, workspaceID)
		if err != nil {
			v2DomainError(w, err)
			return
		}
		writeData(w, r, http.StatusOK, map[string]any{"stats": stats, "counts": counts})
	}
}

// ---------- workspace members ----------

func v2ListWorkspaceMembers(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		principal, _, ok := v2MemberSession(w, r, deps)
		if !ok {
			return
		}
		workspaceID := r.PathValue("workspaceId")
		if !requirePathUUID(w, workspaceID) {
			return
		}
		items, err := deps.WorkspaceService.ListMembers(r.Context(), principal, workspaceID)
		if err != nil {
			v2DomainError(w, err)
			return
		}
		writeData(w, r, http.StatusOK, map[string]any{"items": items, "page": CursorPage{NextCursor: nil, HasMore: false}})
	}
}

type v2AddWorkspaceMemberRequest struct {
	UserID string `json:"user_id"`
	Role   string `json:"role"`
}

func v2AddWorkspaceMember(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		principal, _, ok := v2MemberSession(w, r, deps)
		if !ok {
			return
		}
		if _, ok := requireIdempotencyKeyV2(w, r); !ok {
			return
		}
		workspaceID := r.PathValue("workspaceId")
		if !requirePathUUID(w, workspaceID) {
			return
		}
		var input v2AddWorkspaceMemberRequest
		if !decodeV2Body(w, r, &input, 16*1024) {
			return
		}
		item, err := deps.WorkspaceService.AddMember(r.Context(), principal, workspaceID, input.UserID, input.Role)
		if err != nil {
			v2DomainError(w, err)
			return
		}
		writeData(w, r, http.StatusCreated, item)
	}
}

func v2PatchWorkspaceMember(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		principal, _, ok := v2MemberSession(w, r, deps)
		if !ok {
			return
		}
		if _, ok := requireIdempotencyKeyV2(w, r); !ok {
			return
		}
		workspaceID, membershipID := r.PathValue("workspaceId"), r.PathValue("membershipId")
		if !requirePathUUID(w, workspaceID, membershipID) {
			return
		}
		var input struct {
			Role string `json:"role"`
		}
		if !decodeV2Body(w, r, &input, 16*1024) {
			return
		}
		item, err := deps.WorkspaceService.UpdateMember(r.Context(), principal, membershipID, input.Role, workspaceID)
		if err != nil {
			v2DomainError(w, err)
			return
		}
		writeData(w, r, http.StatusOK, item)
	}
}

func v2RemoveWorkspaceMember(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		principal, _, ok := v2MemberSession(w, r, deps)
		if !ok {
			return
		}
		if _, ok := requireIdempotencyKeyV2(w, r); !ok {
			return
		}
		workspaceID, membershipID := r.PathValue("workspaceId"), r.PathValue("membershipId")
		if !requirePathUUID(w, workspaceID, membershipID) {
			return
		}
		if err := deps.WorkspaceService.RemoveMember(r.Context(), principal, membershipID, workspaceID); err != nil {
			v2DomainError(w, err)
			return
		}
		writeNoContent(w)
	}
}

func v2LeaveWorkspace(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		principal, _, ok := v2MemberSession(w, r, deps)
		if !ok {
			return
		}
		if _, ok := requireIdempotencyKeyV2(w, r); !ok {
			return
		}
		workspaceID := r.PathValue("workspaceId")
		if !requirePathUUID(w, workspaceID) {
			return
		}
		if err := deps.WorkspaceService.Leave(r.Context(), principal, workspaceID); err != nil {
			v2DomainError(w, err)
			return
		}
		writeNoContent(w)
	}
}

func v2ListEligibleWorkspaceMembers(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		principal, _, ok := v2MemberSession(w, r, deps)
		if !ok {
			return
		}
		workspaceID := r.PathValue("workspaceId")
		if !requirePathUUID(w, workspaceID) {
			return
		}
		items, err := deps.WorkspaceService.EligibleMembers(r.Context(), principal, workspaceID,
			r.URL.Query().Get("q"), atoiDefault(r.URL.Query().Get("limit"), 20))
		if err != nil {
			v2DomainError(w, err)
			return
		}
		writeData(w, r, http.StatusOK, map[string]any{"items": items, "page": CursorPage{NextCursor: nil, HasMore: false}})
	}
}

// ---------- workspace invitations (organization aggregate) ----------

func v2ListWorkspaceInvitations(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		principal, _, ok := v2MemberSession(w, r, deps)
		if !ok {
			return
		}
		workspaceID := r.PathValue("workspaceId")
		if !requirePathUUID(w, workspaceID) {
			return
		}
		items, err := deps.InvitationService.ListWorkspaceInvitations(r.Context(), principal, workspaceID)
		if err != nil {
			v2DomainError(w, err)
			return
		}
		writeData(w, r, http.StatusOK, map[string]any{"items": items, "page": CursorPage{NextCursor: nil, HasMore: false}})
	}
}

type v2CreateWorkspaceInvitationRequest struct {
	Email          string `json:"email"`
	DisplayName    string `json:"display_name"`
	Role           string `json:"role"`
	ExpiresInHours int    `json:"expires_in_hours"`
}

func v2CreateWorkspaceInvitation(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		principal, _, ok := v2MemberSession(w, r, deps)
		if !ok {
			return
		}
		if _, ok := requireIdempotencyKeyV2(w, r); !ok {
			return
		}
		workspaceID := r.PathValue("workspaceId")
		if !requirePathUUID(w, workspaceID) {
			return
		}
		var input v2CreateWorkspaceInvitationRequest
		if !decodeV2Body(w, r, &input, 32*1024) {
			return
		}
		invitation, token, err := deps.InvitationService.CreateWorkspaceScoped(r.Context(), principal, workspaceID, organization.WorkspaceInviteInput{
			Email: input.Email, DisplayName: input.DisplayName, Role: input.Role, ExpiresInHours: input.ExpiresInHours,
		})
		if err != nil {
			v2DomainError(w, err)
			return
		}
		writeETag(w, invitation.ETag)
		writeData(w, r, http.StatusCreated, map[string]any{"invitation": invitation, "invitation_token": token})
	}
}

func v2ResendWorkspaceInvitation(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		principal, _, ok := v2MemberSession(w, r, deps)
		if !ok {
			return
		}
		if _, ok := requireIdempotencyKeyV2(w, r); !ok {
			return
		}
		workspaceID, invitationID := r.PathValue("workspaceId"), r.PathValue("invitationId")
		if !requirePathUUID(w, workspaceID, invitationID) {
			return
		}
		item, err := deps.InvitationService.ResendWorkspaceScoped(r.Context(), principal, workspaceID, invitationID)
		if err != nil {
			v2DomainError(w, err)
			return
		}
		writeETag(w, item.ETag)
		writeData(w, r, http.StatusOK, item)
	}
}

func v2RevokeWorkspaceInvitation(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		principal, _, ok := v2MemberSession(w, r, deps)
		if !ok {
			return
		}
		if _, ok := requireIdempotencyKeyV2(w, r); !ok {
			return
		}
		workspaceID, invitationID := r.PathValue("workspaceId"), r.PathValue("invitationId")
		if !requirePathUUID(w, workspaceID, invitationID) {
			return
		}
		if err := deps.InvitationService.RevokeWorkspaceScoped(r.Context(), principal, workspaceID, invitationID); err != nil {
			v2DomainError(w, err)
			return
		}
		writeNoContent(w)
	}
}
