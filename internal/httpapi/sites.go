package httpapi

// sites.go — the phase 5 public-site management surface: workspace site
// CRUD with If-Match revisions, binding CRUD behind the write-time binding
// gate and the JSON preview snapshot. Handlers only authenticate, enforce the
// workspace policy, call the site service and map domain errors; all SQL
// lives inside internal/site.

import (
	"encoding/json"
	"errors"
	"net/http"

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
		writeError(w, http.StatusInternalServerError, "internal_error")
	}
}

type CreateSiteRequest struct {
	Slug                string          `json:"slug"`
	Name                string          `json:"name"`
	Domain              string          `json:"domain"`
	Template            string          `json:"template"`
	DefaultContentScope string          `json:"default_content_scope"`
	HomepageConfig      json.RawMessage `json:"homepage_config"`
	NavigationConfig    json.RawMessage `json:"navigation_config"`
	StyleConfig         json.RawMessage `json:"style_config"`
}

type UpdateSiteRequest struct {
	Name                *string          `json:"name"`
	Domain              *string          `json:"domain"`
	Template            *string          `json:"template"`
	DefaultContentScope *string          `json:"default_content_scope"`
	HomepageConfig      *json.RawMessage `json:"homepage_config"`
	NavigationConfig    *json.RawMessage `json:"navigation_config"`
	StyleConfig         *json.RawMessage `json:"style_config"`
	Status              *string          `json:"status"`
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
		case http.MethodPost:
			if !requireWorkspaceAction(w, r, deps, principal, workspaceID, authz.ActionSiteManage) {
				return
			}
			if _, ok := requireIdempotencyKey(w, r); !ok {
				return
			}
			var input CreateSiteRequest
			if !decodeBody(w, r, &input, 256*1024) {
				return
			}
			item, err := deps.Sites.CreateSite(r.Context(), principal, workspaceID, site.CreateSiteInput{
				Slug:                input.Slug,
				Name:                input.Name,
				Domain:              input.Domain,
				Template:            input.Template,
				DefaultContentScope: input.DefaultContentScope,
				HomepageConfig:      input.HomepageConfig,
				NavigationConfig:    input.NavigationConfig,
				StyleConfig:         input.StyleConfig,
			})
			if err != nil {
				SiteError(w, err, "slug_conflict")
				return
			}
			writeETag(w, item.ETag)
			writeData(w, r, http.StatusCreated, item)
		default:
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
		}
	}
}

