package delivery

// viewmodel.go — the template-facing view models of the HTML face (design
// doc §5.1: templates receive ViewModel structs only, never raw asset DTOs).
// Every field is plain string/[]string/time.Time; the resolver projects the
// site.PublicReader DTOs through this whitelist.

import (
	"encoding/json"
	"fmt"
	"strings"

	"time"

	"agentchunzhi/internal/query"
	"agentchunzhi/internal/site"
	"agentchunzhi/internal/tag"

	"html/template"
)

// NavItem is one navigation link parsed from navigation_config.
type NavItem struct {
	Label string
	Href  string
}

// Chrome is the per-site page furniture every template receives.
type Chrome struct {
	Slug        string
	Name        string
	SiteLang    string
	// Languages 是访客端语言切换器（D11/E）：启用语言 >1 时非空。
	Languages []NavItem
	ScopePublic bool
	// 品牌媒体：站点 Logo、Favicon 与社交分享图的公开媒体地址（空 = 未配置）。
	LogoURL        string
	FaviconURL     string
	SocialImageURL string
	Nav            []NavItem
	HomeHref       string
	PostsHref      string
	TagsHref       string
	SearchHref     string
	RSSHref        string
	// Style carries the resolved style document.
	Style site.StyleConfig
	// StyleCSSVars is the generated CSS custom-properties block plus the
	// static base stylesheet (inline, no external requests).
	StyleCSSVars template.CSS
	// ModeAttribute is the data-mode value ("" = auto).
	ModeAttribute string
	// LayoutClasses are the root body classes.
	LayoutClasses string
}

// Page is the shared page skeleton the layout consumes.
type Page struct {
	Site        Chrome
	Title       string
	Description string
	Canonical   string
	// CanonicalImage is the absolute cover URL feeding og:image / twitter
	// card summary_large_image; empty when the post has no cover.
	// CanonicalImageAlt is the cover alt (G6), falling back to the title.
	CanonicalImage    string
	CanonicalImageAlt string
	NoIndex           bool
	// ModifiedISO feeds og/article:modified_time on detail pages.
	ModifiedISO string
	// JSONLD carries pre-marshaled structured data (json.Marshal escapes
	// < > & so the script context cannot be broken out of).
	JSONLD template.JS
	// Kind names the page template (content block).
	Kind string
}


// FormatDate renders one timestamp for display.
func FormatDate(value *time.Time) string {
	if value == nil || value.IsZero() {
		return ""
	}
	return value.UTC().Format("2006-01-02")
}

// FormatISO renders one timestamp for machine consumption.
func FormatISO(value *time.Time) string {
	if value == nil || value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339)
}

// TagChip is one tag link of a card or a tag cloud.
type TagChip struct {
	Key         string
	DisplayName string
	Href        string
	Count       int64
}

// CardVM is one post card (list, home sections, search results).
type CardVM struct {
	Fields      []FieldValueVM
	Title       string
	Href        string
	Summary     string
	PublishedOn string
	UpdatedOn   string
	Tags        []TagChip
	// CoverURL is the same-origin media route of the version cover (二期 §6).
	CoverURL string
}

// PaginationVM is the cursor pagination footer.
type PaginationVM struct {
	NextHref string
}

// HomeVM renders the homepage: optional hero block plus ordered sections.
type HomeVM struct {
	Page
	HeroTitle   string
	HeroSummary string
	Sections    []SectionVM
	// Blocks 是 pages_config v2 的模块（C1）：非空时优先于 Sections 渲染。
	Blocks       []BlockVM
	TagCloud     []TagChip
	ShowTagCloud bool
}

// BlockVM is one resolved pages_config v2 module (C1/D6/D10).
type BlockVM struct {
	Type       string
	Title      string
	Subtitle   string
	BodyHTML   string
	Href       string
	Layout     string
	StyleClass string
	Items      []CardVM
	Links      []NavItem
	Categories []CategoryLinkVM
}

// CategoryLinkVM is one public category entry of the categories block.
type CategoryLinkVM struct {
	Name  string
	Href  string
	Count int
}

// SectionVM is one homepage section (featured / latest / column).
type SectionVM struct {
	Type  string // featured | latest | column
	Title string
	// ModelKey is set when the section is narrowed to one resource model
	// (P1-B); templates may use it for a data attribute.
	ModelKey string
	Items    []CardVM
}

