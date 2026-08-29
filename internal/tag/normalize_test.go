package tag

import "testing"

func TestNormalizeKeyFoldsUnicodeEquivalents(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		{"Release", "release"},
		{"  RELEASE  ", "release"},
		{"ＲＥＬＥＡＳＥ", "release"},           // full-width NFKC
		{"  多个   空格  ", "多个 空格"},             // whitespace folding
		{"İstanbul", "i̇stanbul"},             // case folding expands
	}
	for _, tc := range cases {
		got, err := NormalizeKey(tc.input)
		if err != nil {
			t.Fatalf("NormalizeKey(%q): %v", tc.input, err)
		}
		if got != tc.want {
			t.Fatalf("NormalizeKey(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

func TestNormalizeKeyRejectsForbidden(t *testing.T) {
	for _, input := range []string{"", "a\u0000b", "a\u200bb"} {
		if _, err := NormalizeKey(input); err == nil {
			t.Fatalf("NormalizeKey(%q) must be rejected", input)
		}
	}
}

func TestDeriveSlugRules(t *testing.T) {
	if got := DeriveSlug("release", "00000000-0000-0000-0000-000000000001"); got != "release" {
		t.Fatalf("slug = %q", got)
	}
	if got := DeriveSlug("经典 镜头!", "00000000-0000-0000-0000-000000000001"); got != "经典-镜头" {
		t.Fatalf("unicode slug = %q", got)
	}
	if got := DeriveSlug("!!!", "abcd1234efgh"); got != "tag-abcd1234efgh" {
		t.Fatalf("fallback slug = %q", got)
	}
	long := DeriveSlug(string(make([]rune, 200)), "x")
	if len([]rune(long)) > 120 {
		t.Fatalf("slug too long: %d", len([]rune(long)))
	}
}

func TestFilterNormalizationAndContradictions(t *testing.T) {
	filter, err := NormalizeFilter(KeyFilter{
		Any:  []string{"  Release ", "release"},
		All:  []string{"APPROVED"},
		None: []string{"deprecated"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(filter.Any) != 1 || filter.Any[0] != "release" {
		t.Fatalf("any = %#v", filter.Any)
	}
	if _, err := NormalizeFilter(KeyFilter{Any: []string{"release"}, None: []string{"release"}}); err != ErrContradictoryFilter {
		t.Fatalf("contradiction must fail, got %v", err)
	}
	if _, err := NormalizeFilter(KeyFilter{Any: make([]string, 51)}); err != ErrTooManyTags {
		t.Fatalf("oversized group must fail, got %v", err)
	}
	if (Filter{}).Empty() != true {
		t.Fatal("empty filter must report empty")
	}
}

func TestResolveFailsLoudlyOnUnknownKeys(t *testing.T) {
	resolved, err := Resolve(KeyFilter{Any: []string{"release"}}, func(key string) (string, bool) {
		if key == "release" {
			return "tag-uuid", true
		}
		return "", false
	})
	if err != nil || len(resolved.Any) != 1 {
		t.Fatalf("resolve failed: %#v %v", resolved, err)
	}
	if _, err := Resolve(KeyFilter{Any: []string{"typo"}}, func(string) (string, bool) { return "", false }); err != ErrUnknownTag {
		t.Fatalf("unknown key must fail, got %v", err)
	}
}
