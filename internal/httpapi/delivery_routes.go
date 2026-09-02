package httpapi

// delivery_routes.go — the server-rendered HTML face of the public sites
// (design doc §4.1). These routes live outside /api, so the OpenAPI contract
// gate does not cover them; delivery_routes_test.go pins the registration
// table instead. Handlers resolve the optional member session, precompute
// the throttled address and map delivery/site errors onto HTML error pages.

import (
	"errors"
	"math"
	"net/http"
	"strconv"

	"agentchunzhi/internal/delivery"
	"agentchunzhi/internal/site"

	agentquery "agentchunzhi/internal/query"
)

// deliveryCSP is the Content-Security-Policy of every HTML response (design
// doc §10.7): inline style only (CSS variables injection), same-origin
// scripts (the search island ships as a static file), no framing, no base.
const deliveryCSP = "default-src 'none'; style-src 'unsafe-inline'; script-src 'self'; img-src 'self' data:; base-uri 'none'; frame-ancestors 'none'; form-action 'self'"

// requireDelivery answers 500 when the delivery service is not wired.
func requireDelivery(w http.ResponseWriter, deps Dependencies) *delivery.Service {
	if deps.Delivery == nil {
		writeError(w, http.StatusInternalServerError, "internal_error")
		return nil
	}
	return deps.Delivery
}

