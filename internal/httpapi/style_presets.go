package httpapi

// style_presets.go — org-level custom style presets and the site comment
// surfaces (二期 §5/§8). Presets are data bundles: members create and list,
// the creator or an org admin deletes. Comments: members write on the public
// face, moderation runs behind site.manage on the workspace face.

import (
	"encoding/json"
	"net/http"
)

// OrganizationStylePresets serves GET/POST /api/organization/style-presets.
func OrganizationStylePresets(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		principal, ok := sessionPrincipal(w, r, deps)
		if !ok {
			return
		}
		if !requireSiteService(w, deps) {
			return
		}
		switch r.Method {
		case http.MethodGet:
			items, err := deps.Sites.ListStylePresets(r.Context(), principal)
			if err != nil {
				SiteError(w, err, "slug_conflict")
				return
			}
			writeData(w, r, http.StatusOK, map[string]any{"items": items})
		case http.MethodPost:
			if _, ok := requireIdempotencyKey(w, r); !ok {
				return
			}
			var input struct {
				Name        string          `json:"name"`
				StyleConfig json.RawMessage `json:"style_config"`
				CustomCss   string          `json:"custom_css"`
			}
			if !decodeBody(w, r, &input, 96*1024) {
				return
			}
			item, err := deps.Sites.CreateStylePreset(r.Context(), principal, input.Name, input.StyleConfig, input.CustomCss)
			if err != nil {
				SiteError(w, err, "slug_conflict")
				return
			}
			writeData(w, r, http.StatusCreated, item)
		default:
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
		}
	}
}

// OrganizationStylePresetResource serves DELETE /api/organization/style-presets/{presetId}.
func OrganizationStylePresetResource(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		principal, ok := sessionPrincipal(w, r, deps)
		if !ok {
			return
		}
		if !requireSiteService(w, deps) {
			return
		}
		if r.Method != http.MethodDelete {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		if err := deps.Sites.DeleteStylePreset(r.Context(), principal, r.PathValue("presetId")); err != nil {
			SiteError(w, err, "slug_conflict")
			return
		}
		writeData(w, r, http.StatusOK, map[string]string{"status": "deleted"})
	}
}

// publicSiteCommentCreate serves POST /api/public/sites/{slug}/comments
// (body: display_path + body): a member write on the anonymous face. The
// display path rides the body because the ServeMux wildcard cannot be
// followed by more segments; the session cookie + Origin gate apply, no
// idempotency key by the public-face convention — the per-member cooldown
// is the backstop.
func publicSiteCommentCreate(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		if !requireSiteService(w, deps) {
			return
		}
		principal, err := deps.SessionService.Authenticate(r.Context(), r)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		var input struct {
			DisplayPath string `json:"display_path"`
			Body        string `json:"body"`
		}
		if !decodeBody(w, r, &input, 8*1024) {
			return
		}
		comment, err := deps.Sites.CreateComment(r.Context(), principal,
			r.PathValue("slug"), input.DisplayPath, input.Body)
		if err != nil {
			writePublicSiteError(w, err)
			return
		}
		writeData(w, r, http.StatusCreated, comment)
	}
}

// SiteComments serves GET /api/workspaces/{workspaceId}/sites/{siteId}/comments
// (moderation queue; site.read).
func SiteComments(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		principal, ok := sessionPrincipal(w, r, deps)
		if !ok {
			return
		}
		if !requireSiteService(w, deps) {
			return
		}
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		workspaceID := r.PathValue("workspaceId")
		siteID := r.PathValue("siteId")
		if !requirePathUUID(w, workspaceID, siteID) {
			return
		}
		page, err := deps.Sites.ListComments(r.Context(), principal, workspaceID, siteID, r.URL.Query().Get("status"))
		if err != nil {
			SiteError(w, err, "slug_conflict")
			return
		}
		writeData(w, r, http.StatusOK, map[string]any{"items": page.Items})
	}
}

// SiteCommentResource serves PATCH/DELETE
// /api/workspaces/{workspaceId}/sites/{siteId}/comments/{commentId}.
func SiteCommentResource(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		principal, ok := sessionPrincipal(w, r, deps)
		if !ok {
			return
		}
		if !requireSiteService(w, deps) {
			return
		}
		workspaceID := r.PathValue("workspaceId")
		siteID := r.PathValue("siteId")
		commentID := r.PathValue("commentId")
		if !requirePathUUID(w, workspaceID, siteID, commentID) {
			return
		}
		switch r.Method {
		case http.MethodPatch:
			if _, ok := requireIdempotencyKey(w, r); !ok {
				return
			}
			var input struct {
				Status string `json:"status"`
			}
			if !decodeBody(w, r, &input, 1024) {
				return
			}
			if err := deps.Sites.ModerateComment(r.Context(), principal, workspaceID, siteID, commentID, input.Status); err != nil {
				SiteError(w, err, "slug_conflict")
				return
			}
			writeData(w, r, http.StatusOK, map[string]string{"status": input.Status})
		case http.MethodDelete:
			if _, ok := requireIdempotencyKey(w, r); !ok {
				return
			}
			if err := deps.Sites.DeleteComment(r.Context(), principal, workspaceID, siteID, commentID); err != nil {
				SiteError(w, err, "slug_conflict")
				return
			}
			writeData(w, r, http.StatusOK, map[string]string{"status": "deleted"})
		default:
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
		}
	}
}
