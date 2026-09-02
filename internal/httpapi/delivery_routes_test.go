package httpapi

import (
	"net/http"
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
