package httpapi

// delivery_routes.go — the server-rendered HTML face of the public sites
// (design doc §4.1). These routes live outside /api, so the OpenAPI contract
// gate does not cover them; delivery_routes_test.go pins the registration
// table instead. Handlers resolve the optional member session, precompute
// the throttled address and map delivery/site errors onto HTML error pages.

import (
	"errors"
	"io"
	"log"
	"math"
	"net/http"
	"strconv"
	"strings"

	"agentchunzhi/internal/auth"
	"agentchunzhi/internal/delivery"
	"agentchunzhi/internal/objectstore"
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

// deliveryBaseURL resolves the absolute URL prefix for canonical/og:url/
// sitemap/JSON-LD. The configured DeliveryPublicBaseURL wins whenever it is
// set: page bodies are cached under a Host-agnostic key, so a Host-derived
// prefix would let any internal probe bake its own origin into every
// visitor's canonical for one cache TTL. Request-derived stays the
// development fallback (no PUBLIC_APP_BASE_URL configured).
func (d Dependencies) deliveryBaseURL(r *http.Request) string {
	if strings.TrimSpace(d.DeliveryPublicBaseURL) != "" {
		return strings.TrimRight(strings.TrimSpace(d.DeliveryPublicBaseURL), "/")
	}
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
	if page.RedirectPath != "" {
		// Same-site 301 (moved display_path, G2): Location only, no body,
		// no CSP/ETag machinery — never cached by the page cache.
		w.Header().Set("Location", page.RedirectPath)
		w.Header().Set("Content-Type", page.ContentType)
		w.Header().Set("Cache-Control", page.CacheControl)
		w.Header().Set("X-Robots-Tag", "noindex, nofollow")
		w.WriteHeader(page.Status)
		return
	}
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
	if status == http.StatusInternalServerError {
		log.Printf("delivery internal error path=%s err=%v", r.URL.Path, err)
	}
	writeDeliveryPage(w, r, service, service.ErrorPage(status))
}

// deliverySiteHome serves /sites/{slug} and /sites/{slug}/ (the subtree
// registration doubles as the site-scoped 404 catch-all).
// deliveryLocale extracts and shape-validates the optional locale prefix
// (D11/E)。空串 = 默认语言（无前缀访问）；非法形态一律空串（兜底路由吞掉）。
func deliveryLocale(r *http.Request) string {
	return localeCodeShape(r.PathValue("locale"))
}

// localeCodeShape normalizes a two-letter language code, or answers "" for
// any other shape.
func localeCodeShape(value string) string {
	locale := strings.ToLower(strings.TrimSpace(value))
	if len(locale) != 2 {
		return ""
	}
	for _, char := range locale {
		if char < 'a' || char > 'z' {
			return ""
		}
	}
	return locale
}

// withLocalePrefix rewrites the optional /{locale}/ prefix of the public-site
// tree (D11/E): /sites/{slug}/{locale}/… → /sites/{slug}/…, exposing the
// locale to handlers via r.SetPathValue. Only strict two-letter codes are
// consumed — fixed route segments (posts/tags/media/c/…) never match, and
// content slugs equal to a language code are refused at write time (K7
// reserved slugs), so no ambiguity exists.
func withLocalePrefix(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if rest, ok := strings.CutPrefix(r.URL.Path, "/sites/"); ok {
			parts := strings.SplitN(rest, "/", 3)
			if len(parts) >= 2 && site.ValidSlug(parts[0]) {
				if code := localeCodeShape(parts[1]); code != "" {
					trailing := ""
					if len(parts) == 3 {
						trailing = parts[2]
					}
					r.URL.Path = "/sites/" + parts[0] + "/" + trailing
					r.SetPathValue("locale", code)
				}
			}
		}
		next.ServeHTTP(w, r)
	})
}

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
			publicVisitorPrincipal(r, deps), slug, deps.deliveryBaseURL(r), deliveryLocale(r))
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
			publicVisitorPrincipal(r, deps), slug, r.URL.Query().Get("cursor"), deps.deliveryBaseURL(r))
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
		// The detail page's JS-free comment form posts here (action is
		// {{.PostPath}}/comments); every other non-GET stays a 405 page.
		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/comments") {
			deliverySiteCommentPost(deps)(w, r)
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
		// The canonical post URL carries no trailing slash (sitemap, postHref
		// and rel=canonical all use the bare path); the slashed variant 301s
		// so crawlers settle on one indexable form.
		if trimmed := strings.TrimSuffix(displayPath, "/"); trimmed != "" && trimmed != displayPath {
			http.Redirect(w, r, "/sites/"+slug+"/posts/"+trimmed, http.StatusMovedPermanently)
			return
		}
		if displayPath == "" {
			page, err := service.Posts(r.Context(), effectiveClientAddr(r, deps.TrustedProxyCIDRs),
				publicVisitorPrincipal(r, deps), slug, r.URL.Query().Get("cursor"), deps.deliveryBaseURL(r))
			if err != nil {
				writeDeliveryError(w, r, service, err)
				return
			}
			writeDeliveryPage(w, r, service, page)
			return
		}
		page, err := service.Post(r.Context(), effectiveClientAddr(r, deps.TrustedProxyCIDRs),
			publicVisitorPrincipal(r, deps), slug, displayPath, deps.deliveryBaseURL(r), deliveryLocale(r))
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
			publicVisitorPrincipal(r, deps), slug, r.PathValue("sectionSlug"),
			r.URL.Query().Get("model_key"), deps.deliveryBaseURL(r))
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
			publicVisitorPrincipal(r, deps), slug, deps.deliveryBaseURL(r))
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
		key := r.PathValue("key")
		// 尾斜杠收敛（对标审计 P2-8）：标签页 canonical 无斜杠，带斜杠 301。
		// {key} 段不含斜杠，须看原始路径是否以 / 结尾。
		if strings.HasSuffix(r.URL.Path, "/") {
			http.Redirect(w, r, "/sites/"+slug+"/tags/"+key, http.StatusMovedPermanently)
			return
		}
		page, err := service.TagPage(r.Context(), effectiveClientAddr(r, deps.TrustedProxyCIDRs),
			publicVisitorPrincipal(r, deps), slug, key, r.URL.Query().Get("cursor"), deps.deliveryBaseURL(r))
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
			publicVisitorPrincipal(r, deps), slug, r.URL.Query().Get("q"), deps.deliveryBaseURL(r))
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
					publicVisitorPrincipal(r, deps), slug, deps.deliveryBaseURL(r))
			case "sitemap":
				page, err = service.Sitemap(r.Context(), effectiveClientAddr(r, deps.TrustedProxyCIDRs),
					publicVisitorPrincipal(r, deps), slug, deps.deliveryBaseURL(r))
			default:
				page, err = service.Robots(r.Context(), effectiveClientAddr(r, deps.TrustedProxyCIDRs),
					publicVisitorPrincipal(r, deps), slug, deps.deliveryBaseURL(r))
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

