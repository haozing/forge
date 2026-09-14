package site

// public.go — the anonymous/optional-member read face of the public-site
// domain (stage 5 P5-3, plan D2/D3/D4/D5'). Zero body copying (D1): every
// read resolves the live published pointer through the binding catalog.
//
// Responsibility split:
//   - list/tag-page/search reads run through the unified query service
//     (ChannelPublicSite); bindings contribute display metadata only and the
//     service layer merges them — a hit without a binding is dropped, the
//     site is a binding whitelist (plan D2);
//   - homepage featured/column sections and section pages enumerate the
//     binding catalog and re-check every asset with the extracted final
//     authorizer predicate (query.AuthorizePublicSiteAsset, plan §3.3);
//   - every served byte passes the whitelisted DTO projection of plan §3.4
//     (no workspace/member/audit/draft/confidence internals);
//   - ETags follow D4 (detail = site+asset revision+version+binding touch,
//     list/home = site revision+result fingerprint) and throttling runs on
//     the shared public_site_ip bucket (plan B5).
//
// The reader never touches *http.Request: the HTTP layer precomputes the
// effective client address and resolves the optional session cookie into an
// auth.Principal.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"agentchunzhi/internal/auth"
	agentquery "agentchunzhi/internal/query"
	"agentchunzhi/internal/resourcemodel"
	"agentchunzhi/internal/store"
	"agentchunzhi/internal/tag"

	"github.com/jackc/pgx/v5"
)

// PublicSiteQuerier is the unified query surface the public reader consumes.
// agentquery.Service satisfies it directly (plan D2: exactly one query
// service; the public face picks the mode per endpoint).
type PublicSiteQuerier interface {
	PublicSiteQuery(ctx context.Context, site agentquery.PublicSiteRef, visitor agentquery.VisitorIdentity, req agentquery.Request) (agentquery.Response, error)
}

// PublicThrottle is the anonymous per-address budget of the public face
// (plan B5). *auth.PublicSiteIPThrottle implements it; nil disables the
// limit (unit tests only — production always wires the shared bucket).
type PublicThrottle interface {
	Allow(ctx context.Context, effectiveAddr string) (bool, time.Duration, error)
}

// PublicReader serves the public read face of one site slug. It carries no
// per-request state; Store, Query, Throttle and Facets are wired once.
type PublicReader struct {
	Store *store.Store
	// Query is the unified query service (plan D2). Nil is a bootstrapping
	// defect; the read methods fail closed on it.
	Query PublicSiteQuerier
	// Throttle is the shared public_site_ip bucket (nullable in tests).
	Throttle PublicThrottle
	// Facets counts the tag cloud with the fixed (public, published) scope.
	Facets tag.FacetService
}

// PublicRateLimitError reports a refused public read: the caller's address
// prefix exhausted the shared public_site_ip budget. RetryAfter is the
// server-side block remainder for the Retry-After header.
type PublicRateLimitError struct {
	RetryAfter time.Duration
}

func (e *PublicRateLimitError) Error() string { return "public site rate limited" }

// ErrPublicThrottleUnavailable reports a throttle bucket store failure: the
// public face fails closed instead of serving without its budget (the bucket
// store is the same database the content lives in).
var ErrPublicThrottleUnavailable = errors.New("public site throttle unavailable")

// Public list defaults. The page size cannot exceed the query contract's
// MaxTopK, otherwise every request would fail request validation.
const (
	publicDefaultLimit = 20
	publicMaxLimit     = 50
	// publicMaxBindings caps one binding-driven page (featured, section).
	publicMaxBindings = 100
	// publicSummaryRunes caps the projected list summary (plan D3 safe
	// truncation: markdown markers stripped plus a hard length cap).
	publicSummaryRunes = 280
)

// PublicCacheControl is the D4 cache policy of the public read face.
const PublicCacheControl = "public, max-age=60, stale-while-revalidate=300"

// normalizePublicLimit clamps a client-supplied page size into the query
// contract envelope.
func normalizePublicLimit(limit int) int {
	if limit <= 0 {
		return publicDefaultLimit
	}
	if limit > publicMaxLimit {
		return publicMaxLimit
	}
	return limit
}

// allow runs the per-address budget at every public entry point. Every
// request counts (plan B5: no success/failure distinction on this face).
func (r *PublicReader) allow(ctx context.Context, visitorAddr string) error {
	if r.Throttle == nil {
		return nil
	}
	allowed, retryAfter, err := r.Throttle.Allow(ctx, visitorAddr)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrPublicThrottleUnavailable, err)
	}
	if !allowed {
		return &PublicRateLimitError{RetryAfter: retryAfter}
	}
	return nil
}

// loadSite resolves one active site by slug. A disabled site answers
// ErrSiteDisabled and an unknown slug ErrSiteNotFound; the handler collapses
// both into the same 404 so probing cannot distinguish them (plan §4).
func (r *PublicReader) loadSite(ctx context.Context, slug string) (Site, error) {
	if !ValidSlug(slug) {
		return Site{}, ErrSiteNotFound
	}
	if r.Store == nil || r.Store.Pool == nil {
		return Site{}, errors.New("database store is not initialized")
	}
	item, err := scanSiteRow(r.Store.Pool.QueryRow(ctx, `
		SELECT `+siteColumns+`
		FROM site.public_sites
		WHERE slug = $1
	`, slug))
	if errors.Is(err, pgx.ErrNoRows) {
		return Site{}, ErrSiteNotFound
	}
	if err != nil {
		return Site{}, fmt.Errorf("load public site: %w", err)
	}
	if item.Status != StatusActive {
		return Site{}, ErrSiteDisabled
	}
	return item, nil
}

