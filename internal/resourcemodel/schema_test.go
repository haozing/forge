package resourcemodel

import (
	"errors"
	"strings"
	"testing"
)

func validSchemas() (map[string]any, map[string]any, map[string]any, map[string]any) {
	return map[string]any{"fields": []any{map[string]any{"key": "shot_size", "type": "enum", "options": []any{map[string]any{"value": "wide", "label": "Wide"}, map[string]any{"value": "medium", "label": "Medium"}}}}, "additional_properties": false},
		map[string]any{"sections": []any{map[string]any{"key": "basic", "fields": []any{"shot_size"}}}},
		map[string]any{"columns": []any{"shot_size", "title"}, "filters": []any{"shot_size"}},
		map[string]any{
			"visibility": map[string]any{"default": "workspace", "allowed": []any{"workspace", "organization", "public"}},
			"channels": map[string]any{
				"workspace":   map[string]any{"enabled": true},
				"public_site": map[string]any{"enabled": false},
				"agent":       map[string]any{"enabled": true, "content_scope": "published"},
				"open_api":    map[string]any{"enabled": false, "content_scope": "published"},
			},
			"retrieval": map[string]any{
				"structured": map[string]any{"enabled": true},
				"fulltext":   map[string]any{"enabled": true},
				"semantic":   map[string]any{"enabled": true},
			},
			"publishing": map[string]any{"mode": "direct", "required_fields": []any{}, "require_clean_attachments": true, "require_human_confirmation": true},
		}
}

func TestValidateDynamicSchema(t *testing.T) {
	field, form, list, policy := validSchemas()
	if err := Validate("record", field, form, list, policy); err != nil {
		t.Fatalf("valid schema rejected: %v", err)
	}
}

func TestValidateRejectsReservedAndUnknownFields(t *testing.T) {
	field := map[string]any{"fields": []any{map[string]any{"key": "title", "type": "string"}}, "additional_properties": false}
	form := map[string]any{"sections": []any{map[string]any{"fields": []any{"missing"}}}}
	list := map[string]any{"columns": []any{"missing"}, "filters": []any{}}
	policy := map[string]any{}
	err := Validate("record", field, form, list, policy)
	if !errors.Is(err, ErrSchemaInvalid) {
		t.Fatalf("expected schema error, got %v", err)
	}
}

func TestValidateAcceptsNoteAndAssetReference(t *testing.T) {
	field := map[string]any{"fields": []any{map[string]any{"key": "source_asset", "type": "asset_reference"}}, "additional_properties": false}
	form := map[string]any{"sections": []any{map[string]any{"fields": []any{"source_asset"}}}}
	list := map[string]any{"columns": []any{"source_asset"}, "filters": []any{}}
	if err := Validate("note", field, form, list, map[string]any{}); err != nil {
		t.Fatalf("note asset reference schema rejected: %v", err)
	}
}

func TestValidateRejectsLegacyPropertiesSchema(t *testing.T) {
	field := map[string]any{"properties": map[string]any{"legacy": map[string]any{"type": "string"}}}
	err := Validate("record", field, map[string]any{"sections": []any{}}, map[string]any{"columns": []any{}, "filters": []any{}}, map[string]any{})
	if !errors.Is(err, ErrSchemaInvalid) {
		t.Fatalf("legacy schema should be rejected, got %v", err)
	}
}

func TestValidateRejectsTagsFieldType(t *testing.T) {
	field := map[string]any{"fields": []any{map[string]any{"key": "tagged", "type": "tags"}}, "additional_properties": false}
	err := Validate("record", field, map[string]any{"sections": []any{}}, map[string]any{"columns": []any{}, "filters": []any{}}, map[string]any{})
	if !errors.Is(err, ErrSchemaInvalid) {
		t.Fatalf("tags field type should be rejected, got %v", err)
	}
}

func TestSchemaChecksumStable(t *testing.T) {
	field, form, list, policy := validSchemas()
	first, err := SchemaChecksum("record", field, form, list, policy)
	if err != nil {
		t.Fatal(err)
	}
	second, err := SchemaChecksum("record", field, form, list, policy)
	if err != nil {
		t.Fatal(err)
	}
	if first == "" || first != second {
		t.Fatalf("checksum is not stable: %q %q", first, second)
	}
}