// deliverySiteAbout serves /sites/{slug}/about/ (二期 §7.1).
func deliverySiteAbout(deps Dependencies) http.HandlerFunc {
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
		page, err := service.About(r.Context(), effectiveClientAddr(r, deps.TrustedProxyCIDRs),
			publicVisitorPrincipal(r, deps), slug, deps.deliveryBaseURL(r))
		if err != nil {
			writeDeliveryError(w, r, service, err)
			return
		}
		writeDeliveryPage(w, r, service, page)
	}
}

// deliverySiteArchive serves /sites/{slug}/archive/ (二期 §7.2).
func deliverySiteArchive(deps Dependencies) http.HandlerFunc {
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
		page, err := service.Archive(r.Context(), effectiveClientAddr(r, deps.TrustedProxyCIDRs),
			publicVisitorPrincipal(r, deps), slug, deps.deliveryBaseURL(r))
		if err != nil {
			writeDeliveryError(w, r, service, err)
			return
		}
		writeDeliveryPage(w, r, service, page)
	}
}

// deliverySiteMedia serves /sites/{slug}/media/{attachmentId}: the public
// cover stream (same-origin, immutable, Content-Type locked to images).
func deliverySiteMedia(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		service := requireDelivery(w, deps)
		if service == nil {
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		slug := r.PathValue("slug")
		attachmentID := r.PathValue("attachmentId")
		if !site.ValidSlug(slug) || !agentquery.ValidUUID(attachmentID) {
			writeError(w, http.StatusNotFound, "site_not_found")
			return
		}
		media, err := service.Media(r.Context(), effectiveClientAddr(r, deps.TrustedProxyCIDRs),
			publicVisitorPrincipal(r, deps), slug, attachmentID)
		if err != nil {
			writeError(w, http.StatusNotFound, "site_not_found")
			return
		}
		// G5 responsive variants: the query parameter selects one entry of a
		// server-owned closed enum — the OSS process directive itself never
		// travels through the request. Unknown values answer 400; the
		// Content-Type comes from the same enum (never the upstream header).
		variant := r.URL.Query().Get("variant")
		process, variantType := "", ""
		switch variant {
		case "":
		case "full":
			process, variantType = "image/resize,w_1280", ""
		case "card":
			process, variantType = "image/resize,w_640/format,webp", "image/webp"
		case "thumb":
			process, variantType = "image/resize,w_320/format,webp", "image/webp"
		default:
			writeError(w, http.StatusBadRequest, "variant_invalid")
			return
		}
		reader, err := service.Objects.Get(r.Context(), objectstore.ObjectRef{Key: media.ObjectKey, Process: process})
		if err != nil && process != "" {
			// Processing failed upstream (quota, unsupported source): degrade
			// to the original rather than a broken image.
			reader, err = service.Objects.Get(r.Context(), objectstore.ObjectRef{Key: media.ObjectKey})
		}
		if err != nil {
			writeError(w, http.StatusNotFound, "site_not_found")
			return
		}
		defer reader.Body.Close()
		contentType := media.MediaType
		if process != "" && variantType != "" {
			contentType = variantType
		}
		w.Header().Set("Content-Type", contentType)
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if !strings.HasPrefix(contentType, "image/") {
			w.Header().Set("Content-Disposition", "attachment; filename=\""+media.OriginalFilename+"\"")
		}
		// The original's length is known from the DB; a processed variant
		// reports its own length from the object store response.
		responseLength := media.ByteSize
		if process != "" {
			responseLength = reader.ContentLength
		}
		if responseLength > 0 {
			w.Header().Set("Content-Length", strconv.FormatInt(responseLength, 10))
		}
		w.WriteHeader(http.StatusOK)
		if r.Method != http.MethodHead {
			_, _ = io.Copy(w, reader.Body)
		}
	}
}

