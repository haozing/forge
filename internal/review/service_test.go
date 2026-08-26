package review

import (
	"errors"
	"testing"
	"time"
)

func TestReviewCursorRoundTrip(t *testing.T) {
	submittedAt := time.Date(2026, 8, 26, 8, 30, 0, 123000000, time.UTC)
	id := "00000000-0000-4000-8000-000000000001"
	encoded := encodeReviewCursor(submittedAt, id)
	cursor, err := decodeReviewCursor(encoded)
	if err != nil || cursor.ID != id || cursor.SubmittedAt == "" {
		t.Fatalf("unexpected cursor: %#v err=%v", cursor, err)
	}
	if _, err := decodeReviewCursor("invalid"); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("invalid cursor should fail, got %v", err)
	}
}

func TestParseReviewTime(t *testing.T) {
	if value, err := parseReviewTime(""); err != nil || value != nil {
		t.Fatalf("empty time should be omitted: %#v %v", value, err)
	}
	if value, err := parseReviewTime("2026-08-26T08:30:00+08:00"); err != nil || value == nil {
		t.Fatalf("RFC3339 time should parse: %#v %v", value, err)
	}
	if _, err := parseReviewTime("2026-08-26"); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("date-only filter should fail, got %v", err)
	}
}