// visitor resolves the optional member identity of a public read (plan D5').
// Anonymous visitors and sessions from another organization stay zero-valued;
// a same-organization member keeps its user id and gains the site-workspace
// membership flag from one EXISTS query. ForPublicSite re-verifies every
// claim against the membership tables — this only fills what the session
// already proves, and a lookup failure degrades the tier instead of failing
// the read.
func (r *PublicReader) visitor(ctx context.Context, item Site, principal auth.Principal) agentquery.VisitorIdentity {
	if principal.UserType != auth.UserTypeMember || !agentquery.ValidUUID(principal.UserID) {
		return agentquery.VisitorIdentity{}
	}
	if principal.OrganizationID != item.OrganizationID {
		return agentquery.VisitorIdentity{}
	}
	identity := agentquery.VisitorIdentity{
		UserType:       auth.UserTypeMember,
		OrganizationID: item.OrganizationID,
		UserID:         principal.UserID,
	}
	if r.Store == nil || r.Store.Pool == nil {
		return identity
	}
	var workspaceMember bool
	err := r.Store.Pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM content.workspace_members wm
			JOIN content.workspaces w ON w.organization_id = wm.organization_id
			  AND w.id = wm.workspace_id AND w.status = 'active'
			JOIN identity.users u ON u.id = wm.user_id AND u.user_type = 'member'
			  AND u.status = 'active'
			WHERE wm.organization_id = $1::uuid AND wm.workspace_id = $2::uuid
			  AND wm.user_id = $3::uuid
		)
	`, item.OrganizationID, item.WorkspaceID, principal.UserID).Scan(&workspaceMember)
	if err == nil {
		identity.WorkspaceMember = workspaceMember
	}
	return identity
}

// siteRef renders the query-channel site reference (plan D5': the site's
// default_content_scope is the exposure ceiling).
func siteRef(item Site) agentquery.PublicSiteRef {
	return agentquery.PublicSiteRef{
		OrganizationID: item.OrganizationID,
		WorkspaceID:    item.WorkspaceID,
		DefaultScope:   item.DefaultContentScope,
	}
}

// SiteFacts is the delivery-facing snapshot of one public site: the site row
// plus the effective configuration — the published release snapshot when the
// pointer is set, the working columns otherwise (bootstrap path, design doc
// §7.4) — and the release revision for cache keying (0 = working mode).
type SiteFacts struct {
	Site            Site
	ReleaseRevision int64
	// Effective configuration the render consumes.
	CommentsMode string
	// ThemeFiles 是已发布主题的文件集；空对象 = 全槽位回退内置默认主题。
	ThemeFiles json.RawMessage
	// Nav 是数据化导航（固定路由 + 自定义页派生）。
	Nav []NavEntry
}

// SiteFacts resolves the delivery chrome facts of one slug without touching
// the throttle budget (the page reads that follow are throttled reads).
// Disabled and unknown sites collapse into the same errors as loadSite.
func (r *PublicReader) SiteFacts(ctx context.Context, slug string) (SiteFacts, error) {
	item, err := r.loadSite(ctx, slug)
	if err != nil {
		return SiteFacts{}, err
	}
	facts := SiteFacts{
		Site:         item,
		CommentsMode: item.CommentsMode,
		ThemeFiles:   json.RawMessage("{}"),
	}
	if item.PublishedReleaseID != nil && r.Store != nil && r.Store.Pool != nil {
		var revision int64
		var config json.RawMessage
		if err := r.Store.Pool.QueryRow(ctx, `
			SELECT revision, config
			FROM site.site_releases
			WHERE organization_id = $1::uuid AND id = $2::uuid
		`, item.OrganizationID, *item.PublishedReleaseID).Scan(&revision, &config); err == nil && len(config) > 0 {
			var snapshot struct {
				CommentsMode    string `json:"comments_mode"`
				ThemeRevisionID string `json:"theme_revision_id"`
			}
			if json.Unmarshal(config, &snapshot) == nil {
				facts.ReleaseRevision = revision
				if snapshot.CommentsMode != "" {
					facts.CommentsMode = snapshot.CommentsMode
				}
				if snapshot.ThemeRevisionID != "" {
					var files json.RawMessage
					if err := r.Store.Pool.QueryRow(ctx, `
						SELECT files FROM site.site_theme_revisions
						WHERE organization_id = $1::uuid AND id = $2::uuid
					`, item.OrganizationID, snapshot.ThemeRevisionID).Scan(&files); err == nil && len(files) > 0 {
						facts.ThemeFiles = files
					}
				}
			}
		}
	}
	if facts.ReleaseRevision == 0 && item.PublishedThemeRevisionID != "" && r.Store != nil && r.Store.Pool != nil {
		var files json.RawMessage
		if err := r.Store.Pool.QueryRow(ctx, `
			SELECT files FROM site.site_theme_revisions
			WHERE organization_id = $1::uuid AND id = $2::uuid
		`, item.OrganizationID, item.PublishedThemeRevisionID).Scan(&files); err == nil && len(files) > 0 {
			facts.ThemeFiles = files
		}
	}
	facts.Nav = r.deriveNav(ctx, item)
	return facts, nil
}

// deriveNav 构造数据化导航：固定路由 + 顶级公开分类 + 自定义页（nav_order）。
func (r *PublicReader) deriveNav(ctx context.Context, item Site) []NavEntry {
	nav := []NavEntry{
		{ID: "home", Name: "首页", Href: "/sites/" + item.Slug + "/"},
		{ID: "posts", Name: "文章", Href: "/sites/" + item.Slug + "/posts/"},
		{ID: "tags", Name: "标签", Href: "/sites/" + item.Slug + "/tags/"},
	}
	if r.Store == nil || r.Store.Pool == nil {
		return nav
	}
	// 顶级公开分类（§4.4）：public_flag 树的第一层进导航。
	catRows, err := r.Store.Pool.Query(ctx, `
		SELECT slug, COALESCE(NULLIF(public_title, ''), title)
		FROM content.containers
		WHERE organization_id = $1::uuid AND workspace_id = $2::uuid
		  AND public_flag = true AND status = 'active' AND parent_id IS NULL
		ORDER BY sort_key, title
	`, item.OrganizationID, item.WorkspaceID)
	if err == nil {
		defer catRows.Close()
		for catRows.Next() {
			var slug, title string
			if err := catRows.Scan(&slug, &title); err == nil {
				nav = append(nav, NavEntry{
					ID:   "/sites/" + item.Slug + "/c/" + slug,
					Name: title,
					Href: "/sites/" + item.Slug + "/c/" + slug,
				})
			}
		}
	}
	rows, err := r.Store.Pool.Query(ctx, `
		SELECT title, slug, nav_order
		FROM site.site_pages
		WHERE organization_id = $1::uuid AND site_id = $2::uuid AND nav_hidden = false
		ORDER BY nav_order, slug
	`, item.OrganizationID, item.ID)
	if err != nil {
		return nav
	}
	defer rows.Close()
	for rows.Next() {
		var title, slug string
		var order int
		if err := rows.Scan(&title, &slug, &order); err == nil {
			nav = append(nav, NavEntry{
				ID:   "/sites/" + item.Slug + "/p/" + slug,
				Name: title,
				Href: "/sites/" + item.Slug + "/p/" + slug,
			})
		}
	}
	return nav
}

// AllowPublic runs only the shared public_site_ip budget check (the HTML
// delivery face applies it on every request, cache hits included).
func (r *PublicReader) AllowPublic(ctx context.Context, visitorAddr string) error {
	return r.allow(ctx, visitorAddr)
}

// VisitorFor resolves the visitor band of one public read for callers
// outside the read methods (the delivery media route runs the same tiering
// through the shared authorizer instead of its own visibility logic).
func (r *PublicReader) VisitorFor(ctx context.Context, item Site, principal auth.Principal) agentquery.VisitorIdentity {
	return r.visitor(ctx, item, principal)
}

// SectionSlugs enumerates the distinct binding section slugs of one site for
// the delivery sitemap and section navigation (binding catalog metadata, not
// an asset content read; plan D2 keeps content reads on the query service).
func (r *PublicReader) SectionSlugs(ctx context.Context, visitorAddr string, principal auth.Principal, slug string) ([]string, error) {
	if err := r.allow(ctx, visitorAddr); err != nil {
		return nil, err
	}
	item, err := r.loadSite(ctx, slug)
	if err != nil {
		return nil, err
	}
	rows, err := r.Store.Pool.Query(ctx, `
		SELECT DISTINCT section_slug
		FROM site.site_content_bindings
		WHERE organization_id = $1::uuid AND site_id = $2::uuid AND section_slug <> ''
		ORDER BY section_slug
	`, item.OrganizationID, item.ID)
	if err != nil {
		return nil, fmt.Errorf("load section slugs: %w", err)
	}
	defer rows.Close()
	slugs := []string{}
	for rows.Next() {
		var section string
		if err := rows.Scan(&section); err != nil {
			return nil, err
		}
		slugs = append(slugs, section)
	}
	return slugs, rows.Err()
}

// ---------------------------------------------------------------------------
// Public DTOs (plan §3.4 whitelist)
// ---------------------------------------------------------------------------

// PublicPost is one whitelisted list projection. No workspace, model,
// member, audit, draft or confidence internals travel on the public face.
type PublicPost struct {
	AssetID     string `json:"asset_id"`
	DisplayPath string `json:"display_path"`
	Title       string `json:"title"`
	Summary     string `json:"summary"`
	ContentKind string `json:"content_kind"`
	// Fields carries the site whitelist's card_fields for the bound model
	// (empty when no whitelist is configured — fail-closed).
	Fields      []PublicFieldValue      `json:"fields,omitempty"`
	Tags        []agentquery.TagSummary `json:"tags"`
	UpdatedAt   *time.Time              `json:"updated_at"`
	PublishedAt *time.Time              `json:"published_at"`
	// CoverAttachmentID is the published version's cover image (二期 §6;
	// empty = no cover; media type is verified image at render).
	CoverAttachmentID string `json:"cover_attachment_id"`
}

// PublicPostPage is one page of the public list face; ETag stays off the wire.
type PublicPostPage struct {
	Items      []PublicPost `json:"items"`
	NextCursor string       `json:"next_cursor"`
	HasMore    bool         `json:"has_more"`
	ETag       string       `json:"-"`
}

// PublicSection is one rendered homepage/section slice.
type PublicSection struct {
	Type        string `json:"type"`
	Title       string `json:"title,omitempty"`
	SectionSlug string `json:"section_slug,omitempty"`
	// ModelKey is the resource-model narrowing of this slice (P1-B); empty
	// means the section spans every model.
	ModelKey string       `json:"model_key,omitempty"`
	Items    []PublicPost `json:"items"`
}

// PublicSiteInfo is the whitelisted site header of the public face.
type PublicSiteInfo struct {
	Slug             string          `json:"slug"`
	Name             string          `json:"name"`
	NavigationConfig json.RawMessage `json:"navigation_config"`
}

// PublicHomeView is the GET /sites/{slug} payload: the site header plus the
// homepage_config sections rendered with live content.
type PublicHomeView struct {
	Site     PublicSiteInfo  `json:"site"`
	Sections []PublicSection `json:"sections"`
	ETag  string     `json:"-"`
}

// NavEntry is one auto-enumerated navigation entry (C1 nav)。
type NavEntry struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Href string `json:"href"`
}

// PublicCategoryRef is one public category reference (categories 模块)。
type PublicCategoryRef struct {
	Name  string `json:"name"`
	Href  string `json:"href"`
	Count int    `json:"count"`
}

// PublicPostContent is the detail projection of plan §3.4: markdown travels
// verbatim (sanitization is the React renderer's job), fields are projected
// through the model schema whitelist.
type PublicPostContent struct {
	AssetID     string `json:"id"`
	DisplayPath string `json:"display_path"`
	Section     string `json:"section"`
	Title       string `json:"title"`
	Summary     string `json:"summary"`
	Markdown    string `json:"markdown"`
	// Fields carries only the site whitelist's detail_fields, in whitelist
	// order (fail-closed: empty whitelist = no fields).
	Fields      []PublicFieldValue      `json:"fields"`
	Tags        []agentquery.TagSummary `json:"tags"`
	ContentKind string                  `json:"content_kind"`
	UpdatedAt   *time.Time              `json:"updated_at"`
	PublishedAt *time.Time              `json:"published_at"`
	// CoverAttachmentID is the published version's cover image (二期 §6).
	CoverAttachmentID string `json:"cover_attachment_id"`
	// CoverAlt is the cover's alt text frozen with the version (G6).
	CoverAlt string `json:"cover_alt"`
	// Publications is the recent publication history (D16: 版本轨迹，倒序，
	// 限 10 条；只含已发布动作的元数据，不含历史内容)。
	Publications []PublicPublication `json:"publications"`
	// VersionNo is the currently published version number (D16 元信息行).
	VersionNo int    `json:"version_no"`
	ETag      string `json:"-"`
}

// PublicPublication is one entry of the detail page's update-history block.
// change_note 是作者发布时主动写的公开说明（D16：不做自动 diff）。
type PublicPublication struct {
	VersionNo   int       `json:"version_no"`
	PublishedAt time.Time `json:"published_at"`
	ChangeNote  string    `json:"change_note,omitempty"`
	Origin      string    `json:"origin"`
	Confirmed   bool      `json:"confirmed"`
}

// ---------------------------------------------------------------------------
// Read operations
// ---------------------------------------------------------------------------

// PublicPostQuery carries the list surface knobs: the query text, tag key
// filters, the frozen-session cursor and the page size.
type PublicPostQuery struct {
	QueryText string
	TagsAny   []string
	TagsAll   []string
	TagsNone  []string
	Cursor    string
	Limit     int
	// Locale 过滤（D11）：空 = 不过滤（单语言站点）。
	Locale        string
	DefaultLocale string
	Fallback      bool
}

// Posts serves the site post list: a structured (no q) or fulltext (q set)
// ChannelPublicSite query merged with the binding catalog. Unbound hits are
// dropped (site = binding whitelist, plan D2); the query session pagination
// (cursor) is preserved verbatim.
func (r *PublicReader) Posts(ctx context.Context, visitorAddr string, principal auth.Principal, slug string, list PublicPostQuery) (PublicPostPage, error) {
	if err := r.allow(ctx, visitorAddr); err != nil {
		return PublicPostPage{}, err
	}
	item, err := r.loadSite(ctx, slug)
	if err != nil {
		return PublicPostPage{}, err
	}
	req := agentquery.Request{
		TagsAny:  list.TagsAny,
		TagsAll:  list.TagsAll,
		TagsNone: list.TagsNone,
		TopK:     normalizePublicLimit(list.Limit),
		Cursor:   list.Cursor,
	}
	// Mode selection per plan D2: empty q is the authoritative main-data
	// listing, a query text switches to the fulltext plan.
	if queryText := strings.TrimSpace(list.QueryText); queryText == "" {
		req.Mode = agentquery.ModeStructured
	} else {
		req.Mode = agentquery.ModeFulltext
		req.Query = queryText
	}
	response, err := r.Query.PublicSiteQuery(ctx, siteRef(item), r.visitor(ctx, item, principal), req)
	if err != nil {
		return PublicPostPage{}, err
	}
	page, err := r.mergeResponse(ctx, item, response)
	if err != nil {
		return PublicPostPage{}, err
	}
	page.ETag = ListETag(item.Revision, page.Items)
	page.Items = r.filterByLocale(ctx, item, page.Items, list.Locale)
	return page, nil
}

// Search serves the site search face: fulltext or hybrid with a mandatory
// query text, merged with the binding catalog like Posts.
func (r *PublicReader) Search(ctx context.Context, visitorAddr string, principal auth.Principal, slug, queryText, mode string, list PublicPostQuery) (PublicPostPage, error) {
	if err := r.allow(ctx, visitorAddr); err != nil {
		return PublicPostPage{}, err
	}
	item, err := r.loadSite(ctx, slug)
	if err != nil {
		return PublicPostPage{}, err
	}
	if mode == "" {
		mode = agentquery.ModeFulltext
	}
	if mode != agentquery.ModeFulltext && mode != agentquery.ModeHybrid {
		return PublicPostPage{}, agentquery.ErrInvalidQueryMode
	}
	req := agentquery.Request{
		Query:  strings.TrimSpace(queryText),
		Mode:   mode,
		TopK:   normalizePublicLimit(list.Limit),
		Cursor: list.Cursor,
	}
	response, err := r.Query.PublicSiteQuery(ctx, siteRef(item), r.visitor(ctx, item, principal), req)
	if err != nil {
		return PublicPostPage{}, err
	}
	page, err := r.mergeResponse(ctx, item, response)
	if err != nil {
		return PublicPostPage{}, err
	}
	page.ETag = ListETag(item.Revision, page.Items)
	page.Items = r.filterByLocale(ctx, item, page.Items, list.Locale)
	return page, nil
}

// TagPage serves one tag archive page: the tag key becomes the tags_any
// filter of the standard list face (plan §4).
func (r *PublicReader) TagPage(ctx context.Context, visitorAddr string, principal auth.Principal, slug, key string, list PublicPostQuery) (PublicPostPage, error) {
	list.TagsAny = []string{strings.TrimSpace(key)}
	return r.Posts(ctx, visitorAddr, principal, slug, list)
}

// Tags serves the site tag cloud: FacetService counts with the fixed
// (public, published) scope of plan B4/§5.2 over the site workspace. The
// cloud never widens with the visitor tier so tag existence never leaks.
func (r *PublicReader) Tags(ctx context.Context, visitorAddr string, principal auth.Principal, slug string, limit int) ([]tag.FacetItem, error) {
	if err := r.allow(ctx, visitorAddr); err != nil {
		return nil, err
	}
	item, err := r.loadSite(ctx, slug)
	if err != nil {
		return nil, err
	}
	if r.Facets.Store == nil || r.Facets.Store.Pool == nil {
		return nil, errors.New("facet store is not initialized")
	}
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	return r.Facets.CountsForOrganization(ctx, item.OrganizationID, item.WorkspaceID, tag.FacetScope{
		Scope:             "published",
		Visibility:        "public",
		PublicationStatus: "published",
	}, tag.KeyFilter{}, tag.StatusActive, limit)
}

// postRow is the detail read model: the binding, the live published version
// main data and the asset/model facts the projection needs.
type postRow struct {
	Binding           Binding
	PublicView        []byte
	AssetLocale       string
	AssetRevision     int64
	AssetUpdatedAt    time.Time
	AssetPublishedAt  *time.Time
	ContentKind       string
	ModelID           string
	VersionID         string
	Title             string
	Summary           string
	Markdown          string
	Fields            []byte
	FieldSchema       []byte
	CoverAttachmentID string
	// CoverAlt is the cover's alt text frozen with the published version
	// (G6); empty falls back to the title at render time.
	CoverAlt string
}

// Post serves the detail projection: binding → asset → live published
// version main data, re-checked with the extracted final authorizer
// predicate (plan §3.3). Any gate failure answers ErrSiteNotFound so
// unpublished, archived, visibility-downgraded or channel-disabled content
// is indistinguishable from a missing path.
func (r *PublicReader) Post(ctx context.Context, visitorAddr string, principal auth.Principal, slug, displayPath, locale string) (PublicPostContent, error) {
	if err := r.allow(ctx, visitorAddr); err != nil {
		return PublicPostContent{}, err
	}
	item, err := r.loadSite(ctx, slug)
	if err != nil {
		return PublicPostContent{}, err
	}
	displayPath = strings.Trim(displayPath, "/")
	if !ValidDisplayPath(displayPath) {
		return PublicPostContent{}, ErrSiteNotFound
	}
	visitor := r.visitor(ctx, item, principal)
	row, err := r.loadPostRow(ctx, item, displayPath)
	if err != nil {
		return PublicPostContent{}, err
	}
	// D11：详情不回退——资产语言 NULL 或等于访问语言才渲染。
	if !item.localeMatchesDetail(row.AssetLocale, locale) {
		return PublicPostContent{}, ErrSiteNotFound
	}
	authorized, err := agentquery.AuthorizePublicSiteAsset(ctx, r.Store, siteRef(item), visitor, row.Binding.AssetID, row.VersionID)
	if err != nil {
		return PublicPostContent{}, err
	}
	if !authorized {
		return PublicPostContent{}, ErrSiteNotFound
	}
	tags, err := r.tagSummaries(ctx, item.OrganizationID, []string{row.VersionID})
	if err != nil {
		return PublicPostContent{}, err
	}
	summary := tags[row.VersionID]
	if summary == nil {
		summary = []agentquery.TagSummary{}
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(row.Fields, &fields); err != nil || fields == nil {
		fields = map[string]json.RawMessage{}
	}
	schemaTypes := ParseFieldSchema(row.FieldSchema)
	projected := ProjectFields(fields, schemaTypes)
	whitelisted := WhitelistFieldsWithLabels(projected, schemaTypes, ParseFieldLabels(row.FieldSchema), resourcemodel.DecodePublicView(row.PublicView), false)
	publishedAt := ResolveDisplayPublishedAt(row.Binding.DisplayPublishedAt, row.AssetPublishedAt)
	publications, versionNo := r.publicationHistory(ctx, item.OrganizationID, row.Binding.AssetID)
	return PublicPostContent{
		AssetID:           row.Binding.AssetID,
		DisplayPath:       row.Binding.DisplayPath,
		Section:           row.Binding.SectionSlug,
		Title:             row.Title,
		Summary:           row.Summary,
		Markdown:          row.Markdown,
		Fields:            whitelisted,
		Tags:              summary,
		ContentKind:       row.ContentKind,
		UpdatedAt:         timePtr(row.AssetUpdatedAt),
		PublishedAt:       publishedAt,
		Publications:      publications,
		VersionNo:         versionNo,
		CoverAttachmentID: row.CoverAttachmentID,
		CoverAlt:          row.CoverAlt,
		ETag:              DetailETag(item.Revision, row.AssetRevision, row.VersionID, row.Binding.UpdatedAt),
	}, nil
}

// PathRedirect resolves a display_path that no longer has a live binding to
// its moved target (one hop; chains are flattened at write time by
// recordPathRedirectTx). ok=false means no redirect exists and the caller
// keeps its 404. Site loading errors surface unchanged; the address budget
// is not re-charged here (the caller already passed AllowPublic).
func (r *PublicReader) PathRedirect(ctx context.Context, slug, displayPath string) (string, bool, error) {
	item, err := r.loadSite(ctx, slug)
	if err != nil {
		return "", false, err
	}
	displayPath = strings.Trim(displayPath, "/")
	if !ValidDisplayPath(displayPath) {
		return "", false, nil
	}
	var toPath string
	err = r.Store.Pool.QueryRow(ctx, `
		SELECT to_path FROM site.path_redirects
		WHERE organization_id = $1::uuid AND site_id = $2::uuid AND from_path = $3
	`, item.OrganizationID, item.ID, displayPath).Scan(&toPath)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("load path redirect: %w", err)
	}
	return toPath, true, nil
}

// publicationHistory resolves the update-history entries of one asset
// (D16：最近 10 条已发布动作的元数据，倒序) plus the current version number.
// 读失败一律降级为空轨迹——更新记录是增强项，绝不阻断详情页。
func (r *PublicReader) publicationHistory(ctx context.Context, organizationID, assetID string) ([]PublicPublication, int) {
	out := []PublicPublication{}
	versionNo := 0
	if r.Store == nil || r.Store.Pool == nil {
		return out, versionNo
	}
	_ = r.Store.Pool.QueryRow(ctx, `
		SELECT COALESCE(v.version_no, 0)
		FROM asset.assets a
		LEFT JOIN asset.asset_versions v
		  ON v.organization_id = a.organization_id AND v.id = a.current_published_version_id
		WHERE a.organization_id = $1::uuid AND a.id = $2::uuid
	`, organizationID, assetID).Scan(&versionNo)
	rows, err := r.Store.Pool.Query(ctx, `
		SELECT version_no, published_at, COALESCE(change_note, ''), origin, confirmation_status
		FROM asset.asset_publications
		WHERE organization_id = $1::uuid AND asset_id = $2::uuid
		ORDER BY published_at DESC, version_no DESC
		LIMIT 10
	`, organizationID, assetID)
	if err != nil {
		return out, versionNo
	}
	defer rows.Close()
	for rows.Next() {
		var item PublicPublication
		var note, status string
		if err := rows.Scan(&item.VersionNo, &item.PublishedAt, &note, &item.Origin, &status); err != nil {
			return out, versionNo
		}
		item.ChangeNote = note
		item.Confirmed = status == "human_confirmed"
		out = append(out, item)
	}
	return out, versionNo
}

// loadPostRow reads the detail row: binding by display path joined with the
// asset, its live published version and the model version schema that
// governs the fields projection. A missing binding answers ErrSiteNotFound.
func (r *PublicReader) loadPostRow(ctx context.Context, item Site, displayPath string) (postRow, error) {
	var row postRow
	err := r.Store.Pool.QueryRow(ctx, `
		SELECT `+bindingColumns+`,
		       a.revision, a.updated_at, a.published_at, rm.content_kind,
		       a.resource_model_id::text,
		       COALESCE(pv.id::text, ''), COALESCE(pv.title, ''), COALESCE(pv.summary, ''),
		       COALESCE(pv.markdown, ''), COALESCE(pv.fields, '{}'::jsonb),
		       COALESCE(mv.field_schema, '{}'::jsonb), COALESCE(mv.public_view, '{}'::jsonb),
		       COALESCE(a.locale, ''), COALESCE(cover.id::text, ''), COALESCE(cav.alt_text, '')
		FROM site.site_content_bindings b
		JOIN asset.assets a
		  ON a.organization_id = b.organization_id AND a.id = b.asset_id
		JOIN model.resource_models rm
		  ON rm.organization_id = a.organization_id AND rm.id = a.resource_model_id
		LEFT JOIN asset.asset_versions pv
		  ON pv.organization_id = a.organization_id AND pv.id = a.current_published_version_id
		LEFT JOIN model.resource_model_versions mv
		  ON mv.organization_id = pv.organization_id AND mv.id = pv.resource_model_version_id
		LEFT JOIN asset.asset_version_attachments cav
		  ON cav.organization_id = pv.organization_id AND cav.asset_version_id = pv.id
		  AND cav.role = 'cover'
		LEFT JOIN asset.attachments cover
		  ON cover.organization_id = cav.organization_id AND cover.id = cav.attachment_id
		  AND cover.deleted_at IS NULL AND cover.media_type LIKE 'image/%'
		WHERE b.organization_id = $1::uuid AND b.site_id = $2::uuid AND b.display_path = $3
	`, item.OrganizationID, item.ID, displayPath).Scan(
		&row.Binding.ID, &row.Binding.SiteID, &row.Binding.AssetID, &row.Binding.DisplayPath,
		&row.Binding.ContentType, &row.Binding.SectionSlug, &row.Binding.SortOrder, &row.Binding.OnHomepage,
		&row.Binding.OnNavigation, &row.Binding.DisplayConfig, &row.Binding.DisplayPublishedAt,
		&row.Binding.CreatedAt, &row.Binding.UpdatedAt,
		&row.AssetRevision, &row.AssetUpdatedAt, &row.AssetPublishedAt, &row.ContentKind,
		&row.ModelID,
		&row.VersionID, &row.Title, &row.Summary, &row.Markdown, &row.Fields, &row.FieldSchema,
		&row.PublicView, &row.AssetLocale,
		&row.CoverAttachmentID, &row.CoverAlt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return postRow{}, ErrSiteNotFound
	}
	if err != nil {
		return postRow{}, fmt.Errorf("load public post: %w", err)
	}
	return row, nil
}

// Home renders the homepage: homepage_config.sections expanded against live
// content (latest → query face, featured/column → binding catalog with the
// §3.3 re-check). Unknown section types are skipped.
func (r *PublicReader) Home(ctx context.Context, visitorAddr string, principal auth.Principal, slug string) (PublicHomeView, error) {
	return r.HomeWithConfig(ctx, visitorAddr, principal, slug, "")
}

// HomeWithConfig renders the homepage against one explicit homepage config
// (the delivery face passes the effective release snapshot; nil falls back to
// the working columns). pagesConfig v2 优先：非空文档走模块渲染（C1），
// 空 = 旧 sections 路径。
func (r *PublicReader) HomeWithConfig(ctx context.Context, visitorAddr string, principal auth.Principal, slug string, locale string) (PublicHomeView, error) {
	if err := r.allow(ctx, visitorAddr); err != nil {
		return PublicHomeView{}, err
	}
	item, err := r.loadSite(ctx, slug)
	if err != nil {
		return PublicHomeView{}, err
	}
	visitor := r.visitor(ctx, item, principal)
	view := PublicHomeView{
		Site:     PublicSiteInfo{Slug: item.Slug, Name: item.Name},
		Sections: []PublicSection{},
	}
	rendered := PublicSection{Type: HomepageSectionLatest, Title: "最新内容", Items: []PublicPost{}}
	rendered.Items, err = r.latestPosts(ctx, item, visitor, "", 10, locale)
	if err != nil {
		return PublicHomeView{}, err
	}
	view.Sections = append(view.Sections, rendered)
	view.ETag = ListETag(item.Revision, rendered.Items)
	return view, nil
}

// Section serves one section page: the binding catalog slice of the section
// slug, every row re-checked against the current published pointer and the
// visitor band (plan §3.3), ordered by binding sort order.
func (r *PublicReader) Section(ctx context.Context, visitorAddr string, principal auth.Principal, slug, sectionSlug, modelKey string, limit int, locale string) (PublicPostPage, error) {
	if err := r.allow(ctx, visitorAddr); err != nil {
		return PublicPostPage{}, err
	}
	item, err := r.loadSite(ctx, slug)
	if err != nil {
		return PublicPostPage{}, err
	}
	sectionSlug = strings.Trim(strings.TrimSpace(sectionSlug), "/")
	if sectionSlug == "" || len(sectionSlug) > 120 {
		return PublicPostPage{}, ErrSiteNotFound
	}
	visitor := r.visitor(ctx, item, principal)
	rows, err := r.boundVersionRows(ctx, item, sectionSlug, modelKey, false, limit, locale)
	if err != nil {
		return PublicPostPage{}, err
	}
	items, err := r.projectBoundRows(ctx, item, visitor, rows)
	if err != nil {
		return PublicPostPage{}, err
	}
	return PublicPostPage{Items: items, ETag: ListETag(item.Revision, items)}, nil
}

// modelIDByKey resolves one resource-model key to its id inside the site's
// organization/workspace. Builtin models are organization-level
// (workspace_id NULL), so both scopes are accepted. An unknown key returns
// empty (callers treat it as "no narrowing" or "no content").
func (r *PublicReader) modelIDByKey(ctx context.Context, item Site, modelKey string) (string, error) {
	if strings.TrimSpace(modelKey) == "" {
		return "", nil
	}
	var id string
	err := r.Store.Pool.QueryRow(ctx, `
		SELECT id::text FROM model.resource_models
		WHERE organization_id = $1::uuid AND model_key = $2 AND status = 'active'
		  AND (workspace_id = $3::uuid OR workspace_id IS NULL)
		ORDER BY workspace_id NULLS LAST
		LIMIT 1
	`, item.OrganizationID, modelKey, item.WorkspaceID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("resolve resource model key: %w", err)
	}
	return id, nil
}

// latestPosts runs the structured "latest" face for one homepage section and
// merges it through the binding whitelist. A non-empty modelKey narrows the
// query to that resource model (P1-B content collections).
func (r *PublicReader) latestPosts(ctx context.Context, item Site, visitor agentquery.VisitorIdentity, modelKey string, limit int, locale string) ([]PublicPost, error) {
	modelID, err := r.modelIDByKey(ctx, item, modelKey)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(modelKey) != "" && modelID == "" {
		// Unknown model key: the section is empty rather than silently
		// falling back to every model (a wrong key must not leak content).
		return []PublicPost{}, nil
	}
	req := agentquery.Request{Mode: agentquery.ModeStructured, TopK: normalizePublicLimit(limit)}
	if modelID != "" {
		req.ResourceModelIDs = []string{modelID}
	}
	response, err := r.Query.PublicSiteQuery(ctx, siteRef(item), visitor, req)
	if err != nil {
		return nil, err
	}
	page, err := r.mergeResponse(ctx, item, response)
	if err != nil {
		return nil, err
	}
	return r.filterByLocale(ctx, item, page.Items, locale), nil
}

// filterByLocale applies the D11 locale visibility in Go for query-face
// results (the unified query cannot express the locale predicate): an asset
// passes when its locale is NULL, equals the requested locale, or equals the
// default locale while fallback is enabled.
func (r *PublicReader) filterByLocale(ctx context.Context, item Site, posts []PublicPost, locale string) []PublicPost {
	if locale == "" || locale == item.DefaultLocale || len(posts) == 0 {
		return posts
	}
	ids := make([]string, 0, len(posts))
	for _, post := range posts {
		ids = append(ids, post.AssetID)
	}
	rows, err := r.Store.Pool.Query(ctx, `
		SELECT id::text, COALESCE(locale, '') FROM asset.assets
		WHERE organization_id = $1::uuid AND id::text = ANY($2::text[])
	`, item.OrganizationID, ids)
	if err != nil {
		return posts // 过滤失效时保守放行（可见性仍由三道闸约束）
	}
	defer rows.Close()
	locales := map[string]string{}
	for rows.Next() {
		var id, assetLocale string
		if rows.Scan(&id, &assetLocale) == nil {
			locales[id] = assetLocale
		}
	}
	out := make([]PublicPost, 0, len(posts))
	for _, post := range posts {
		assetLocale := locales[post.AssetID]
		if assetLocale == "" || assetLocale == locale || (item.FallbackToDefault && assetLocale == item.DefaultLocale) {
			out = append(out, post)
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// Binding merge (plan D2: site = binding whitelist)
// ---------------------------------------------------------------------------

// boundFact is the display metadata of one bound hit asset: the binding plus
// the content facts the list projection needs from the main data.
type boundFact struct {
	Binding           Binding
	ContentKind       string
	UpdatedAt         time.Time
	CoverAttachmentID string
}

// loadBoundFacts fetches the binding catalog slice for the given assets in
// one query. Assets without a binding are absent from the result — the merge
// drops them.
func (r *PublicReader) loadBoundFacts(ctx context.Context, item Site, assetIDs []string) (map[string]boundFact, error) {
	result := make(map[string]boundFact, len(assetIDs))
	if len(assetIDs) == 0 {
		return result, nil
	}
	rows, err := r.Store.Pool.Query(ctx, `
		SELECT `+bindingColumns+`, rm.content_kind, a.updated_at,
		       COALESCE(cover.id::text, '')
		FROM site.site_content_bindings b
		JOIN asset.assets a
		  ON a.organization_id = b.organization_id AND a.id = b.asset_id
		JOIN model.resource_models rm
		  ON rm.organization_id = a.organization_id AND rm.id = a.resource_model_id
		LEFT JOIN asset.asset_version_attachments cav
		  ON cav.organization_id = a.organization_id
		  AND cav.asset_version_id = a.current_published_version_id
		  AND cav.role = 'cover'
		LEFT JOIN asset.attachments cover
		  ON cover.organization_id = cav.organization_id AND cover.id = cav.attachment_id
		  AND cover.deleted_at IS NULL AND cover.media_type LIKE 'image/%'
		WHERE b.organization_id = $1::uuid AND b.site_id = $2::uuid
		  AND b.asset_id = ANY($3::uuid[])
	`, item.OrganizationID, item.ID, assetIDs)
	if err != nil {
		return nil, fmt.Errorf("load bound facts: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var fact boundFact
		if err := rows.Scan(&fact.Binding.ID, &fact.Binding.SiteID, &fact.Binding.AssetID, &fact.Binding.DisplayPath,
			&fact.Binding.ContentType, &fact.Binding.SectionSlug, &fact.Binding.SortOrder, &fact.Binding.OnHomepage,
			&fact.Binding.OnNavigation, &fact.Binding.DisplayConfig, &fact.Binding.DisplayPublishedAt,
			&fact.Binding.CreatedAt, &fact.Binding.UpdatedAt,
			&fact.ContentKind, &fact.UpdatedAt, &fact.CoverAttachmentID); err != nil {
			return nil, fmt.Errorf("scan bound fact: %w", err)
		}
		result[fact.Binding.AssetID] = fact
	}
	return result, rows.Err()
}

// mergeResponse merges one query response page through the binding whitelist.
func (r *PublicReader) mergeResponse(ctx context.Context, item Site, response agentquery.Response) (PublicPostPage, error) {
	assetIDs := make([]string, 0, len(response.Items))
	for _, hit := range response.Items {
		assetIDs = append(assetIDs, hit.AssetID)
	}
	facts, err := r.loadBoundFacts(ctx, item, assetIDs)
	if err != nil {
		return PublicPostPage{}, err
	}
	return PublicPostPage{
		Items:      mergePublicPosts(response.Items, facts),
		NextCursor: response.Page.NextCursor,
		HasMore:    response.Page.HasMore,
	}, nil
}

// mergePublicPosts is the pure core of the D2 merge: unbound hits are dropped
// (the site is a binding whitelist) and a binding whose asset the query did
// not return (e.g. just archived — filtered by the published pointer) never
// renders either, because the merge iterates hits only. Output ordering:
// binding sort_order ASC, then query score DESC (fulltext/hybrid hits), then
// published_at DESC with the asset id as the deterministic tie-break.
func mergePublicPosts(hits []agentquery.Item, facts map[string]boundFact) []PublicPost {
	type merged struct {
		post      PublicPost
		order     int
		score     float64
		hasScore  bool
		published time.Time
		assetID   string
	}
	rows := make([]merged, 0, len(hits))
	for _, hit := range hits {
		fact, bound := facts[hit.AssetID]
		if !bound {
			continue
		}
		row := merged{
			post: PublicPost{
				AssetID:           hit.AssetID,
				DisplayPath:       fact.Binding.DisplayPath,
				Title:             hit.Title,
				Summary:           SafeSummary(hit.Summary, publicSummaryRunes),
				ContentKind:       fact.ContentKind,
				Tags:              hit.Tags,
				UpdatedAt:         timePtr(fact.UpdatedAt),
				PublishedAt:       ResolveDisplayPublishedAt(fact.Binding.DisplayPublishedAt, hit.PublishedAt),
				CoverAttachmentID: fact.CoverAttachmentID,
			},
			order:     fact.Binding.SortOrder,
			hasScore:  hit.Score != nil,
			published: publishedOrZero(hit.PublishedAt),
			assetID:   hit.AssetID,
		}
		if hit.Score != nil {
			row.score = *hit.Score
		}
		if row.post.Tags == nil {
			row.post.Tags = []agentquery.TagSummary{}
		}
		rows = append(rows, row)
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].order != rows[j].order {
			return rows[i].order < rows[j].order
		}
		if rows[i].hasScore != rows[j].hasScore {
			return rows[i].hasScore // scored hits (search) lead unscored ones
		}
		if rows[i].hasScore && rows[i].score != rows[j].score {
			return rows[i].score > rows[j].score
		}
		if !rows[i].published.Equal(rows[j].published) {
			return rows[i].published.After(rows[j].published)
		}
		return rows[i].assetID < rows[j].assetID
	})
	items := make([]PublicPost, 0, len(rows))
	for index := range rows {
		items = append(items, rows[index].post)
	}
	return items
}

// ---------------------------------------------------------------------------
// Binding-driven reads (featured / column / section pages)
// ---------------------------------------------------------------------------

// boundVersionRow joins one binding with the live published version main
// data and the asset/model facts the projection needs.
type boundVersionRow struct {
	Binding           Binding
	VersionID         string
	Title             string
	Summary           string
	ContentKind       string
	ModelID           string
	Fields            []byte
	FieldSchema       []byte
	PublicView        []byte
	AssetUpdatedAt    time.Time
	AssetPublishedAt  *time.Time
	CoverAttachmentID string
}

// boundVersionRows enumerates a binding catalog slice with the live version
// main data. Either the homepage flag or the section slug filters the slice;
// both empty returns the whole catalog (capped).
// boundVersionRows enumerates a binding catalog slice. sectionSlug and
// modelKey both narrow the slice (either can be empty); homepageOnly selects
// the homepage-flagged rows. P1-B: modelKey lets one resource model own a
// dedicated homepage/list slot.
func (r *PublicReader) boundVersionRows(ctx context.Context, item Site, sectionSlug, modelKey string, homepageOnly bool, limit int, locale string) ([]boundVersionRow, error) {
	localeClause := item.localePredicate("a", locale)
	if limit <= 0 || limit > publicMaxBindings {
		limit = publicMaxBindings
	}
	clause := ""
	args := []any{item.OrganizationID, item.ID}
	switch {
	case homepageOnly:
		clause = " AND b.on_homepage = $3"
		args = append(args, true)
	case sectionSlug != "":
		clause = " AND b.section_slug = $3"
		args = append(args, sectionSlug)
	}
	if strings.TrimSpace(modelKey) != "" {
		args = append(args, modelKey)
		clause += fmt.Sprintf(" AND rm.model_key = $%d", len(args))
	}
	rows, err := r.Store.Pool.Query(ctx, `
		SELECT `+bindingColumns+`, rm.content_kind, a.resource_model_id::text,
		       a.updated_at, a.published_at,
		       COALESCE(pv.id::text, ''), COALESCE(pv.title, ''), COALESCE(pv.summary, ''),
		       COALESCE(pv.fields, '{}'::jsonb), COALESCE(mv.field_schema, '{}'::jsonb),
		       COALESCE(mv.public_view, '{}'::jsonb),
		       COALESCE(cover.id::text, '')
		FROM site.site_content_bindings b
		JOIN asset.assets a
		  ON a.organization_id = b.organization_id AND a.id = b.asset_id AND a.deleted_at IS NULL
		JOIN model.resource_models rm
		  ON rm.organization_id = a.organization_id AND rm.id = a.resource_model_id
		LEFT JOIN asset.asset_versions pv
		  ON pv.organization_id = a.organization_id AND pv.id = a.current_published_version_id
		LEFT JOIN model.resource_model_versions mv
		  ON mv.organization_id = pv.organization_id AND mv.id = pv.resource_model_version_id
		LEFT JOIN asset.asset_version_attachments cav
		  ON cav.organization_id = pv.organization_id AND cav.asset_version_id = pv.id
		  AND cav.role = 'cover'
		LEFT JOIN asset.attachments cover
		  ON cover.organization_id = cav.organization_id AND cover.id = cav.attachment_id
		  AND cover.deleted_at IS NULL AND cover.media_type LIKE 'image/%'
		WHERE b.organization_id = $1::uuid AND b.site_id = $2::uuid
		  AND `+localeClause+clause+`
		ORDER BY b.sort_order ASC, b.created_at ASC
		LIMIT `+fmt.Sprint(limit)+`
	`, args...)
	if err != nil {
		return nil, fmt.Errorf("load bound versions: %w", err)
	}
	defer rows.Close()
	out := []boundVersionRow{}
	for rows.Next() {
		var row boundVersionRow
		if err := rows.Scan(&row.Binding.ID, &row.Binding.SiteID, &row.Binding.AssetID, &row.Binding.DisplayPath,
			&row.Binding.ContentType, &row.Binding.SectionSlug, &row.Binding.SortOrder, &row.Binding.OnHomepage,
			&row.Binding.OnNavigation, &row.Binding.DisplayConfig, &row.Binding.DisplayPublishedAt,
			&row.Binding.CreatedAt, &row.Binding.UpdatedAt,
			&row.ContentKind, &row.ModelID, &row.AssetUpdatedAt, &row.AssetPublishedAt,
			&row.VersionID, &row.Title, &row.Summary, &row.Fields, &row.FieldSchema,
			&row.PublicView,
			&row.CoverAttachmentID); err != nil {
			return nil, fmt.Errorf("scan bound version row: %w", err)
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

// projectBoundRows re-checks every bound row with the §3.3 predicate and
// projects the survivors; gate failures drop silently (zero-hit semantics,
// plan D5': below-tier content never leaks its existence).
func (r *PublicReader) projectBoundRows(ctx context.Context, item Site, visitor agentquery.VisitorIdentity, rows []boundVersionRow) ([]PublicPost, error) {
	versionIDs := make([]string, 0, len(rows))
	for _, row := range rows {
		if row.VersionID != "" {
			versionIDs = append(versionIDs, row.VersionID)
		}
	}
	tags, err := r.tagSummaries(ctx, item.OrganizationID, versionIDs)
	if err != nil {
		return nil, err
	}
	items := []PublicPost{}
	for _, row := range rows {
		if row.VersionID == "" {
			continue // published pointer gone: the binding renders nothing
		}
		authorized, err := agentquery.AuthorizePublicSiteAsset(ctx, r.Store, siteRef(item), visitor, row.Binding.AssetID, row.VersionID)
		if err != nil {
			return nil, err
		}
		if !authorized {
			continue
		}
		summary := tags[row.VersionID]
		if summary == nil {
			summary = []agentquery.TagSummary{}
		}
		var cardFields []PublicFieldValue
		if len(row.Fields) > 0 {
			var raw map[string]json.RawMessage
			if err := json.Unmarshal(row.Fields, &raw); err == nil && raw != nil {
				schemaTypes := ParseFieldSchema(row.FieldSchema)
				cardFields = WhitelistFieldsWithLabels(ProjectFields(raw, schemaTypes), schemaTypes,
					ParseFieldLabels(row.FieldSchema), resourcemodel.DecodePublicView(row.PublicView), true)
			}
		}
		items = append(items, PublicPost{
			AssetID:           row.Binding.AssetID,
			DisplayPath:       row.Binding.DisplayPath,
			Title:             row.Title,
			Summary:           SafeSummary(row.Summary, publicSummaryRunes),
			ContentKind:       row.ContentKind,
			Fields:            cardFields,
			Tags:              summary,
			UpdatedAt:         timePtr(row.AssetUpdatedAt),
			PublishedAt:       ResolveDisplayPublishedAt(row.Binding.DisplayPublishedAt, row.AssetPublishedAt),
			CoverAttachmentID: row.CoverAttachmentID,
		})
	}
	return items, nil
}

// About serves the site about page: the first content_type='about' binding
// projected through the same detail pipeline (二期 §7.1). No binding answers
// ErrSiteNotFound.
func (r *PublicReader) About(ctx context.Context, visitorAddr string, principal auth.Principal, slug string) (PublicPostContent, error) {
	if err := r.allow(ctx, visitorAddr); err != nil {
		return PublicPostContent{}, err
	}
	item, err := r.loadSite(ctx, slug)
	if err != nil {
		return PublicPostContent{}, err
	}
	var displayPath string
	if err := r.Store.Pool.QueryRow(ctx, `
		SELECT b.display_path
		FROM site.site_content_bindings b
		WHERE b.organization_id = $1::uuid AND b.site_id = $2::uuid
		  AND b.content_type = 'about'
		ORDER BY b.sort_order ASC, b.created_at ASC
		LIMIT 1
	`, item.OrganizationID, item.ID).Scan(&displayPath); err != nil {
		return PublicPostContent{}, ErrSiteNotFound
	}
	return r.Post(ctx, visitorAddr, principal, slug, displayPath, "")
}

// tagSummaries resolves the phase 2 tag summaries of the given versions
// (same shape the query service renders, doc §6.5).
func (r *PublicReader) tagSummaries(ctx context.Context, organizationID string, versionIDs []string) (map[string][]agentquery.TagSummary, error) {
	result := make(map[string][]agentquery.TagSummary, len(versionIDs))
	if len(versionIDs) == 0 {
		return result, nil
	}
	rows, err := r.Store.Pool.Query(ctx, `
		SELECT avt.asset_version_id::text, t.id::text, t.normalized_key, t.display_name, t.slug
		FROM asset.asset_version_tags avt
		JOIN asset.tags t ON t.organization_id = avt.organization_id AND t.id = avt.tag_id
		WHERE avt.organization_id = $1::uuid AND avt.asset_version_id = ANY($2::uuid[])
		ORDER BY t.normalized_key
	`, organizationID, versionIDs)
	if err != nil {
		return nil, fmt.Errorf("load public tag summaries: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var versionID string
		var summary agentquery.TagSummary
		if err := rows.Scan(&versionID, &summary.ID, &summary.Key, &summary.DisplayName, &summary.Slug); err != nil {
			return nil, err
		}
		result[versionID] = append(result[versionID], summary)
	}
	return result, rows.Err()
}

// ---------------------------------------------------------------------------
// Pure projection helpers (unit-tested without a database)
// ---------------------------------------------------------------------------

// timePtr returns a pointer copy of a time.
func timePtr(value time.Time) *time.Time {
	copied := value
	return &copied
}

// publishedOrZero normalizes an optional published timestamp for ordering.
func publishedOrZero(value *time.Time) time.Time {
	if value == nil {
		return time.Time{}
	}
	return *value
}

// DetailETag derives the D4 detail fingerprint: site revision, asset
// revision, served version id and the binding touch time.
func DetailETag(siteRevision, assetRevision int64, versionID string, bindingUpdatedAt time.Time) string {
	return etagHash([]string{
		"detail",
		fmt.Sprint(siteRevision),
		fmt.Sprint(assetRevision),
		versionID,
		fmt.Sprint(bindingUpdatedAt.UnixNano()),
	})
}

// ListETag derives the D4 list/home fingerprint: site revision plus the
// identity fingerprint of the rendered items (path + freshness markers), so
// any republish, rebind or reorder rotates the representation.
func ListETag(siteRevision int64, items []PublicPost) string {
	parts := []string{"list", fmt.Sprint(siteRevision), fmt.Sprint(len(items))}
	for _, item := range items {
		var updated, published int64
		if item.UpdatedAt != nil {
			updated = item.UpdatedAt.UnixNano()
		}
		if item.PublishedAt != nil {
			published = item.PublishedAt.UnixNano()
		}
		parts = append(parts, item.DisplayPath, item.AssetID,
			fmt.Sprint(updated), fmt.Sprint(published))
	}
	return etagHash(parts)
}

// etagHash renders a sha256 fingerprint over null-delimited parts.
func etagHash(parts []string) string {
	hash := sha256.New()
	for _, part := range parts {
		_, _ = hash.Write([]byte(part))
		_, _ = hash.Write([]byte{0})
	}
	return hex.EncodeToString(hash.Sum(nil))
}

// ETagMatches implements the If-None-Match comparison of the public read
// face (RFC 7232 §2.3 weak comparison for GET/HEAD): the header may carry a
// comma-separated validator list, W/ prefixes and quotes; "*" matches any
// current representation. An absent header never matches.
func ETagMatches(ifNoneMatch, etag string) bool {
	header := strings.TrimSpace(ifNoneMatch)
	if header == "" {
		return false
	}
	etag = strings.Trim(strings.TrimSpace(etag), "\"")
	if etag == "" {
		return false
	}
	for _, candidate := range strings.Split(header, ",") {
		candidate = strings.TrimSpace(candidate)
		if candidate == "*" {
			return true
		}
		candidate = strings.TrimPrefix(candidate, "W/")
		if strings.Trim(candidate, "\"") == etag {
			return true
		}
	}
	return false
}

// Homepage section types understood by the renderer (plan §4). Unknown types
// are skipped so the presentation layer can extend the config forward- and
// backward-compatibly.
const (
	HomepageSectionLatest   = "latest"
	HomepageSectionFeatured = "featured"
	HomepageSectionColumn   = "column"
)

// PublicSectionConfig is one parsed homepage section declaration.
// ModelKey optionally narrows the section to one resource model (P1-B content
// collections: each model can own its own homepage slot).
type PublicSectionConfig struct {
	Type        string
	Title       string
	SectionSlug string
	ModelKey    string
	Limit       int
}

// ParseHomepageConfig decodes homepage_config.sections. The config is a
// free-form extension point, so the parse is tolerant: absent or malformed
// documents yield no sections; unknown types are carried through for the
// caller to skip.
func ParseHomepageConfig(raw json.RawMessage) []PublicSectionConfig {
	if len(strings.TrimSpace(string(raw))) == 0 {
		return nil
	}
	var config struct {
		Sections []struct {
			Type        string `json:"type"`
			Title       string `json:"title"`
			SectionSlug string `json:"section_slug"`
			ModelKey    string `json:"model_key"`
			Limit       int    `json:"limit"`
		} `json:"sections"`
	}
	if err := json.Unmarshal(raw, &config); err != nil {
		return nil
	}
	if len(config.Sections) == 0 {
		return nil
	}
	sections := make([]PublicSectionConfig, 0, len(config.Sections))
	for _, section := range config.Sections {
		sections = append(sections, PublicSectionConfig{
			Type:        strings.TrimSpace(section.Type),
			Title:       section.Title,
			SectionSlug: strings.TrimSpace(section.SectionSlug),
			ModelKey:    strings.TrimSpace(section.ModelKey),
			Limit:       section.Limit,
		})
	}
	return sections
}

// SafeSummary implements the D3 list-summary truncation: markdown markers
// are stripped (headings, emphasis, code spans, quotes, list markers, links
// and images collapse to their visible text) and the result is hard-capped
// at maxRunes code points. The output is plain text and never re-parsed.
func SafeSummary(value string, maxRunes int) string {
	stripped := strings.Join(strings.Fields(stripMarkdown(value)), " ")
	if maxRunes <= 0 {
		maxRunes = publicSummaryRunes
	}
	if utf8.RuneCountInString(stripped) <= maxRunes {
		return stripped
	}
	count := 0
	out := make([]rune, 0, maxRunes)
	for _, char := range stripped {
		if count == maxRunes {
			break
		}
		out = append(out, char)
		count++
	}
	return strings.TrimRight(string(out), " ") + "…"
}

// stripMarkdown removes the common block and inline markdown markers. This
// is a display aid, not a parser: anything it misses stays safe because the
// summary is JSON-escaped by the encoder and never rendered as HTML here.
func stripMarkdown(value string) string {
	if value == "" {
		return ""
	}
	var builder strings.Builder
	for index, line := range strings.Split(value, "\n") {
		if index > 0 {
			builder.WriteByte(' ')
		}
		trimmed := strings.TrimSpace(line)
		trimmed = strings.TrimLeft(trimmed, "#>")                     // headings and quotes
		trimmed = strings.TrimLeft(strings.TrimSpace(trimmed), "-*+") // list markers
		builder.WriteString(stripInlineMarkdown(strings.TrimSpace(trimmed)))
	}
	return builder.String()
}

// stripInlineMarkdown removes emphasis and code markers and collapses links
// and images to their visible text ([text](url) → text, ![alt](url) → alt).
func stripInlineMarkdown(value string) string {
	var out strings.Builder
	runes := []rune(value)
	for index := 0; index < len(runes); index++ {
		char := runes[index]
		switch {
		case strings.ContainsRune("*_`~", char):
			// Emphasis or code marker: drop.
		case char == '!' && index+1 < len(runes) && runes[index+1] == '[':
			// Image marker: drop, the alt text follows in brackets.
		case char == '[':
			// Bracket opens visible text: keep the text, drop the bracket.
		case char == ']':
			// Bracket closes visible text: skip a following (target).
			if index+1 < len(runes) && runes[index+1] == '(' {
				depth := 0
				end := len(runes) - 1
				for cursor := index + 1; cursor < len(runes); cursor++ {
					switch runes[cursor] {
					case '(':
						depth++
					case ')':
						depth--
						if depth == 0 {
							end = cursor
							cursor = len(runes)
						}
					}
				}
				index = end
			}
		default:
			out.WriteRune(char)
		}
	}
	return out.String()
}