// deliveryCarouselScript serves the embedded carousel enhancement.
func deliveryCarouselScript(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if deps.Delivery == nil {
			writeError(w, http.StatusInternalServerError, "internal_error")
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
		w.Header().Set("Cache-Control", "public, max-age=3600")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(delivery.CarouselScript())
	}
}

// renderRobotsTxt builds the domain-level robots.txt: one Sitemap line per
// active, released public site on this deployment. robots.txt is a
// domain-scoped file while sites are a workspace-level resource, so the
// answer must aggregate every site the domain serves.
func renderRobotsTxt(baseURL string, slugs []string) string {
	var builder strings.Builder
	builder.WriteString("User-agent: *\nAllow: /\n")
	for _, slug := range slugs {
		builder.WriteString("Sitemap: " + baseURL + "/sites/" + slug + "/sitemap.xml\n")
	}
	return builder.String()
}

// robotsTxt serves the domain-level /robots.txt. nginx routes this path to
// the API (the console front end owns "/" otherwise); listing the active
// released sites gives crawlers the sitemap discovery entry point.
func robotsTxt(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		slugs := []string{}
		if deps.Store != nil && deps.Store.Pool != nil {
			// 只聚合"正式对外"的站：active + 已发布 + public scope（内部/测试
			// 站不进域级 robots，避免在搜索引擎面前暴露）。
			rows, err := deps.Store.Pool.Query(r.Context(), `
				SELECT slug FROM site.public_sites
				WHERE status = 'active' AND published_release_id IS NOT NULL
				  AND default_content_scope = 'public'
				ORDER BY created_at, slug
			`)
			if err == nil {
				defer rows.Close()
				for rows.Next() {
					var slug string
					if err := rows.Scan(&slug); err == nil {
						slugs = append(slugs, slug)
					}
				}
			}
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Cache-Control", "public, max-age=300")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(renderRobotsTxt(deps.deliveryBaseURL(r), slugs)))
	}
}

