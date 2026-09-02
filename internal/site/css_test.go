package site

import (
	"strings"
	"testing"
)

func TestSanitizeCSSKeepsWhitelisted(t *testing.T) {
	input := `.card:hover { border-radius: 12px; box-shadow: 0 4px 8px rgba(0, 0, 0, .2); transform: translateY(-2px); transition: transform .2s ease; }
.article-body h2 { letter-spacing: .5px; }
@media (prefers-color-scheme: dark) { .card { background-color: #1a1a1a; } }
@media (min-width: 920px) { .article-layout.has-sidebar { grid-template-columns: minmax(0, 1fr) 240px; } }`
	output, stripped := SanitizeCSS(input)
	if len(stripped) != 0 {
		t.Fatalf("whitelisted css was stripped: %v", stripped)
	}
	for _, required := range []string{"border-radius: 12px", "transform: translateY(-2px)", "@media (prefers-color-scheme: dark)", "grid-template-columns: minmax(0, 1fr) 240px", ".card:hover{"} {
		if !strings.Contains(output, required) {
			t.Fatalf("output missing %q:\n%s", required, output)
		}
	}
}

func TestSanitizeCSSForbiddenMatrix(t *testing.T) {
	// marker: a fragment that must NOT appear in the sanitized output.
	cases := []struct {
		name   string
		input  string
		marker string
	}{
		{"position fixed clickjacking", ".x { position: fixed; top: 0; }", "position: fixed"},
		{"position absolute", ".x { position: absolute; }", "position: absolute"},
		{"position sticky hijack", ".x { position: sticky; }", "position: sticky"},
		{"url exfil in value", ".x { background-image: url(https://evil.example/x); }", "url("},
		{"expression", ".x { width: expression(alert(1)); }", "expression"},
		{"behavior", ".x { behavior: url(evil.htc); }", "behavior"},
		{"pointer-events overlay", ".x { pointer-events: none; }", "pointer-events"},
		{"content injection", ".x::before { content: \"click me\"; }", "content:"},
		{"filter perf", ".x { filter: blur(4px); }", "filter"},
		{"@import escape", "@import url('https://evil.example/e.css');", "@import"},
		{"keyframes at-rule", "@keyframes slide { from { transform: translateX(0); } }", "@keyframes"},
		{"charset at-rule", "@charset 'utf-8';", "@charset"},
		{"unlisted media feature", "@media (prefers-contrast: more) { .x { color: red; } }", "@media"},
		{"javascript selector", "a[href^=\"javascript:\"] { color: red; }", "javascript:"},
		{"oversized selector", "." + strings.Repeat("a", 300) + " { color: red; }", "color: red"},
		{"z-index runaway", ".x { z-index: 99999; }", "z-index: 99999"},
		{"unknown property", ".x { -webkit-hack: x; }", "-webkit-hack"},
	}
	for _, tc := range cases {
		output, stripped := SanitizeCSS(tc.input)
		if strings.Contains(output, tc.marker) {
			t.Errorf("%s: forbidden marker %q survived:\noutput=%s", tc.name, tc.marker, output)
		}
		if len(stripped) == 0 && strings.TrimSpace(output) != "" {
			t.Errorf("%s: nothing stripped but output non-empty (%q)", tc.name, output)
		}
	}
}

func TestSanitizeCSSPositionRelativeAllowed(t *testing.T) {
	output, stripped := SanitizeCSS(`.x { position: relative; top: 2px; }`)
	if len(stripped) != 0 || !strings.Contains(output, "position: relative") {
		t.Fatalf("relative position must survive: %q %v", output, stripped)
	}
}

func TestSanitizeCSSUrlInSelector(t *testing.T) {
	output, _ := SanitizeCSS("a { background: url(x) } .y { color: red }")
	if strings.Contains(strings.ToLower(output), "url(") {
		t.Fatalf("url() survived: %s", output)
	}
	if !strings.Contains(output, ".y{color: red;}") {
		t.Fatalf("sibling rule lost: %s", output)
	}
}

func TestSanitizeCSSCanonicalizesStoredForm(t *testing.T) {
	// Write side stores the sanitized output; rendering sanitizes again —
	// the operation must be idempotent.
	first, _ := SanitizeCSS(".card{border-radius:12px;position:fixed;}")
	second, stripped := SanitizeCSS(first)
	if second != first {
		t.Fatalf("not idempotent: %q -> %q (%v)", first, second, stripped)
	}
}
