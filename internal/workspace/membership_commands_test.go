package workspace

import "testing"

func TestMaskedEmail(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		{"alice@example.com", "a***@example.com"},
		{"a@b.co", "a***@b.co"},
		{"bob.smith@example.org", "b***@example.org"},
		{"@example.com", "***"},
		{"no-at-sign", "***"},
		{"", "***"},
	}
	for _, tc := range cases {
		if got := MaskedEmail(tc.input); got != tc.want {
			t.Errorf("MaskedEmail(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

func TestSummaryCarriesSlugField(t *testing.T) {
	// The contract exposes the workspace slug on the member summary.
	summary := Summary{ID: "w-1", Slug: "ws-demo", Name: "Demo"}
	if summary.Slug != "ws-demo" {
		t.Fatalf("slug = %q", summary.Slug)
	}
}
