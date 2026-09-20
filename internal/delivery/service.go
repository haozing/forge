package delivery

// service.go — the page pipeline of the HTML face. One request walks:
// shared public_site_ip budget → site facts (effective config: published
// release snapshot or working columns) → visitor tier → page cache →
// singleflight render → cache store. Data reads always run through the
// site.PublicReader (the JSON face's visibility layer — no second visibility
// implementation exists here, design doc §4.2).

import (
	"html"
	"reflect"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"sort"
	"strings"
	"time"

	"agentchunzhi/internal/auth"
	"agentchunzhi/internal/objectstore"
	"agentchunzhi/internal/site"
	"agentchunzhi/internal/theme"
	"agentchunzhi/internal/store"
	"agentchunzhi/internal/tag"

	agentquery "agentchunzhi/internal/query"

	"golang.org/x/sync/singleflight"
)

// Cache control policies (design doc §6.1).
const (
	publicCachePolicy  = "public, max-age=30, stale-while-revalidate=300"
	privateCachePolicy = "private, max-age=30, stale-while-revalidate=300"
	feedCachePolicy    = "public, max-age=600"
	noStorePolicy      = "no-store"
)

// Content types of the delivery face.
const (
	contentHTML = "text/html; charset=utf-8"
	contentRSS  = "application/rss+xml; charset=utf-8"
	contentXML  = "application/xml; charset=utf-8"
	contentText = "text/plain; charset=utf-8"
	contentJS   = "text/javascript; charset=utf-8"
)

// Service is the wired delivery face: reader, cache, renderer and the
// management service used by previews.
type Service struct {
	Store  *store.Store
	Reader *site.PublicReader
	Sites  *site.Service
	Cache  *PageCache
	Render *Renderer
	// Objects streams public cover media (二期 §6); nil disables the
	// media route (404 parity).
	Objects objectstore.ObjectStore
	Logf    func(string, ...any)
	// group collapses concurrent cold-key renders.
	group singleflight.Group
}

// NewService wires the delivery service with a fresh cache and renderer.
func NewService(database *store.Store, reader *site.PublicReader, sites *site.Service, cacheCapacity int, logf func(string, ...any)) *Service {
	if logf == nil {
		logf = log.Printf
	}
	return &Service{
		Store:  database,
		Reader: reader,
		Sites:  sites,
		Cache:  NewPageCache(cacheCapacity, 0),
		Render: NewRenderer(),
		Logf:   logf,
	}
}

// Response is one rendered response representation.
type Response struct {
	Body         []byte
	ContentType  string
	ETag         string
	CacheControl string
	NoIndex      bool
	Status       int
	// RedirectPath, when set, makes the response a same-site redirect
	// (Status carries the code, Location the path). Redirects bypass the
	// page cache by construction.
	RedirectPath string
}

// renderOutput is what one page builder produces: either a pre-rendered page
// (gate, feeds) or a template kind plus its view model.
type renderOutput struct {
	page     *Response
	kind     string
	vm       any
	noIndex  bool
	redirect string
}

// buildFunc builds one page against already-loaded facts and visitor band.
type buildFunc func(ctx context.Context, facts site.SiteFacts, band string, queries *theme.Queries) (renderOutput, error)

// tier resolves the visitor band of one request against the site row (the
// reader applies the authoritative per-read re-verification; this only picks
// the cache band, mirroring PublicReader.visitor semantics).
func tier(facts site.SiteFacts, principal auth.Principal) string {
	if principal.UserType == auth.UserTypeMember && principal.OrganizationID == facts.Site.OrganizationID {
		return "member"
	}
	return "anon"
}

// revision renders the cache generation key segment: the published release
// revision (release mode) or the working row revision (bootstrap mode).
func revision(facts site.SiteFacts) string {
	if facts.ReleaseRevision > 0 {
		return fmt.Sprintf("r%d", facts.ReleaseRevision)
	}
	return fmt.Sprintf("w%d", facts.Site.Revision)
}

// sanitizedCustomCSS runs the render-side sanitizer pass (idempotent).
func sanitizedCustomCSS(raw string) string {
	clean, _ := site.SanitizeCSS(raw)
	return clean
}

