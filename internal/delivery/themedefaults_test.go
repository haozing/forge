package delivery

// themedefaults_test.go — 默认主题全槽位「真实 VM 渲染」守卫：用 delivery
// 的实际视图模型逐槽渲染内置主题，捕获模板字段名与 VM 漂移（历史教训：
// category.tmpl 读 .Crumb/.Children 而 VM 是 Crumbs/Subcategories，静默空渲染）。

import (
	"strings"
	"testing"

	"agentchunzhi/internal/theme"
)

func mustChrome() Chrome {
	return Chrome{
		Slug: "demo", Name: "演示站", SiteLang: "zh", ScopePublic: true,
		Nav:       []NavItem{{Label: "首页", Href: "/sites/demo/"}},
		HomeHref:  "/sites/demo/", PostsHref: "/sites/demo/posts/",
		TagsHref:  "/sites/demo/tags/", SearchHref: "/sites/demo/search",
		RSSHref:   "/sites/demo/rss.xml",
	}
}

func pageOf(kind string) Page {
	return Page{
		Kind: kind, Title: "页标题", Description: "描述",
		Canonical: "https://x/sites/demo/kind", Site: mustChrome(),
	}
}

func card() CardVM {
	return CardVM{Title: "文章甲", Href: "/sites/demo/posts/hello", Summary: "摘要",
		PublishedOn: "2026-09-13", Tags: []TagChip{{Key: "go", DisplayName: "Go", Href: "/sites/demo/tags/go"}}}
}

// TestDefaultThemeRendersEverySlot 逐槽渲染默认主题，字段漂移即报错。
func TestDefaultThemeRendersEverySlot(t *testing.T) {
	compiled, err := theme.Compile(map[string]string{}, theme.Options{SiteSlug: "demo"})
	if err != nil {
		t.Fatalf("compile defaults: %v", err)
	}

	cases := []struct {
		slot string
		vm   any
		want string // 输出必须包含
	}{
		{theme.SlotHome, HomeVM{Page: pageOf("home"), Items: []CardVM{card()},
			TagCloud: []TagChip{{Key: "go", DisplayName: "Go", Href: "/sites/demo/tags/go", Count: 2}}, ShowTagCloud: true},
			`href="/sites/demo/tags/go"`},
		{theme.SlotDetail, DetailVM{Page: pageOf("detail"), ContentHTML: "<p>正文</p>",
			Fields:     []FieldValueVM{{Key: "f", Label: "字段", Value: "值"}},
			Tags:       []TagChip{{Key: "go", DisplayName: "Go", Href: "/sites/demo/tags/go"}},
			Publications: []PublicationVM{{VersionNo: 1, PublishedOn: "2026-09-13"}, {VersionNo: 2, PublishedOn: "2026-09-14"}},
			Prev:       &NeighborLink{Title: "上", Href: "/sites/demo/posts/a"}, Next: &NeighborLink{Title: "下", Href: "/sites/demo/posts/b"}},
			"正文"},
		{theme.SlotAbout, DetailVM{Page: pageOf("about"), ContentHTML: "<p>关于我们</p>"}, "关于我们"},
		{theme.SlotList, ListVM{Page: pageOf("list"), Heading: "文章列表", Items: []CardVM{card()},
			Pagination: PaginationVM{NextHref: "/sites/demo/posts/?cursor=x"}}, "文章列表"},
		{theme.SlotSection, ListVM{Page: pageOf("section"), Heading: "栏目", Items: []CardVM{card()}}, "栏目"},
		{theme.SlotCategory, CategoryVM{Page: pageOf("category"), Heading: "分类页",
			Crumbs:        []CrumbVM{{Name: "上级", Href: "/sites/demo/c/parent"}},
			Subcategories: []SubcategoryVM{{Name: "子分类", Href: "/sites/demo/c/parent/child", Count: 3}},
			Items:         []CardVM{card()}},
			"子分类"},
		{theme.SlotTags, TagsVM{Page: pageOf("tags"), Tags: []TagChip{{Key: "go", DisplayName: "Go", Href: "/sites/demo/tags/go", Count: 1}}}, `href="/sites/demo/tags/go"`},
		{theme.SlotTagPage, TagPageVM{Page: pageOf("tag_page"), TagKey: "go", TagName: "Go", Items: []CardVM{card()}}, "Go"},
		{theme.SlotSearch, SearchVM{Page: pageOf("search"), Query: "镜头"}, "镜头"},
		{theme.SlotPage, CustomPageVM{Page: pageOf("page"), Heading: "自定义页", ContentHTML: "<p>自定义内容</p>"}, "自定义内容"},
		{theme.SlotArchive, struct {
			Page
			Years []ArchiveYearVM
		}{Page: pageOf("archive"), Years: []ArchiveYearVM{{Year: "2026", Months: []ArchiveMonthVM{{Month: "2026-09", Label: "09 月", Items: []CardVM{card()}}}}}},
			"2026"},
	}
	for _, tc := range cases {
		var sb strings.Builder
		if err := compiled.Render(&sb, tc.slot, tc.vm); err != nil {
			t.Errorf("slot %s render: %v", tc.slot, err)
			continue
		}
		if !strings.Contains(sb.String(), tc.want) {
			t.Errorf("slot %s output missing %q", tc.slot, tc.want)
		}
		if strings.Contains(sb.String(), "//sites/") {
			t.Errorf("slot %s produced protocol-relative //sites/ href (double-prefix bug)", tc.slot)
		}
		// SEO 基线：每个页面槽位恰好一个 h1（§9：SEO 回归必须绿）。
		if h1 := strings.Count(sb.String(), "<h1"); h1 != 1 {
			t.Errorf("slot %s renders %d h1 elements, want exactly 1", tc.slot, h1)
		}
	}
}
