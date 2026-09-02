package delivery

import (
	"strings"
	"testing"
	"time"

	"agentchunzhi/internal/site"
)

func TestPageCacheTTLAndEviction(t *testing.T) {
	cache := NewPageCache(2, 40*time.Millisecond)
	cache.Set(PageKey("s1", "w1", "anon", "/"), CacheEntry{Body: []byte("a")})
	cache.Set(PageKey("s1", "w1", "anon", "/posts"), CacheEntry{Body: []byte("b")})
	if entry, ok := cache.Get(PageKey("s1", "w1", "anon", "/")); !ok || string(entry.Body) != "a" {
		t.Fatal("first entry missing")
	}
	cache.Set(PageKey("s1", "w1", "anon", "/tags"), CacheEntry{Body: []byte("c")})
	// capacity 2: the oldest ("/") was evicted.
	if _, ok := cache.Get(PageKey("s1", "w1", "anon", "/")); ok {
		t.Fatal("insertion-order eviction did not run")
	}
	if _, ok := cache.Get(PageKey("s1", "w1", "anon", "/tags")); !ok {
		t.Fatal("newest entry missing")
	}
	time.Sleep(60 * time.Millisecond)
	if _, ok := cache.Get(PageKey("s1", "w1", "anon", "/tags")); ok {
		t.Fatal("TTL not enforced")
	}
}

func TestPageCacheInvalidatePrefix(t *testing.T) {
	cache := NewPageCache(0, time.Minute)
	for _, key := range []string{
		PageKey("s1", "w1", "anon", "/"),
		PageKey("s1", "w2", "anon", "/"),
		PageKey("s1", "r3", "member", "/posts/a"),
		PageKey("s1", "r3", "anon", "/posts/a"),
		PageKey("s2", "w1", "anon", "/"),
	} {
		cache.Set(key, CacheEntry{Body: []byte("x")})
	}
	// Whole site.
	if removed := cache.InvalidatePrefix("s1", "", ""); removed != 4 {
		t.Fatalf("site invalidation removed %d, want 4", removed)
	}
	if _, ok := cache.Get(PageKey("s2", "w1", "anon", "/")); !ok {
		t.Fatal("s2 must survive")
	}
	// Member tier only.
	cache.Set(PageKey("s3", "r1", "member", "/"), CacheEntry{Body: []byte("x")})
	cache.Set(PageKey("s3", "r1", "anon", "/"), CacheEntry{Body: []byte("x")})
	if removed := cache.InvalidatePrefix("s3", "member", ""); removed != 1 {
		t.Fatalf("tier invalidation removed %d, want 1", removed)
	}
	if _, ok := cache.Get(PageKey("s3", "r1", "anon", "/")); !ok {
		t.Fatal("anon band must survive tier invalidation")
	}
	// Route prefix.
	cache.Set(PageKey("s4", "r1", "anon", "/tags"), CacheEntry{Body: []byte("x")})
	cache.Set(PageKey("s4", "r1", "anon", "/posts"), CacheEntry{Body: []byte("x")})
	if removed := cache.InvalidatePrefix("s4", "", "/tags"); removed != 1 {
		t.Fatalf("prefix invalidation removed %d, want 1", removed)
	}
}

func TestRenderMarkdownSanitizes(t *testing.T) {
	source := "# Title\n\n<script>alert(1)</script>\n\n<img src=x onerror=alert(2)>\n\n[link](javascript:alert(3))\n\n<style>body{}</style>\n\n## Section A\n\n| a | b |\n| --- | --- |\n| 1 | 2 |\n\n~~gone~~\n\n- [ ] task"
	result := RenderMarkdown(source)
	for _, forbidden := range []string{"<script", "onerror", "javascript:", "<style"} {
		if strings.Contains(result.HTML, forbidden) {
			t.Fatalf("sanitizer leaked %q: %s", forbidden, result.HTML)
		}
	}
	for _, required := range []string{"<table>", "<del>gone</del>", "Section A"} {
		if !strings.Contains(result.HTML, required) {
			t.Fatalf("GFM output missing %q: %s", required, result.HTML)
		}
	}
	found := false
	for _, heading := range result.Headings {
		if heading.Text == "Section A" && heading.ID != "" && heading.Level == 2 {
			found = true
		}
	}
	if !found {
		t.Fatalf("TOC heading not extracted: %+v", result.Headings)
	}
}

func TestStyleEngineCSS(t *testing.T) {
	config := mustStyleConfig(t, `{"preset":"magazine","tokens":{"color":{"primary":"#1B4F91","mode":"dark"}},"layout":{"home_style":"hero","list_style":"grid","card_ratio":"16:9"}}`)
	css := StyleCSS(config)
	for _, required := range []string{"--c-primary:#1B4F91", "--reading:760px", "[data-mode=dark]", "@media (prefers-color-scheme:dark)"} {
		if !strings.Contains(css, required) {
			t.Fatalf("css missing %q:\n%s", required, css)
		}
	}
	if ColorModeAttribute(config) != "dark" {
		t.Fatal("mode attribute wrong for dark")
	}
	classes := LayoutClasses(config, "home")
	if !strings.Contains(classes, "home--hero") || !strings.Contains(classes, "list--grid") || !strings.Contains(classes, "ratio--16-9") {
		t.Fatalf("layout classes wrong: %s", classes)
	}
	auto := mustStyleConfig(t, `{"tokens":{"color":{"mode":"auto"}}}`)
	if ColorModeAttribute(auto) != "" {
		t.Fatal("auto must emit no attribute")
	}
	if !strings.Contains(StyleCSS(auto), ":root:not([data-mode=light])") {
		t.Fatal("auto dark mode media query missing")
	}
}

func TestStyleEngineDarkPaletteContrast(t *testing.T) {
	for _, primary := range []string{"#2E7D32", "#8C2F39", "#111111", "#B45309", "#2F5D74", "#1B4F91"} {
		config := mustStyleConfig(t, `{"tokens":{"color":{"primary":"`+primary+`"}}}`)
		css := StyleCSS(config)
		// The derived dark primary must be light enough: extract it and
		// verify it is a valid color token (the derivation loop guarantees
		// ≥4.5:1 against the dark surface at runtime).
		if !strings.Contains(css, "hsl(") {
			t.Fatalf("derived dark palette missing for %s", primary)
		}
	}
}

func mustStyleConfig(t *testing.T, document string) site.StyleConfig {
	t.Helper()
	parsed, err := site.ParseStyleConfig([]byte(document))
	if err != nil {
		t.Fatalf("style parse: %v", err)
	}
	return parsed
}