// ParseFieldSchema decodes the {"fields":[{"key","type"...}]} model schema
// document into the projection whitelist (key → type). A malformed document
// yields an empty whitelist, which projects nothing (fail closed, §3.4).
func ParseFieldSchema(raw json.RawMessage) map[string]string {
	out := map[string]string{}
	if len(raw) == 0 {
		return out
	}
	var schema struct {
		Fields []struct {
			Key  string `json:"key"`
			Type string `json:"type"`
		} `json:"fields"`
	}
	if err := json.Unmarshal(raw, &schema); err != nil {
		return out
	}
	for _, field := range schema.Fields {
		if field.Key == "" {
			continue
		}
		out[field.Key] = field.Type
	}
	return out
}

// ParseFieldLabels decodes the model schema document into key → label
// (D17). Labels are optional; absent keys yield no entry and the render
// layer falls back to the raw key.
func ParseFieldLabels(raw json.RawMessage) map[string]string {
	out := map[string]string{}
	if len(raw) == 0 {
		return out
	}
	var schema struct {
		Fields []struct {
			Key   string `json:"key"`
			Label string `json:"label"`
		} `json:"fields"`
	}
	if err := json.Unmarshal(raw, &schema); err != nil {
		return out
	}
	for _, field := range schema.Fields {
		if field.Key == "" || field.Label == "" {
			continue
		}
		out[field.Key] = field.Label
	}
	return out
}