// ListVM renders the post list and section pages.
type ListVM struct {
	Page
	Heading    string
	Items      []CardVM
	Pagination PaginationVM
}

// DetailVM renders one post detail page.
// FieldValueVM is one whitelisted structured field rendered as a
// label/value row (presentation text already formatted per type).
type FieldValueVM struct {
	Key   string
	Label string
	Type  string
	Value string
}

type DetailVM struct {
	Page
	AssetID     string
	Section     string
	SectionHref string
	ContentHTML string
	TOC         []Heading
	Fields      []FieldValueVM
	PublishedOn string
	UpdatedISO  string
	Tags        []TagChip
	CoverURL    string
	// CoverAlt is the cover's versioned alt text (G6); empty falls back to
	// the title at render time.
	CoverAlt string
	// Comments (二期 §8): enabled by the site mode, listed newest-last,
	// writable by members (the form posts through the JS-free fallback: the
	// console owns the rich UX; the page renders the plain form).
	// 附件下载列表：已发布版本上的文件附件（非内嵌图片）。
	Attachments []AttachmentVM
	// 同站点内按发布顺序的上一篇/下一篇。
	Prev            *NeighborLink
	Next            *NeighborLink
	CommentsEnabled bool
	Comments        []CommentVM
	CanComment      bool
	Moderation      string
	PostPath        string
	// Related is the related-posts block (G7): fulltext recall over the
	// unified query service with same-tag / latest fallbacks.
	Related []CardVM
	// D16 更新记录：版本号元信息行 + 折叠版本轨迹（≥2 个已发布版本才渲染）。
	VersionNo    int
	Publications []PublicationVM
}

// CommentVM is one rendered comment (plain text, escaped by the template).
type CommentVM struct {
	Author  string
	Body    string
	Created string
}

// ArchiveYearVM groups the archive listing (二期 §7.2).
type ArchiveYearVM struct {
	Year   string
	Months []ArchiveMonthVM
}

// ArchiveMonthVM lists one month's entries.
type ArchiveMonthVM struct {
	Month string
	Label string
	Items []CardVM
}

// TagsVM renders the tag index.
type TagsVM struct {
	Page
	Tags []TagChip
}

// TagPageVM renders one tag archive.
type TagPageVM struct {
	Page
	TagKey     string
	TagName    string
	Items      []CardVM
	Pagination PaginationVM
}

// SearchVM renders the search shell (results arrive via the JS island).
type SearchVM struct {
	Page
	Query string
}

// GateVM renders the member login gate of organization/workspace-scope sites
// for anonymous visitors (design doc §4.1).
type GateVM struct {
	Page
}

// ErrorVM renders one error page.
type ErrorVM struct {
	Page
	Status int
}

// RSSItem is one RSS entry.
type RSSItem struct {
	Title       string
	Href        string
	Summary     string
	PublishedOn string
}

// RSSVM renders rss.xml.
type RSSVM struct {
	Site        Chrome
	SelfURL     string
	Items       []RSSItem
	LastBuildOn string
}

// SitemapURL is one sitemap entry.
type SitemapURL struct {
	Loc       string
	LastmodOn string
}

// SitemapVM renders sitemap.xml.
type SitemapVM struct {
	Site Chrome
	URLs []SitemapURL
}

// ---------------------------------------------------------------------------
// Resolver: PublicReader DTOs → view models
// ---------------------------------------------------------------------------

func postHref(slug, displayPath string) string {
	return "/sites/" + slug + "/posts/" + displayPath
}

func sectionHref(slug, section string) string {
	return "/sites/" + slug + "/sections/" + section
}

func tagHref(slug, key string) string {
	return "/sites/" + slug + "/tags/" + key
}

func tagChips(slug string, summaries []query.TagSummary) []TagChip {
	chips := make([]TagChip, 0, len(summaries))
	for _, summary := range summaries {
		display := summary.DisplayName
		if display == "" {
			display = summary.Key
		}
		chips = append(chips, TagChip{Key: summary.Key, DisplayName: display, Href: tagHref(slug, summary.Key)})
	}
	return chips
}

