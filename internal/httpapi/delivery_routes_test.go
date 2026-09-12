package httpapi

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

// TestDeliveryRoutesRegistered pins the /sites HTML route table (design doc
// §12): the OpenAPI contract gate only covers /api, so this table-driven
// test is the drift guard for the delivery face. Every pattern must resolve
// to a registered handler (not the mux 404), and the listed extra paths
// under the site subtree must resolve to the catch-all home handler.
func TestDeliveryRoutesRegistered(t *testing.T) {
	deps := Dependencies{}
	mux := newRouter(deps)
	table := []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/sites/demo"},
		{http.MethodGet, "/sites/demo/"},
		{http.MethodGet, "/sites/demo/posts"},
		{http.MethodGet, "/sites/demo/posts/"},
		{http.MethodGet, "/sites/demo/posts/a/b-c"},
		{http.MethodGet, "/sites/demo/sections/docs"},
		{http.MethodGet, "/sites/demo/sections/docs/"},
		{http.MethodGet, "/sites/demo/tags"},
		{http.MethodGet, "/sites/demo/tags/"},
		{http.MethodGet, "/sites/demo/tags/go"},
		{http.MethodGet, "/sites/demo/tags/go/"},
		{http.MethodGet, "/sites/demo/search"},
		{http.MethodGet, "/sites/demo/rss.xml"},
		{http.MethodGet, "/sites/demo/sitemap.xml"},
		{http.MethodGet, "/sites/demo/robots.txt"},
		{http.MethodGet, "/static/delivery-search.js"},
		// Subtree catch-all: unknown site paths must resolve to a handler
		// (which answers the site-scoped 404 page), never a mux-level miss.
		{http.MethodGet, "/sites/demo/unknown/deeper/path"},
	}
	for _, entry := range table {
		request := &http.Request{Method: entry.method, URL: &url.URL{Path: entry.path}}
		_, pattern := mux.Handler(request)
		if pattern == "" {
			t.Errorf("%s %s resolves to no registered pattern", entry.method, entry.path)
		}
	}
	// The /api JSON face must stay untouched and coexist.
	request := &http.Request{Method: http.MethodGet, URL: &url.URL{Path: "/api/public/sites/demo"}}
	if _, pattern := mux.Handler(request); pattern == "" {
		t.Error("/api/public/sites/{slug} no longer registered")
	}
}

// TestRenderRobotsTxt pins the domain-level robots.txt shape: blanket allow
// plus one Sitemap line per active released site.
func TestRenderRobotsTxt(t *testing.T) {
	body := renderRobotsTxt("https://seo.example", []string{"alpha", "beta"})
	want := "User-agent: *\nAllow: /\nSitemap: https://seo.example/sites/alpha/sitemap.xml\nSitemap: https://seo.example/sites/beta/sitemap.xml\n"
	if body != want {
		t.Fatalf("robots.txt mismatch:\n%s", body)
	}
}

// TestDeliveryBaseURLPrefersConfigured pins the cache-poisoning fix: with a
// configured public base the request Host never leaks into canonical/og:url.
func TestDeliveryBaseURLPrefersConfigured(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "http://localhost:3000/sites/demo/", nil)
	deps := Dependencies{DeliveryPublicBaseURL: "https://seo.example"}
	if got := deps.deliveryBaseURL(r); got != "https://seo.example" {
		t.Fatalf("configured base must win, got %q", got)
	}
	empty := Dependencies{}
	if got := empty.deliveryBaseURL(r); got != "http://localhost:3000" {
		t.Fatalf("fallback must derive from request, got %q", got)
	}
	if got := (Dependencies{DeliveryPublicBaseURL: "https://seo.example/"}).deliveryBaseURL(r); got != "https://seo.example" {
		t.Fatal("trailing slash must be trimmed")
	}
}

// TestDeliveryCommentFormRouteRegistered pins the JS-free comment fallback:
// the detail form posts to {PostPath}/comments, so the pattern must be
// routed (not a mux-level miss) and must not answer the 405 page.
func TestDeliveryCommentFormRouteRegistered(t *testing.T) {
	deps := Dependencies{}
	mux := newRouter(deps)
	request := &http.Request{Method: http.MethodPost, URL: &url.URL{Path: "/sites/demo/posts/hello/comments"}}
	if _, pattern := mux.Handler(request); pattern == "" {
		t.Fatal("POST {PostPath}/comments resolves to no registered pattern")
	}
}