// ProjectFields keeps only the schema-declared keys of the version fields;
// values travel as raw JSON so numbers, booleans and nested shapes survive
// verbatim. Every key the schema does not declare — pipeline-internal or
// operator-added — is stripped (plan §3.4: no unauthorized fields).
func ProjectFields(fields map[string]json.RawMessage, schema map[string]string) map[string]json.RawMessage {
	projected := make(map[string]json.RawMessage, len(schema))
	for key := range schema {
		if value, ok := fields[key]; ok && json.Valid(value) {
			projected[key] = value
		}
	}
	return projected
}

// PublicAssetRef is the render-time resolution of one asset reference (D13):
// the public target exists only when the referenced asset passes the
// inclusion derivation for this site.
type PublicAssetRef struct {
	Title string
	Slug  string
}

// AssetRefLookup resolves chunzhi-asset reference targets against the
// inclusion derivation of one site. Only same-site public targets resolve;
// everything else is absent from the map and the render layer strips the
// token (fail-closed).
func (r *PublicReader) AssetRefLookup(ctx context.Context, visitorAddr string, principal auth.Principal, siteSlug string, ids []string) map[string]PublicAssetRef {
	out := map[string]PublicAssetRef{}
	if len(ids) == 0 || r.Store == nil || r.Store.Pool == nil {
		return out
	}
	if err := r.allow(ctx, visitorAddr); err != nil {
		return out
	}
	item, err := r.loadSite(ctx, siteSlug)
	if err != nil {
		return out
	}
	rows, err := r.Store.Pool.Query(ctx, `
		SELECT sl.asset_id::text, COALESCE(pv.title, ''), sl.slug
		FROM site.site_slugs sl
		JOIN asset.assets a
		  ON a.organization_id = sl.organization_id AND a.id = sl.asset_id
		 AND a.deleted_at IS NULL AND a.current_published_version_id IS NOT NULL
		LEFT JOIN asset.asset_versions pv
		  ON pv.organization_id = a.organization_id AND pv.id = a.current_published_version_id
		WHERE sl.organization_id = $1::uuid AND sl.site_id = $2::uuid AND sl.is_current
		  AND sl.asset_id::text = ANY($3::text[])
	`, item.OrganizationID, item.ID, ids)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var id, title, slug string
		if err := rows.Scan(&id, &title, &slug); err == nil && title != "" {
			out[id] = PublicAssetRef{Title: title, Slug: slug}
		}
	}
	return out
}

