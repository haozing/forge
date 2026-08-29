package asset

import "testing"

func TestPublicationTransitionsAreClosed(t *testing.T) {
	cases := []struct {
		from    string
		command string
		want    string
		ok      bool
	}{
		{"", "create", PublicationDraft, true},
		{PublicationDraft, "publish", PublicationPublished, true},
		{PublicationPublished, "publish", PublicationPublished, true},
		{PublicationDraft, "archive", PublicationArchived, true},
		{PublicationPublished, "archive", PublicationArchived, true},
		{PublicationArchived, "restore", PublicationDraft, true},
		// Forbidden jumps.
		{PublicationArchived, "publish", "", false},
		{PublicationArchived, "archive", "", false},
		{PublicationPublished, "restore", "", false},
		{PublicationDraft, "restore", "", false},
		{PublicationPublished, "create", "", false},
		{"", "publish", "", false},
	}
	for _, tc := range cases {
		got, err := TransitionTarget(tc.from, tc.command)
		if tc.ok && err != nil {
			t.Fatalf("%s --%s--> must be legal, got %v", tc.from, tc.command, err)
		}
		if !tc.ok && err == nil {
			t.Fatalf("%s --%s--> must be illegal", tc.from, tc.command)
		}
		if tc.ok && got != tc.want {
			t.Fatalf("%s --%s--> = %s, want %s", tc.from, tc.command, got, tc.want)
		}
	}
}

func TestContentChecksumIsOrderInsensitiveAndSetSensitive(t *testing.T) {
	fields := map[string]any{"a": 1.0}
	base := ContentChecksum("t", "s", "m", fields, []string{"tag-1", "tag-2"}, []string{"att-1"})
	reordered := ContentChecksum("t", "s", "m", fields, []string{"tag-2", "tag-1"}, []string{"att-1"})
	if base != reordered {
		t.Fatal("checksum must be insensitive to tag order")
	}
	changed := ContentChecksum("t", "s", "m", fields, []string{"tag-1", "tag-2", "tag-3"}, []string{"att-1"})
	if base == changed {
		t.Fatal("checksum must change when the tag set changes")
	}
	contentChanged := ContentChecksum("t2", "s", "m", fields, []string{"tag-1", "tag-2"}, []string{"att-1"})
	if base == contentChanged {
		t.Fatal("checksum must change when content changes")
	}
}

func TestOriginAndConfirmationContract(t *testing.T) {
	for _, value := range []string{OriginHuman, OriginImported, OriginAIGenerated, OriginAIAssisted} {
		if !ValidOrigin(value) {
			t.Fatalf("origin %s must be valid", value)
		}
	}
	if ValidOrigin("raw") || ValidOrigin("ai_generated ") {
		t.Fatal("legacy or padded origins must be rejected")
	}
	if !ValidConfirmation(ConfirmationUnconfirmed) || !ValidConfirmation(ConfirmationHumanConfirmed) {
		t.Fatal("confirmation contract drifted")
	}
	if ValidConfirmation("human_unconfirmed") {
		t.Fatal("unknown confirmation must be rejected")
	}
}
