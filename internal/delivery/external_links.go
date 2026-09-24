package delivery

// external_links.go — 模型公开目录页（外链板块 v2，2026-09-23）。站点级
// directory_config（0047）声明目录实例（slug/模型/分组/排序/TDK/提交），
// 本文件按配置把已发布记录装配为可收录的公开目录页。外链板块是第一个
// 配置实例；知识卡/FAQ 等模型的公开目录 = 加一条配置，零代码。

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"html/template"
	"sort"
	"strings"

	"agentchunzhi/internal/auth"
	"agentchunzhi/internal/site"
	"agentchunzhi/internal/theme"
)

// DirectoryConfigEntry 是站点级目录页配置的一项（site.public_sites.
// directory_config JSON 数组元素）。
type DirectoryConfigEntry struct {
	Slug              string `json:"slug"`
	ModelKey          string `json:"model_key"`
	GroupBy           string `json:"group_by"`
	Sort              string `json:"sort"`
	PageSize          int    `json:"page_size"`
	TDKTitle          string `json:"title"`
	TDKDescription    string `json:"description"`
	TDKH1             string `json:"h1"`
	Intro             string `json:"intro"`
	SubmissionEnabled bool   `json:"submission_enabled"`
	OutlinkNofollow   bool   `json:"outlink_nofollow"`
}

// 分类枚举（与 SEO 执行方案 §2.2 一一对应）；label 为公开展示名。
var directoryCategoryLabels = map[string]string{
	"seo-tools":        "SEO & Keyword Tools",
	"content-creation": "Content Creation Resources",
	"monetization":     "Monetization & SaaS Guides",
	"operations":       "Operations & Compliance",
	"website-building": "Website Building Tools",
}

func DirectoryCategoryValid(category string) bool {
	_, ok := directoryCategoryLabels[category]
	return ok
}

