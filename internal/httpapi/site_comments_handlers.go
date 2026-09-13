package httpapi

// comments_handlers.go — 公开站评论与摘要 HTTP 面（自旧 style_presets.go 拆出：
// 预设 handler 随样式体系退役，评论/摘要与此无关）。

import (
	"strconv"
	"net/http"

)

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
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		page, err := deps.Sites.ListComments(r.Context(), principal, workspaceID, siteID,
			r.URL.Query().Get("status"), r.URL.Query().Get("cursor"), limit)
		if err != nil {
			SiteError(w, err, "slug_conflict")
			return
		}
		writeData(w, r, http.StatusOK, map[string]any{
			"items": page.Items,
			"page":  map[string]any{"next_cursor": page.NextCursor, "has_more": page.HasMore},
		})
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

// SiteSummary serves GET /api/workspaces/{workspaceId}/sites/{siteId}/summary:
// the site-level counter block (bindings, pending comments) behind site.read.
func SiteSummary(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		principal, ok := sessionPrincipal(w, r, deps)
		if !ok {
			return
		}
		if !requireSiteService(w, deps) {
			return
		}
		workspaceID := r.PathValue("workspaceId")
		siteID := r.PathValue("siteId")
		if !requirePathUUID(w, workspaceID, siteID) {
			return
		}
		summary, err := deps.Sites.Summary(r.Context(), principal, workspaceID, siteID)
		if err != nil {
			SiteError(w, err, "slug_conflict")
			return
		}
		writeData(w, r, http.StatusOK, summary)
	}
}
