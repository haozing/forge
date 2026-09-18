package httpapi

// sites.go — the phase 5 public-site management surface: workspace site
// CRUD with If-Match revisions, binding CRUD behind the write-time binding
// gate and the JSON preview snapshot. Handlers only authenticate, enforce the
// workspace policy, call the site service and map domain errors; all SQL
// lives inside internal/site.

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strings"

	"agentchunzhi/internal/authz"
	"agentchunzhi/internal/delivery"
	"agentchunzhi/internal/site"
)

// requireSiteService answers 500 when the site service is not wired; only
// misconfigured process bootstrapping can hit this.
func requireSiteService(w http.ResponseWriter, deps Dependencies) bool {
	if deps.Sites == nil {
		writeError(w, http.StatusInternalServerError, "internal_error")
		return false
	}
	return true
}

// SiteError maps site domain errors onto the HTTP status/code contract.
// conflictCode is supplied by the caller so site endpoints answer
// slug_conflict while binding endpoints answer path_conflict on the shared
// ErrConflict sentinel.
func SiteError(w http.ResponseWriter, err error, conflictCode string) {
	switch {
	case err == nil:
		return
	case errors.Is(err, site.ErrSiteNotFound):
		writeError(w, http.StatusNotFound, "site_not_found")
	case errors.Is(err, site.ErrBindingNotFound):
		writeError(w, http.StatusNotFound, "binding_not_found")
	case errors.Is(err, site.ErrReleaseNotFound):
		writeError(w, http.StatusNotFound, "release_not_found")
	case errors.Is(err, site.ErrCommentNotFound):
		writeError(w, http.StatusNotFound, "comment_not_found")
	case errors.Is(err, site.ErrSlugInvalid):
		writeError(w, http.StatusUnprocessableEntity, "slug_invalid")
	case errors.Is(err, site.ErrPathInvalid):
		writeError(w, http.StatusUnprocessableEntity, "path_invalid")
	case errors.Is(err, site.ErrBindingTargetInvalid):
		writeError(w, http.StatusUnprocessableEntity, "binding_target_invalid")
	case errors.Is(err, site.ErrSiteDisabled):
		writeError(w, http.StatusConflict, "site_disabled")
	case errors.Is(err, site.ErrConflict):
		writeError(w, http.StatusConflict, conflictCode)
	case errors.Is(err, site.ErrInvalidInput):
		writeError(w, http.StatusUnprocessableEntity, "validation_failed")
	case errors.Is(err, site.ErrForbidden):
		writeError(w, http.StatusForbidden, "action_not_allowed")
	default:
		// 未映射错误不留日志就是盲区（§7.2 工具链验收时因此排查过久）：
		// 打出错误链与请求标识再返回 500。
		log.Printf("site unmapped error: %v", err)
		writeError(w, http.StatusInternalServerError, "internal_error")
	}
}

type CreateSiteRequest struct {
	Slug                string          `json:"slug"`
	Name                string          `json:"name"`
	Domain              string          `json:"domain"`
	DefaultContentScope string          `json:"default_content_scope"`
	HomepageConfig      json.RawMessage `json:"homepage_config"`
	PagesConfig         json.RawMessage `json:"pages_config"`
	NavigationConfig    json.RawMessage `json:"navigation_config"`
	StyleConfig         json.RawMessage `json:"style_config"`
}

type UpdateSiteRequest struct {
	Name                    *string   `json:"name"`
	Description             *string   `json:"description"`
	Brief                   *string   `json:"brief"`
	Domain                  *string   `json:"domain"`
	DefaultContentScope     *string   `json:"default_content_scope"`
	DefaultLocale           *string   `json:"default_locale"`
	EnabledLocales          *[]string `json:"enabled_locales"`
	FallbackToDefault       *bool     `json:"fallback_to_default"`
	CommentsMode            *string   `json:"comments_mode"`
	Status                  *string   `json:"status"`
	LogoAttachmentID        *string   `json:"logo_attachment_id"`
	FaviconAttachmentID     *string   `json:"favicon_attachment_id"`
	SocialImageAttachmentID *string   `json:"social_image_attachment_id"`
}

