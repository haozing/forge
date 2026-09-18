package site

// lint.go — 站点体检（内容治理四件套 F2，对应 llm_wiki 的 Lint 操作）。
// 纯规则引擎：Linter 只消费 lintSnapshot（由 gatherLintSnapshot 采集），
// 规则函数零 DB 依赖可单测。所有发现均为 advisory——体检报告永不阻断发布，
// 与 publish_checklist 的「warnings never block publishing」同一哲学。
//
// 引用协议正则与 delivery/markdown.go 的渲染端保持一致（delivery 依赖 site，
// 此处不能反向导入；渲染端是协议唯一权威，改动必须双侧同步）。

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"agentchunzhi/internal/auth"
	"agentchunzhi/internal/authz"
)

// LintSeverity 分级：error=访客可见的坏体验（死链），warn=SEO/结构缺陷，
// info=改进建议。
type LintSeverity string

const (
	LintError LintSeverity = "error"
	LintWarn  LintSeverity = "warn"
	LintInfo  LintSeverity = "info"
)

// LintFinding 是单条体检发现。ResourceType/ResourceID 定位问题载体
// （site/page/asset/container/redirect）。
type LintFinding struct {
	Check        string       `json:"check"`
	Severity     LintSeverity `json:"severity"`
	ResourceType string       `json:"resource_type"`
	ResourceID   string       `json:"resource_id"`
	Message      string       `json:"message"`
	Hint         string       `json:"hint,omitempty"`
}

// LintReport 是一次站点体检的完整产出；Findings 永不为 nil（nil slice
// 序列化成 null 的教训——前端按数组消费）。
type LintReport struct {
	SiteID   string        `json:"site_id"`
	Findings []LintFinding `json:"findings"`
}

// Counts 返回分级计数（error/warn/info）。
func (r LintReport) Counts() map[string]int {
	counts := map[string]int{"error": 0, "warn": 0, "info": 0}
	for _, finding := range r.Findings {
		counts[string(finding.Severity)]++
	}
	return counts
}

// lintIncluded 是收录面单条内容的体检视角快照。
type lintIncluded struct {
	AssetID            string
	Slug               string
	Title              string
	Markdown           string
	Locale             string
	TranslationGroupID string
	CategoryID         string
	Featured           bool
}

type lintRedirect struct {
	FromPath string
	ToPath   string
}

type lintCategory struct {
	ID       string
	ParentID string
	Slug     string
}

type lintPage struct {
	ID             string
	Slug           string
	SEODescription string
	BodyMarkdown   string
}

// lintSnapshot 汇集体检所需的全部数据，规则函数的唯一输入。
type lintSnapshot struct {
	SiteDescription string
	EnabledLocales  []string
	Included        []lintIncluded
	Redirects       []lintRedirect
	Categories      []lintCategory
	Pages           []lintPage
	// BacklogAssetIDs 是已发布但有更新草稿待发布的收录资产。
	BacklogAssetIDs []string
}

// chunzhi-asset:// 引用协议（与 delivery/markdown.go 渲染端同一形状）。
var lintAssetRefPattern = regexp.MustCompile(
	`chunzhi-asset://([0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12})`)

// chunzhi-media:// 图片协议（引用内链协议的媒体镜像）。
var lintMediaPattern = regexp.MustCompile(
	`!\[([^\]]*)\]\(chunzhi-media://([0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12})\)`)

// lintFromSnapshot 跑全部规则（L1-L8），返回结果保证非 nil。
func lintFromSnapshot(siteDescription string, snap lintSnapshot) []LintFinding {
	findings := []LintFinding{}
	findings = append(findings, lintDeadRefs(snap)...)
	findings = append(findings, lintStaleRedirects(snap)...)
	findings = append(findings, lintOrphans(snap)...)
	findings = append(findings, lintEmptyCategories(snap)...)
	findings = append(findings, lintSEOBaseline(siteDescription, snap.Pages)...)
	findings = append(findings, lintLocaleBreaks(snap)...)
	findings = append(findings, lintPublishBacklog(snap)...)
	findings = append(findings, lintMissingAlt(snap)...)
	return findings
}

// lintDeadRefs（L1, error）：正文 chunzhi-asset:// 指向未收录资产——渲染端
// fail-closed 会把引用静默降级，访客看到的是凭空消失的链接。
func lintDeadRefs(snap lintSnapshot) []LintFinding {
	publicIDs := map[string]bool{}
	for _, item := range snap.Included {
		publicIDs[item.AssetID] = true
	}
	var findings []LintFinding
	for _, item := range snap.Included {
		for _, ref := range lintRefIDs(item.Markdown) {
			if publicIDs[ref] {
				continue
			}
			findings = append(findings, LintFinding{
				Check: "dead_asset_ref", Severity: LintError,
				ResourceType: "asset", ResourceID: item.AssetID,
				Message: fmt.Sprintf("《%s》正文引用的资产 %s 未收录（已删除、被排除或未发布）", item.Title, ref),
				Hint:    "移除该引用，或将目标资产重新收录到本站",
			})
		}
	}
	return findings
}

