package httpapi

// site_theme_pages.go — 主题草稿 / 修订版 / 发布 与 自定义页 的 HTTP 面。

import (
	"encoding/json"
	"net/http"

	"agentchunzhi/internal/authz"
	"agentchunzhi/internal/site"
)

// SiteThemeDraft serves GET/PUT /api/workspaces/{ws}/sites/{siteId}/theme/draft.
// GET 返回站点草稿文件集（无草稿回退 published）；PUT 整体替换（经主题引擎
// 编译 + 扫描校验，失败返回 422 与结构化问题清单）。
func SiteThemeDraft(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		principal, ok := requireMemberSession(w, r, deps)
		if !ok {
			return
		}
		workspaceID := r.PathValue("workspaceId")
		siteID := r.PathValue("siteId")
		if !requirePathUUID(w, workspaceID, siteID) {
			return
		}
		if !requireWorkspaceAction(w, r, deps, principal, workspaceID, authz.ActionSiteRead) {
			return
		}
		switch r.Method {
		case http.MethodGet:
			draft, err := deps.Sites.ThemeDraft(r.Context(), principal, workspaceID, siteID)
			if err != nil {
				SiteError(w, err, "theme_draft_failed")
				return
			}
			writeData(w, r, http.StatusOK, draft)
		case http.MethodPut:
			if !requireWorkspaceAction(w, r, deps, principal, workspaceID, authz.ActionSiteDesign) {
				return
			}
			var input struct {
				Files json.RawMessage `json:"files"`
			}
			if !decodeBody(w, r, &input, 1<<20) {
				return
			}
			draft, err := deps.Sites.SaveThemeDraft(r.Context(), principal, workspaceID, siteID, input.Files)
			if err != nil {
				SiteError(w, err, "theme_draft_failed")
				return
			}
			writeData(w, r, http.StatusOK, draft)
		default:
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
		}
	}
}

// SiteThemeRevisions serves GET /.../theme/revisions（修订版历史）。
func SiteThemeRevisions(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		principal, ok := requireMemberSession(w, r, deps)
		if !ok {
			return
		}
		workspaceID := r.PathValue("workspaceId")
		siteID := r.PathValue("siteId")
		if !requirePathUUID(w, workspaceID, siteID) {
			return
		}
		revisions, err := deps.Sites.ListThemeRevisions(r.Context(), principal, workspaceID, siteID)
		if err != nil {
			SiteError(w, err, "theme_revisions_failed")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"items": revisions, "has_more": false})
	}
}

// SiteThemePublish serves POST /.../theme/revisions/{revisionId}/publish.
// 发布（或回滚到）一个修订版；site.lifecycle（人类），agent 不可达。
func SiteThemePublish(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		principal, ok := requireMemberSession(w, r, deps)
		if !ok {
			return
		}
		workspaceID := r.PathValue("workspaceId")
		siteID := r.PathValue("siteId")
		revisionID := r.PathValue("revisionId")
		if !requirePathUUID(w, workspaceID, siteID, revisionID) {
			return
		}
		if !requireWorkspaceAction(w, r, deps, principal, workspaceID, authz.ActionSiteLifecycle) {
			return
		}
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		if _, ok := requireIdempotencyKey(w, r); !ok {
			return
		}
		revision, err := deps.Sites.PublishThemeRevision(r.Context(), principal, workspaceID, siteID, revisionID)
		if err != nil {
			SiteError(w, err, "theme_publish_failed")
			return
		}
		writeData(w, r, http.StatusOK, revision)
	}
}

// SitePagesCollection serves GET/POST /api/workspaces/{ws}/sites/{siteId}/pages.
// POST 需 Idempotency-Key（站点域写命令）。
func SitePagesCollection(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		principal, ok := requireMemberSession(w, r, deps)
		if !ok {
			return
		}
		workspaceID := r.PathValue("workspaceId")
		siteID := r.PathValue("siteId")
		if !requirePathUUID(w, workspaceID, siteID) {
			return
		}
		switch r.Method {
		case http.MethodGet:
			pages, err := deps.Sites.ListSitePages(r.Context(), principal, workspaceID, siteID)
			if err != nil {
				SiteError(w, err, "site_pages_failed")
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"items": pages, "has_more": false})
		case http.MethodPost:
			if !requireWorkspaceAction(w, r, deps, principal, workspaceID, authz.ActionSiteDesign) {
				return
			}
			if _, ok := requireIdempotencyKey(w, r); !ok {
				return
			}
			var input site.CustomPageInput
			if !decodeBody(w, r, &input, 64*1024) {
				return
			}
			page, err := deps.Sites.CreateSitePage(r.Context(), principal, workspaceID, siteID, input)
			if err != nil {
				SiteError(w, err, "site_pages_failed")
				return
			}
			writeData(w, r, http.StatusCreated, page)
		default:
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
		}
	}
}

// SitePageResource serves PATCH/DELETE /api/workspaces/{ws}/sites/{siteId}/pages/{pageId}.
func SitePageResource(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		principal, ok := requireMemberSession(w, r, deps)
		if !ok {
			return
		}
		workspaceID := r.PathValue("workspaceId")
		siteID := r.PathValue("siteId")
		pageID := r.PathValue("pageId")
		if !requirePathUUID(w, workspaceID, siteID, pageID) {
			return
		}
		if !requireWorkspaceAction(w, r, deps, principal, workspaceID, authz.ActionSiteDesign) {
			return
		}
		switch r.Method {
		case http.MethodPatch:
			var input site.CustomPageInput
			if !decodeBody(w, r, &input, 64*1024) {
				return
			}
			page, err := deps.Sites.UpdateSitePage(r.Context(), principal, workspaceID, siteID, pageID, input)
			if err != nil {
				SiteError(w, err, "site_pages_failed")
				return
			}
			writeData(w, r, http.StatusOK, page)
		case http.MethodDelete:
			if _, ok := requireIdempotencyKey(w, r); !ok {
				return
			}
			if err := deps.Sites.DeleteSitePage(r.Context(), principal, workspaceID, siteID, pageID); err != nil {
				SiteError(w, err, "site_pages_failed")
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"ok": true})
		default:
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
		}
	}
}