// chrome builds the page furniture from the effective facts and style. The
// L2 custom CSS is sanitized again on every render (defense in depth; the
// write side already stores the canonical output) and lands last in the
// cascade so it overrides the base stylesheet (二期 §4.4).
func chrome(facts site.SiteFacts, pageKind string) Chrome {
	slug := facts.Site.Slug
	logoURL, faviconURL, socialURL := "", "", ""
	if facts.Site.LogoAttachmentID != "" {
		logoURL = "/sites/" + slug + "/media/" + facts.Site.LogoAttachmentID
	}
	if facts.Site.FaviconAttachmentID != "" {
		faviconURL = "/sites/" + slug + "/media/" + facts.Site.FaviconAttachmentID
	}
	if facts.Site.SocialImageAttachmentID != "" {
		socialURL = "/sites/" + slug + "/media/" + facts.Site.SocialImageAttachmentID
	}
	languages := []NavItem{}
	if len(facts.Site.EnabledLocales) > 1 {
		for _, locale := range facts.Site.EnabledLocales {
			locale = strings.ToLower(strings.TrimSpace(locale))
			if locale == "" {
				continue
			}
			href := "/sites/" + slug
			if locale != facts.Site.DefaultLocale {
				href += "/" + locale
			}
			languages = append(languages, NavItem{Label: strings.ToUpper(locale), Href: href, Hreflang: locale})
		}
	}
	return Chrome{
		Slug:           slug,
		Name:           facts.Site.Name,
		SiteLang:       facts.Site.DefaultLocale,
		ScopePublic:    facts.Site.DefaultContentScope == site.ScopePublic,
		LogoURL:        logoURL,
		FaviconURL:     faviconURL,
		SocialImageURL: socialURL,
		Nav:            navFor(facts, slug),
		Languages:      languages,
		HomeHref:       "/sites/" + slug + "/",
		PostsHref:      "/sites/" + slug + "/posts/",
		TagsHref:       "/sites/" + slug + "/tags/",
		SearchHref:     "/sites/" + slug + "/search",
		RSSHref:        "/sites/" + slug + "/rss.xml",
	}
}


// pipeline runs the shared budget/cache/ETag flow around one page build.
func (s *Service) pipeline(ctx context.Context, addr string, principal auth.Principal, slug, routePath, baseURL string, build buildFunc) (*Response, error) {
	if s.Reader == nil {
		return nil, fmt.Errorf("delivery reader is not wired")
	}
	// The shared IP budget applies even on cache hits (design doc §10.8).
	if err := s.Reader.AllowPublic(ctx, addr); err != nil {
		return nil, err
	}
	facts, err := s.Reader.SiteFacts(ctx, slug)
	if err != nil {
		return nil, err
	}
	band := tier(facts, principal)
	key := PageKey(facts.Site.ID, revision(facts), band, routePath)
	if entry, ok := s.Cache.Get(key); ok {
		return &Response{
			Body: entry.Body, ContentType: entry.ContentType, ETag: entry.ETag,
			CacheControl: entry.CacheControl, NoIndex: entry.NoIndex, Status: 200,
		}, nil
	}
	queries := theme.NewQueries(s.themeQueryFn(ctx, principal, facts.Site))
	result, err, _ := s.group.Do(key, func() (any, error) {
		output, err := build(ctx, facts, band, queries)
		if err != nil {
			return nil, err
		}
		if output.redirect != "" {
			// Same-site 301 (moved display_path): served per request, never
			// cached — the underlying page keeps its own cache entry.
			return &Response{
				ContentType:  contentHTML,
				CacheControl: "private, max-age=30",
				NoIndex:      true,
				Status:       http.StatusMovedPermanently,
				RedirectPath: output.redirect,
			}, nil
		}
		if output.page != nil {
			s.storeEntry(key, revision(facts), band, routePath, output.page)
			return output.page, nil
		}
		var body []byte
		var rerr error
		if output.kind == "gate" || output.kind == "error" {
			body, rerr = s.Render.RenderSystemPage(output.kind, output.vm)
		} else {
			body, rerr = renderThemed(facts.ThemeFiles,
				revision(facts)+"-"+facts.Site.PublishedThemeRevisionID,
				facts.Site.Slug, output.kind, output.vm, queries, baseURL)
		}
		if rerr != nil {
			return nil, rerr
		}
		// D11/E：多语言站点为每个页面注入 hreflang 备选（单语言为空）。
		if alts := s.alternates(facts, routePath); len(alts) > 0 {
			body = injectHeadTags(body, alts)
		}
		// OG / Twitter 卡 / JSON-LD 服务端注入（对标分析 P2）：任何主题
		//（含存量自定义主题）都即时生效。
		if page, ok := pageMetaFromVM(output.vm); ok {
			body = injectSEOMeta(body, page)
		}
		cacheControl := publicCachePolicy
		if band == "member" || facts.Site.DefaultContentScope != site.ScopePublic {
			// Member-tier or gated representations never sit in shared caches.
			cacheControl = privateCachePolicy
		}
		page := &Response{
			Body: body, ContentType: contentHTML,
			ETag:         representationETag(revision(facts), band, routePath, body),
			CacheControl: cacheControl, NoIndex: output.noIndex, Status: 200,
		}
		s.storeEntry(key, revision(facts), band, routePath, page)
		return page, nil
	})
	if err != nil {
		return nil, err
	}
	return result.(*Response), nil
}

// storeEntry files one rendered page (ETag + headers) in the page cache.
func (s *Service) storeEntry(key, rev, band, routePath string, page *Response) {
	if page.ETag == "" {
		page.ETag = representationETag(rev, band, routePath, page.Body)
	}
	s.Cache.Set(key, CacheEntry{
		Body: page.Body, ETag: page.ETag, ContentType: page.ContentType,
		CacheControl: page.CacheControl, NoIndex: page.NoIndex,
	})
}

