package tag

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestTagListCursorRoundtrip(t *testing.T) {
	for _, sort := range []string{"key:asc", "display_name:asc"} {
		token, err := encodeTagListCursor(sort, "wide shot", "00000000-0000-4000-8000-000000000001")
		if err != nil {
			t.Fatalf("encode %s: %v", sort, err)
		}
		cursor, err := decodeTagListCursor(token, sort)
		if err != nil {
			t.Fatalf("decode %s: %v", sort, err)
		}
		if cursor.Value != "wide shot" || cursor.ID != "00000000-0000-4000-8000-000000000001" {
			t.Fatalf("decoded %s cursor = %#v", sort, cursor)
		}
	}
	stamp := time.Now().UTC().Format(time.RFC3339Nano)
	token, err := encodeTagListCursor("created_at:desc", stamp, "00000000-0000-4000-8000-000000000002")
	if err != nil {
		t.Fatalf("encode created_at: %v", err)
	}
	cursor, err := decodeTagListCursor(token, "created_at:desc")
	if err != nil {
		t.Fatalf("decode created_at: %v", err)
	}
	if cursor.Value != stamp {
		t.Fatalf("created_at value = %q, want %q", cursor.Value, stamp)
	}
}

func TestTagListCursorRejectsGarbage(t *testing.T) {
	cases := []struct {
		name  string
		token string
		sort  string
	}{
		{"not base64", "!!!", "key:asc"},
		{"not json", "YWJj", "key:asc"},
		{"wrong version", mustEncodeCursor(t, tagListCursor{Version: 0, Sort: "key:asc", Value: "a", ID: "00000000-0000-4000-8000-000000000001"}), "key:asc"},
		{"sort mismatch", mustEncodeCursor(t, tagListCursor{Version: tagListCursorVersion, Sort: "key:asc", Value: "a", ID: "00000000-0000-4000-8000-000000000001"}), "display_name:asc"},
		{"missing id", mustEncodeCursor(t, tagListCursor{Version: tagListCursorVersion, Sort: "key:asc", Value: "a"}), "key:asc"},
		{"missing value", mustEncodeCursor(t, tagListCursor{Version: tagListCursorVersion, Sort: "key:asc", ID: "00000000-0000-4000-8000-000000000001"}), "key:asc"},
		{"bad created_at value", mustEncodeCursor(t, tagListCursor{Version: tagListCursorVersion, Sort: "created_at:desc", Value: "yesterday", ID: "00000000-0000-4000-8000-000000000001"}), "created_at:desc"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cursor, err := decodeTagListCursor(tc.token, tc.sort)
			if err == nil {
				t.Fatalf("garbage cursor %q must be rejected, got %#v", tc.token, cursor)
			}
			if err != ErrInvalidInput {
				t.Fatalf("error = %v, want ErrInvalidInput", err)
			}
		})
	}
	// An absent cursor means "first page" and never fails; the created_at
	// timestamp format is enforced only for that sort.
	if _, err := decodeTagListCursor("", "key:asc"); err != nil {
		t.Fatalf("empty cursor is the first page: %v", err)
	}
	if _, err := decodeTagListCursor(
		mustEncodeCursor(t, tagListCursor{Version: tagListCursorVersion, Sort: "key:asc", Value: "2026-08-29", ID: "00000000-0000-4000-8000-000000000001"}),
		"key:asc"); err != nil {
		t.Fatalf("key values are not timestamps: %v", err)
	}
}

func mustEncodeCursor(t *testing.T, cursor tagListCursor) string {
	t.Helper()
	payload, err := json.Marshal(cursor)
	if err != nil {
		t.Fatalf("encode cursor payload: %v", err)
	}
	return base64.RawURLEncoding.EncodeToString(payload)
}

func TestTagListCursorIsOpaque(t *testing.T) {
	token, err := encodeTagListCursor("display_name:asc", "a|b|c", "00000000-0000-4000-8000-000000000003")
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	// Delimiter-bearing display names survive the roundtrip because the cursor
	// is base64(JSON), not a delimited tuple.
	if strings.Contains(token, "|") {
		t.Fatalf("cursor must be opaque: %q", token)
	}
	cursor, err := decodeTagListCursor(token, "display_name:asc")
	if err != nil || cursor.Value != "a|b|c" {
		t.Fatalf("roundtrip = %#v, %v", cursor, err)
	}
}
