package delivery

import (
	"strings"
	"testing"

	"agentchunzhi/internal/site"
)

// TestRendererCompilesEveryTemplate asserts the embedded template sets parse
// at boot (NewRenderer panics otherwise, so this also guards construction).
func TestRendererCompilesEveryTemplate(t *testing.T) {
	renderer := NewRenderer()
	for _, kind := range []string{"home", "list", "detail", "tags", "tag_page", "search", "gate", "error"} {
		if _, ok := renderer.pages[kind]; !ok {
			t.Fatalf("page template %s not compiled", kind)
		}
	}
	for _, kind := range []string{"rss", "sitemap", "robots"} {
		if _, ok := renderer.xml[kind]; !ok {
			t.Fatalf("xml template %s not compiled", kind)
		}
	}
}

func renderChrome(config site.StyleConfig, kind string) Chrome {
	return Chrome{
		Slug: "demo", Name: "Demo Site", ScopePublic: true,
		HomeHref: "/sites/demo/", PostsHref: "/sites/demo/posts/",
		TagsHref: "/sites/demo/tags/", SearchHref: "/sites/demo/search",
		RSSHref:       "/sites/demo/rss.xml",
		Style:         config,
		StyleCSSVars:  "/*vars*/",
		LayoutClasses: LayoutClasses(config, kind),
	}
}

func TestRenderPagesSmoke(t *testing.T) {
	renderer := NewRenderer()
	config := mustStyleConfig(t, `{"preset":"calm"}`)
	chrome := renderChrome(config, "home")

	home := HomeVM{Page: Page{Kind: "home", Site: chrome, Title: "Demo Site", Canonical: "http://x/sites/demo/"}}
	home.Sections = []SectionVM{{Type: "latest", Title: "最新", Items: []CardVM{{
		Title: "Hello World", Href: "/sites/demo/posts/hello", Summary: "First post",
		Tags: []TagChip{{Key: "go", DisplayName: "Go", Href: "/sites/demo/tags/go"}},
	}}}}
	home.TagCloud = home.Sections[0].Items[0].Tags
	home.ShowTagCloud = true
	body, err := renderer.RenderPage("home", home)
	if err != nil {
		t.Fatalf("home render: %v", err)
	}
	for _, marker := range []string{"<title>Demo Site</title>", `rel="canonical"`, "Hello World", "/sites/demo/tags/go", "tag-chip", "<style>"} {
		if !strings.Contains(string(body), marker) {
			t.Fatalf("home output missing %q", marker)
		}
	}

	detail := DetailVM{Page: Page{Kind: "detail", Site: chrome, Title: "Post · Demo", Canonical: "http://x/sites/demo/posts/hello", CanonicalImage: "http://x/sites/demo/media/att-1", JSONLD: articleJSONLD(site.SiteFacts{Site: site.Site{Name: "Demo Site"}}, site.PublicPostContent{Title: "Post"}, "http://x/sites/demo/posts/hello", "http://x/sites/demo", "", "http://x/sites/demo/media/att-1")}}
	detail.ContentHTML = "<h2 id=\"a\">A</h2><p>body</p>"
	detail.TOC = []Heading{{ID: "a", Text: "A", Level: 2}, {ID: "b", Text: "B", Level: 2}}
	body, err = renderer.RenderPage("detail", detail)
	if err != nil {
		t.Fatalf("detail render: %v", err)
	}
	for _, marker := range []string{"application/ld+json", `"@type":"Article"`, `"@type":"BreadcrumbList"`, `property="og:image"`, "summary_large_image", "article-body", "toc"} {
		if !strings.Contains(string(body), marker) {
			t.Fatalf("detail output missing %q", marker)
		}
	}
	noCover := DetailVM{Page: Page{Kind: "detail", Site: chrome, Title: "Plain · Demo", Canonical: "http://x/sites/demo/posts/plain", JSONLD: articleJSONLD(site.SiteFacts{Site: site.Site{Name: "Demo Site"}}, site.PublicPostContent{Title: "Plain"}, "http://x/sites/demo/posts/plain", "http://x/sites/demo", "", "")}}
	noCover.ContentHTML = "<p>body</p>"
	body, _ = renderer.RenderPage("detail", noCover)
	for _, marker := range []string{`name="twitter:card" content="summary"`} {
		if !strings.Contains(string(body), marker) {
			t.Fatalf("no-cover detail output missing %q", marker)
		}
	}
	if strings.Contains(string(body), `property="og:image"`) {
		t.Fatal("no-cover detail must not emit og:image")
	}

	// 回归：发布时间非空时 with 块把点绑到 PublishedOn 字符串上，datetime
	// 必须走根 VM 的 UpdatedISO（线上曾因 {{.UpdatedISO}} 落在 string 上下文
	// 而整页 500）。有发布时间 + 无发布时间两种形态都要渲染成功。
	dated := DetailVM{Page: Page{Kind: "detail", Site: chrome, Title: "Dated · Demo", Canonical: "http://x/sites/demo/posts/dated"}}
	dated.ContentHTML = "<p>body</p>"
	dated.PublishedOn = "2026-09-13"
	dated.UpdatedISO = "2026-09-13T08:00:00Z"
	dated.VersionNo = 3
	dated.Publications = []PublicationVM{{VersionNo: 3, PublishedOn: "2026-09-13", ChangeNote: "first", AILabel: ""}}
	body, err = renderer.RenderPage("detail", dated)
	if err != nil {
		t.Fatalf("dated detail render: %v", err)
	}
	for _, marker := range []string{`datetime="2026-09-13T08:00:00Z"`, "发布于 2026-09-13", `v3`} {
		if !strings.Contains(string(body), marker) {
			t.Fatalf("dated detail output missing %q", marker)
		}
	}

	// The markdown sanitizer pipeline output is marked safe by noescape —
	// verify a raw <script> can never reach it via the VM either (defense in
	// depth documented in design doc §10).
	evil := DetailVM{Page: Page{Kind: "detail", Site: chrome}}
	evil.ContentHTML = RenderMarkdown("<script>x</script>").HTML
	body, _ = renderer.RenderPage("detail", evil)
	if strings.Contains(string(body), "<script>x") {
		t.Fatal("unsanitized script reached the template")
	}

	feeds := map[string]any{
		"rss":     RSSVM{Site: chrome, Items: []RSSItem{{Title: "T", Href: "http://x/a"}}},
		"sitemap": SitemapVM{Site: chrome, URLs: []SitemapURL{{Loc: "http://x/a", LastmodOn: "2026-09-01T00:00:00Z"}}},
		"robots":  struct{ ScopePublic bool }{true},
	}
	for kind, vm := range feeds {
		body, err := renderer.RenderXML(kind, vm)
		if err != nil {
			t.Fatalf("%s render: %v", kind, err)
		}
		if len(body) == 0 {
			t.Fatalf("%s empty", kind)
		}
	}
}