// representationETag hashes the rendered representation.
func representationETag(revision, band, routePath string, body []byte) string {
	hash := sha256.New()
	hash.Write([]byte(revision))
	hash.Write([]byte{0})
	hash.Write([]byte(band))
	hash.Write([]byte{0})
	hash.Write([]byte(routePath))
	hash.Write([]byte{0})
	hash.Write(body)
	return hex.EncodeToString(hash.Sum(nil))[:32]
}

// gated answers whether the anonymous gate page applies (design doc §4.1).
func gated(facts site.SiteFacts, band string) bool {
	return facts.Site.DefaultContentScope != site.ScopePublic && band == "anon"
}

// gatePage renders the member login gate as a private, noindex page.
func (s *Service) gatePage(facts site.SiteFacts) (*Response, error) {
	vm := GateVM{Page: Page{Kind: "gate", Title: facts.Site.Name, NoIndex: true,
		Description: "该站点仅对成员开放"}}
	vm.Site = chrome(facts, "gate")
	vm.Canonical = vm.Site.HomeHref
	body, err := s.Render.RenderSystemPage("gate", vm)
	if err != nil {
		return nil, err
	}
	return &Response{Body: body, ContentType: contentHTML, CacheControl: privateCachePolicy, NoIndex: true, Status: 200}, nil
}

// gateOutput is the builder result for gated anonymous reads.
func (s *Service) gateOutput(facts site.SiteFacts) (renderOutput, error) {
	page, err := s.gatePage(facts)
	if err != nil {
		return renderOutput{}, err
	}
	return renderOutput{page: page}, nil
}

// ErrorPage renders one error page with default chrome.
func (s *Service) ErrorPage(status int) *Response {
	vm := ErrorVM{Page: Page{Kind: "error", Title: fmt.Sprintf("%d", status), NoIndex: true}, Status: status}
	vm.Site = Chrome{
		Name:          "站点",
		HomeHref:      "/",
	}
	body, err := s.Render.RenderSystemPage("error", vm)
	if err != nil {
		body = []byte(fmt.Sprintf("<!DOCTYPE html><html><head><meta charset=\"utf-8\"><title>%d</title></head><body><h1>%d</h1></body></html>", status, status))
	}
	return &Response{Body: body, ContentType: contentHTML, CacheControl: noStorePolicy, NoIndex: true, Status: status}
}

// Home serves the site homepage.
func (s *Service) Home(ctx context.Context, addr string, principal auth.Principal, slug, baseURL string, locale string) (*Response, error) {
	routePath := "/sites/" + slug + "/" + locale
	return s.pipeline(ctx, addr, principal, slug, routePath, baseURL, func(ctx context.Context, facts site.SiteFacts, band string, queries *theme.Queries) (renderOutput, error) {
		if gated(facts, band) {
			return s.gateOutput(facts)
		}
		view, err := s.Reader.HomeWithConfig(ctx, addr, principal, slug, locale)
		if err != nil {
			return renderOutput{}, err
		}
		if len(view.Sections) == 0 {
			// Default homepage: the latest posts (an unconfigured site still
			// renders its content, not an empty shell).
			page, err := s.Reader.Posts(ctx, addr, principal, slug, site.PublicPostQuery{Limit: 10})
			if err != nil {
				return renderOutput{}, err
			}
			view.Sections = []site.PublicSection{{Type: site.HomepageSectionLatest, Title: "最新", Items: page.Items}}
		}
		var facets []tag.FacetItem
		if tags, err := s.Reader.Tags(ctx, addr, principal, slug, 24); err == nil {
			facets = tags
		} else {
			s.Logf("delivery: home tag cloud degraded slug=%s err=%v", slug, err)
		}
		vm := ResolveHome(view, facets)
		// 顶层公开分类区块（对标分析 P2）：加载失败降级为不渲染。
		if cats, err := s.Reader.PublicCategoryIndex(ctx, addr, principal, slug); err == nil {
			for _, cat := range cats {
				vm.Categories = append(vm.Categories, CategoryLinkVM{Name: cat.Name, Href: cat.Href, Count: cat.Count})
			}
		} else {
			s.Logf("delivery: home categories degraded slug=%s err=%v", slug, err)
		}
		vm.Site = chrome(facts, "home")
		vm.Queries = queries
		vm.Title = facts.Site.Name
		// 站点描述（§7.3）优先；空则回退站点名（meta description 不留空）。
		vm.Description = facts.Site.Description
		if vm.Description == "" {
			vm.Description = facts.Site.Name
		}
		vm.Canonical = baseURL + routePath
		vm.NoIndex = !vm.Site.ScopePublic
		// 首页结构化数据（对标审计 P1-4）：WebSite + Organization。
		if !vm.NoIndex {
			siteURL := baseURL + "/sites/" + slug + "/"
			ld, _ := json.Marshal([]map[string]any{
				{"@context": "https://schema.org", "@type": "WebSite",
					"name": facts.Site.Name, "url": siteURL,
					"description": vm.Description, "inLanguage": facts.Site.DefaultLocale},
				{"@context": "https://schema.org", "@type": "Organization",
					"name": facts.Site.Name, "url": siteURL},
			})
			vm.JSONLD = template.JS(ld)
		}
		return renderOutput{kind: "home", vm: vm, noIndex: vm.NoIndex}, nil
	})
}