func TestPolicyFromJSONAcceptsFinalStructure(t *testing.T) {
	raw := []byte(`{
		"visibility": {"default": "workspace", "allowed": ["workspace", "organization", "public"]},
		"channels": {
			"workspace": {"enabled": true},
			"public_site": {"enabled": false},
			"agent": {"enabled": true, "content_scope": "published"},
			"open_api": {"enabled": false, "content_scope": "published"}
		},
		"retrieval": {"structured": {"enabled": true}, "fulltext": {"enabled": true}, "semantic": {"enabled": true}},
		"publishing": {"mode": "direct", "required_fields": [], "require_clean_attachments": true, "require_human_confirmation": true}
	}`)
	policy, err := PolicyFromJSON(raw)
	if err != nil {
		t.Fatalf("final policy structure rejected: %v", err)
	}
	if !policy.Channels["agent"].Enabled || policy.Channels["agent"].ContentScope != "published" {
		t.Fatalf("unexpected agent channel: %#v", policy.Channels["agent"])
	}
	if policy.Publishing.Mode != "direct" || !policy.Publishing.RequireCleanAttachments {
		t.Fatalf("unexpected publishing policy: %#v", policy.Publishing)
	}
	canonical := policy.ToJSON()
	again, err := PolicyFromJSON(canonical)
	if err != nil || string(again.ToJSON()) != string(canonical) {
		t.Fatalf("canonical round trip failed: %s %v", canonical, err)
	}
}

func TestPolicyFromJSONLegacyRejected(t *testing.T) {
	cases := map[string]string{
		"legacy_outlets_rejected":       `{"outlets": {"workspace": {"enabled": true}}}`,
		"legacy_agent_tool_rejected":    `{"channels": {"agent_tool": {"enabled": true}}}`,
		"legacy_member_search_rejected": `{"channels": {"member_search": {"enabled": true}}}`,
		"legacy_external_rejected":      `{"channels": {"external": {"enabled": true}}}`,
		"unknown_section_rejected":      `{"frontend": {"enabled": true}}`,
		"unknown_channel_rejected":      `{"channels": {"email": {"enabled": true}}}`,
		"unknown_retrieval_rejected":    `{"retrieval": {"lexical": {"enabled": true}}}`,
		"invalid_content_scope":         `{"channels": {"agent": {"enabled": true, "content_scope": "drafts"}}}`,
		"invalid_publishing_mode":       `{"publishing": {"mode": "auto"}}`,
		"invalid_visibility_default":    `{"visibility": {"default": "private"}}`,
		"invalid_visibility_allowed":    `{"visibility": {"default": "workspace", "allowed": ["private"]}}`,
		"scoped_channel_content_scope":  `{"channels": {"workspace": {"enabled": true, "content_scope": "all"}}}`,
	}
	for name, raw := range cases {
		if _, err := PolicyFromJSON([]byte(raw)); !errors.Is(err, ErrSchemaInvalid) {
			t.Fatalf("%s: expected schema error, got %v", name, err)
		}
	}
}

func TestValidatePolicyLegacyRejected(t *testing.T) {
	_, _, _, policy := validSchemas()
	policy["outlets"] = map[string]any{"agent_tool": map[string]any{"enabled": true}}
	err := Validate("record", mustFieldSchema(), mustFormSchema(), mustListSchema(), policy)
	if !errors.Is(err, ErrSchemaInvalid) {
		t.Fatalf("legacy outlets should be rejected, got %v", err)
	}
	if !strings.Contains(err.Error(), "outlets") {
		t.Fatalf("error should mention the legacy key, got %v", err)
	}
}

func mustFieldSchema() map[string]any {
	return map[string]any{"fields": []any{}, "additional_properties": false}
}

func mustFormSchema() map[string]any {
	return map[string]any{"sections": []any{}}
}

func mustListSchema() map[string]any {
	return map[string]any{"columns": []any{}, "filters": []any{}}
}
