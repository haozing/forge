package site

// public_test.go — the pure rules of the public read face (stage 5 P5-3):
// the D2 binding merge, the §3.4 fields whitelist projection, the D4 ETag
// derivation and the conditional-GET branch, the D5' visitor resolution
// fallbacks, the D3 summary truncation and the homepage config parse. All of
// these run without a database; the SQL paths are exercised by the real-
// database acceptance run.

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"agentchunzhi/internal/auth"
	agentquery "agentchunzhi/internal/query"
)

func hit(assetID, title string, score *float64, published time.Time) agentquery.Item {
	return agentquery.Item{
		AssetID:     assetID,
		Title:       title,
		Summary:     "plain text",
		PublishedAt: &published,
		Score:       score,
	}
}

func fact(assetID, displayPath string, sortOrder int, publishedAt *time.Time) boundFact {
	updatedAt := time.Date(2026, 8, 2, 0, 0, 0, 0, time.UTC)
	return boundFact{
		Binding: Binding{
			AssetID:            assetID,
			DisplayPath:        displayPath,
			SortOrder:          sortOrder,
			DisplayPublishedAt: publishedAt,
		},
		ContentKind: "document",
		UpdatedAt:   updatedAt,
	}
}

// TestMergePublicPostsDropsUnboundHits pins the D2 whitelist rule: a query
// hit without a site binding never renders, and a binding whose asset the
// query did not return (e.g. just archived) never renders either.
func TestMergePublicPostsDropsUnboundHits(t *testing.T) {
	published := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	hits := []agentquery.Item{
		hit("00000000-0000-4000-8000-00000000000a", "bound", nil, published),
		hit("00000000-0000-4000-8000-00000000000b", "unbound", nil, published),
	}
	facts := map[string]boundFact{
		// Only the first hit is bound. A second binding exists for an asset
		// the query did not return — it must not appear either.
		"00000000-0000-4000-8000-00000000000a": fact("00000000-0000-4000-8000-00000000000a", "posts/bound", 0, nil),
		"00000000-0000-4000-8000-00000000000c": fact("00000000-0000-4000-8000-00000000000c", "posts/ghost", 0, nil),
	}
	items := mergePublicPosts(hits, facts)
	if len(items) != 1 {
		t.Fatalf("items = %d, want only the single bound hit", len(items))
	}
	if items[0].DisplayPath != "posts/bound" || items[0].AssetID != "00000000-0000-4000-8000-00000000000a" {
		t.Fatalf("item = %+v, want the bound display path mapping", items[0])
	}
}

// TestMergePublicPostsOrdersBySortOrderThenScore pins the D2 ordering:
// binding sort_order ASC first, then query score DESC inside one order, then
// published_at DESC, with the asset id as the deterministic tie-break.
func TestMergePublicPostsOrdersBySortOrderThenScore(t *testing.T) {
	base := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	idA := "00000000-0000-4000-8000-00000000000a"
	idB := "00000000-0000-4000-8000-00000000000b"
	idC := "00000000-0000-4000-8000-00000000000c"
	idD := "00000000-0000-4000-8000-00000000000d"
	hits := []agentquery.Item{
		hit(idA, "order1 low score", floatPtr(0.5), base),
		hit(idB, "order0", nil, base),
		hit(idC, "order1 high score", floatPtr(0.9), base),
		hit(idD, "order1 unscored", nil, base),
	}
	facts := map[string]boundFact{
		idA: fact(idA, "a", 1, nil),
		idB: fact(idB, "b", 0, nil),
		idC: fact(idC, "c", 1, nil),
		idD: fact(idD, "d", 1, nil),
	}
	items := mergePublicPosts(hits, facts)
	got := ""
	for _, item := range items {
		got += item.DisplayPath
	}
	// order0 leads; inside order1 the scored hits lead by score DESC, the
	// unscored ones follow by published_at DESC/asset id tie-break.
	if got != "bcad" {
		t.Fatalf("order = %q, want %q", got, "bcad")
	}
}

// TestMergePublicPostsMapsDisplayPublishedAt pins the double-source published
// timestamp: the binding mirror wins over the asset published_at.
func TestMergePublicPostsMapsDisplayPublishedAt(t *testing.T) {
	assetPublished := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	bindingMirror := time.Date(2026, 7, 15, 0, 0, 0, 0, time.UTC)
	id := "00000000-0000-4000-8000-00000000000a"
	items := mergePublicPosts(
		[]agentquery.Item{hit(id, "t", nil, assetPublished)},
		map[string]boundFact{id: fact(id, "a", 0, &bindingMirror)},
	)
	if items[0].PublishedAt == nil || !items[0].PublishedAt.Equal(bindingMirror) {
		t.Fatalf("published_at = %v, want the binding mirror %v", items[0].PublishedAt, bindingMirror)
	}
}

func floatPtr(value float64) *float64 { return &value }

