package delivery

// viewmodel.go — the template-facing view models of the HTML face (design
// doc §5.1: templates receive ViewModel structs only, never raw asset DTOs).
// Every field is plain string/[]string/time.Time; the resolver projects the
// site.PublicReader DTOs through this whitelist.

import (
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
	Template    string
	ScopePublic bool
	Nav         []NavItem
	HomeHref    string
	PostsHref   string
	TagsHref    string
	SearchHref  string
	RSSHref     string
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
	HeroTitle    string
	HeroSummary  string
	Sections     []SectionVM
	TagCloud     []TagChip
	ShowTagCloud bool
}

// SectionVM is one homepage section (featured / latest / column).
type SectionVM struct {
	Type  string // featured | latest | column
	Title string
	Items []CardVM
}

// ListVM renders the post list and section pages.
type ListVM struct {
	Page
	Heading    string
	Items      []CardVM
	Pagination PaginationVM
}

// DetailVM renders one post detail page.
type DetailVM struct {
	Page
	AssetID     string
	Section     string
	SectionHref string
	ContentHTML string
	TOC         []Heading
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
	CommentsEnabled bool
	Comments        []CommentVM
	CanComment      bool
	Moderation      string
	PostPath        string
	// Related is the related-posts block (G7): fulltext recall over the
	// unified query service with same-tag / latest fallbacks.
	Related []CardVM
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
	var featured, latest []CardVM
	columnOrder := []string{}
	columnsBySlug := map[string]SectionVM{}
	for _, section := range view.Sections {
		items := make([]CardVM, 0, len(section.Items))
		for _, post := range section.Items {
			items = append(items, cardVM(view.Site.Slug, post, style.SummaryLength))
		}
		switch section.Type {
		case site.HomepageSectionFeatured:
			featured = append(featured, items...)
		case site.HomepageSectionLatest:
			latest = append(latest, items...)
		case site.HomepageSectionColumn:
			title := section.Title
			if title == "" {
				title = section.SectionSlug
			}
			existing, ok := columnsBySlug[section.SectionSlug]
			if !ok {
				columnOrder = append(columnOrder, section.SectionSlug)
				existing = SectionVM{Type: "column", Title: title}
			}
			existing.Items = append(existing.Items, items...)
			columnsBySlug[section.SectionSlug] = existing
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
func ResolveDetail(slug string, content site.PublicPostContent, authorizedImages map[string]bool) DetailVM {
	markdown := RenderSiteMarkdown(content.Markdown, slug, authorizedImages)
	detail := DetailVM{
		Page:        Page{Kind: "detail", Title: content.Title, Description: content.Summary},
		AssetID:     content.AssetID,
		Section:     content.Section,
		SectionHref: sectionHref(slug, content.Section),
		ContentHTML: markdown.HTML,
		TOC:         markdown.Headings,
		PublishedOn: FormatDate(content.PublishedAt),
		UpdatedISO:  FormatISO(content.UpdatedAt),
		Tags:        tagChips(slug, content.Tags),
		PostPath:    postHref(slug, content.DisplayPath),
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