// lintStaleRedirects（L2, warn）：301 落点既不是当前收录路径也不是另一条
// 重定向（链），访客跟着跳会 404。
func lintStaleRedirects(snap lintSnapshot) []LintFinding {
	live := map[string]bool{}
	for _, item := range snap.Included {
		live[item.Slug] = true
	}
	var findings []LintFinding
	for _, redirect := range snap.Redirects {
		if live[redirect.ToPath] {
			continue
		}
		chained := false
		for _, other := range snap.Redirects {
			if other.FromPath == redirect.ToPath {
				chained = true
				break
			}
		}
		if chained {
			continue
		}
		findings = append(findings, LintFinding{
			Check: "stale_redirect", Severity: LintWarn,
			ResourceType: "redirect", ResourceID: redirect.FromPath,
			Message: fmt.Sprintf("重定向 %s 的落点 %s 已无收录内容", redirect.FromPath, redirect.ToPath),
			Hint:    "更新重定向落点或移除该条 301",
		})
	}
	return findings
}

// lintOrphans（L3, info）：已收录但零入链、未挂分类且非精选——只能靠搜索
// 或列表翻页到达。
func lintOrphans(snap lintSnapshot) []LintFinding {
	inbound := map[string]bool{}
	for _, item := range snap.Included {
		for _, ref := range lintRefIDs(item.Markdown) {
			inbound[ref] = true
		}
	}
	var findings []LintFinding
	for _, item := range snap.Included {
		if item.Featured || item.CategoryID != "" || inbound[item.AssetID] {
			continue
		}
		findings = append(findings, LintFinding{
			Check: "orphan_content", Severity: LintInfo,
			ResourceType: "asset", ResourceID: item.AssetID,
			Message: fmt.Sprintf("《%s》无入链、未挂分类且未精选（孤岛内容）", item.Title),
			Hint:    "从相关内容正文引用它、挂到分类或设为精选",
		})
	}
	return findings
}

// lintEmptyCategories（L4, warn）：公开分类子树下没有任何收录内容——公开
// 分类树出现空罗列页。
func lintEmptyCategories(snap lintSnapshot) []LintFinding {
	byParent := map[string][]lintCategory{}
	for _, category := range snap.Categories {
		byParent[category.ParentID] = append(byParent[category.ParentID], category)
	}
	populated := map[string]bool{}
	var walk func(id string) bool
	walk = func(id string) bool {
		populatedBySelf := false
		for _, item := range snap.Included {
			if item.CategoryID == id {
				populatedBySelf = true
				break
			}
		}
		for _, child := range byParent[id] {
			if walk(child.ID) {
				populatedBySelf = true
			}
		}
		populated[id] = populatedBySelf
		return populatedBySelf
	}
	roots := map[string]bool{}
	for _, category := range snap.Categories {
		roots[category.ParentID] = true
	}
	for _, category := range snap.Categories {
		if !roots[category.ID] {
			continue
		}
		_ = walk(category.ID)
	}
	var findings []LintFinding
	for _, category := range snap.Categories {
		if populated[category.ID] {
			continue
		}
		findings = append(findings, LintFinding{
			Check: "empty_public_category", Severity: LintWarn,
			ResourceType: "container", ResourceID: category.ID,
			Message: fmt.Sprintf("公开分类 %s 子树下没有收录内容", category.Slug),
			Hint:    "向该分类挂载并收录内容，或暂停分类公开",
		})
	}
	return findings
}

// lintSEOBaseline（L5）：站点描述为空（warn）；自定义页缺 seo_description
// （info）或正文含一级标题（warn，与主题 h1 冲突破坏单 h1 基线）。
func lintSEOBaseline(siteDescription string, pages []lintPage) []LintFinding {
	var findings []LintFinding
	if strings.TrimSpace(siteDescription) == "" {
		findings = append(findings, LintFinding{
			Check: "site_description_missing", Severity: LintWarn,
			ResourceType: "site", ResourceID: "",
			Message: "站点描述为空，首页 meta description 只能回退站名",
			Hint:    "在站点配置里填写站点描述",
		})
	}
	for _, page := range pages {
		if strings.TrimSpace(page.SEODescription) == "" {
			findings = append(findings, LintFinding{
				Check: "page_seo_description_missing", Severity: LintInfo,
				ResourceType: "page", ResourceID: page.ID,
				Message: fmt.Sprintf("自定义页 %s 缺少 SEO 描述", page.Slug),
			})
		}
		for _, line := range strings.Split(page.BodyMarkdown, "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "# ") {
				findings = append(findings, LintFinding{
					Check: "page_body_h1", Severity: LintWarn,
					ResourceType: "page", ResourceID: page.ID,
					Message: fmt.Sprintf("自定义页 %s 正文含一级标题，与主题页头 h1 冲突", page.Slug),
					Hint:    "把正文一级标题降为二级",
				})
				break
			}
		}
	}
	return findings
}