// Posts serves the post list.
func (s *Service) Posts(ctx context.Context, addr string, principal auth.Principal, slug, cursor, baseURL string) (*Response, error) {
	routePath := "/sites/" + slug + "/posts/"
	if cursor != "" {
		routePath += "?cursor=" + cursor
	}
	return s.pipeline(ctx, addr, principal, slug, routePath, baseURL, func(ctx context.Context, facts site.SiteFacts, band string, queries *theme.Queries) (renderOutput, error) {
		if gated(facts, band) {
			return s.gateOutput(facts)
		}
		page, err := s.Reader.Posts(ctx, addr, principal, slug, site.PublicPostQuery{Cursor: cursor, Limit: 12})
		if err != nil {
			// 无效/过期 cursor（会话级令牌）不回 422：301 到干净列表页
			//（对标审计 P1-1/P3-4：cursor 不该成为可收录 URL）。
			if cursor != "" && errors.Is(err, agentquery.ErrInvalidRequest) {
				return renderOutput{redirect: "/sites/" + slug + "/posts/"}, nil
			}
			return renderOutput{}, err
		}
		vm := ResolveList(slug, "文章", "/sites/"+slug+"/posts/", page, page.NextCursor)
		vm.Site = chrome(facts, "list")
		vm.Queries = queries
		vm.Title = "文章 · " + facts.Site.Name
		vm.Description = facts.Site.Name + " 全部文章，按发布时间排列。"
		// 分页页 canonical 固定指干净首页 URL：cursor 是会话级令牌，
		// 自指等于把注定失效的 URL 交给搜索引擎（对标审计 P1-1）。
		vm.Canonical = baseURL + "/sites/" + slug + "/posts/"
		vm.NoIndex = !vm.Site.ScopePublic
		return renderOutput{kind: "list", vm: vm, noIndex: vm.NoIndex}, nil
	})
}

// Post serves one post detail page.
func (s *Service) Post(ctx context.Context, addr string, principal auth.Principal, slug, displayPath, baseURL string, locale string) (*Response, error) {
	routePath := "/sites/" + slug + "/posts/" + displayPath
	return s.pipeline(ctx, addr, principal, slug, routePath, baseURL, func(ctx context.Context, facts site.SiteFacts, band string, queries *theme.Queries) (renderOutput, error) {
		if gated(facts, band) {
			return s.gateOutput(facts)
		}
		content, err := s.Reader.Post(ctx, addr, principal, slug, displayPath, locale)
		if err != nil {
			// A path with no live binding may have been renamed: answer one
			// hop 301 to the moved target (G2) before giving up on the 404.
			if errors.Is(err, site.ErrSiteNotFound) {
				if to, ok, redirectErr := s.Reader.PathRedirect(ctx, slug, displayPath); redirectErr == nil && ok {
					return renderOutput{redirect: "/sites/" + slug + "/posts/" + to}, nil
				}
			}
			return renderOutput{}, err
		}
		// D13: 解析正文引用 —— 公开目标转站内 dofollow 链接，其余剥除。
		refs := map[string]AssetRefView{}
		if ids := AssetRefIDs(content.Markdown); len(ids) > 0 {
			lookup := s.Reader.AssetRefLookup(ctx, addr, principal, slug, ids)
			for id, target := range lookup {
				refs[id] = AssetRefView{Title: target.Title, Href: "/sites/" + slug + "/posts/" + target.Slug}
			}
		}
		vm := ResolveDetailWithRefs(slug, content, s.authorizedBodyImages(ctx, facts, content.Markdown), refs)
		vm.Site = chrome(facts, "detail")
		vm.Queries = queries
		vm.Title = content.Title + " · " + facts.Site.Name
		// Description comes from ResolveDetail (summary -> body excerpt ->
		// title); overwriting it with the raw summary would drop the
		// fallback for posts without one.
		vm.Canonical = baseURL + routePath
		if vm.CoverURL != "" {
			vm.CanonicalImage = baseURL + vm.CoverURL
			vm.CanonicalImageAlt = vm.Title
			if vm.CoverAlt != "" {
				vm.CanonicalImageAlt = vm.CoverAlt
			}
		}
		vm.NoIndex = !vm.Site.ScopePublic
		vm.ModifiedISO = vm.UpdatedISO
		// 文章面包屑只留 首页→文章 两级：中间层曾是内部模型键
		//（builtin_document，审计 P2-2），等文章分类面包屑数据就位再补。
		vm.JSONLD = articleJSONLD(facts, content, vm.Canonical, baseURL+"/sites/"+slug, "", vm.CanonicalImage)
		// 附件下载列表与上/下篇导航（产品文档 §11.2）。
		if attachments, err := s.postAttachments(ctx, facts, content.AssetID); err == nil && len(attachments) > 0 {
			vm.Attachments = attachments
		}
		vm.Prev, vm.Next = s.postNeighbors(ctx, facts, content.AssetID)
		s.attachDetailComments(ctx, &vm, facts, band, slug)
		s.attachDetailRelated(ctx, addr, principal, slug, content, &vm)
		return renderOutput{kind: "detail", vm: vm, noIndex: vm.NoIndex}, nil
	})
}

