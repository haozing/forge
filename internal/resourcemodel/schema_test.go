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

func TestValidateFieldDefaultMatrix(t *testing.T) {
	basePolicy := func() map[string]any {
		_, _, _, p := validSchemas()
		return p
	}
	cases := []struct {
		name    string
		field   map[string]any
		wantErr bool
		code    string
	}{
		{"string_ok", map[string]any{"key": "demo", "type": "string", "default": "draft"}, false, ""},
		{"text_ok", map[string]any{"key": "demo", "type": "text", "default": "notes"}, false, ""},
		{"markdown_ok", map[string]any{"key": "demo", "type": "markdown", "default": "**hi**"}, false, ""},
		{"string_number_rejected", map[string]any{"key": "demo", "type": "string", "default": 3.0}, true, "invalid_default"},
		{"integer_ok", map[string]any{"key": "demo", "type": "integer", "default": 7.0}, false, ""},
		{"integer_fraction_rejected", map[string]any{"key": "demo", "type": "integer", "default": 7.5}, true, "invalid_default"},
		{"integer_string_rejected", map[string]any{"key": "demo", "type": "integer", "default": "7"}, true, "invalid_default"},
		{"number_ok", map[string]any{"key": "demo", "type": "number", "default": 2.5}, false, ""},
		{"boolean_ok", map[string]any{"key": "demo", "type": "boolean", "default": true}, false, ""},
		{"boolean_string_rejected", map[string]any{"key": "demo", "type": "boolean", "default": "yes"}, true, "invalid_default"},
		{"date_ok", map[string]any{"key": "demo", "type": "date", "default": "2026-09-03"}, false, ""},
		{"date_format_rejected", map[string]any{"key": "demo", "type": "date", "default": "09/03/2026"}, true, "invalid_default"},
		{"datetime_ok", map[string]any{"key": "demo", "type": "datetime", "default": "2026-09-03T10:00:00Z"}, false, ""},
		{"datetime_format_rejected", map[string]any{"key": "demo", "type": "datetime", "default": "2026-09-03"}, true, "invalid_default"},
		{"enum_ok", map[string]any{"key": "demo", "type": "enum", "options": []any{map[string]any{"value": "a", "label": "A"}}, "default": "a"}, false, ""},
		{"enum_not_option_rejected", map[string]any{"key": "demo", "type": "enum", "options": []any{map[string]any{"value": "a", "label": "A"}}, "default": "b"}, true, "invalid_default"},
		{"multiselect_ok", map[string]any{"key": "demo", "type": "multiselect", "options": []any{map[string]any{"value": "a", "label": "A"}, map[string]any{"value": "b", "label": "B"}}, "default": []any{"a", "b"}}, false, ""},
		{"multiselect_offoption_rejected", map[string]any{"key": "demo", "type": "multiselect", "options": []any{map[string]any{"value": "a", "label": "A"}}, "default": []any{"a", "z"}}, true, "invalid_default"},
		{"multiselect_scalar_rejected", map[string]any{"key": "demo", "type": "multiselect", "options": []any{map[string]any{"value": "a", "label": "A"}}, "default": "a"}, true, "invalid_default"},
		{"object_default_rejected", map[string]any{"key": "demo", "type": "object", "properties": map[string]any{"x": map[string]any{"type": "string"}}, "default": map[string]any{"x": "y"}}, true, "unsupported_default"},
		{"array_default_rejected", map[string]any{"key": "demo", "type": "array", "items": map[string]any{"type": "string"}, "default": []any{"a"}}, true, "unsupported_default"},
		{"asset_reference_default_rejected", map[string]any{"key": "demo", "type": "asset_reference", "default": map[string]any{"asset_id": "x", "asset_version_id": "y"}}, true, "unsupported_default"},
	}
	for _, testCase := range cases {
		schema := map[string]any{"fields": []any{testCase.field}, "additional_properties": false}
		err := Validate("record", schema, mustFormSchema(), mustListSchema(), basePolicy())
		if testCase.wantErr {
			if !errors.Is(err, ErrSchemaInvalid) {
				t.Fatalf("%s: expected schema error, got %v", testCase.name, err)
			}
			schemaErr := err.(*SchemaValidationError)
			found := false
			for _, item := range schemaErr.Issues {
				if strings.HasSuffix(item.Path, ".default") && item.Code == testCase.code {
					found = true
				}
			}
			if !found {
				t.Fatalf("%s: expected issue code %s on .default, got %+v", testCase.name, testCase.code, schemaErr.Issues)
			}
		} else if err != nil {
			t.Fatalf("%s: unexpected error %v", testCase.name, err)
		}
	}
}

func TestValidateNestedFieldDefaultRejected(t *testing.T) {
	_, _, _, policy := validSchemas()
	schema := map[string]any{"fields": []any{map[string]any{
		"key": "demo", "type": "object",
		"properties": map[string]any{"child": map[string]any{"type": "string", "default": "x"}},
	}}, "additional_properties": false}
	err := Validate("record", schema, mustFormSchema(), mustListSchema(), policy)
	if !errors.Is(err, ErrSchemaInvalid) {
		t.Fatalf("nested default should be rejected, got %v", err)
	}
	found := false
	for _, item := range err.(*SchemaValidationError).Issues {
		if strings.Contains(item.Path, "properties.child.default") && item.Code == "unsupported_default" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected unsupported_default on nested path, got %+v", err.(*SchemaValidationError).Issues)
	}
}

func TestSchemaChecksumChangesWithDefault(t *testing.T) {
	fieldSchema := map[string]any{"fields": []any{map[string]any{"key": "demo", "type": "string"}}, "additional_properties": false}
	_, _, _, policy := validSchemas()
	before, err := SchemaChecksum("record", fieldSchema, mustFormSchema(), mustListSchema(), policy)
	if err != nil {
		t.Fatal(err)
	}
	fieldSchema["fields"].([]any)[0].(map[string]any)["default"] = "seed"
	after, err := SchemaChecksum("record", fieldSchema, mustFormSchema(), mustListSchema(), policy)
	if err != nil {
		t.Fatal(err)
	}
	if before == after {
		t.Fatal("checksum must change when a default is added")
	}
}