// lintLocaleBreaks（L6, warn）：站点启用多语言时，某个翻译组缺启用语言——
// 语言切换器/hreflang 指向落空。语言无关（locale 空）与单语言站点跳过。
func lintLocaleBreaks(snap lintSnapshot) []LintFinding {
	if len(snap.EnabledLocales) <= 1 {
		return nil
	}
	enabled := map[string]bool{}
	for _, locale := range snap.EnabledLocales {
		enabled[strings.ToLower(strings.TrimSpace(locale))] = true
	}
	type groupKey = string
	groups := map[groupKey]map[string]string{} // group -> locale -> title
	for _, item := range snap.Included {
		if item.TranslationGroupID == "" || item.Locale == "" {
			continue
		}
		if groups[item.TranslationGroupID] == nil {
			groups[item.TranslationGroupID] = map[string]string{}
		}
		groups[item.TranslationGroupID][strings.ToLower(item.Locale)] = item.Title
	}
	var findings []LintFinding
	for group, locales := range groups {
		for locale := range enabled {
			if _, ok := locales[locale]; ok {
				continue
			}
			findings = append(findings, LintFinding{
				Check: "locale_group_gap", Severity: LintWarn,
				ResourceType: "asset", ResourceID: group,
				Message: fmt.Sprintf("翻译组 %s 缺少启用语言 %s 的版本", group, locale),
				Hint:    "补充该语言翻译，或把该语言从站点启用语言里移除",
			})
		}
	}
	return findings
}

// lintPublishBacklog（L7, info）：已发布内容存在未发布的更新草稿。
func lintPublishBacklog(snap lintSnapshot) []LintFinding {
	titles := map[string]string{}
	for _, item := range snap.Included {
		titles[item.AssetID] = item.Title
	}
	var findings []LintFinding
	for _, assetID := range snap.BacklogAssetIDs {
		findings = append(findings, LintFinding{
			Check: "publish_backlog", Severity: LintInfo,
			ResourceType: "asset", ResourceID: assetID,
			Message: fmt.Sprintf("《%s》有已确认未发布的更新版本", titles[assetID]),
			Hint:    "到内容清单检查并发布更新",
		})
	}
	return findings
}

// lintMissingAlt（L8, info）：正文 chunzhi-media:// 图片缺 alt 文本。
func lintMissingAlt(snap lintSnapshot) []LintFinding {
	var findings []LintFinding
	for _, item := range snap.Included {
		missing := 0
		for _, match := range lintMediaPattern.FindAllStringSubmatch(item.Markdown, -1) {
			if strings.TrimSpace(match[1]) == "" {
				missing++
			}
		}
		if missing == 0 {
			continue
		}
		findings = append(findings, LintFinding{
			Check: "image_missing_alt", Severity: LintInfo,
			ResourceType: "asset", ResourceID: item.AssetID,
			Message: fmt.Sprintf("《%s》有 %d 张正文图片缺 alt 文本", item.Title, missing),
			Hint:    "补齐图片 alt（无障碍与图片 SEO）",
		})
	}
	return findings
}

// lintRefIDs 提取正文里的引用资产 ID（去重，首现顺序）。
func lintRefIDs(markdown string) []string {
	seen := map[string]bool{}
	ids := []string{}
	for _, match := range lintAssetRefPattern.FindAllStringSubmatch(markdown, -1) {
		if seen[match[1]] {
			continue
		}
		seen[match[1]] = true
		ids = append(ids, match[1])
	}
	return ids
}

// LintSite 采集快照并跑体检（HTTP 与 agent 工具共用入口）。只读、advisory。
func (s *Service) LintSite(ctx context.Context, principal auth.Principal, workspaceID, siteID string) (LintReport, error) {
	if err := s.require(ctx, principal, workspaceID, authz.ActionSiteRead); err != nil {
		return LintReport{}, err
	}
	site, err := s.GetSite(ctx, principal, workspaceID, siteID)
	if err != nil {
		return LintReport{}, err
	}
	snap, err := s.gatherLintSnapshot(ctx, principal.OrganizationID, siteID)
	if err != nil {
		return LintReport{}, err
	}
	report := LintReport{SiteID: siteID, Findings: lintFromSnapshot(site.Description, snap)}
	if report.Findings == nil {
		report.Findings = []LintFinding{}
	}
	return report, nil
}

