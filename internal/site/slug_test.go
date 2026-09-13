package site

import "testing"

func TestGenerateSlug(t *testing.T) {
	cases := []struct {
		name  string
		title string
		want  string
	}{
		{"英文保持原词", "Dune Part Two", "dune-part-two"},
		{"英文已规范则原样", "already-a-slug", "already-a-slug"},
		{"中文转拼音", "沙丘2", "shaqiu2"},
		{"中英混合分段", "镜头 Lens 指南", "jingtou-lens-zhinan"},
		{"标点转连字符", "Hello, World! (2026)", "hello-world-2026"},
		{"大小写归一", "CamelCase Title", "camelcase-title"},
	}
	for _, tc := range cases {
		if got := GenerateSlug(tc.title); got != tc.want {
			t.Errorf("%s: GenerateSlug(%q) = %q, want %q", tc.name, tc.title, got, tc.want)
		}
	}
}

func TestGenerateSlugHandlesNoise(t *testing.T) {
	if got := GenerateSlug("   "); got != "" {
		t.Errorf("blank title must yield empty slug, got %q", got)
	}
	if got := GenerateSlug("……！！"); got != "" {
		t.Errorf("punctuation-only title must yield empty slug, got %q", got)
	}
	long := GenerateSlug("这是一段特别特别特别长的中文标题用来验证截断逻辑是否会按照连字符边界回退而不是切出半个音节")
	if n := len([]rune(long)); n > slugMaxTitleRunes {
		t.Errorf("slug must respect the title cap: %d runes in %q", n, long)
	}
}