func cardVM(slug string, post site.PublicPost, summaryRunes int) CardVM {
	summary := post.Summary
	if summaryRunes > 0 {
		summary = site.SafeSummary(post.Summary, summaryRunes)
	}
	card := CardVM{
		Title:       post.Title,
		Href:        postHref(slug, post.DisplayPath),
		Summary:     summary,
		Fields:      FormatFieldValues(post.Fields),
		PublishedOn: FormatDate(post.PublishedAt),
		UpdatedOn:   FormatDate(post.UpdatedAt),
		Tags:        tagChips(slug, post.Tags),
	}
	if post.CoverAttachmentID != "" {
		card.CoverURL = "/sites/" + slug + "/media/" + post.CoverAttachmentID
	}
	return card
}

// ResolveHome projects the reader home view into the home VM, honoring the
// home component order of the style IA (featured → latest → tag_cloud).
func ResolveHome(view site.PublicHomeView, style site.StyleConfig, tags []tag.FacetItem) HomeVM {
	vm := HomeVM{Page: Page{Kind: "home"}}
	for _, block := range view.Blocks {
		bvm := BlockVM{
			Type:       block.Type,
			Title:      block.Title,
			Subtitle:   block.Subtitle,
			Href:       block.Href,
			Layout:     block.Layout,
			StyleClass: blockStyleClass(block),
		}
		for _, item := range block.Items {
			bvm.Items = append(bvm.Items, cardVM(view.Site.Slug, item, style.SummaryLength))
		}
		for _, link := range block.Links {
			bvm.Links = append(bvm.Links, NavItem{Label: link.Label, Href: link.Href})
		}
		for _, cat := range block.Categories {
			bvm.Categories = append(bvm.Categories, CategoryLinkVM{Name: cat.Name, Href: cat.Href, Count: cat.Count})
		}
		vm.Blocks = append(vm.Blocks, bvm)
	}
	// P1-B: a section may carry model_key (one resource model owns a slot).
	// Model-scoped latest/featured slots must NOT merge into the generic
	// "最新/精选" buckets — each becomes its own titled section. Only
	// model-less sections keep the legacy merge-by-component behaviour.
	columnOrder := []string{}
	columnsBySlug := map[string]SectionVM{}
	namedOrder := []string{}
	namedByKey := map[string]SectionVM{}
	var featured, latest []CardVM

	for _, section := range view.Sections {
		items := make([]CardVM, 0, len(section.Items))
		for _, post := range section.Items {
			items = append(items, cardVM(view.Site.Slug, post, style.SummaryLength))
		}
		modelScoped := strings.TrimSpace(section.ModelKey) != ""
		switch {
		case section.Type == site.HomepageSectionColumn:
			title := section.Title
			if title == "" {
				title = section.SectionSlug
			}
			key := section.SectionSlug + ":" + section.ModelKey
			existing, ok := columnsBySlug[key]
			if !ok {
				columnOrder = append(columnOrder, key)
				existing = SectionVM{Type: "column", Title: title, ModelKey: section.ModelKey}
			}
			existing.Items = append(existing.Items, items...)
			columnsBySlug[key] = existing
		case modelScoped:
			// Model-scoped latest/featured: standalone titled section.
			title := section.Title
			if title == "" {
				title = section.ModelKey
			}
			key := section.Type + ":" + section.ModelKey
			existing, ok := namedByKey[key]
			if !ok {
				namedOrder = append(namedOrder, key)
				existing = SectionVM{Type: section.Type, Title: title, ModelKey: section.ModelKey}
			}
			existing.Items = append(existing.Items, items...)
			namedByKey[key] = existing
		case section.Type == site.HomepageSectionFeatured:
			featured = append(featured, items...)
		case section.Type == site.HomepageSectionLatest:
			latest = append(latest, items...)
		}
	}
	componentSet := map[string]bool{}
	vm.Sections = []SectionVM{}
	for _, component := range style.HomeComponents {
		componentSet[component] = true
		switch component {
		case "featured":
			if len(featured) > 0 {
				vm.Sections = append(vm.Sections, SectionVM{Type: "featured", Title: "精选", Items: featured})
			}
		case "latest":
			if len(latest) > 0 {
				vm.Sections = append(vm.Sections, SectionVM{Type: "latest", Title: "最新", Items: latest})
			}
		}
	}
	for _, slug := range columnOrder {
		if column := columnsBySlug[slug]; len(column.Items) > 0 {
			vm.Sections = append(vm.Sections, column)
		}
	}
	for _, key := range namedOrder {
		if named := namedByKey[key]; len(named.Items) > 0 {
			vm.Sections = append(vm.Sections, named)
		}
	}
	if tags != nil {
		vm.TagCloud = facetChips(view.Site.Slug, tags)
	}
	vm.ShowTagCloud = componentSet["tag_cloud"]
	if hero := firstCard(vm.Sections); hero != nil && style.HomeStyle == "hero" {
		vm.HeroTitle = hero.Title
		vm.HeroSummary = hero.Summary
	}
	return vm
}