// SitesCollection serves GET/POST /api/workspaces/{workspaceId}/sites.
// Reads need site.read; creating needs site.manage plus an Idempotency-Key.
func SitesCollection(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		principal, ok := sessionPrincipal(w, r, deps)
		if !ok {
			return
		}
		if !requireSiteService(w, deps) {
			return
		}
		workspaceID := r.PathValue("workspaceId")
		if !requirePathUUID(w, workspaceID) {
			return
		}
		switch r.Method {
		case http.MethodGet:
			if !requireWorkspaceAction(w, r, deps, principal, workspaceID, authz.ActionSiteRead) {
				return
			}
			page, err := deps.Sites.ListSites(r.Context(), principal, workspaceID,
				r.URL.Query().Get("cursor"), atoiDefault(r.URL.Query().Get("limit"), 50))
			if err != nil {
				SiteError(w, err, "slug_conflict")
				return
			}
			writeData(w, r, http.StatusOK, map[string]any{
				"items": page.Items,
				"page":  cursorPageFrom(page.HasMore, page.NextCursor),
			})
		default:
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
		}
	}
}

// SiteResource serves GET/PATCH/DELETE
// /api/workspaces/{workspaceId}/sites/{siteId}. PATCH demands the site
// revision If-Match; DELETE is the soft disable (status='disabled') and
// honors an optional If-Match. G: PATCH = site.design（外观配置），
// 但携带 Status/Domain 的补丁升级为 site.lifecycle（停用/域名是生命周期权）。
func SiteResource(deps Dependencies) http.HandlerFunc {
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
		if !requirePathUUID(w, workspaceID, siteID) {
			return
		}
		switch r.Method {
		case http.MethodGet:
			if !requireWorkspaceAction(w, r, deps, principal, workspaceID, authz.ActionSiteRead) {
				return
			}
			item, err := deps.Sites.GetSite(r.Context(), principal, workspaceID, siteID)
			if err != nil {
				SiteError(w, err, "slug_conflict")
				return
			}
			writeETag(w, item.ETag)
			writeData(w, r, http.StatusOK, item)
		case http.MethodPatch:
			if _, ok := requireIfMatch(w, r); !ok {
				return
			}
			var input UpdateSiteRequest
			if !decodeBody(w, r, &input, 256*1024) {
				return
			}
			if !requireWorkspaceAction(w, r, deps, principal, workspaceID, authz.ActionSiteDesign) {
				return
			}
			if input.Status != nil || input.Domain != nil {
				if !requireWorkspaceAction(w, r, deps, principal, workspaceID, authz.ActionSiteLifecycle) {
					return
				}
			}
			item, err := deps.Sites.UpdateSite(r.Context(), principal, workspaceID, siteID,
				expectedRevisionFromIfMatch(r), site.UpdateSiteInput{
					Name:                    input.Name,
					Description:             input.Description,
					Brief:                   input.Brief,
					Domain:                  input.Domain,
					DefaultContentScope:     input.DefaultContentScope,
					DefaultLocale:           input.DefaultLocale,
					EnabledLocales:          input.EnabledLocales,
					FallbackToDefault:       input.FallbackToDefault,
					CommentsMode:            input.CommentsMode,
					Status:                  input.Status,
					LogoAttachmentID:        input.LogoAttachmentID,
					FaviconAttachmentID:     input.FaviconAttachmentID,
					SocialImageAttachmentID: input.SocialImageAttachmentID,
				})
			if err != nil {
				SiteError(w, err, "slug_conflict")
				return
			}
			writeETag(w, item.ETag)
			writeData(w, r, http.StatusOK, item)
		case http.MethodDelete:
			// G: 停用是站点生命周期权（admin）。
			if !requireWorkspaceAction(w, r, deps, principal, workspaceID, authz.ActionSiteLifecycle) {
				return
			}
			if _, ok := requireIdempotencyKey(w, r); !ok {
				return
			}
			item, err := deps.Sites.DisableSite(r.Context(), principal, workspaceID, siteID,
				expectedRevisionFromIfMatch(r))
			if err != nil {
				SiteError(w, err, "slug_conflict")
				return
			}
			writeETag(w, item.ETag)
			writeData(w, r, http.StatusOK, item)
		default:
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
		}
	}
}

