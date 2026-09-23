package delivery

// service.go — the page pipeline of the HTML face. One request walks:
// shared public_site_ip budget → site facts (effective config: published
// release snapshot or working columns) → visitor tier → page cache →
// singleflight render → cache store. Data reads always run through the
// site.PublicReader (the JSON face's visibility layer — no second visibility
// implementation exists here, design doc §4.2).

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"html/template"
	"log"
	"net/http"
	"reflect"
	"sort"
	"strings"
	"time"

	"agentchunzhi/internal/auth"
	"agentchunzhi/internal/objectstore"
	"agentchunzhi/internal/site"
	"agentchunzhi/internal/store"
	"agentchunzhi/internal/tag"
	"agentchunzhi/internal/theme"

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

// ErrChatLoginRequired / ErrChatInvalidMessage / ErrChatQuotaExhausted 是
// 公开 AI 问答的终端错误（httpapi 映射 401/422/429）。
var (
	ErrChatLoginRequired  = fmt.Errorf("delivery: chat login required")
	ErrChatInvalidMessage = fmt.Errorf("delivery: invalid chat message")
	ErrChatQuotaExhausted = fmt.Errorf("delivery: chat quota exhausted")
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
	// ChatModels / ChatAgentApplicationID / ChatDailyQuota：公开 AI 问答
	//（/ask）的可选装配；未配置时问答端点明确降级不可用。
	ChatModels              ChatModelResolver
	ChatAgentApplicationID string
	ChatDailyQuota         int
	// RootSiteSlug 是"单品牌部署"的根站点（DELIVERY_ROOT_SITE_SLUG）：httpapi
	// 把 "/" 直接路由到该站首页、/sites/{slug} 301 到根，首页 canonical/
	// JSON-LD/sitemap/RSS 的站 URL 因此收拢为 baseURL + "/"，避免把根域权重
	// 让渡给 /sites/{slug}/ 路径形态。空串 = 关闭（多站部署维持路径形态）。
	RootSiteSlug string
	// group collapses concurrent cold-key renders.
	group singleflight.Group
}

// homeURLFor 解析站点首页的绝对 URL：根站点取根形态，其余站取路径形态。
func (s *Service) homeURLFor(slug, baseURL string) string {
	if s.RootSiteSlug != "" && s.RootSiteSlug == slug {
		return baseURL + "/"
	}
	return baseURL + "/sites/" + slug + "/"
}

// originOf 从绝对 canonical 提取 scheme+host：路径形态（含 /sites/ 前缀）
// 截到前缀前；根形态（裸 origin，可能带尾斜杠）原样收敛。canonical 由
// baseURL（无尾斜杠）+ 路径拼出，两种形态覆盖全部现状。
func originOf(canonical string) string {
	if idx := strings.Index(canonical, "/sites/"); idx > 0 {
		return canonical[:idx]
	}
	return strings.TrimRight(canonical, "/")
}

// NewService wires the delivery service with a fresh cache and renderer.
// ChatModels/ChatAgentApplicationID/ChatDailyQuota 为公开 AI 问答的可选
// 装配（未配置时 /ask 端点明确降级），由 cmd/api 在构造后注入。
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
		Name:     "站点",
		HomeHref: "/",
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
	return s.pipeline(ctx, addr, principal, slug, routePath, baseURL, s.buildHome(slug, baseURL, "", addr, locale, principal))
}

// HomeRoot serves the homepage of the deployment's root site (RootSiteSlug)
// at "/": identical page body family, but the canonical / JSON-LD site URL
// collapse onto the bare origin so the root domain owns the homepage.
// locale 固定默认语言——根路径只有一种语言形态（带前缀的翻译走原路径）。
func (s *Service) HomeRoot(ctx context.Context, addr string, principal auth.Principal, slug, baseURL string) (*Response, error) {
	return s.pipeline(ctx, addr, principal, slug, "/", baseURL, s.buildHome(slug, baseURL, baseURL+"/", addr, "", principal))
}