func TestErrorPageRendering(t *testing.T) {
	service := &Service{Render: NewRenderer()}
	page := service.ErrorPage(404)
	if page.Status != 404 || !strings.Contains(string(page.Body), "404") {
		t.Fatal("error page broken")
	}
	if !strings.Contains(page.CacheControl, "no-store") || !page.NoIndex {
		t.Fatal("error page must be no-store + noindex")
	}
}

// TestDetailTemplateRendersNeighborsAndAttachments locks the §11.2 detail
// page additions from commit 363d32f: the prev/next post navigation anchors
// and the downloadable attachment list, plus the empty cases staying silent.
func TestDetailTemplateRendersNeighborsAndAttachments(t *testing.T) {
	renderer := NewRenderer()
	chrome := renderChrome(mustStyleConfig(t, `{"preset":"calm"}`), "detail")

	vm := DetailVM{Page: Page{Kind: "detail", Site: chrome, Title: "Post · Demo"}}
	vm.ContentHTML = "<p>body</p>"
	vm.Prev = &NeighborLink{Title: "上一篇标题", Href: "/sites/demo/posts/prev"}
	vm.Next = &NeighborLink{Title: "下一篇标题", Href: "/sites/demo/posts/next"}
	vm.Attachments = []AttachmentVM{
		{Name: "spec.pdf", URL: "/sites/demo/media/att-1", MediaType: "application/pdf", ByteSize: 2048},
	}
	body, err := renderer.RenderPage("detail", vm)
	if err != nil {
		t.Fatalf("detail render: %v", err)
	}
	for _, marker := range []string{
		`class="neighbor prev"`, `rel="prev"`, "/sites/demo/posts/prev", "上一篇标题", "上一篇",
		`class="neighbor next"`, `rel="next"`, "/sites/demo/posts/next", "下一篇标题", "下一篇",
		`class="attachments"`, "附件下载", "spec.pdf", "/sites/demo/media/att-1", "attachment",
	} {
		if !strings.Contains(string(body), marker) {
			t.Fatalf("detail output missing %q", marker)
		}
	}

	// Without neighbors or attachments the blocks must not render at all.
	plain := DetailVM{Page: Page{Kind: "detail", Site: chrome, Title: "Plain · Demo"}}
	plain.ContentHTML = "<p>body</p>"
	body, err = renderer.RenderPage("detail", plain)
	if err != nil {
		t.Fatalf("plain detail render: %v", err)
	}
	for _, marker := range []string{"neighbor", "attachments", "附件下载"} {
		if strings.Contains(string(body), marker) {
			t.Fatalf("plain detail must not render %q block", marker)
		}
	}
}

// TestDetailBodyHeadingsDemoteToH2 pins the SEO contract: the chrome renders
// the post title as the page's single h1, so a level-1 heading inside the
// body markdown must demote to h2 in both the HTML and the TOC outline.
func TestDetailBodyHeadingsDemoteToH2(t *testing.T) {
	result := RenderMarkdown("# 顶级标题\n\n正文")
	if strings.Contains(result.HTML, "<h1") {
		t.Fatalf("body h1 must demote, got %q", result.HTML)
	}
	if !strings.Contains(result.HTML, "<h2 id=") {
		t.Fatalf("demoted heading missing: %q", result.HTML)
	}
	if len(result.Headings) != 1 || result.Headings[0].Level != 2 {
		t.Fatalf("TOC must carry the demoted level, got %+v", result.Headings)
	}
}

// TestPlainTextExcerpt pins the meta-description fallback: markdown flattens
// to readable text, images vanish, whitespace collapses and long bodies
// truncate with an ellipsis.
func TestPlainTextExcerpt(t *testing.T) {
	excerpt := PlainTextExcerpt("## 标题\n\n第一段**强调**与[链接](https://x)文字。\n\n![图](chunzhi-media://00000000-0000-4000-8000-000000000001)\n\n第二段。", 100)
	if excerpt != "标题 第一段 强调 与 链接 文字。 第二段。" {
		t.Fatalf("unexpected excerpt: %q", excerpt)
	}
	long := PlainTextExcerpt(strings.Repeat("字", 300), 150)
	if got := len([]rune(long)); got != 151 {
		t.Fatalf("truncated excerpt should be limit+ellipsis runes, got %d", got)
	}
	if !strings.HasSuffix(long, "…") {
		t.Fatal("truncated excerpt must end with ellipsis")
	}
	if PlainTextExcerpt("   ", 100) != "" {
		t.Fatal("blank source must yield empty excerpt")
	}
}