// SiteInclusionsCollection serves GET
// /api/workspaces/{workspaceId}/sites/{siteId}/bindings —— 收录清单读模型
// （派生视图，站点方案 B4/B6）。写操作走 inclusions/{assetId}/exclusion|feature。
func SiteInclusionsCollection(deps Dependencies) http.HandlerFunc {
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
		if !requirePathUUID(w, workspaceID, siteID) {
			return
		}
		if !requireWorkspaceAction(w, r, deps, principal, workspaceID, authz.ActionSiteDesign) {
			return
		}
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		page, err := deps.Sites.ListBindings(r.Context(), principal, workspaceID, siteID,
			r.URL.Query().Get("cursor"), atoiDefault(r.URL.Query().Get("limit"), 50))
		if err != nil {
			SiteError(w, err, "path_conflict")
			return
		}
		writeData(w, r, http.StatusOK, map[string]any{
			"items": page.Items,
			"page":  cursorPageFrom(page.HasMore, page.NextCursor),
		})
	}
}

// SiteLint serves GET /api/workspaces/{workspaceId}/sites/{siteId}/lint:
// 站点体检报告（F2）。只读 advisory——任何发现都不阻断发布。site.read 即可看。
func SiteLint(deps Dependencies) http.HandlerFunc {
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
		if !requireWorkspaceAction(w, r, deps, principal, workspaceID, authz.ActionSiteRead) {
			return
		}
		report, err := deps.Sites.LintSite(r.Context(), principal, workspaceID, siteID)
		if err != nil {
			SiteError(w, err, "slug_conflict")
			return
		}
		writeData(w, r, http.StatusOK, map[string]any{
			"site_id":  report.SiteID,
			"findings": report.Findings,
			"counts":   report.Counts(),
		})
	}
}

// SiteActivity serves GET /api/workspaces/{workspaceId}/sites/{siteId}/activity:
// 站点动态时间线（F3-D2）：治理审计 ∪ 发布史，合并时间线（新→旧）。
func SiteActivity(deps Dependencies) http.HandlerFunc {
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
		if !requireWorkspaceAction(w, r, deps, principal, workspaceID, authz.ActionSiteRead) {
			return
		}
		items, err := deps.Sites.SiteActivity(r.Context(), principal, workspaceID, siteID,
			atoiDefault(r.URL.Query().Get("limit"), 50))
		if err != nil {
			SiteError(w, err, "slug_conflict")
			return
		}
		writeData(w, r, http.StatusOK, map[string]any{"items": items})
	}
}

// SiteInclusionAssetResource serves PUT/DELETE
// /api/workspaces/{workspaceId}/sites/{siteId}/inclusions/{assetId}/exclusion
// and /feature. PUT = 排除/精选生效，DELETE = 恢复收录/取消精选
// (站点方案 D2/B4；权限 site.design，统一方案 G)。
func SiteInclusionAssetResource(deps Dependencies) http.HandlerFunc {
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
		assetID := r.PathValue("assetId")
		if !requirePathUUID(w, workspaceID, siteID, assetID) {
			return
		}
		if !requireWorkspaceAction(w, r, deps, principal, workspaceID, authz.ActionSiteDesign) {
			return
		}
		kind := "exclusion"
		if strings.HasSuffix(r.URL.Path, "/feature") {
			kind = "feature"
		}
		active := r.Method == http.MethodPut
		var err error
		if kind == "feature" {
			err = deps.Sites.SetFeatured(r.Context(), principal, workspaceID, siteID, assetID, active)
		} else {
			err = deps.Sites.SetExcluded(r.Context(), principal, workspaceID, siteID, assetID, active)
		}
		if err != nil {
			SiteError(w, err, "inclusion_update_failed")
			return
		}
		writeData(w, r, http.StatusOK, map[string]any{"ok": true})
	}
}