// deliveryBaseURL derives the absolute URL prefix of the current request.
func deliveryBaseURL(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if r.Header.Get("X-Forwarded-Proto") == "https" {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

// writeDeliveryPage stamps the delivery headers and writes the page body;
// a matching If-None-Match answers 304 without a body.
func writeDeliveryPage(w http.ResponseWriter, r *http.Request, service *delivery.Service, page *delivery.Response) {
	writeETag(w, page.ETag)
	w.Header().Set("Content-Type", page.ContentType)
	w.Header().Set("Cache-Control", page.CacheControl)
	w.Header().Set("Content-Security-Policy", deliveryCSP)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
	if page.NoIndex {
		w.Header().Set("X-Robots-Tag", "noindex, nofollow")
	}
	if page.ETag != "" && site.ETagMatches(r.Header.Get("If-None-Match"), page.ETag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.WriteHeader(page.Status)
	_, _ = w.Write(page.Body)
}

// writeDeliveryError maps one delivery pipeline error onto the HTML status
// contract (404/410 collapse into the same page, anti-probing parity with
// the JSON face).
func writeDeliveryError(w http.ResponseWriter, r *http.Request, service *delivery.Service, err error) {
	status := http.StatusInternalServerError
	switch {
	case err == nil:
		return
	case errors.Is(err, site.ErrSiteNotFound),
		errors.Is(err, site.ErrSiteDisabled),
		errors.Is(err, site.ErrPathInvalid),
		errors.Is(err, delivery.ErrFeedDisabled):
		status = http.StatusNotFound
	case errors.Is(err, site.ErrPublicThrottleUnavailable):
		writeError(w, http.StatusServiceUnavailable, "database_unavailable")
		return
	}
	var rateLimited *site.PublicRateLimitError
	if errors.As(err, &rateLimited) {
		if rateLimited.RetryAfter > 0 {
			w.Header().Set("Retry-After", strconv.Itoa(int(math.Ceil(rateLimited.RetryAfter.Seconds()))))
		}
		writeError(w, http.StatusTooManyRequests, "rate_limited")
		return
	}
	var apiErr *agentquery.APIError
	if errors.As(err, &apiErr) && status == http.StatusInternalServerError {
		status, _ = agentquery.HTTPStatus(err)
	}
	writeDeliveryPage(w, r, service, service.ErrorPage(status))
}

// deliverySiteHome serves /sites/{slug} and /sites/{slug}/ (the subtree
// registration doubles as the site-scoped 404 catch-all).
func deliverySiteHome(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		service := requireDelivery(w, deps)
		if service == nil {
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			writeDeliveryPage(w, r, service, service.ErrorPage(http.StatusMethodNotAllowed))
			return
		}
		slug := r.PathValue("slug")
		if !site.ValidSlug(slug) {
			writeDeliveryPage(w, r, service, service.ErrorPage(http.StatusNotFound))
			return
		}
		if r.URL.Path != "/sites/"+slug && r.URL.Path != "/sites/"+slug+"/" {
			writeDeliveryPage(w, r, service, service.ErrorPage(http.StatusNotFound))
			return
		}
		page, err := service.Home(r.Context(), effectiveClientAddr(r, deps.TrustedProxyCIDRs),
			publicVisitorPrincipal(r, deps), slug, deliveryBaseURL(r))
		if err != nil {
			writeDeliveryError(w, r, service, err)
			return
		}
		writeDeliveryPage(w, r, service, page)
	}
}

// deliverySitePosts serves /sites/{slug}/posts and /sites/{slug}/posts/
// (the list page; the empty displayPath of the detail pattern).
func deliverySitePosts(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		service := requireDelivery(w, deps)
		if service == nil {
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			writeDeliveryPage(w, r, service, service.ErrorPage(http.StatusMethodNotAllowed))
			return
		}
		slug := r.PathValue("slug")
		if !site.ValidSlug(slug) {
			writeDeliveryPage(w, r, service, service.ErrorPage(http.StatusNotFound))
			return
		}
		page, err := service.Posts(r.Context(), effectiveClientAddr(r, deps.TrustedProxyCIDRs),
			publicVisitorPrincipal(r, deps), slug, r.URL.Query().Get("cursor"), deliveryBaseURL(r))
		if err != nil {
			writeDeliveryError(w, r, service, err)
			return
		}
		writeDeliveryPage(w, r, service, page)
	}
}

// deliverySitePost serves /sites/{slug}/posts/{displayPath...}: an empty
// displayPath is the list, anything else the detail page.
func deliverySitePost(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		service := requireDelivery(w, deps)
		if service == nil {
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			writeDeliveryPage(w, r, service, service.ErrorPage(http.StatusMethodNotAllowed))
			return
		}
		slug := r.PathValue("slug")
		if !site.ValidSlug(slug) {
			writeDeliveryPage(w, r, service, service.ErrorPage(http.StatusNotFound))
			return
		}
		displayPath := r.PathValue("displayPath")
		if displayPath == "" {
			page, err := service.Posts(r.Context(), effectiveClientAddr(r, deps.TrustedProxyCIDRs),
				publicVisitorPrincipal(r, deps), slug, r.URL.Query().Get("cursor"), deliveryBaseURL(r))
			if err != nil {
				writeDeliveryError(w, r, service, err)
				return
			}
			writeDeliveryPage(w, r, service, page)
			return
		}
		page, err := service.Post(r.Context(), effectiveClientAddr(r, deps.TrustedProxyCIDRs),
			publicVisitorPrincipal(r, deps), slug, displayPath, deliveryBaseURL(r))
		if err != nil {
			writeDeliveryError(w, r, service, err)
			return
		}
		writeDeliveryPage(w, r, service, page)
	}
}

// deliverySiteSection serves /sites/{slug}/sections/{sectionSlug}(/).
func deliverySiteSection(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		service := requireDelivery(w, deps)
		if service == nil {
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			writeDeliveryPage(w, r, service, service.ErrorPage(http.StatusMethodNotAllowed))
			return
		}
		slug := r.PathValue("slug")
		if !site.ValidSlug(slug) {
			writeDeliveryPage(w, r, service, service.ErrorPage(http.StatusNotFound))
			return
		}
		page, err := service.Section(r.Context(), effectiveClientAddr(r, deps.TrustedProxyCIDRs),
			publicVisitorPrincipal(r, deps), slug, r.PathValue("sectionSlug"), deliveryBaseURL(r))
		if err != nil {
			writeDeliveryError(w, r, service, err)
			return
		}
		writeDeliveryPage(w, r, service, page)
	}
}

