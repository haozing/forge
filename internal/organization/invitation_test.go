package organization

import "testing"

func TestNormalizeEmail(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  string
		isErr bool
	}{
		{"trims and case folds", "  Alice@Example.COM ", "alice@example.com", false},
		{"already normalized", "alice@example.com", "alice@example.com", false},
		{"empty", "", "", true},
		{"whitespace only", "   ", "", true},
		{"missing domain", "alice", "", true},
		{"missing tld", "alice@localhost", "", true},
		{"contains space", "alice smith@example.com", "", true},
		{"two at signs", "alice@@example.com", "", true},
		{"too long", string(make([]byte, 0)) + repeat('a', 255) + "@example.com", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := NormalizeEmail(tc.input)
			if tc.isErr {
				if err == nil {
					t.Fatalf("NormalizeEmail(%q) = %q, want error", tc.input, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("NormalizeEmail(%q) failed: %v", tc.input, err)
			}
			if got != tc.want {
				t.Fatalf("NormalizeEmail(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

func repeat(ch byte, count int) string {
	raw := make([]byte, count)
	for i := range raw {
		raw[i] = ch
	}
	return string(raw)
}

func TestMaskEmail(t *testing.T) {
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
		if got := maskEmail(tc.input); got != tc.want {
			t.Errorf("maskEmail(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}