// TestProjectFieldsWhitelist pins the §3.4 fields projection: only
// schema-declared keys survive; pipeline-internal or operator-added keys are
// stripped even when the version carries them.
func TestProjectFieldsWhitelist(t *testing.T) {
	schema := ParseFieldSchema(json.RawMessage(`{"fields":[{"key":"author","type":"text"},{"key":"rating","type":"number"}]}`))
	fields := map[string]json.RawMessage{
		"author":         json.RawMessage(`"Ada"`),
		"rating":         json.RawMessage(`4.5`),
		"_internal_note": json.RawMessage(`"secret"`),
		"retrieval_hint": json.RawMessage(`{"boost":2}`),
	}
	projected := ProjectFields(fields, schema)
	if len(projected) != 2 {
		t.Fatalf("projected keys = %d, want 2", len(projected))
	}
	if string(projected["author"]) != `"Ada"` {
		t.Fatalf("author = %s, want the verbatim raw value", projected["author"])
	}
	if string(projected["rating"]) != `4.5` {
		t.Fatalf("rating = %s, want the verbatim number", projected["rating"])
	}
	if _, leaked := projected["_internal_note"]; leaked {
		t.Fatal("internal key must be stripped")
	}
	if _, leaked := projected["retrieval_hint"]; leaked {
		t.Fatal("undeclared key must be stripped")
	}
	// A schema-declared key the version does not carry stays absent, and an
	// empty schema projects nothing (fail closed).
	if got := ProjectFields(fields, ParseFieldSchema(json.RawMessage(`{"fields":[]}`))); len(got) != 0 {
		t.Fatal("empty schema must project nothing")
	}
	if got := ParseFieldSchema(json.RawMessage(`not-json`)); len(got) != 0 {
		t.Fatal("malformed schema must fail closed to an empty whitelist")
	}
}

// TestDetailETagStableAndSensitive pins the D4 detail fingerprint: stable for
// identical inputs, sensitive to every component.
func TestDetailETagStableAndSensitive(t *testing.T) {
	touch := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	base := DetailETag(3, 7, "version-a", touch)
	if base != DetailETag(3, 7, "version-a", touch) {
		t.Fatal("identical inputs must yield the same etag")
	}
	if base == DetailETag(4, 7, "version-a", touch) {
		t.Fatal("site revision must rotate the etag")
	}
	if base == DetailETag(3, 8, "version-a", touch) {
		t.Fatal("asset revision must rotate the etag")
	}
	if base == DetailETag(3, 7, "version-b", touch) {
		t.Fatal("version id must rotate the etag")
	}
	if base == DetailETag(3, 7, "version-a", touch.Add(time.Second)) {
		t.Fatal("binding touch time must rotate the etag")
	}
}

// TestListETagStableAndSensitive pins the D4 list fingerprint: the same item
// set yields the same etag, any republish/rebind/reorder rotates it.
func TestListETagStableAndSensitive(t *testing.T) {
	updated := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	items := []PublicPost{
		{AssetID: "a", DisplayPath: "a", UpdatedAt: &updated},
		{AssetID: "b", DisplayPath: "b", UpdatedAt: &updated},
	}
	base := ListETag(2, items)
	if base != ListETag(2, items) {
		t.Fatal("identical item sets must yield the same etag")
	}
	if base == ListETag(3, items) {
		t.Fatal("site revision must rotate the etag")
	}
	reordered := []PublicPost{items[1], items[0]}
	if base == ListETag(2, reordered) {
		t.Fatal("reordering must rotate the etag")
	}
	changed := append([]PublicPost{}, items...)
	newTime := updated.Add(time.Minute)
	changed[0] = PublicPost{AssetID: "a", DisplayPath: "a", UpdatedAt: &newTime}
	if base == ListETag(2, changed) {
		t.Fatal("a content change must rotate the etag")
	}
	if ListETag(2, nil) == ListETag(2, []PublicPost{{AssetID: "a"}}) {
		t.Fatal("empty and non-empty pages must differ")
	}
}

// TestETagMatches pins the If-None-Match comparison of the public face:
// quoted validators, W/ weak prefixes, validator lists, the "*" wildcard and
// the absent-header behavior.
func TestETagMatches(t *testing.T) {
	etag := "abc123"
	cases := []struct {
		header string
		want   bool
	}{
		{"", false},                 // absent header never matches (no 304)
		{`"abc123"`, true},          // exact quoted validator
		{"abc123", true},            // unquoted tolerated
		{`W/"abc123"`, true},        // weak comparison
		{`"xyz", "abc123"`, true},   // validator list
		{"*", true},                 // wildcard
		{`"abc124"`, false},         // different representation
		{`W/"abc124"`, false},       // different, weak
	}
	for _, tc := range cases {
		if got := ETagMatches(tc.header, etag); got != tc.want {
			t.Fatalf("ETagMatches(%q, %q) = %v, want %v", tc.header, etag, got, tc.want)
		}
	}
}