// Section serves one section page.
func (s *Service) Section(ctx context.Context, addr string, principal auth.Principal, slug, sectionSlug, modelKey, baseURL string) (*Response, error) {
	routePath := "/sites/" + slug + "/sections/" + sectionSlug + "/"
	return s.pipeline(ctx, addr, principal, slug, routePath, baseURL, func(ctx context.Context, facts site.SiteFacts, band string, queries *theme.Queries) (renderOutput, error) {
		if gated(facts, band) {
			return s.gateOutput(facts)
		}
		page, err := s.Reader.Section(ctx, addr, principal, slug, sectionSlug, modelKey, 12, "")
		if err != nil {
			return renderOutput{}, err
		}
		vm := ResolveList(slug, sectionSlug, "/sites/"+slug+"/sections/"+sectionSlug+"/", page, "")
		vm.Site = chrome(facts, "list")
		vm.Queries = queries
		vm.Title = sectionSlug + " · " + facts.Site.Name
		vm.Canonical = baseURL + routePath
		// 模型键集合页是实现细节面（审计 P2-2）：恒 noindex，靠分类页承担聚合。
		vm.NoIndex = true
		return renderOutput{kind: "list", vm: vm, noIndex: vm.NoIndex}, nil
	})
}

// Tags serves the tag index.
func (s *Service) Tags(ctx context.Context, addr string, principal auth.Principal, slug, baseURL string) (*Response, error) {
	routePath := "/sites/" + slug + "/tags/"
	return s.pipeline(ctx, addr, principal, slug, routePath, baseURL, func(ctx context.Context, facts site.SiteFacts, band string, queries *theme.Queries) (renderOutput, error) {
		if gated(facts, band) {
			return s.gateOutput(facts)
		}
		items, err := s.Reader.Tags(ctx, addr, principal, slug, 50)
		if err != nil {
			return renderOutput{}, err
		}
		vm := ResolveTags(slug, items)
		vm.Site = chrome(facts, "tags")
		vm.Queries = queries
		vm.Title = "标签 · " + facts.Site.Name
		vm.Description = facts.Site.Name + " 的主题标签总览：按标签浏览全部文章。"
		vm.Canonical = baseURL + routePath
		vm.NoIndex = !vm.Site.ScopePublic
		return renderOutput{kind: "tags", vm: vm, noIndex: vm.NoIndex}, nil
	})
}

// TagPage serves one tag archive.
func (s *Service) TagPage(ctx context.Context, addr string, principal auth.Principal, slug, key, cursor, baseURL string) (*Response, error) {
	routePath := "/sites/" + slug + "/tags/" + key
	if cursor != "" {
		routePath += "?cursor=" + cursor
	}
	return s.pipeline(ctx, addr, principal, slug, routePath, baseURL, func(ctx context.Context, facts site.SiteFacts, band string, queries *theme.Queries) (renderOutput, error) {
		if gated(facts, band) {
			return s.gateOutput(facts)
		}
		page, err := s.Reader.TagPage(ctx, addr, principal, slug, key, site.PublicPostQuery{Cursor: cursor, Limit: 12})
		if err != nil {
			// 无效 cursor 同列表页：301 到干净标签页（对标审计 P1-1）。
			if cursor != "" && errors.Is(err, agentquery.ErrInvalidRequest) {
				return renderOutput{redirect: "/sites/" + slug + "/tags/" + key}, nil
			}
			return renderOutput{}, err
		}
		vm := TagPageVM{Page: Page{Kind: "tag_page"}, TagKey: key, TagName: key,
			Items: []CardVM{}, Pagination: PaginationVM{}}
		vm.Site = chrome(facts, "list")
		vm.Queries = queries
		for _, post := range page.Items {
			vm.Items = append(vm.Items, cardVM(slug, post, 160))
		}
		if page.HasMore && page.NextCursor != "" {
			vm.Pagination.NextHref = "/sites/" + slug + "/tags/" + key + "/?cursor=" + page.NextCursor
		}
		vm.Title = "标签 " + key + " · " + facts.Site.Name
		vm.Description = facts.Site.Name + " 中标签为 " + key + " 的文章合集。"
		// canonical 无尾斜杠（与 posts/分类一致）；cursor 不进 canonical。
		vm.Canonical = baseURL + routePath
		// 薄标签页（<3 篇且无下一页）noindex：标签聚合页天然薄内容/高重复，
		// 业界惯例只把够分量的标签页留在索引里（对标分析 P3）。
		vm.NoIndex = !vm.Site.ScopePublic || (!page.HasMore && len(page.Items) < 3)
		return renderOutput{kind: "tag_page", vm: vm, noIndex: vm.NoIndex}, nil
	})
}

