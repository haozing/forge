package delivery

// hreflang_test.go — D11/E：多语言站点 hreflang alternates 注入契约。

import (
	"strings"
	"testing"

	"agentchunzhi/internal/site"
)

func TestAlternatesSingleLocaleIsEmpty(t *testing.T) {
	s := &Service{}
	facts := site.SiteFacts{Site: site.Site{EnabledLocales: []string{"zh"}, DefaultLocale: "zh"}}
	if alts := s.alternates(facts, "/"); alts != nil {
		t.Fatalf("single-locale site must have no alternates, got %v", alts)
	}
}

func TestAlternatesMultiLocaleCoversAllAndXDefault(t *testing.T) {
	s := &Service{}
	facts := site.SiteFacts{Site: site.Site{
		Slug:          "demo",
		DefaultLocale: "zh",
		EnabledLocales: []string{"en", "zh"},
	}}
	alts := s.alternates(facts, "/sites/demo/sections/lens")
	if len(alts) != 3 {
		t.Fatalf("expected zh/en/x-default, got %v", alts)
	}
	joined := ""
	for _, alt := range alts {
		joined += alt.Hreflang + "=" + alt.Href + ";"
	}
	for _, want := range []string{
		"zh=/sites/demo/sections/lens",
		"en=/sites/demo/en/sections/lens",
		"x-default=/sites/demo",
	} {
		if !contains(joined, want) {
			t.Errorf("alternates missing %q, got %s", want, joined)
		}
	}
}

func TestInjectHeadTags(t *testing.T) {
	body := []byte("<html lang=\"zh\"><head><title>t</title></head><body></body></html>")
	got := injectHeadTags(body, []hreflangAlternate{
		{Hreflang: "en", Href: "/sites/demo/en"},
	})
	out := string(got)
	if !strings.Contains(out, `rel="alternate" hreflang="en" href="/sites/demo/en"`) {
		t.Fatalf("alternate link not injected: %s", out)
	}
	if strings.Count(out, "</head>") != 1 {
		t.Fatalf("injection must not duplicate head closing tag: %s", out)
	}
}

func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && strings.Contains(haystack, needle)
}