// TestVisitorResolutionMatrix pins the D5' identity fill: anonymous visitors
// and cross-organization sessions stay zero-valued, a same-organization
// member keeps its identity (the workspace EXISTS degrades without a store
// and the compiler re-verifies every claim anyway).
func TestVisitorResolutionMatrix(t *testing.T) {
	reader := &PublicReader{}
	orgID := "00000000-0000-4000-8000-00000000000a"
	wsID := "00000000-0000-4000-8000-000000000001"
	item := Site{OrganizationID: orgID, WorkspaceID: wsID}

	anonymous := reader.visitor(nil, item, auth.Principal{})
	if anonymous.UserType != "" || anonymous.UserID != "" || anonymous.WorkspaceMember {
		t.Fatalf("anonymous visitor = %+v, want the zero identity", anonymous)
	}
	foreign := reader.visitor(nil, item, auth.Principal{
		UserType:       auth.UserTypeMember,
		UserID:         "00000000-0000-4000-8000-00000000000b",
		OrganizationID: "00000000-0000-4000-8000-00000000000f",
	})
	if foreign.UserType != "" || foreign.UserID != "" {
		t.Fatalf("cross-organization session = %+v, want the zero identity", foreign)
	}
	member := reader.visitor(nil, item, auth.Principal{
		UserType:       auth.UserTypeMember,
		UserID:         "00000000-0000-4000-8000-00000000000b",
		OrganizationID: orgID,
	})
	if member.UserType != auth.UserTypeMember || member.UserID != "00000000-0000-4000-8000-00000000000b" ||
		member.OrganizationID != orgID {
		t.Fatalf("member visitor = %+v, want identity preserved", member)
	}
	if member.WorkspaceMember {
		t.Fatal("without a store the workspace flag must degrade to false (compiler re-verifies)")
	}
}

// TestSafeSummaryStripsMarkdownAndTruncates pins the D3 list truncation.
func TestSafeSummaryStripsMarkdownAndTruncates(t *testing.T) {
	stripped := SafeSummary("# Heading\n\nSome **bold** and `code` text [link](https://example.com) ![alt](https://img).", 280)
	for _, marker := range []string{"#", "**", "`", "](", "https://example.com"} {
		if contains(stripped, marker) {
			t.Fatalf("summary %q still carries markdown marker %q", stripped, marker)
		}
	}
	if !contains(stripped, "Heading") || !contains(stripped, "link") || !contains(stripped, "alt") {
		t.Fatalf("summary %q lost visible text", stripped)
	}
	truncated := SafeSummary("abcdefgh", 5)
	if runeCount(truncated) != 6 { // 5 runes plus the ellipsis
		t.Fatalf("truncated = %q (%d runes), want a 5-rune cap plus ellipsis", truncated, runeCount(truncated))
	}
	if SafeSummary("", 280) != "" {
		t.Fatal("empty summary stays empty")
	}
}

// TestParseHomepageConfig pins the tolerant homepage_config.sections parse.
func TestParseHomepageConfig(t *testing.T) {
	if ParseHomepageConfig(json.RawMessage(`{}`)) != nil {
		t.Fatal("empty config yields no sections")
	}
	if ParseHomepageConfig(json.RawMessage(`broken`)) != nil {
		t.Fatal("malformed config yields no sections")
	}
	sections := ParseHomepageConfig(json.RawMessage(`{"sections":[
		{"type":"latest","limit":5,"title":"Newest"},
		{"type":"featured"},
		{"type":"column","section_slug":"news"},
		{"type":"future-thing"}
	]}`))
	if len(sections) != 4 {
		t.Fatalf("sections = %d, want 4", len(sections))
	}
	if sections[0].Type != "latest" || sections[0].Limit != 5 || sections[0].Title != "Newest" {
		t.Fatalf("latest section = %+v", sections[0])
	}
	if sections[2].SectionSlug != "news" {
		t.Fatalf("column section = %+v", sections[2])
	}
	if sections[3].Type != "future-thing" {
		t.Fatalf("unknown section carried through = %+v", sections[3])
	}
}

// TestNormalizePublicLimit pins the page-size clamp: the default fills
// absent values and the cap matches the query contract envelope.
func TestNormalizePublicLimit(t *testing.T) {
	cases := []struct{ in, want int }{
		{0, publicDefaultLimit}, {-3, publicDefaultLimit},
		{1, 1}, {20, 20}, {publicMaxLimit, publicMaxLimit},
		{1000, publicMaxLimit},
	}
	for _, tc := range cases {
		if got := normalizePublicLimit(tc.in); got != tc.want {
			t.Fatalf("normalizePublicLimit(%d) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

func contains(value, fragment string) bool {
	return strings.Contains(value, fragment)
}

func runeCount(value string) int { return utf8.RuneCountInString(value) }
