package httpapi

// v2_public_sites.go — the phase 5 public read surface (stage 5 P5-3):
// anonymous or optional-member reads of one site slug under
// /api/public/v2/sites/{slug}/.... Handlers only resolve the optional session
// cookie, precompute the throttled client address, parse the query envelope
// and map domain errors; every content decision lives in internal/site and
// the query service. These endpoints never require an Idempotency-Key (safe
// reads, and requiresHTTPIdempotency does not cover /api/public/v2).

import (
	"errors"
	"math"
	"net/http"
	"strconv"
	"strings"

	"agentchunzhi/internal/auth"
	agentquery "agentchunzhi/internal/query"
	"agentchunzhi/internal/site"
)

// requirePublicSites answers 500 when the public reader is not wired; only
// misconfigured process bootstrapping can hit this.
func requirePublicSites(w http.ResponseWriter, deps Dependencies) bool {
	if deps.PublicSites == nil {
		writeError(w, http.StatusInternalServerError, "internal_error")
		return false
	}
	return true
}

// publicVisitorPrincipal resolves the optional member session of a public
// read (plan D5' decision 1: the public face accepts the API session cookie).
// Every authentication failure degrades to anonymous instead of failing the
// read — the public face never requires a session.
func publicVisitorPrincipal(r *http.Request, deps Dependencies) auth.Principal {
	principal, err := deps.SessionService.Authenticate(r.Context(), r)
	if err != nil {
		return auth.Principal{}
	}
	return principal
}

// publicSiteSlug validates the {slug} path segment against the site rule and
// answers the anti-probing 404 for malformed values.
func publicSiteSlug(w http.ResponseWriter, r *http.Request) (string, bool) {
	slug := r.PathValue("slug")
	if !site.ValidSlug(slug) {
		writeError(w, http.StatusNotFound, "site_not_found")
		return "", false
	}
	return slug, true
}

// writePublicSiteError maps the public read face's domain errors onto the
// status contract: disabled and unknown sites collapse into one 404
// (anti-probing, plan §4), throttle refusals answer 429 with Retry-After,
// query contract errors keep their fixed status/code and everything else is
// an infrastructure failure.
func writePublicSiteError(w http.ResponseWriter, err error) {
	switch {
	case err == nil:
		return
	case errors.Is(err, site.ErrSiteNotFound),
		errors.Is(err, site.ErrSiteDisabled),
		errors.Is(err, site.ErrPathInvalid):
		// One 404 for missing, malformed and disabled targets: no existence
		// or configuration feedback (plan §4 / D5').
		writeError(w, http.StatusNotFound, "site_not_found")
		return
	case errors.Is(err, site.ErrPublicThrottleUnavailable):
		writeError(w, http.StatusServiceUnavailable, "database_unavailable")
		return
	}
	var rateLimited *site.PublicRateLimitError
	if errors.As(err, &rateLimited) {
		if rateLimited.RetryAfter > 0 {
			seconds := int(math.Ceil(rateLimited.RetryAfter.Seconds()))
			w.Header().Set("Retry-After", strconv.Itoa(seconds))
		}
		writeError(w, http.StatusTooManyRequests, "rate_limited")
		return
	}
	var apiErr *agentquery.APIError
	if errors.As(err, &apiErr) {
		// The query contract errors (validation 422, fail-closed 503
		// site_content_unavailable, expired cursors) keep their fixed pairs.
		status, code := agentquery.HTTPStatus(err)
		writeError(w, status, code)
		return
	}
	writeError(w, http.StatusInternalServerError, "internal_error")
}

// publicCacheHeaders stamps the D4 cache policy and the conditional-GET
// validator. When If-None-Match matches, the representation is answered with
// 304 and no body; the handler must then return without writing a payload.
func publicCacheHeaders(w http.ResponseWriter, r *http.Request, etag string) bool {
	writeETag(w, etag)
	w.Header().Set("Cache-Control", site.PublicCacheControl)
	if site.ETagMatches(r.Header.Get("If-None-Match"), etag) {
		w.WriteHeader(http.StatusNotModified)
		return true
	}
	return false
}

// publicSiteView serves GET /api/public/v2/sites/{slug}: the site header
// plus the homepage sections rendered with live content (plan §4).
func publicSiteView(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		if !requirePublicSites(w, deps) {
			return
		}
		slug, ok := publicSiteSlug(w, r)
		if !ok {
			return
		}
		view, err := deps.PublicSites.Home(r.Context(),
			effectiveClientAddr(r, deps.TrustedProxyCIDRs),
			publicVisitorPrincipal(r, deps), slug)
		if err != nil {
			writePublicSiteError(w, err)
			return
		}
		if publicCacheHeaders(w, r, view.ETag) {
			return
		}
		writeData(w, r, http.StatusOK, view)
	}
}

// publicSitePosts serves GET /api/public/v2/sites/{slug}/posts with the
// query envelope q/tags_any/tags_all/tags_none/cursor/limit (plan §4).
func publicSitePosts(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		if !requirePublicSites(w, deps) {
			return
		}
		slug, ok := publicSiteSlug(w, r)
		if !ok {
			return
		}
		params := r.URL.Query()
		page, err := deps.PublicSites.Posts(r.Context(),
			effectiveClientAddr(r, deps.TrustedProxyCIDRs),
			publicVisitorPrincipal(r, deps), slug, site.PublicPostQuery{
				QueryText: params.Get("q"),
				TagsAny:   splitPublicTagKeys(params.Get("tags_any")),
				TagsAll:   splitPublicTagKeys(params.Get("tags_all")),
				TagsNone:  splitPublicTagKeys(params.Get("tags_none")),
				Cursor:    params.Get("cursor"),
				Limit:     atoiDefault(params.Get("limit"), 20),
			})
		if err != nil {
			writePublicSiteError(w, err)
			return
		}
		if publicCacheHeaders(w, r, page.ETag) {
			return
		}
		writeData(w, r, http.StatusOK, page)
	}
}