// SitePreview serves the site preview surface:
//
//	GET  — the JSON snapshot (site + bindings, no-store) behind site.read;
//	POST — the real Delivery render behind site.read: body
//	       {style_config?, page?, display_path?} answers the full HTML of the
//	       requested page with the candidate style merged over the working
//	       style (design doc §8.2), always noindex + no-store.
func SitePreview(deps Dependencies) http.HandlerFunc {
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
		if !requirePathUUID(w, workspaceID, siteID) {
			return
		}
		if !requireWorkspaceAction(w, r, deps, principal, workspaceID, authz.ActionSiteRead) {
			return
		}
		switch r.Method {
		case http.MethodGet:
			snapshot, err := deps.Sites.Preview(r.Context(), principal, workspaceID, siteID)
			if err != nil {
				SiteError(w, err, "slug_conflict")
				return
			}
			// Previews are member-gated working state: they must never sit in
			// any shared cache, unlike the public face's short-lived caching.
			w.Header().Set("Cache-Control", "no-store")
			writeData(w, r, http.StatusOK, snapshot)
		case http.MethodPost:
			if deps.Delivery == nil {
				writeError(w, http.StatusInternalServerError, "internal_error")
				return
			}
			var input delivery.PreviewInput
			if !decodeBody(w, r, &input, 256*1024) {
				return
			}
			page, err := deps.Delivery.RenderPreview(r.Context(), principal, workspaceID, siteID, input)
			if err != nil {
				SiteError(w, err, "slug_conflict")
				return
			}
			w.Header().Set("Cache-Control", "no-store")
			w.Header().Set("X-Robots-Tag", "noindex, nofollow")
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(page.Body)
		default:
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
		}
	}
}

// SiteReleases serves GET/POST
// /api/workspaces/{workspaceId}/sites/{siteId}/releases. POST publishes the
// current working configuration (or, with base_release_id, republishes one
// historical snapshot — the rollback path, design doc §7.4) as a new
// immutable release and moves the published pointer. Both surfaces sit
// behind site.manage for writes / site.read for reads.
func SiteReleases(deps Dependencies) http.HandlerFunc {
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
		if !requirePathUUID(w, workspaceID, siteID) {
			return
		}
		switch r.Method {
		case http.MethodGet:
			if !requireWorkspaceAction(w, r, deps, principal, workspaceID, authz.ActionSiteRead) {
				return
			}
			page, err := deps.Sites.ListReleases(r.Context(), principal, workspaceID, siteID,
				r.URL.Query().Get("cursor"), atoiDefault(r.URL.Query().Get("limit"), 20))
			if err != nil {
				SiteError(w, err, "slug_conflict")
				return
			}
			writeData(w, r, http.StatusOK, map[string]any{
				"items": page.Items,
				"page":  cursorPageFrom(page.HasMore, page.NextCursor),
			})
		case http.MethodPost:
			if !requireWorkspaceAction(w, r, deps, principal, workspaceID, authz.ActionSiteDesign) {
				return
			}
			if _, ok := requireIdempotencyKey(w, r); !ok {
				return
			}
			var input struct {
				BaseReleaseID   string `json:"base_release_id"`
				ThemeRevisionID string `json:"theme_revision_id"`
			}
			if !decodeBody(w, r, &input, 4096) {
				return
			}
			item, err := deps.Sites.PublishRelease(r.Context(), principal, workspaceID, siteID, input.BaseReleaseID, input.ThemeRevisionID)
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
