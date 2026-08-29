package asset

import (
	"strings"
	"testing"

	"agentchunzhi/internal/tag"
)

func TestImportFieldsUsesNestedFieldsAndReservedColumns(t *testing.T) {
	fields := importFields(map[string]any{
		"title":    "Shot",
		"markdown": "body",
		"source":   map[string]any{"file": "input.csv"},
		"fields":   map[string]any{"shot_size": "wide"},
		"ignored":  "value",
	})
	if len(fields) != 1 || fields["shot_size"] != "wide" {
		t.Fatalf("unexpected nested fields: %#v", fields)
	}
	flat := importFields(map[string]any{"title": "Shot", "markdown": "body", "shot_size": "wide"})
	if len(flat) != 1 || flat["shot_size"] != "wide" {
		t.Fatalf("unexpected flat fields: %#v", flat)
	}
}

func TestStringPointerRejectsNonStringValues(t *testing.T) {
	if stringPointer(42) != nil {
		t.Fatal("non-string import value should not become a title")
	}
	value := stringPointer("title")
	if value == nil || *value != "title" {
		t.Fatalf("unexpected string pointer: %#v", value)
	}
}

func TestExportAssetQueryBindsPermissionScopeAndFilters(t *testing.T) {
	query, args := exportAssetQuery("org", "workspace", "model", map[string]any{
		"filters": map[string]any{
			"q":                  "wide",
			"visibility":         "workspace",
			"publication_status": "published",
			"origin":             "imported",
			"fields":             map[string]any{"shot_size": "wide"},
		},
	}, tag.ResolvedFilter{})
	if len(args) != 8 {
		t.Fatalf("expected 8 bound arguments, got %d", len(args))
	}
	for _, fragment := range []string{"a.workspace_id = $2::uuid", "v.title", "a.visibility = $5", "v.origin = $7", "v.fields @> $8"} {
		if !strings.Contains(query, fragment) {
			t.Fatalf("query missing %q: %s", fragment, query)
		}
	}
}