func firstCard(sections []SectionVM) *CardVM {
	for index := range sections {
		if len(sections[index].Items) > 0 {
			return &sections[index].Items[0]
		}
	}
	return nil
}

func facetChips(slug string, items []tag.FacetItem) []TagChip {
	chips := make([]TagChip, 0, len(items))
	for _, item := range items {
		display := item.Tag.DisplayName
		if display == "" {
			display = item.Tag.Key
		}
		chips = append(chips, TagChip{Key: item.Tag.Key, DisplayName: display, Href: tagHref(slug, item.Tag.Key), Count: item.AssetCount})
	}
	return chips
}

// ResolveList projects one post page into the list VM.
func ResolveList(slug, heading, basePath string, page site.PublicPostPage, style site.StyleConfig, nextCursor string) ListVM {
	vm := ListVM{Page: Page{Kind: "list"}, Heading: heading}
	vm.Items = make([]CardVM, 0, len(page.Items))
	for _, post := range page.Items {
		vm.Items = append(vm.Items, cardVM(slug, post, style.SummaryLength))
	}
	if page.HasMore && nextCursor != "" {
		vm.Pagination.NextHref = basePath + "?cursor=" + nextCursor
	}
	return vm
}

// ResolveDetail projects the detail DTO into the detail VM with the
// sanitized markdown body and the extracted TOC. Only image references in
// the authorized set leave the database as same-origin media; every other
// image is stripped whole.
// PublicationVM is one entry of the detail update-history block (D16)。
type PublicationVM struct {
	VersionNo   int
	PublishedOn string
	ChangeNote  string
	AILabel     string
}

// publicationVMs projects the publication history, newest first. The
// provenance label renders only for AI-assisted origins (human stays
// unlabelled); ≥2 published entries required for the block to render
// is enforced in the template via len.
func publicationVMs(content site.PublicPostContent) []PublicationVM {
	items := make([]PublicationVM, 0, len(content.Publications))
	for _, p := range content.Publications {
		vm := PublicationVM{
			VersionNo:   p.VersionNo,
			PublishedOn: FormatDate(&p.PublishedAt),
			ChangeNote:  p.ChangeNote,
		}
		switch {
		case p.Origin == "ai_generated" && p.Confirmed:
			vm.AILabel = "AI 起草 · 人工确认"
		case p.Origin == "ai_assisted" && p.Confirmed:
			vm.AILabel = "AI 协助 · 人工确认"
		case p.Origin == "ai_generated" || p.Origin == "ai_assisted":
			vm.AILabel = "AI 参与"
		}
		items = append(items, vm)
	}
	return items
}

// DetailVM ends

func ResolveDetail(slug string, content site.PublicPostContent, authorizedImages map[string]bool) DetailVM {
	return ResolveDetailWithRefs(slug, content, authorizedImages, nil)
}

// ResolveDetailWithRefs is ResolveDetail with chunzhi-asset reference
// resolution (D13): public targets become same-site dofollow links.
func ResolveDetailWithRefs(slug string, content site.PublicPostContent, authorizedImages map[string]bool, refs map[string]AssetRefView) DetailVM {
	markdown := RenderSiteMarkdown(applyAssetRefs(content.Markdown, refs), slug, authorizedImages)
	// Meta description: the editor's summary when present, otherwise a
	// plain-text excerpt of the body, otherwise the title — a detail page
	// without any description is the single most common on-page SEO defect,
	// and field-only records (empty markdown) must still carry one.
	description := strings.TrimSpace(content.Summary)
	if description == "" {
		description = PlainTextExcerpt(content.Markdown, 150)
	}
	if description == "" {
		description = content.Title
	}
	detail := DetailVM{
		Page:         Page{Kind: "detail", Title: content.Title, Description: description},
		AssetID:      content.AssetID,
		Section:      content.Section,
		SectionHref:  sectionHref(slug, content.Section),
		ContentHTML:  markdown.HTML,
		TOC:          markdown.Headings,
		Fields:       FormatFieldValues(content.Fields),
		PublishedOn:  FormatDate(content.PublishedAt),
		UpdatedISO:   FormatISO(content.UpdatedAt),
		Tags:         tagChips(slug, content.Tags),
		PostPath:     postHref(slug, content.DisplayPath),
		VersionNo:    content.VersionNo,
		Publications: publicationVMs(content),
	}
	if content.CoverAttachmentID != "" {
		detail.CoverURL = "/sites/" + slug + "/media/" + content.CoverAttachmentID
		detail.CoverAlt = content.CoverAlt
	}
	return detail
}

