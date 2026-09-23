package delivery

// seo_meta_test.go — 2026-09-23 SEO 审计修复的单元回归：首页标题拼接、
// meta description 的 Markdown 标记剥离、根站点 URL 形态。

import (
	"strings"
	"testing"
)

func TestHomeTitle(t *testing.T) {
	cases := []struct {
		name, description, want string
	}{
		// 描述取 " · " 前首段，站名做前缀。
		{"七渡出海", "独立开发者出海工具站 · 独立站建站、SEO、外链、变现的实战知识库。",
			"七渡出海｜独立开发者出海工具站"},
		// 无分隔符取全段。
		{"站点", "一句话描述", "站点｜一句话描述"},
		// 描述缺省/与站名相同：不重复拼接。
		{"站点", "", "站点"},
		{"站点", "站点", "站点"},
		{"", "描述", ""},
		// 超长首段 24 字符截断。
		{"站", "一二三四五六七八九十一二三四五六七八九十一二三四五 · 其余",
			"站｜一二三四五六七八九十一二三四五六七八九十一二三四"},
	}
	for _, tc := range cases {
		if got := homeTitle(tc.name, tc.description); got != tc.want {
			t.Errorf("homeTitle(%q, %q) = %q, want %q", tc.name, tc.description, got, tc.want)
		}
	}
}

func TestSanitizeMetaDescriptionStripsMarkdownMarkers(t *testing.T) {
	got := sanitizeMetaDescription("今天带大家看一个网站。**【截图文字】** 流量数据（Similarweb）。")
	want := "今天带大家看一个网站。 流量数据（Similarweb）。"
	if got != want {
		t.Fatalf("sanitizeMetaDescription = %q, want %q", got, want)
	}
	if got := sanitizeMetaDescription("用 `代码` 与 __下划线__ 标记"); got != "用 代码 与 下划线 标记" {
		t.Fatalf("backtick/underscore strip failed: %q", got)
	}
}

func TestHomeURLFor(t *testing.T) {
	root := &Service{RootSiteSlug: "itd-site"}
	if got := root.homeURLFor("itd-site", "https://www.qidu.site"); got != "https://www.qidu.site/" {
		t.Fatalf("root site must collapse to origin root, got %q", got)
	}
	if got := root.homeURLFor("other-site", "https://www.qidu.site"); got != "https://www.qidu.site/sites/other-site/" {
		t.Fatalf("non-root site keeps path form, got %q", got)
	}
	plain := &Service{}
	if got := plain.homeURLFor("itd-site", "https://www.qidu.site"); got != "https://www.qidu.site/sites/itd-site/" {
		t.Fatalf("no root config keeps path form, got %q", got)
	}
}

func TestOriginOf(t *testing.T) {
	cases := map[string]string{
		"https://www.qidu.site/sites/itd/posts/x": "https://www.qidu.site",
		"https://www.qidu.site/":                  "https://www.qidu.site",
		"https://www.qidu.site":                   "https://www.qidu.site",
	}
	for canonical, want := range cases {
		if got := originOf(canonical); got != want {
			t.Errorf("originOf(%q) = %q, want %q", canonical, got, want)
		}
	}
}

func TestRewriteRootSiteURLs(t *testing.T) {
	body := []byte(`<a href="/sites/itd/posts/x">L</a>` +
		`<a href="/sites/itd">H</a>` +
		`<link rel="canonical" href="https://www.qidu.site/sites/itd/posts/x">` +
		`"item":"https://www.qidu.site/sites/itd/c/seo"` +
		`data-site-slug="itd"` +
		`/api/public/sites/itd/chat`)
	got := string(rewriteRootSiteURLs(body, "itd", "https://www.qidu.site"))
	for _, want := range []string{
		`href="/posts/x"`,
		`href="/"`,
		`href="https://www.qidu.site/posts/x"`,
		`"https://www.qidu.site/c/seo"`,
		`data-site-slug="itd"`,
		`/api/public/sites/itd/chat`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("rewrite lost %q in %s", want, got)
		}
	}
	if strings.Contains(got, `href="/sites/`) {
		t.Errorf("rewrite left a path-form href: %s", got)
	}
}