// deliverySiteTags serves /sites/{slug}/tags and /sites/{slug}/tags/.
func deliverySiteTags(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		service := requireDelivery(w, deps)
		if service == nil {
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			writeDeliveryPage(w, r, service, service.ErrorPage(http.StatusMethodNotAllowed))
			return
		}
		slug := r.PathValue("slug")
		if !site.ValidSlug(slug) {
			writeDeliveryPage(w, r, service, service.ErrorPage(http.StatusNotFound))
			return
		}
		page, err := service.Tags(r.Context(), effectiveClientAddr(r, deps.TrustedProxyCIDRs),
			publicVisitorPrincipal(r, deps), slug, deliveryBaseURL(r))
		if err != nil {
			writeDeliveryError(w, r, service, err)
			return
		}
		writeDeliveryPage(w, r, service, page)
	}
}

// deliverySiteTagPage serves /sites/{slug}/tags/{key}(/).
func deliverySiteTagPage(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		service := requireDelivery(w, deps)
		if service == nil {
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			writeDeliveryPage(w, r, service, service.ErrorPage(http.StatusMethodNotAllowed))
			return
		}
		slug := r.PathValue("slug")
		if !site.ValidSlug(slug) {
			writeDeliveryPage(w, r, service, service.ErrorPage(http.StatusNotFound))
			return
		}
		page, err := service.TagPage(r.Context(), effectiveClientAddr(r, deps.TrustedProxyCIDRs),
			publicVisitorPrincipal(r, deps), slug, r.PathValue("key"), r.URL.Query().Get("cursor"), deliveryBaseURL(r))
		if err != nil {
			writeDeliveryError(w, r, service, err)
			return
		}
		writeDeliveryPage(w, r, service, page)
	}
}

// deliverySiteSearch serves /sites/{slug}/search (static shell + JS island).
func deliverySiteSearch(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		service := requireDelivery(w, deps)
		if service == nil {
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			writeDeliveryPage(w, r, service, service.ErrorPage(http.StatusMethodNotAllowed))
			return
		}
		slug := r.PathValue("slug")
		if !site.ValidSlug(slug) {
			writeDeliveryPage(w, r, service, service.ErrorPage(http.StatusNotFound))
			return
		}
		page, err := service.Search(r.Context(), effectiveClientAddr(r, deps.TrustedProxyCIDRs),
			publicVisitorPrincipal(r, deps), slug, r.URL.Query().Get("q"), deliveryBaseURL(r))
		if err != nil {
			writeDeliveryError(w, r, service, err)
			return
		}
		writeDeliveryPage(w, r, service, page)
	}
}

// deliverySiteFeed serves rss.xml / sitemap.xml / robots.txt.
func deliverySiteFeed(kind string) func(deps Dependencies) http.HandlerFunc {
	return func(deps Dependencies) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodGet && r.Method != http.MethodHead {
				writeDeliveryPage(w, r, deps.Delivery, deps.Delivery.ErrorPage(http.StatusMethodNotAllowed))
				return
			}
			service := requireDelivery(w, deps)
			if service == nil {
				return
			}
			slug := r.PathValue("slug")
			if !site.ValidSlug(slug) {
				writeDeliveryPage(w, r, service, service.ErrorPage(http.StatusNotFound))
				return
			}
			var page *delivery.Response
			var err error
			switch kind {
			case "rss":
				page, err = service.RSS(r.Context(), effectiveClientAddr(r, deps.TrustedProxyCIDRs),
					publicVisitorPrincipal(r, deps), slug, deliveryBaseURL(r))
			case "sitemap":
				page, err = service.Sitemap(r.Context(), effectiveClientAddr(r, deps.TrustedProxyCIDRs),
					publicVisitorPrincipal(r, deps), slug, deliveryBaseURL(r))
			default:
				page, err = service.Robots(r.Context(), effectiveClientAddr(r, deps.TrustedProxyCIDRs),
					publicVisitorPrincipal(r, deps), slug, deliveryBaseURL(r))
			}
			if err != nil {
				writeDeliveryError(w, r, service, err)
				return
			}
			writeDeliveryPage(w, r, service, page)
		}
	}
}

// deliverySearchScript serves the embedded search island script.
func deliverySearchScript(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if deps.Delivery == nil {
			writeError(w, http.StatusInternalServerError, "internal_error")
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		page := deps.Delivery.SearchScript()
		w.Header().Set("Content-Type", page.ContentType)
		w.Header().Set("Cache-Control", page.CacheControl)
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.WriteHeader(page.Status)
		_, _ = w.Write(page.Body)
	}
}
