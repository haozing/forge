package asset

import (
	"strings"
	"testing"
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
			"tags":               []any{"camera"},
			"fields":             map[string]any{"shot_size": "wide"},
		},
	})
	if len(args) != 8 {
		t.Fatalf("expected 8 bound arguments, got %d", len(args))
	}
	for _, fragment := range []string{"a.workspace_id = $2::uuid", "v.title", "a.visibility = $5", "v.tags @>", "v.fields @>"} {
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
	if string(fields) != `[{"field":"shot_size","operator":"eq","value":"wide"}]` {
		t.Fatalf("unexpected field predicates: %s", fields)
	}
	if string(tags) != `[{"field":"tags","operator":"contains_any","value":["camera"]}]` {
		t.Fatalf("unexpected tag predicates: %s", tags)
	}
}