// SiteResource serves GET/PATCH/DELETE
// /api/workspaces/{workspaceId}/sites/{siteId}. PATCH demands the site
// revision If-Match; DELETE is the soft disable (status='disabled') and
// honors an optional If-Match.
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
			if !requireWorkspaceAction(w, r, deps, principal, workspaceID, authz.ActionSiteManage) {
				return
			}
			if _, ok := requireIfMatch(w, r); !ok {
				return
			}
			var input UpdateSiteRequest
			if !decodeBody(w, r, &input, 256*1024) {
				return
			}
			item, err := deps.Sites.UpdateSite(r.Context(), principal, workspaceID, siteID,
				expectedRevisionFromIfMatch(r), site.UpdateSiteInput{
					Name:                input.Name,
					Domain:              input.Domain,
					Template:            input.Template,
					DefaultContentScope: input.DefaultContentScope,
					HomepageConfig:      input.HomepageConfig,
					NavigationConfig:    input.NavigationConfig,
					StyleConfig:         input.StyleConfig,
					Status:              input.Status,
				})
			if err != nil {
				SiteError(w, err, "slug_conflict")
				return
			}
			writeETag(w, item.ETag)
			writeData(w, r, http.StatusOK, item)
		case http.MethodDelete:
			if !requireWorkspaceAction(w, r, deps, principal, workspaceID, authz.ActionSiteManage) {
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

type CreateBindingRequest struct {
	AssetID       string          `json:"asset_id"`
	DisplayPath   string          `json:"display_path"`
	ContentType   string          `json:"content_type"`
	SectionSlug   string          `json:"section_slug"`
	SortOrder     int             `json:"sort_order"`
	OnHomepage    bool            `json:"on_homepage"`
	OnNavigation  bool            `json:"on_navigation"`
	DisplayConfig json.RawMessage `json:"display_config"`
}

type UpdateBindingRequest struct {
	DisplayPath   *string          `json:"display_path"`
	ContentType   *string          `json:"content_type"`
	SectionSlug   *string          `json:"section_slug"`
	SortOrder     *int             `json:"sort_order"`
	OnHomepage    *bool            `json:"on_homepage"`
	OnNavigation  *bool            `json:"on_navigation"`
	DisplayConfig *json.RawMessage `json:"display_config"`
}

// SiteBindingsCollection serves GET/POST
// /api/workspaces/{workspaceId}/sites/{siteId}/bindings. The stage 5
// matrix gates both surfaces behind site.manage.
func SiteBindingsCollection(deps Dependencies) http.HandlerFunc {
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
		if !requireWorkspaceAction(w, r, deps, principal, workspaceID, authz.ActionSiteManage) {
			return
		}
		switch r.Method {
		case http.MethodGet:
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
		case http.MethodPost:
			if _, ok := requireIdempotencyKey(w, r); !ok {
				return
			}
			var input CreateBindingRequest
			if !decodeBody(w, r, &input, 64*1024) {
				return
			}
			item, err := deps.Sites.CreateBinding(r.Context(), principal, workspaceID, siteID, site.CreateBindingInput{
				AssetID:       input.AssetID,
				DisplayPath:   input.DisplayPath,
				ContentType:   input.ContentType,
				SectionSlug:   input.SectionSlug,
				SortOrder:     input.SortOrder,
				OnHomepage:    input.OnHomepage,
				OnNavigation:  input.OnNavigation,
				DisplayConfig: input.DisplayConfig,
			})
			if err != nil {
				SiteError(w, err, "path_conflict")
				return
			}
			writeData(w, r, http.StatusCreated, item)
		default:
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
		}
	}
}

// SiteBindingResource serves PATCH/DELETE
// /api/workspaces/{workspaceId}/sites/{siteId}/bindings/{bindingId}.
// Bindings carry no revision column, so these commands use the
// Idempotency-Key contract without If-Match.
func SiteBindingResource(deps Dependencies) http.HandlerFunc {
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
		bindingID := r.PathValue("bindingId")
		if !requirePathUUID(w, workspaceID, siteID, bindingID) {
			return
		}
		if !requireWorkspaceAction(w, r, deps, principal, workspaceID, authz.ActionSiteManage) {
			return
		}
		switch r.Method {
		case http.MethodPatch:
			if _, ok := requireIdempotencyKey(w, r); !ok {
				return
			}
			var input UpdateBindingRequest
			if !decodeBody(w, r, &input, 64*1024) {
				return
			}
			item, err := deps.Sites.UpdateBinding(r.Context(), principal, workspaceID, siteID, bindingID, site.UpdateBindingInput{
				DisplayPath:   input.DisplayPath,
				ContentType:   input.ContentType,
				SectionSlug:   input.SectionSlug,
				SortOrder:     input.SortOrder,
				OnHomepage:    input.OnHomepage,
				OnNavigation:  input.OnNavigation,
				DisplayConfig: input.DisplayConfig,
			})
			if err != nil {
				SiteError(w, err, "path_conflict")
				return
			}
			writeData(w, r, http.StatusOK, item)
		case http.MethodDelete:
			if _, ok := requireIdempotencyKey(w, r); !ok {
				return
			}
			item, err := deps.Sites.DeleteBinding(r.Context(), principal, workspaceID, siteID, bindingID)
			if err != nil {
				SiteError(w, err, "path_conflict")
				return
			}
			writeData(w, r, http.StatusOK, item)
		default:
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
		}
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
			if !requireWorkspaceAction(w, r, deps, principal, workspaceID, authz.ActionSiteManage) {
				return
			}
			if _, ok := requireIdempotencyKey(w, r); !ok {
				return
			}
			var input struct {
				BaseReleaseID string `json:"base_release_id"`
			}
			if !decodeBody(w, r, &input, 4096) {
				return
			}
			item, err := deps.Sites.PublishRelease(r.Context(), principal, workspaceID, siteID, input.BaseReleaseID)
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
