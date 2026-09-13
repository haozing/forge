package delivery

import (
	"strings"
	"testing"
	"time"

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