// Search serves the search shell (results arrive via the JS island).
func (s *Service) Search(ctx context.Context, addr string, principal auth.Principal, slug, query, baseURL string) (*Response, error) {
	routePath := "/sites/" + slug + "/search"
	return s.pipeline(ctx, addr, principal, slug, routePath, baseURL, func(ctx context.Context, facts site.SiteFacts, band string, queries *theme.Queries) (renderOutput, error) {
		if gated(facts, band) {
			return s.gateOutput(facts)
		}
		vm := SearchVM{Page: Page{Kind: "search", Title: "搜索 · " + facts.Site.Name, NoIndex: true}, Query: query}
		vm.Site = chrome(facts, "search")
		vm.Queries = queries
		vm.Canonical = baseURL + routePath
		return renderOutput{kind: "search", vm: vm, noIndex: true}, nil
	})
}

// SearchScript serves the embedded search island JavaScript.
func (s *Service) SearchScript() *Response {
	return &Response{Body: SearchJavaScript(), ContentType: contentJS, CacheControl: "public, max-age=3600", Status: 200}
}

// ErrFeedDisabled marks feeds disabled for the site scope (public sites only
// publish feeds, design doc §9).
var ErrFeedDisabled = fmt.Errorf("delivery: feed disabled for site scope")

// RSS serves the site feed: the latest 50 published bindings.
func (s *Service) RSS(ctx context.Context, addr string, principal auth.Principal, slug, baseURL string) (*Response, error) {
	routePath := "/sites/" + slug + "/rss.xml"
	return s.pipeline(ctx, addr, principal, slug, routePath, baseURL, func(ctx context.Context, facts site.SiteFacts, band string, queries *theme.Queries) (renderOutput, error) {
		if facts.Site.DefaultContentScope != site.ScopePublic {
			return renderOutput{}, ErrFeedDisabled
		}
		page, err := s.Reader.Posts(ctx, addr, principal, slug, site.PublicPostQuery{Limit: 50})
		if err != nil {
			return renderOutput{}, err
		}
		vm := RSSVM{Site: chrome(facts, "rss"), Items: []RSSItem{}, HomeURL: baseURL + "/sites/" + slug + "/"}
		for _, post := range page.Items {
			vm.Items = append(vm.Items, RSSItem{
				Title:       post.Title,
				Href:        baseURL + postHref(slug, post.DisplayPath),
				Summary:     post.Summary,
				PublishedOn: rssDate(post.PublishedAt),
			})
		}
		vm.LastBuildOn = time.Now().UTC().Format(time.RFC1123Z)
		body, err := s.Render.RenderXML("rss", vm)
		if err != nil {
			return renderOutput{}, err
		}
		return renderOutput{page: &Response{Body: body, ContentType: contentRSS, CacheControl: feedCachePolicy, Status: 200}}, nil
	})
}