// ResolveTags projects the facet cloud into the tag index VM.
func ResolveTags(slug string, items []tag.FacetItem) TagsVM {
	return TagsVM{Page: Page{Kind: "tags"}, Tags: facetChips(slug, items)}
}

// FormatFieldValues renders whitelisted public fields into presentable
// label/value rows, formatted per the field's declared type. All values pass
// through html/template escaping at render time.
func FormatFieldValues(fields []site.PublicFieldValue) []FieldValueVM {
	if len(fields) == 0 {
		return nil
	}
	out := make([]FieldValueVM, 0, len(fields))
	for _, field := range fields {
		out = append(out, FieldValueVM{Key: field.Key, Label: field.Label, Type: field.Type, Value: formatFieldValue(field)})
	}
	return out
}

func formatFieldValue(field site.PublicFieldValue) string {
	switch field.Type {
	case "boolean":
		if string(field.Value) == "true" {
			return "是"
		}
		return "否"
	case "multiselect":
		var items []string
		if err := json.Unmarshal(field.Value, &items); err == nil {
			return strings.Join(items, "、")
		}
		return ""
	case "string", "enum":
		var text string
		if err := json.Unmarshal(field.Value, &text); err == nil {
			return text
		}
		return ""
	case "date", "datetime":
		// Dates travel as JSON strings ("2024-03-08"); render the raw text
		// without the JSON quotes.
		var text string
		if err := json.Unmarshal(field.Value, &text); err == nil {
			return text
		}
		return string(field.Value)
	default: // integer / number travel as bare JSON scalars
		return string(field.Value)
	}
}

type AttachmentVM struct {
	Name      string
	URL       string
	MediaType string
	ByteSize  int64
}

type NeighborLink struct {
	Title string
	Href  string
}

// CategoryVM renders one public category listing page (站点方案 C5/D14).
type CategoryVM struct {
	Page
	Site          Chrome
	Heading       string
	Items         []CardVM
	Crumbs        []CrumbVM
	Subcategories []SubcategoryVM
}

// CrumbVM is one breadcrumb entry (name + public href).
type CrumbVM struct {
	Name string
	Href string
}

// SubcategoryVM is one public child category of the listing page.
type SubcategoryVM struct {
	Name  string
	Href  string
	Count int
}

// buildBreadcrumbItems emits BreadcrumbList itemListElement entries for the
// trail plus the current page.
func buildBreadcrumbItems(current string, crumbs []site.CategoryCrumb) []map[string]any {
	items := []map[string]any{}
	for i, crumb := range crumbs {
		items = append(items, map[string]any{
			"@type":    "ListItem",
			"position": i + 1,
			"name":     crumb.Name,
			"item":     crumb.Href,
		})
	}
	items = append(items, map[string]any{
		"@type":    "ListItem",
		"position": len(crumbs) + 1,
		"name":     "当前页",
		"item":     current,
	})
	return items
}

// blockStyleClass renders the closed style vocabulary as template classes
// (D10 第③层)：与 LayoutClasses 同思路，模块不携带自由 CSS。
func blockStyleClass(block site.PublicBlock) string {
	class := "block--" + block.Type
	if block.Style == nil {
		return class
	}
	s := block.Style
	if s.Width != "" {
		class += " block--width-" + s.Width
	}
	if s.Variant != "" {
		class += " block--variant-" + s.Variant
	}
	if s.Columns > 0 {
		class += fmt.Sprintf(" block--cols-%d", s.Columns)
	}
	if s.Background != "" {
		class += " block--bg-" + s.Background
	}
	return class
}

// CustomPageVM renders one pages_config v2 custom page (C1)。
type CustomPageVM struct {
	Page
	Site    Chrome
	Heading string
	Blocks  []BlockVM
}
