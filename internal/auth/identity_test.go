package auth

import "testing"

func TestValidatePassword(t *testing.T) {
	cases := []struct {
		name     string
		password string
		wantErr  bool
	}{
		{"exactly 12 runes", "abcdefghijkl", false},
		{"long with unicode", "P@sswörds-腳本-🔐-extra-long-password", false},
		{"too short", "short", true},
		{"eleven runes", "abcdefghijk", true},
		{"empty", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidatePassword(tc.password)
			if (err != nil) != tc.wantErr {
				t.Fatalf("ValidatePassword(%q) error = %v, wantErr %v", tc.password, err, tc.wantErr)
			}
		})
	}
}

func TestValidatePasswordBytesLimit(t *testing.T) {
	// 129 ASCII runes is over the rune limit; a 200-rune CJK string is over
	// 512 bytes even though the rune count would be fine.
	if err := ValidatePassword(string(make([]byte, 0)) + "abcdefghijkmn"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	heavy := make([]rune, 200)
	for i := range heavy {
		heavy[i] = '腳'
	}
	if err := ValidatePassword(string(heavy)); err == nil {
		t.Fatal("expected byte-length overflow to be rejected")
	}
}

func TestClientIPPrefix(t *testing.T) {
	cases := []struct {
		addr string
		want string
	}{
		{"203.0.113.45:8443", "203.0.113.0"},
		{"198.51.100.7:80", "198.51.100.0"},
		{"203.0.113.99", "203.0.113.0"},
		// /56 for IPv6: the first three hextets plus the top byte of the fourth.
		{"[2001:db8:1:2:3:4:5:6]:443", "2001:db8:1::"},
		{"[2001:db8:1:2::]:8080", "2001:db8:1::"},
		// Unbracketed IPv6 with a port cannot be split and degrades to
		// the stable "invalid" bucket instead of crashing.
		{"2001:db8:1:2:3:4:5:6:443", "invalid"},
		{"garbage", "invalid"},
	}
	for _, tc := range cases {
		if got := ClientIPPrefix(tc.addr); got != tc.want {
			t.Errorf("ClientIPPrefix(%q) = %q, want %q", tc.addr, got, tc.want)
		}
	}
}