// Sitemap serves the sitemap: fixed pages, sections, tags and every bound
// post (paginated reads through the reader, capped at 10 pages).
func (s *Service) Sitemap(ctx context.Context, addr string, principal auth.Principal, slug, baseURL string) (*Response, error) {
	routePath := "/sites/" + slug + "/sitemap.xml"
	return s.pipeline(ctx, addr, principal, slug, routePath, baseURL, func(ctx context.Context, facts site.SiteFacts, band string, queries *theme.Queries) (renderOutput, error) {
		if facts.Site.DefaultContentScope != site.ScopePublic {
			return renderOutput{}, ErrFeedDisabled
		}
		vm := SitemapVM{Site: chrome(facts, "sitemap"), URLs: []SitemapURL{
			{Loc: baseURL + "/sites/" + slug + "/"},
			{Loc: baseURL + "/sites/" + slug + "/posts/"},
		}}
		if _, err := s.Reader.About(ctx, addr, principal, slug); err == nil {
			vm.URLs = append(vm.URLs, SitemapURL{Loc: baseURL + "/sites/" + slug + "/about/"})
		}
		// 分类页是关键词落地页（对标分析 P2）：顶层公开分类进 sitemap。
		if cats, err := s.Reader.PublicCategoryIndex(ctx, addr, principal, slug); err == nil {
			for _, cat := range cats {
				vm.URLs = append(vm.URLs, SitemapURL{Loc: baseURL + cat.Href})
			}
		}
		// sections（模型键集合页）已 noindex（审计 P2-2），不入 sitemap。
		tags, err := s.Reader.Tags(ctx, addr, principal, slug, 100)
		if err != nil {
			return renderOutput{}, err
		}
		for _, item := range tags {
			// 薄标签页已 noindex（TagPage <3 篇），sitemap 不再列出。
			if item.AssetCount < 3 {
				continue
			}
			vm.URLs = append(vm.URLs, SitemapURL{Loc: baseURL + "/sites/" + slug + "/tags/" + item.Tag.Key + "/"})
		}
		cursor := ""
		sitemapPosts := 0
		// 40 轮 × 50 = 2000 条上限：列表页 cursor 全量可达的兜底（超出部分
		// 仍可经 /posts/ 翻页被爬到）。
		for round := 0; round < 40; round++ {
			page, err := s.Reader.Posts(ctx, addr, principal, slug, site.PublicPostQuery{Cursor: cursor, Limit: 50})
			if err != nil {
				return renderOutput{}, err
			}
			for _, post := range page.Items {
				// lastmod 用发布时间语义：可见性等运维操作会 bump updated_at，
				// 用它会让全站 lastmod 被批量操作污染（2026-09-20 实测教训）。
				modified := post.PublishedAt
				if modified == nil {
					modified = post.UpdatedAt
				}
				vm.URLs = append(vm.URLs, SitemapURL{
					Loc:       baseURL + postHref(slug, post.DisplayPath),
					LastmodOn: FormatISO(modified),
				})
				sitemapPosts++
			}
			if !page.HasMore || page.NextCursor == "" {
				break
			}
			cursor = page.NextCursor
		}
		if sitemapPosts > 0 {
			vm.URLs = append(vm.URLs, SitemapURL{Loc: baseURL + "/sites/" + slug + "/archive/"})
		}
		body, err := s.Render.RenderXML("sitemap", vm)
		if err != nil {
			return renderOutput{}, err
		}
		return renderOutput{page: &Response{Body: body, ContentType: contentXML, CacheControl: feedCachePolicy, Status: 200}}, nil
	})
}

// Robots serves robots.txt (informational under slug routing; non-public
// sites disallow everything, design doc §4.1).
func (s *Service) Robots(ctx context.Context, addr string, principal auth.Principal, slug, baseURL string) (*Response, error) {
	routePath := "/sites/" + slug + "/robots.txt"
	return s.pipeline(ctx, addr, principal, slug, routePath, baseURL, func(ctx context.Context, facts site.SiteFacts, band string, queries *theme.Queries) (renderOutput, error) {
		vm := struct{ ScopePublic bool }{ScopePublic: facts.Site.DefaultContentScope == site.ScopePublic}
		body, err := s.Render.RenderXML("robots", vm)
		if err != nil {
			return renderOutput{}, err
		}
		return renderOutput{page: &Response{Body: body, ContentType: contentText, CacheControl: feedCachePolicy, Status: 200}}, nil
	})
}

// rssDate formats one timestamp per RFC1123Z (RSS pubDate).
func rssDate(value *time.Time) string {
	if value == nil || value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC1123Z)
}

// articleJSONLD builds the detail structured data: an Article document (with
// cover image when present) plus a BreadcrumbList (home → section → post).
// json.Marshal escapes <, > and & so the script context is closed.
func articleJSONLD(facts site.SiteFacts, content site.PublicPostContent, canonical, homeURL, sectionURL, coverImage string) template.JS {
	article := map[string]any{
		"@context":         "https://schema.org",
		"@type":            "Article",
		"headline":         content.Title,
		"mainEntityOfPage": canonical,
		"author":           map[string]any{"@type": "Organization", "name": facts.Site.Name},
		"publisher":        map[string]any{"@type": "Organization", "name": facts.Site.Name},
	}
	if coverImage != "" {
		article["image"] = coverImage
	}
	if content.PublishedAt != nil && !content.PublishedAt.IsZero() {
		article["datePublished"] = content.PublishedAt.UTC().Format(time.RFC3339)
	}
	if content.UpdatedAt != nil && !content.UpdatedAt.IsZero() {
		article["dateModified"] = content.UpdatedAt.UTC().Format(time.RFC3339)
	}
	crumbs := []any{
		map[string]any{"@type": "ListItem", "position": 1, "name": facts.Site.Name, "item": homeURL},
	}
	position := 2
	if sectionURL != "" {
		crumbs = append(crumbs, map[string]any{"@type": "ListItem", "position": position, "name": content.Section, "item": sectionURL})
		position++
	}
	crumbs = append(crumbs, map[string]any{"@type": "ListItem", "position": position, "name": content.Title, "item": canonical})
	breadcrumb := map[string]any{
		"@context":        "https://schema.org",
		"@type":           "BreadcrumbList",
		"itemListElement": crumbs,
	}
	body, err := json.Marshal([]any{article, breadcrumb})
	if err != nil {
		return ""
	}
	return template.JS(body)
}

// navFor prefers the pages_config v2 auto-enumerated navigation (C1) and
// falls back to the legacy navigation_config parse when absent.
func navFor(facts site.SiteFacts, slug string) []NavItem {
	items := make([]NavItem, 0, len(facts.Nav))
	for _, entry := range facts.Nav {
		items = append(items, NavItem{Label: entry.Name, Href: entry.Href})
	}
	return items
}