func directoryCategoryKeys() []string {
	keys := make([]string, 0, len(directoryCategoryLabels))
	for key := range directoryCategoryLabels {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func directoryCategoryLabel(category string) string {
	if label, ok := directoryCategoryLabels[category]; ok {
		return label
	}
	return category
}

// directoryConfigFor 取指定 slug 的目录配置；未配置返回 false（页面 404，
// 部署未启用该板块时不暴露任何路由行为）。
func directoryConfigFor(facts site.SiteFacts, slug string) (DirectoryConfigEntry, bool) {
	var entries []DirectoryConfigEntry
	if len(facts.Site.DirectoryConfig) > 0 {
		if err := json.Unmarshal(facts.Site.DirectoryConfig, &entries); err != nil {
			return DirectoryConfigEntry{}, false
		}
	}
	for _, entry := range entries {
		if entry.Slug == slug {
			if entry.PageSize <= 0 || entry.PageSize > 100 {
				entry.PageSize = 20
			}
			return entry, true
		}
	}
	return DirectoryConfigEntry{}, false
}

// ExternalLinkEntry 是目录页的一条外链条目。
type ExternalLinkEntry struct {
	Name        string
	URL         string
	Description string
	TestedOn    string
	Skill       string
	Rel         string // 出站链接属性：默认 dofollow（空），站点级开关切 nofollow
}

// ExternalLinkGroup 是一个分类分组。
type ExternalLinkGroup struct {
	Key   string
	Label string
	Items []ExternalLinkEntry
}

// ExternalCategoryCard 是总目录页的分类导航卡片：图标 + 分类名 + 站点数，
// 点击进入该分类的外链列表页（SEO 方案 §页面1 分类导航区）。
type ExternalCategoryCard struct {
	Key    string
	Label  string
	Icon   string
	Count  int
}

var directoryCategoryIcons = map[string]string{
	"seo-tools":        "🔍",
	"content-creation": "✍️",
	"monetization":     "💰",
	"operations":       "🛡️",
	"website-building": "🧱",
}

// ExternalSubmitVM 是提交表单的装配。
type ExternalSubmitVM struct {
	Enabled    bool
	Action     string
	Categories []string
	Honeypot   string
}

// ExternalLinksVM 渲染总目录页与分类子页（IsCategory 区分）。
type ExternalLinksVM struct {
	Page
	Intro      string
	CategoryCards [] ExternalCategoryCard
	Groups     []ExternalLinkGroup
	Featured   []ExternalLinkEntry
	Submit     ExternalSubmitVM
	Pagination PageNavVM
	// IsCategory = 分类子页模式（单分组 + Community Picks 提交者站点位）。
	IsCategory      bool
	CategoryKey     string
	CategoryLabel   string
	CommunityPicks  []ExternalLinkEntry
	SubmittedThanks bool
}

// GuidelinesVM 渲染提交规则说明页（静态内容在模板内）。
type GuidelinesVM struct {
	Page
}

const externalSubmitAction = "/external-links/submit"
const externalHoneypotField = "website_fill"
const externalFormMarker = "EXT_SUBMIT_FORM_PLACEHOLDER"

// externalFAQPairs 是总目录页 FAQ 区的问答对（与模板渲染文案一致），
// 同时驱动 FAQPage 结构化数据。
var externalFAQPairs = [][2]string{
	{"What is an external link in SEO?", "An external link is a hyperlink that points from your website to a page on a different domain. It cites sources and helps search engines understand your content's context."},
	{"Are external links good for search engine rankings?", "Yes — linking out to relevant, authoritative sources is a positive quality signal, and earning external links from other sites to your pages is one of the strongest ranking factors."},
	{"What's the difference between internal and external links?", "Internal links connect pages on the same domain; external links point to other domains. A healthy site uses both: internal links for structure, external links for citations and trust."},
	{"How do you build external links for a new website?", "Start with curated directories in your niche (like this one — submission is free), publish original tools or data worth citing, and do genuine outreach to sites that cover your topic."},
	{"Do follow vs nofollow external links: which matters more?", "Dofollow links pass ranking signals; nofollow links are hints. A natural profile contains mostly dofollow links from relevant, reviewed sources — exactly what this directory provides."},
}

// externalSubmitFormHTML 构建公开提交表单（服务端可信注入，不过主题扫描）。
func externalSubmitFormHTML(categories []string) string {
	var b strings.Builder
	b.WriteString(`<form method="post" action="` + externalSubmitAction + `" class="ext-form">`)
	b.WriteString(`<input type="text" name="` + externalHoneypotField + `" value="" tabindex="-1" autocomplete="off" aria-hidden="true" class="ext-hp">`)
	b.WriteString(`<label>Site Name<input type="text" name="site_name" required maxlength="120"></label>`)
	b.WriteString(`<label>Site URL<input type="text" name="site_url" required maxlength="500" placeholder="https://example.com"></label>`)
	b.WriteString(`<label>Category<select name="category" required>`)
	for _, key := range categories {
		b.WriteString(`<option value="` + key + `">` + key + `</option>`)
	}
	b.WriteString(`</select></label>`)
	b.WriteString(`<label>Short Description<textarea name="description" required maxlength="400" rows="3" placeholder="What does this site do, in one or two sentences?"></textarea></label>`)
	b.WriteString(`<fieldset><legend>Your Website (optional — get listed in Community Picks too)</legend>`)
	b.WriteString(`<label>Your Site Name<input type="text" name="submitter_site_name" maxlength="120"></label>`)
	b.WriteString(`<label>Your Site URL<input type="text" name="submitter_site_url" maxlength="500" placeholder="https://yoursite.com"></label>`)
	b.WriteString(`</fieldset>`)
	b.WriteString(`<label>Contact Email<input type="email" name="contact_email" required maxlength="200"></label>`)
	b.WriteString(`<label>Agent Skill (optional)<textarea name="agent_skill" maxlength="8000" rows="5" placeholder="SKILL.md style steps an AI agent can follow to submit to this site..."></textarea></label>`)
	b.WriteString(`<button type="submit">Submit Site</button></form>`)
	return b.String()
}

// injectExternalSubmitForm 把渲染后 HTML 里的表单占位替换为提交表单。
func injectExternalSubmitForm(body []byte, categories []string) []byte {
	if !bytes.Contains(body, []byte(externalFormMarker)) {
		return body
	}
	return bytes.ReplaceAll(body, []byte(externalFormMarker),
		[]byte(externalSubmitFormHTML(categories)))
}

// externalLinkEntry 把一条记录转成条目 VM；rel 由站点级 nofollow 开关决定。
func externalLinkEntry(rec site.DirectoryRecord, nofollow bool) ExternalLinkEntry {
	rel := ""
	if nofollow {
		rel = "nofollow ugc"
	}
	return ExternalLinkEntry{
		Name:        rec.Title,
		URL:         rec.URL,
		Description: rec.Description,
		TestedOn:    rec.TestedOn,
		Skill:       rec.AgentSkill,
		Rel:         rel,
	}
}

// groupExternalEntries 按分类枚举顺序分组（只输出有记录的分组）。
func groupExternalEntries(records []site.DirectoryRecord, nofollow bool) []ExternalLinkGroup {
	byCategory := map[string][]site.DirectoryRecord{}
	for _, rec := range records {
		byCategory[rec.Category] = append(byCategory[rec.Category], rec)
	}
	groups := []ExternalLinkGroup{}
	for key := range directoryCategoryLabels {
		recordsInCategory, ok := byCategory[key]
		if !ok || len(recordsInCategory) == 0 {
			continue
		}
		items := make([]ExternalLinkEntry, 0, len(recordsInCategory))
		for _, rec := range recordsInCategory {
			items = append(items, externalLinkEntry(rec, nofollow))
		}
		groups = append(groups, ExternalLinkGroup{Key: key, Label: directoryCategoryLabel(key), Items: items})
	}
	return groups
}

// dedupeSubmitterSites 提交者站点去重：同 URL 只保留最新一条（防刷量），
// 保持服务端排序（featured/更新时间）。
func dedupeSubmitterSites(records []site.DirectoryRecord, nofollow bool) []ExternalLinkEntry {
	seen := map[string]bool{}
	items := []ExternalLinkEntry{}
	for _, rec := range records {
		key := strings.TrimSpace(rec.SubmitterSiteURL)
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		rel := ""
		if nofollow {
			rel = "nofollow ugc"
		}
		items = append(items, ExternalLinkEntry{Name: rec.SubmitterSiteName, URL: key, Rel: rel})
	}
	return items
}

// buildExternalLinks 装配总目录页或分类子页。
func (s *Service) buildExternalLinks(addr, baseURL, category string, page int, submitted bool, principal auth.Principal) buildFunc {
	return func(ctx context.Context, facts site.SiteFacts, band string, queries *theme.Queries) (renderOutput, error) {
		cfg, ok := directoryConfigFor(facts, "external-links")
		if !ok {
			return renderOutput{}, site.ErrSiteNotFound
		}
		if gated(facts, band) {
			return s.gateOutput(facts)
		}
		offset := (page - 1) * cfg.PageSize
		records, hasMore, err := s.Reader.DirectoryRecords(ctx, addr, principal, facts.Site.Slug, cfg.ModelKey, category, offset, cfg.PageSize)
		if err != nil {
			return renderOutput{}, err
		}

		vm := ExternalLinksVM{Page: Page{Kind: "external_links"}, Intro: cfg.Intro}
		vm.Site = chrome(facts, "external_links")
		vm.Queries = queries
		vm.Title = cfg.TDKTitle
		vm.Description = cfg.TDKDescription
		vm.NoIndex = !vm.Site.ScopePublic
		vm.Submit = ExternalSubmitVM{
			Enabled:    cfg.SubmissionEnabled,
			Action:     externalSubmitAction,
			Categories: directoryCategoryKeys(),
			Honeypot:   externalHoneypotField,
		}
		vm.Pagination.Page = page
		_ = baseURL

		// 分类导航区：卡片含站点数，点击进入分类列表页（G4 装配）。
		counts, countErr := s.Reader.DirectoryCategoryCounts(ctx, addr, principal, facts.Site.Slug, cfg.ModelKey)
		if countErr != nil {
			counts = map[string]int{}
		}
		for _, key := range directoryCategoryKeys() {
			if count := counts[key]; count > 0 {
				vm.CategoryCards = append(vm.CategoryCards, ExternalCategoryCard{
					Key: key, Label: directoryCategoryLabel(key),
					Icon: directoryCategoryIcons[key], Count: count,
				})
			}
		}
		vm.Groups = groupExternalEntries(records, cfg.OutlinkNofollow)
		if category != "" {
			vm.IsCategory = true
			vm.CategoryKey = category
			vm.CategoryLabel = directoryCategoryLabel(category)
			vm.Title = directoryCategoryLabel(category) + " External Links · " + facts.Site.Name
			vm.Canonical = baseURL + "/external-links/" + category
			vm.CommunityPicks = dedupeSubmitterSites(records, cfg.OutlinkNofollow)
		} else {
			vm.Canonical = baseURL + "/external-links"
			for _, rec := range records {
				if rec.Featured {
					vm.Featured = append(vm.Featured, externalLinkEntry(rec, cfg.OutlinkNofollow))
				}
			}
			vm.SubmittedThanks = submitted
		}

		if page > 1 {
			prev := "/external-links"
			if category != "" {
				prev += "/" + category
			}
			if page > 2 {
				prev += fmt.Sprintf("?page=%d", page-1)
			}
			vm.Pagination.PrevHref = prev
		}
		if hasMore {
			next := "/external-links"
			if category != "" {
				next += "/" + category
			}
			vm.Pagination.NextHref = next + fmt.Sprintf("?page=%d", page+1)
		}

		// 结构化数据：总目录 CollectionPage + WebSite；分类页 ItemList。
		if !vm.NoIndex {
			var ld []byte
			if category != "" {
				items := make([]map[string]any, 0, len(records))
				for i, rec := range records {
					items = append(items, map[string]any{
						"@type":    "ListItem",
						"position": offset + i + 1,
						"name":     rec.Title,
						"url":      rec.URL,
					})
				}
				ld, _ = json.Marshal([]map[string]any{{
					"@context":      "https://schema.org",
					"@type":         "ItemList",
					"name":          vm.CategoryLabel + " External Links",
					"numberOfItems": len(items),
					"itemListElement": items,
				}})
			} else {
				groupList := make([]map[string]any, 0, len(vm.Groups))
				for _, group := range vm.Groups {
					groupList = append(groupList, map[string]any{
						"@type": "ListItem", "name": group.Label,
						"url": baseURL + "/external-links/" + group.Key,
					})
				}
				faqEntities := make([]map[string]any, 0, len(externalFAQPairs))
				for _, qa := range externalFAQPairs {
					faqEntities = append(faqEntities, map[string]any{
						"@type":          "Question",
						"name":           qa[0],
						"acceptedAnswer": map[string]any{"@type": "Answer", "text": qa[1]},
					})
				}
				ld, _ = json.Marshal([]map[string]any{
					{"@context": "https://schema.org", "@type": "CollectionPage",
						"name":        facts.Site.Name + " External Links",
						"description": vm.Description,
						"inLanguage":  facts.Site.DefaultLocale,
						"hasPart":     groupList},
					{"@context": "https://schema.org", "@type": "WebSite",
						"name": facts.Site.Name, "url": s.homeURLFor(facts.Site.Slug, baseURL)},
					{"@context": "https://schema.org", "@type": "FAQPage",
						"mainEntity": faqEntities},
				})
			}
			vm.JSONLD = template.JS(ld)
		}
		return renderOutput{kind: "external_links", vm: vm, noIndex: vm.NoIndex,
			injectSubmitForm: vm.Submit.Enabled, injectSubmitCats: vm.Submit.Categories}, nil
	}
}

// ExternalLinks 服务总目录页（/external-links）。
func (s *Service) ExternalLinks(ctx context.Context, addr string, principal auth.Principal, baseURL string, page int, submitted bool) (*Response, error) {
	routePath := "/external-links"
	if page > 1 {
		routePath = fmt.Sprintf("/external-links?page=%d", page)
	}
	if submitted {
		separator := "?"
		if page > 1 {
			separator = "&"
		}
		routePath += separator + "submitted=1"
	}
	return s.pipeline(ctx, addr, principal, s.RootSiteSlug, routePath, baseURL,
		s.buildExternalLinks(addr, baseURL, "", page, submitted, principal))
}

// ExternalLinksCategory 服务分类子页（/external-links/{category}）。
func (s *Service) ExternalLinksCategory(ctx context.Context, addr string, principal auth.Principal, baseURL, category string, page int) (*Response, error) {
	routePath := "/external-links/" + category
	if page > 1 {
		routePath = fmt.Sprintf("/external-links/%s?page=%d", category, page)
	}
	return s.pipeline(ctx, addr, principal, s.RootSiteSlug, routePath, baseURL,
		s.buildExternalLinks(addr, baseURL, category, page, false, principal))
}

// SubmissionGuidelines 服务提交规则说明页（静态内容在模板内）。
func (s *Service) SubmissionGuidelines(ctx context.Context, addr string, principal auth.Principal, baseURL string) (*Response, error) {
	routePath := "/external-links/submission-guidelines"
	return s.pipeline(ctx, addr, principal, s.RootSiteSlug, routePath, baseURL, func(ctx context.Context, facts site.SiteFacts, band string, queries *theme.Queries) (renderOutput, error) {
		if _, ok := directoryConfigFor(facts, "external-links"); !ok {
			return renderOutput{}, site.ErrSiteNotFound
		}
		if gated(facts, band) {
			return s.gateOutput(facts)
		}
		vm := GuidelinesVM{Page: Page{Kind: "external_links_guidelines"}}
		vm.Site = chrome(facts, "external_links_guidelines")
		vm.Queries = queries
		vm.Title = "External Link Submission Guidelines | Free Backlink Rules"
		vm.Description = "Complete guidelines for submitting your site to our external links directory. Review criteria, approval process and free backlink rules."
		vm.Canonical = baseURL + "/external-links/submission-guidelines"
		vm.NoIndex = !vm.Site.ScopePublic
		return renderOutput{kind: "external_links_guidelines", vm: vm, noIndex: vm.NoIndex}, nil
	})
}