func TestNormalizeMemberFiltersBuildsProjectionPredicates(t *testing.T) {
	fields, tags, err := normalizeMemberFilters(map[string]any{
		"fields": map[string]any{"shot_size": map[string]any{"eq": "wide"}},
		"tags":   map[string]any{"contains_any": []any{"camera"}},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	// Phase 3: predicates are typed values rendered into parameterized SQL by
	// field_predicate.go, not a JSON blob for a stored procedure.
	if len(fields) != 1 || fields[0].Field != "shot_size" || fields[0].Operator != "eq" || fields[0].Value != "wide" {
		t.Fatalf("unexpected field predicates: %#v", fields)
	}
	if tags != nil {
		t.Fatalf("tag jsonb predicates are retired; got %s", tags)
	}
}

func TestImportRowTagKeysExtractionAndMalformedInput(t *testing.T) {
	keys, ok, err := importRowTagKeys(map[string]any{
		"title":    "Shot",
		"tag_keys": []any{"release", " cms ", "release"},
	})
	if err != nil || !ok {
		t.Fatalf("expected present tag_keys, ok=%v err=%v", ok, err)
	}
	if len(keys) != 3 || keys[0] != "release" || keys[1] != "cms" {
		t.Fatalf("unexpected keys: %#v", keys)
	}
	if _, ok, _ := importRowTagKeys(map[string]any{"title": "Shot"}); ok {
		t.Fatal("absent tag_keys must report ok=false")
	}
	if _, ok, err := importRowTagKeys(map[string]any{"tag_keys": nil}); ok || err != nil {
		t.Fatalf("explicit null tag_keys must behave like absent, ok=%v err=%v", ok, err)
	}
	if _, _, err := importRowTagKeys(map[string]any{"tag_keys": "release"}); err == nil {
		t.Fatal("non-array tag_keys must fail")
	}
	if _, _, err := importRowTagKeys(map[string]any{"tag_keys": []any{"release", 42}}); err == nil {
		t.Fatal("non-string tag_keys entries must fail")
	}
}

func TestImportFieldsNeverCarriesTagSystemFields(t *testing.T) {
	fields := importFields(map[string]any{
		"title":    "Shot",
		"tag_keys": []any{"release"},
		"tags":     []any{"legacy"},
		"shot":     "wide",
	})
	if _, leak := fields[ImportTagKeysField]; leak {
		t.Fatal("tag_keys must never enter the dynamic fields document")
	}
	if _, leak := fields[LegacyTagsField]; leak {
		t.Fatal("legacy tags must never enter the dynamic fields document")
	}
	if len(fields) != 1 || fields["shot"] != "wide" {
		t.Fatalf("unexpected fields: %#v", fields)
	}
}

func TestExportTagKeysCellRoundTripSortedAndDeduped(t *testing.T) {
	cell := formatTagKeysCell([]string{"release", "cms", "release", "archive"})
	if cell != `["archive","cms","release"]` {
		t.Fatalf("unexpected tag_keys cell: %s", cell)
	}
	if got := formatTagKeysCell(nil); got != "[]" {
		t.Fatalf("empty tag_keys must render as [], got %s", got)
	}
	decoded := decodeTagKeysCell(cell)
	if len(decoded) != 3 || decoded[0] != "archive" {
		t.Fatalf("unexpected decoded keys: %#v", decoded)
	}
	if fallback := decodeTagKeysCell("not-json"); len(fallback) != 0 {
		t.Fatalf("unparseable cell must degrade to empty array, got %#v", fallback)
	}
}

func TestMergeVersionTagKeysSortsAndDedupes(t *testing.T) {
	merged := mergeVersionTagKeys([]versionTagKeyPair{
		{VersionID: "v1", Key: "release"},
		{VersionID: "v1", Key: "cms"},
		{VersionID: "v1", Key: "release"},
		{VersionID: "v2", Key: "archive"},
	})
	if keys := merged["v1"]; len(keys) != 2 || keys[0] != "cms" || keys[1] != "release" {
		t.Fatalf("unexpected v1 keys: %#v", keys)
	}
	if keys := merged["v2"]; len(keys) != 1 || keys[0] != "archive" {
		t.Fatalf("unexpected v2 keys: %#v", keys)
	}
	if _, ok := merged["v3"]; ok {
		t.Fatal("versions without tags must not appear")
	}
}

func TestNormalizedUnknownTagPolicyDefaultsToReject(t *testing.T) {
	if policy, ok := NormalizedUnknownTagPolicy(""); !ok || policy != UnknownTagPolicyReject {
		t.Fatalf("empty policy must default to reject, got %q ok=%v", policy, ok)
	}
	if policy, ok := NormalizedUnknownTagPolicy("create"); !ok || policy != UnknownTagPolicyCreate {
		t.Fatalf("create policy must be accepted, got %q ok=%v", policy, ok)
	}
	if _, ok := NormalizedUnknownTagPolicy("merge"); ok {
		t.Fatal("unknown policies must be rejected")
	}
}

func TestExportAssetQueryBuildsTagExistsFragments(t *testing.T) {
	query, args := exportAssetQuery("org", "workspace", "model", map[string]any{
		"filters": map[string]any{
			"tags_any":  []any{"release"},
			"tags_none": []any{"deprecated"},
		},
	}, tag.ResolvedFilter{
		Any:  []string{"11111111-1111-1111-1111-111111111111"},
		None: []string{"22222222-2222-2222-2222-222222222222"},
	})
	if len(args) != 5 {
		t.Fatalf("expected 5 bound arguments, got %d", len(args))
	}
	for _, fragment := range []string{
		"fx.asset_version_id = v.id",
		"NOT EXISTS (SELECT 1 FROM asset.asset_version_tags fn",
	} {
		if !strings.Contains(query, fragment) {
			t.Fatalf("query missing %q: %s", fragment, query)
		}
	}
}
