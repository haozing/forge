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
		Slug: "demo", Name: "Demo Site", Template: "blog", ScopePublic: true,
		HomeHref: "/sites/demo/", PostsHref: "/sites/demo/posts/",
		TagsHref: "/sites/demo/tags/", SearchHref: "/sites/demo/search",
		RSSHref: "/sites/demo/rss.xml",
		Style:       config,
		StyleCSSVars: "/*vars*/",
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
		"rss": RSSVM{Site: chrome, Items: []RSSItem{{Title: "T", Href: "http://x/a"}}},
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