// SameCategoryPosts resolves the newest included posts sharing the current
// asset's category (站点方案 C8)：related 的第二层——把权重导向同主题聚类。
// 资产无分类挂载时返回空，调用方降级到同标签层。
func (r *PublicReader) SameCategoryPosts(ctx context.Context, visitorAddr string, principal auth.Principal, siteSlug, assetID string, limit int, locale string) ([]PublicPost, error) {
	if err := r.allow(ctx, visitorAddr); err != nil {
		return nil, err
	}
	item, err := r.loadSite(ctx, siteSlug)
	if err != nil {
		return nil, err
	}
	rows, err := r.Store.Pool.Query(ctx, `
		SELECT sl.slug, COALESCE(pv.title, ''), COALESCE(pv.summary, ''),
		       COALESCE(a.updated_at, now()), a.published_at
		FROM site.site_slugs sl
		JOIN asset.assets a
		  ON a.organization_id = sl.organization_id AND a.id = sl.asset_id
		 AND a.deleted_at IS NULL AND a.current_published_version_id IS NOT NULL
		 AND a.id <> $3::uuid
		LEFT JOIN asset.asset_versions pv
		  ON pv.organization_id = a.organization_id AND pv.id = a.current_published_version_id
		WHERE sl.organization_id = $1::uuid AND sl.site_id = $2::uuid AND sl.is_current
		  AND `+item.localePredicate("a", locale)+`
		  AND a.category_container_id IS NOT NULL
		  AND a.category_container_id IN (
		      SELECT c2.id FROM asset.assets cur
		      JOIN content.containers c ON c.id = cur.category_container_id
		      JOIN content.containers c2
		        ON c2.id = c.id OR c2.parent_id = c.id
		      WHERE cur.id = $3::uuid
		  )
		ORDER BY a.published_at DESC
		LIMIT $4::int
	`, item.OrganizationID, item.ID, assetID, limit)
	if err != nil {
		return nil, fmt.Errorf("load same-category posts: %w", err)
	}
	defer rows.Close()
	out := []PublicPost{}
	for rows.Next() {
		var slug, title, summary string
		var updated time.Time
		var published *time.Time
		if err := rows.Scan(&slug, &title, &summary, &updated, &published); err != nil {
			continue
		}
		out = append(out, PublicPost{
			DisplayPath: slug,
			Title:       title,
			Summary:     SafeSummary(summary, 120),
			UpdatedAt:   &updated,
			PublishedAt: published,
		})
	}
	return out, rows.Err()
}