// publicSitePost serves GET /api/public/v2/sites/{slug}/posts/{displayPath...}:
// the detail projection behind the §3.3 re-check.
func publicSitePost(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		if !requirePublicSites(w, deps) {
			return
		}
		slug, ok := publicSiteSlug(w, r)
		if !ok {
			return
		}
		content, err := deps.PublicSites.Post(r.Context(),
			effectiveClientAddr(r, deps.TrustedProxyCIDRs),
			publicVisitorPrincipal(r, deps), slug, r.PathValue("displayPath"))
		if err != nil {
			writePublicSiteError(w, err)
			return
		}
		if publicCacheHeaders(w, r, content.ETag) {
			return
		}
		writeData(w, r, http.StatusOK, content)
	}
}

// publicSiteSection serves GET /api/public/v2/sites/{slug}/sections/{sectionSlug}.
func publicSiteSection(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		if !requirePublicSites(w, deps) {
			return
		}
		slug, ok := publicSiteSlug(w, r)
		if !ok {
			return
		}
		page, err := deps.PublicSites.Section(r.Context(),
			effectiveClientAddr(r, deps.TrustedProxyCIDRs),
			publicVisitorPrincipal(r, deps), slug,
			r.PathValue("sectionSlug"), atoiDefault(r.URL.Query().Get("limit"), 20))
		if err != nil {
			writePublicSiteError(w, err)
			return
		}
		if publicCacheHeaders(w, r, page.ETag) {
			return
		}
		writeData(w, r, http.StatusOK, page)
	}
}

// publicSiteTags serves GET /api/public/v2/sites/{slug}/tags: the (public,
// published) facet cloud of the site workspace (plan B4/§5.2).
func publicSiteTags(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		if !requirePublicSites(w, deps) {
			return
		}
		slug, ok := publicSiteSlug(w, r)
		if !ok {
			return
		}
		items, err := deps.PublicSites.Tags(r.Context(),
			effectiveClientAddr(r, deps.TrustedProxyCIDRs),
			publicVisitorPrincipal(r, deps), slug,
			atoiDefault(r.URL.Query().Get("limit"), 50))
		if err != nil {
			writePublicSiteError(w, err)
			return
		}
		w.Header().Set("Cache-Control", site.PublicCacheControl)
		writeData(w, r, http.StatusOK, map[string]any{"items": items})
	}
}

// publicSiteTagPage serves GET /api/public/v2/sites/{slug}/tags/{key}: the
// tag archive as the tags_any=[key] list face (plan §4).
func publicSiteTagPage(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		if !requirePublicSites(w, deps) {
			return
		}
		slug, ok := publicSiteSlug(w, r)
		if !ok {
			return
		}
		params := r.URL.Query()
		page, err := deps.PublicSites.TagPage(r.Context(),
			effectiveClientAddr(r, deps.TrustedProxyCIDRs),
			publicVisitorPrincipal(r, deps), slug, r.PathValue("key"), site.PublicPostQuery{
				Cursor: params.Get("cursor"),
				Limit:  atoiDefault(params.Get("limit"), 20),
			})
		if err != nil {
			writePublicSiteError(w, err)
			return
		}
		if publicCacheHeaders(w, r, page.ETag) {
			return
		}
		writeData(w, r, http.StatusOK, page)
	}
}

// publicSiteSearch serves GET /api/public/v2/sites/{slug}/search with a
// mandatory q and mode=fulltext|hybrid (plan §4).
func publicSiteSearch(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		if !requirePublicSites(w, deps) {
			return
		}
		slug, ok := publicSiteSlug(w, r)
		if !ok {
			return
		}
		params := r.URL.Query()
		queryText := strings.TrimSpace(params.Get("q"))
		if queryText == "" {
			writeError(w, http.StatusUnprocessableEntity, "query_text_required")
			return
		}
		page, err := deps.PublicSites.Search(r.Context(),
			effectiveClientAddr(r, deps.TrustedProxyCIDRs),
			publicVisitorPrincipal(r, deps), slug, queryText,
			params.Get("mode"), site.PublicPostQuery{
				Cursor: params.Get("cursor"),
				Limit:  atoiDefault(params.Get("limit"), 20),
			})
		if err != nil {
			writePublicSiteError(w, err)
			return
		}
		if publicCacheHeaders(w, r, page.ETag) {
			return
		}
		writeData(w, r, http.StatusOK, page)
	}
}

// splitPublicTagKeys splits one comma-separated query parameter into tag
// keys; absent parameters yield nil so the query validator sees no group.
func splitPublicTagKeys(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	keys := make([]string, 0, len(parts))
	for _, part := range parts {
		if key := strings.TrimSpace(part); key != "" {
			keys = append(keys, key)
		}
	}
	if len(keys) == 0 {
		return nil
	}
	return keys
}