// buildHome 构造首页 builder。canonicalOverride 非空时（根站点模式）首页
// canonical 用它而非 baseURL+routePath；JSON-LD 站 URL 恒走 homeURLFor。
func (s *Service) buildHome(slug, baseURL, canonicalOverride, addr, locale string, principal auth.Principal) buildFunc {
	return func(ctx context.Context, facts site.SiteFacts, band string, queries *theme.Queries) (renderOutput, error) {
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
		// 卡片截断（2026-09-23 SEO 审计）：默认主题 .kb-latest 只展示前 3 张
		//（theme.css nth-child(n+4) display:none），模板曾渲染全部条目靠 CSS
		// 藏匿——92 个隐藏 h2 全部进了 DOM。渲染层截断，DOM 与所见一致。
		if len(vm.Items) > 3 {
			vm.Items = vm.Items[:3]
		}
		vm.Site = chrome(facts, "home")
		vm.Queries = queries
		vm.Title = homeTitle(facts.Site.Name, facts.Site.Description)
		// 站点描述（§7.3）优先；空则回退站点名（meta description 不留空）。
		vm.Description = facts.Site.Description
		if vm.Description == "" {
			vm.Description = facts.Site.Name
		}
		if canonicalOverride != "" {
			vm.Canonical = canonicalOverride
		} else {
			vm.Canonical = baseURL + "/sites/" + slug + "/" + locale
		}
		vm.NoIndex = !vm.Site.ScopePublic
		// 首页结构化数据（对标审计 P1-4）：WebSite + Organization。
		if !vm.NoIndex {
			siteURL := s.homeURLFor(slug, baseURL)
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
	}
}

// homeTitle 拼首页 <title>：站点名 + 描述首段（" · "前的一段，24 字符封顶）。
// 只写站点名浪费最强页面信号（2026-09-23 SEO 审计）；描述缺省回退站名。
func homeTitle(name, description string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}
	segment := ""
	if trimmed := strings.TrimSpace(description); trimmed != "" {
		if idx := strings.Index(trimmed, " · "); idx > 0 {
			segment = strings.TrimSpace(trimmed[:idx])
		} else {
			segment = trimmed
		}
	}
	if segment == "" || segment == name {
		return name
	}
	runes := []rune(segment)
	if len(runes) > 24 {
		segment = strings.TrimSpace(string(runes[:24]))
	}
	return name + "｜" + segment
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
			// 带 cursor 的列表页出错（无效/过期会话令牌等）一律 301 到干净
			// 列表页：cursor 不是可收录 URL，也不该以 422/500 呈现给爬虫
			//（对标审计 P1-1/P3-4）。
			if cursor != "" {
				return renderOutput{redirect: "/sites/" + slug + "/posts/"}, nil
			}
			return renderOutput{}, err
		}
		vm := ResolveList(slug, "知识库", "/sites/"+slug+"/posts/", page, page.NextCursor)
		vm.Site = chrome(facts, "list")
		vm.Queries = queries
		vm.Title = "知识库 · " + facts.Site.Name
		if cursor != "" {
			// 分页页与第一页共用 title 会判重复（审计 P2-7）：cursor 页加续页标识。
			vm.Title = "知识库（续页） · " + facts.Site.Name
		}
		vm.Description = facts.Site.Name + " 全部文章，按发布时间排列。"
		vm.Filter = s.buildFilterPanel(ctx, addr, principal, slug, "", nil)
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
		// 可见面包屑：首页 > 公开分类 > 文章（对标审计 P2-2）。分类取文章
		// 挂载的第一个公开分类；JSON-LD 面包屑同步用真实分类替代内部模型键。
		if catName, catSlug, ok := s.Reader.PostPrimaryCategory(ctx, facts.Site.OrganizationID, content.AssetID); ok {
			href := "/sites/" + slug + "/c/" + catSlug
			vm.Crumbs = []CrumbVM{{Name: facts.Site.Name, Href: "/sites/" + slug + "/"}, {Name: catName, Href: href}}
			vm.JSONLD = articleJSONLD(facts, content, vm.Canonical, baseURL+"/sites/"+slug,
				vm.CanonicalImage, catName, baseURL+href)
		} else {
			vm.Crumbs = []CrumbVM{{Name: facts.Site.Name, Href: "/sites/" + slug + "/"}}
			vm.JSONLD = articleJSONLD(facts, content, vm.Canonical, baseURL+"/sites/"+slug,
				vm.CanonicalImage, "", "")
		}
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
			if cursor != "" {
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
		vm.Filter = s.buildFilterPanel(ctx, addr, principal, slug, "", []string{key})
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
		vm := RSSVM{Site: chrome(facts, "rss"), Items: []RSSItem{}, HomeURL: s.homeURLFor(slug, baseURL)}
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
			{Loc: s.homeURLFor(slug, baseURL)},
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

// LLMs serves the site-scoped llms.txt content guide (llms.txt 惯例)：站点
// 名/描述 + 已发布文章清单。域级 /llms.txt 仍是平台 MCP 说明（与
// /.well-known/agents.json 配套），面向 LLM 的站点内容指南挂站点级路径。
func (s *Service) LLMs(ctx context.Context, addr string, principal auth.Principal, slug, baseURL string) (*Response, error) {
	routePath := "/sites/" + slug + "/llms.txt"
	return s.pipeline(ctx, addr, principal, slug, routePath, baseURL, func(ctx context.Context, facts site.SiteFacts, band string, queries *theme.Queries) (renderOutput, error) {
		if facts.Site.DefaultContentScope != site.ScopePublic {
			return renderOutput{}, ErrFeedDisabled
		}
		page, err := s.Reader.Posts(ctx, addr, principal, slug, site.PublicPostQuery{Limit: 100})
		if err != nil {
			return renderOutput{}, err
		}
		var b strings.Builder
		b.WriteString("# " + facts.Site.Name + "\n\n")
		if facts.Site.Description != "" {
			b.WriteString("> " + facts.Site.Description + "\n\n")
		}
		b.WriteString("## Posts\n\n")
		for _, post := range page.Items {
			title := strings.ReplaceAll(post.Title, "[", "(")
			title = strings.ReplaceAll(title, "]", ")")
			b.WriteString("- [" + title + "](" + baseURL + postHref(slug, post.DisplayPath) + ")\n")
		}
		return renderOutput{page: &Response{
			Body: []byte(b.String()), ContentType: contentText,
			CacheControl: feedCachePolicy, Status: 200,
		}}, nil
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
func articleJSONLD(facts site.SiteFacts, content site.PublicPostContent, canonical, homeURL, coverImage, categoryName, categoryURL string) template.JS {
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
	if categoryName != "" && categoryURL != "" {
		crumbs = append(crumbs, map[string]any{"@type": "ListItem", "position": position, "name": categoryName, "item": categoryURL})
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
	origin := originOf(page.Canonical)
	image := page.CanonicalImage
	if image == "" {
		// 无封面时回退站点社交图（绝对化：借 canonical 的 scheme+host）。
		if page.Site.SocialImageURL != "" && origin != "" {
			image = origin + page.Site.SocialImageURL
		}
	}
	card := "summary"
	if image != "" {
		card = "summary_large_image"
		b.WriteString(`<meta property="og:image" content="` + esc(image) + `">`)
		if page.CanonicalImageAlt != "" {
			b.WriteString(`<meta property="og:image:alt" content="` + esc(page.CanonicalImageAlt) + `">`)
		}
	}
	b.WriteString(`<meta name="twitter:card" content="` + card + `">`)
	if page.ModifiedISO != "" {
		b.WriteString(`<meta property="article:modified_time" content="` + esc(page.ModifiedISO) + `">`)
	}
	// RSS autodiscovery（2026-09-23 SEO 审计）：rss.xml 一直存在但 head 无
	// 声明，阅读器/爬虫发现不了。绝对化借 canonical 的 origin。
	if rss := page.Site.RSSHref; rss != "" && origin != "" {
		b.WriteString(`<link rel="alternate" type="application/rss+xml" title="` +
			esc(page.Site.Name) + `" href="` + esc(origin+rss) + `">`)
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
// （gate/error 等）返回 false。注意部分 VM（CategoryVM/DetailVM）声明了自己的
// Site 字段，handler 把 chrome() 赋给外层字段时 Page.Site 恒零值——这里做
// 外层回退，否则 og:site_name 输出空串。
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
	if !ok {
		return Page{}, false
	}
	if page.Site.Name == "" {
		siteField := value.FieldByName("Site")
		if siteField.IsValid() && siteField.Type() == reflect.TypeOf(Chrome{}) {
			if chromeVal, ok := siteField.Interface().(Chrome); ok && chromeVal.Name != "" {
				page.Site = chromeVal
			}
		}
	}
	return page, true
}

// buildFilterPanel 构造列表族左侧筛选面板（分类单选 × 标签多选，全链接式）。
// curCat 为当前分类 slug（空=全部）；curTags 为当前选中的标签 key 列表
//（已规范化有序）。href 规则：分类链接保持当前标签集；标签链接对当前
// 标签集做 toggle 后重排序，与分类组合成规范 URL。
func (s *Service) buildFilterPanel(ctx context.Context, addr string, principal auth.Principal, slug, curCat string, curTagKeys []string) *FilterPanelVM {
	panel := &FilterPanelVM{ActiveTagNames: []string{}}
	base := "/sites/" + slug
	selected := map[string]bool{}
	tagDisplay := map[string]string{}
	for _, key := range curTagKeys {
		selected[key] = true
	}
	tagPath := func(cat string, keys []string) string {
		if len(keys) == 0 {
			if cat == "" {
				return base + "/posts/"
			}
			return base + "/c/" + cat
		}
		joined := strings.Join(keys, "+")
		if cat == "" {
			return base + "/tags/" + joined
		}
		return base + "/c/" + cat + "/t/" + joined
	}
	panel.ClearHref = tagPath("", nil)
	panel.AllCategory = FilterLinkVM{Name: "全部分类", Href: tagPath("", curTagKeys), Active: curCat == ""}
	if cats, err := s.Reader.PublicCategoryIndex(ctx, addr, principal, slug); err == nil {
		for _, cat := range cats {
			panel.Categories = append(panel.Categories, FilterLinkVM{
				Name: cat.Name, Href: tagPath(catSlugOf(cat.Href), curTagKeys),
				Count: int64(cat.Count), Active: curCat != "" && catSlugOf(cat.Href) == curCat,
			})
		}
	}
	if facets, err := s.Reader.Tags(ctx, addr, principal, slug, 40); err == nil {
		for _, item := range facets {
			key := item.Tag.Key
			tagDisplay[key] = item.Tag.DisplayName
			next := make([]string, 0, len(curTagKeys)+1)
			if selected[key] {
				for _, k := range curTagKeys {
					if k != key {
						next = append(next, k)
					}
				}
			} else {
				next = append(next, curTagKeys...)
				next = append(next, key)
				sort.Strings(next)
			}
			panel.Tags = append(panel.Tags, FilterLinkVM{
				Name: displayNameOf(item.Tag.DisplayName, key), Href: tagPath(curCat, next),
				Count: item.AssetCount, Active: selected[key],
			})
		}
	}
	if curCat != "" {
		if name, ok := s.Reader.CategoryNameBySlug(ctx, slug, curCat); ok {
			panel.ActiveCategoryName = name
		} else {
			panel.ActiveCategoryName = curCat
		}
	}
	for _, key := range curTagKeys {
		panel.ActiveTagNames = append(panel.ActiveTagNames, displayNameOf(tagDisplay[key], key))
	}
	return panel
}

func catSlugOf(href string) string {
	if idx := strings.LastIndex(href, "/c/"); idx >= 0 {
		return href[idx+3:]
	}
	return href
}

func displayNameOf(display, fallback string) string {
	if strings.TrimSpace(display) != "" {
		return display
	}
	return fallback
}

// FilteredList serves /c/{cat}/t/{t1+t2} 与 /tags/{k1+k2}（多标签）：
// 分类(可空) × 标签(AND) 组合筛选列表，页码分页。组合页 noindex,follow
//（收录策略见 docs/知识库筛选页设计-2026-09-21 §2）。
func (s *Service) FilteredList(ctx context.Context, addr string, principal auth.Principal, slug, categorySlug string, tagKeys []string, pageNo int, baseURL string) (*Response, error) {
	routePath := "/sites/" + slug
	if categorySlug != "" {
		routePath += "/c/" + categorySlug
	}
	if len(tagKeys) > 0 {
		if categorySlug != "" {
			routePath += "/t/" + strings.Join(tagKeys, "+")
		} else {
			routePath += "/tags/" + strings.Join(tagKeys, "+")
		}
	}
	return s.pipeline(ctx, addr, principal, slug, routePath, baseURL, func(ctx context.Context, facts site.SiteFacts, band string, queries *theme.Queries) (renderOutput, error) {
		if gated(facts, band) {
			return s.gateOutput(facts)
		}
		result, err := s.Reader.FilteredPosts(ctx, addr, principal, slug, categorySlug, tagKeys, pageNo, 12)
		if err != nil {
			return renderOutput{}, err
		}
		vm := ListVM{Page: Page{Kind: "list"}, Items: []CardVM{}}
		vm.Heading = "知识库"
		for _, post := range result.Items {
			vm.Items = append(vm.Items, cardVM(slug, post, 160))
		}
		vm.Site = chrome(facts, "list")
		vm.Queries = queries
		vm.Filter = s.buildFilterPanel(ctx, addr, principal, slug, categorySlug, tagKeys)
		// 标题/描述：分类名 × 标签名组合。
		titleParts := []string{}
		if vm.Filter.ActiveCategoryName != "" {
			titleParts = append(titleParts, vm.Filter.ActiveCategoryName)
		}
		for _, name := range vm.Filter.ActiveTagNames {
			titleParts = append(titleParts, name)
		}
		pageTitle := "知识库"
		if len(titleParts) > 0 {
			pageTitle = strings.Join(titleParts, " × ") + " · 知识库"
		}
		vm.Title = pageTitle + " · " + facts.Site.Name
		vm.Description = facts.Site.Name + " 知识库筛选：" + strings.Join(titleParts, "、") + "。"
		vm.Canonical = baseURL + routePath
		if pageNo > 1 {
			vm.Canonical = baseURL + routePath // 分页 canonical 指基础组合 URL
		}
		// 组合页（多标签或分类×标签）noindex,follow：防组合排列稀释收录。
		vm.NoIndex = len(tagKeys) > 1 || (categorySlug != "" && len(tagKeys) > 0)
		// 页码导航。
		totalPages := (result.Total + 11) / 12
		if totalPages < 1 {
			totalPages = 1
		}
		vm.PageNav = PageNavVM{Page: pageNo, TotalPages: totalPages}
		if pageNo > 1 {
			vm.PageNav.PrevHref = routePath + fmt.Sprintf("?page=%d", pageNo-1)
		}
		if pageNo < totalPages {
			vm.PageNav.NextHref = routePath + fmt.Sprintf("?page=%d", pageNo+1)
		}
		return renderOutput{kind: "list", vm: vm, noIndex: vm.NoIndex}, nil
	})
}

// ChatScript serves the chat island JavaScript (公开 AI 问答，同搜索岛模式).
func (s *Service) ChatScript() *Response {
	return &Response{Body: ChatJavaScript(), ContentType: "text/javascript; charset=utf-8", CacheControl: "public, max-age=3600", Status: 200}
}