// hreflangAlternate is one resolved alternate entry.
type hreflangAlternate struct {
	Hreflang string
	Href     string
}

// alternates resolves the enabled-locale alternates for one page path
// (D11/E). 详情页译文 slug 需翻译组查询，此处 v1 以路径回退表达；列表/首页
// 精确。
func (s *Service) alternates(facts site.SiteFacts, routePath string) []hreflangAlternate {
	if len(facts.Site.EnabledLocales) <= 1 {
		return nil
	}
	locales := append([]string{}, facts.Site.EnabledLocales...)
	sort.Strings(locales)
	prefix := "/sites/" + facts.Site.Slug
	trimmed := strings.TrimSuffix(routePath, "/")
	rest := strings.TrimPrefix(trimmed, prefix)
	if rest == "" {
		rest = "/"
	}
	out := []hreflangAlternate{}
	for _, locale := range locales {
		href := "/sites/" + facts.Site.Slug
		if locale != facts.Site.DefaultLocale {
			href += "/" + locale
		}
		if rest != "" {
			href += rest
		}
		out = append(out, hreflangAlternate{Hreflang: locale, Href: href})
	}
	xDefault := prefix + rest
	out = append(out, hreflangAlternate{Hreflang: "x-default", Href: xDefault})
	return out
}

// injectHeadTags inserts rendered link tags right before </head>.
func injectHeadTags(body []byte, alts []hreflangAlternate) []byte {
	var builder strings.Builder
	for _, alt := range alts {
		builder.WriteString(`<link rel="alternate" hreflang="`)
		builder.WriteString(alt.Hreflang)
		builder.WriteString(`" href="`)
		builder.WriteString(alt.Href)
		builder.WriteString(`">`)
	}
	closing := strings.ToUpper("</head>")
	html := string(body)
	idx := strings.Index(html, closing)
	if idx < 0 {
		idx = strings.Index(html, "</head>")
		if idx < 0 {
			return body
		}
		idx += len("</head>")
	} else {
		idx += len(closing)
	}
	return []byte(html[:idx] + builder.String() + html[idx:])
}

// injectSEOMeta 在 </head> 前注入 OG / Twitter 卡与 JSON-LD（对标分析 P2）。
// 走管线后置注入而非主题模板：结构化数据是服务端关注点，且主题扫描器禁
// 止模板出现 <script>（§5.3），存量自定义主题也因此即时受益。
func injectSEOMeta(body []byte, page Page) []byte {
	if page.Title == "" {
		return body
	}
	var b strings.Builder
	esc := html.EscapeString
	b.WriteString(`<meta property="og:site_name" content="` + esc(page.Site.Name) + `">`)
	b.WriteString(`<meta property="og:title" content="` + esc(page.Title) + `">`)
	if page.Description != "" {
		b.WriteString(`<meta property="og:description" content="` + esc(page.Description) + `">`)
	}
	if page.Canonical != "" {
		b.WriteString(`<meta property="og:url" content="` + esc(page.Canonical) + `">`)
	}
	ogType := "website"
	if page.Kind == "detail" {
		ogType = "article"
	}
	b.WriteString(`<meta property="og:type" content="` + ogType + `">`)
	card := "summary"
	if page.CanonicalImage != "" {
		card = "summary_large_image"
		b.WriteString(`<meta property="og:image" content="` + esc(page.CanonicalImage) + `">`)
		if page.CanonicalImageAlt != "" {
			b.WriteString(`<meta property="og:image:alt" content="` + esc(page.CanonicalImageAlt) + `">`)
		}
	}
	b.WriteString(`<meta name="twitter:card" content="` + card + `">`)
	if page.ModifiedISO != "" {
		b.WriteString(`<meta property="article:modified_time" content="` + esc(page.ModifiedISO) + `">`)
	}
	if page.JSONLD != "" && !page.NoIndex {
		b.WriteString(`<script type="application/ld+json">` + string(page.JSONLD) + `</script>`)
	}
	htmlStr := string(body)
	idx := strings.Index(strings.ToLower(htmlStr), "</head>")
	if idx < 0 {
		return body
	}
	// strings.Index 给的是小写化后的位置，需在原文中定位同一位置。
	return []byte(htmlStr[:idx] + b.String() + htmlStr[idx:])
}

// pageMetaFromVM 从任一页面 VM（均内嵌 Page）反射取页面元数据；非页面 VM
// （gate/error 等）返回 false。
func pageMetaFromVM(vm any) (Page, bool) {
	if vm == nil {
		return Page{}, false
	}
	value := reflect.ValueOf(vm)
	if value.Kind() != reflect.Struct {
		return Page{}, false
	}
	field := value.FieldByName("Page")
	if !field.IsValid() || field.Type() != reflect.TypeOf(Page{}) {
		return Page{}, false
	}
	page, ok := field.Interface().(Page)
	return page, ok
}