// resolveBlocks expands the pages_config v2 home blocks against live content
// (C1)：latest/ranked/featured/category 走真实查询，text/hero/links 直接
// 投影。未知类型跳过；同时产出导航 v2 的自动枚举。
func (r *PublicReader) CustomPage(ctx context.Context, visitorAddr string, principal auth.Principal, slug, pageSlug string) (PublicCustomPage, error) {
	if err := r.allow(ctx, visitorAddr); err != nil {
		return PublicCustomPage{}, err
	}
	item, err := r.loadSite(ctx, slug)
	if err != nil {
		return PublicCustomPage{}, err
	}
	pageSlug = strings.Trim(pageSlug, "/")
	var title, bodyMD, seo string
	err = r.Store.Pool.QueryRow(ctx, `
		SELECT title, body_markdown, seo_description
		FROM site.site_pages
		WHERE organization_id = $1::uuid AND site_id = $2::uuid AND slug = $3
	`, item.OrganizationID, item.ID, pageSlug).Scan(&title, &bodyMD, &seo)
	if err != nil {
		return PublicCustomPage{}, ErrSiteNotFound
	}
	// 净化管线在 delivery 层（RenderSiteMarkdown），此处返回原文。
	return PublicCustomPage{Site: item, Title: title, Markdown: bodyMD, Description: seo}, nil
}

// PublicCustomPage is the /p/{pageSlug} payload.
type PublicCustomPage struct {
	Site        Site
	Title       string
	Markdown    string
	Description string
}

// localePredicate 内联语言过滤谓词（D11/E）。locale 已由路由按
// ^[a-z]{2}$ 校验后才进入本层，字面量内联注入安全。回退开关来自站点行。
func (item Site) localePredicate(alias, locale string) string {
	if locale == "" || locale == item.DefaultLocale {
		return "TRUE"
	}
	fallback := "TRUE"
	if !item.FallbackToDefault {
		fallback = "FALSE"
	}
	return fmt.Sprintf(
		"(%[1]s.locale IS NULL OR %[1]s.locale = '%[2]s' OR (%[3]s AND %[1]s.locale = '%[4]s'))",
		alias, locale, fallback, item.DefaultLocale,
	)
}

// localeMatchesDetail 判定详情页可见性：资产语言 NULL（语言无关）或等于
// 访问语言才可渲染；详情不回退（D11：半截外语体验差）。
func (item Site) localeMatchesDetail(assetLocale, locale string) bool {
	if assetLocale == "" || locale == "" {
		return true
	}
	return assetLocale == locale
}