// deliverySiteCommentPost serves the POST target of the detail page's
// JS-free comment form: authenticated members write through the same
// CreateComment gate as the JSON API, then land back on the post (303 so a
// reload does not resubmit). Unauthenticated visitors are redirected to the
// post with the comment hint already on the page.
func deliverySiteCommentPost(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		service := requireDelivery(w, deps)
		if service == nil {
			return
		}
		slug := r.PathValue("slug")
		postPath := strings.TrimSuffix(r.URL.Path, "/comments")
		if err := r.ParseForm(); err != nil {
			http.Redirect(w, r, postPath, http.StatusSeeOther)
			return
		}
		principal, err := deps.SessionService.Authenticate(r.Context(), r)
		if err != nil {
			http.Redirect(w, r, postPath, http.StatusSeeOther)
			return
		}
		if principal.UserType != auth.UserTypeMember {
			// The form only renders for members; a hand-forged POST from an
			// anonymous visitor simply lands back on the page.
			http.Redirect(w, r, postPath, http.StatusSeeOther)
			return
		}
		displayPath := strings.TrimSuffix(r.PathValue("displayPath"), "/comments")
		if _, err := deps.Sites.CreateComment(r.Context(), principal, slug, displayPath, r.PostFormValue("body")); err != nil {
			// Degrade silently to the page (the public face has no error
			// chrome for comment writes); the JSON API surfaces details.
			log.Printf("delivery comment form write failed slug=%s path=%s: %v", slug, displayPath, err)
		}
		http.Redirect(w, r, postPath, http.StatusSeeOther)
	}
}

// deliverySiteCategory serves /sites/{slug}/c/{path...}: the hierarchical
// public category listing (站点方案 C5/D14). GET only; the path walks the
// public container tree by slug.
func deliverySiteCategory(deps Dependencies) http.HandlerFunc {
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
		path := r.PathValue("path")
		// 尾斜杠收敛（对标审计 P2-8）：/c/{path}/ 301 到 /c/{path}；分类
		// 总览 /c/ 保留斜杠形态（它的 canonical 即带斜杠）。
		if trimmed := strings.TrimSuffix(path, "/"); trimmed != "" && trimmed != path {
			http.Redirect(w, r, "/sites/"+slug+"/c/"+trimmed, http.StatusMovedPermanently)
			return
		}
		page, err := service.Category(r.Context(), effectiveClientAddr(r, deps.TrustedProxyCIDRs),
			publicVisitorPrincipal(r, deps), slug, path, deps.deliveryBaseURL(r), deliveryLocale(r))
		if err != nil {
			writeDeliveryError(w, r, service, err)
			return
		}
		writeDeliveryPage(w, r, service, page)
	}
}

// deliverySiteCustomPage serves /sites/{slug}/p/{pageSlug}: pages_config v2
// 自定义页（C1）。GET only；slug 为保留字时永不到达（保存时 422）。
func deliverySiteCustomPage(deps Dependencies) http.HandlerFunc {
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
		page, err := service.CustomPage(r.Context(), effectiveClientAddr(r, deps.TrustedProxyCIDRs),
			publicVisitorPrincipal(r, deps), slug, r.PathValue("pageSlug"), deps.deliveryBaseURL(r), deliveryLocale(r))
		if err != nil {
			writeDeliveryError(w, r, service, err)
			return
		}
		writeDeliveryPage(w, r, service, page)
	}
}