// gatherLintSnapshot 用少量聚合查询收集体检输入；全站一次面，单次调用
// 控制在个位数往返。
func (s *Service) gatherLintSnapshot(ctx context.Context, organizationID, siteID string) (lintSnapshot, error) {
	snap := lintSnapshot{}
	// 收录面 + 最新已发布版本内容 + locale/翻译组/分类挂载/精选。
	rows, err := s.Store.Pool.Query(ctx, `
		SELECT b.asset_id::text, b.display_path,
		       COALESCE(v.markdown, ''), COALESCE(v.title, ''),
		       COALESCE(a.locale, ''), COALESCE(a.translation_group_id::text, ''),
		       COALESCE(a.category_container_id::text, ''),
		       EXISTS (SELECT 1 FROM site.site_featured f
		               WHERE f.site_id = b.site_id AND f.asset_id = b.asset_id)
		FROM site.site_content_bindings b
		JOIN asset.assets a
		  ON a.organization_id = b.organization_id AND a.id = b.asset_id
		LEFT JOIN asset.asset_versions v
		  ON v.id = a.current_published_version_id
		WHERE b.organization_id = $1::uuid AND b.site_id = $2::uuid
		  AND b.content_type = 'article'
	`, organizationID, siteID)
	if err != nil {
		return snap, fmt.Errorf("lint gather included: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var item lintIncluded
		if err := rows.Scan(&item.AssetID, &item.Slug, &item.Markdown, &item.Title,
			&item.Locale, &item.TranslationGroupID, &item.CategoryID, &item.Featured); err != nil {
			return snap, err
		}
		snap.Included = append(snap.Included, item)
	}
	if err := rows.Err(); err != nil {
		return snap, err
	}

	// 待发布积压：存在版本号大于已发布版本的收录资产。
	if err := s.Store.Pool.QueryRow(ctx, `
		SELECT COALESCE(array_agg(DISTINCT b.asset_id::text), '{}')
		FROM site.site_content_bindings b
		JOIN asset.assets a
		  ON a.organization_id = b.organization_id AND a.id = b.asset_id
		JOIN asset.asset_versions pv ON pv.id = a.current_published_version_id
		JOIN asset.asset_versions v
		  ON v.asset_id = a.id AND v.version_no > pv.version_no
		WHERE b.organization_id = $1::uuid AND b.site_id = $2::uuid
	`, organizationID, siteID).Scan(&snap.BacklogAssetIDs); err != nil {
		return snap, fmt.Errorf("lint gather backlog: %w", err)
	}

	redirectRows, err := s.Store.Pool.Query(ctx, `
		SELECT from_path, to_path FROM site.path_redirects
		WHERE organization_id = $1::uuid AND site_id = $2::uuid
	`, organizationID, siteID)
	if err != nil {
		return snap, fmt.Errorf("lint gather redirects: %w", err)
	}
	defer redirectRows.Close()
	for redirectRows.Next() {
		var redirect lintRedirect
		if err := redirectRows.Scan(&redirect.FromPath, &redirect.ToPath); err != nil {
			return snap, err
		}
		snap.Redirects = append(snap.Redirects, redirect)
	}
	if err := redirectRows.Err(); err != nil {
		return snap, err
	}

	categoryRows, err := s.Store.Pool.Query(ctx, `
		SELECT id::text, COALESCE(parent_id::text, ''), COALESCE(slug, '')
		FROM content.containers
		WHERE organization_id = $1::uuid AND kind = 'doc_folder'
		  AND status = 'active' AND public_flag
	`, organizationID)
	if err != nil {
		return snap, fmt.Errorf("lint gather categories: %w", err)
	}
	defer categoryRows.Close()
	for categoryRows.Next() {
		var category lintCategory
		if err := categoryRows.Scan(&category.ID, &category.ParentID, &category.Slug); err != nil {
			return snap, err
		}
		snap.Categories = append(snap.Categories, category)
	}
	if err := categoryRows.Err(); err != nil {
		return snap, err
	}

	pageRows, err := s.Store.Pool.Query(ctx, `
		SELECT id::text, slug, COALESCE(seo_description, ''), COALESCE(body_markdown, '')
		FROM site.site_pages
		WHERE organization_id = $1::uuid AND site_id = $2::uuid
	`, organizationID, siteID)
	if err != nil {
		return snap, fmt.Errorf("lint gather pages: %w", err)
	}
	defer pageRows.Close()
	for pageRows.Next() {
		var page lintPage
		if err := pageRows.Scan(&page.ID, &page.Slug, &page.SEODescription, &page.BodyMarkdown); err != nil {
			return snap, err
		}
		snap.Pages = append(snap.Pages, page)
	}
	if err := pageRows.Err(); err != nil {
		return snap, err
	}
	return snap, nil
}
