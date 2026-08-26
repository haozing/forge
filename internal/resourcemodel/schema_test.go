package resourcemodel

import (
	"errors"
	"testing"
)

func validSchemas() (map[string]any, map[string]any, map[string]any, map[string]any) {
	return map[string]any{"fields": []any{map[string]any{"key": "shot_size", "type": "enum", "options": []any{map[string]any{"value": "wide", "label": "Wide"}, map[string]any{"value": "medium", "label": "Medium"}}}}, "additional_properties": false},
		map[string]any{"sections": []any{map[string]any{"key": "basic", "fields": []any{"shot_size"}}}},
		map[string]any{"columns": []any{"shot_size", "title"}, "filters": []any{"shot_size"}},
		map[string]any{"outlets": map[string]any{"workspace": map[string]any{"enabled": true}}}
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

func TestNormalizePolicyExternalOutlet(t *testing.T) {
	policy, err := NormalizePolicy(map[string]any{
		"outlets": map[string]any{
			"external":      map[string]any{"enabled": true},
			"open_api":      map[string]any{"enabled": true},
			"member_search": map[string]any{"enabled": true},
		},
	})
	if err != nil {
		t.Fatalf("NormalizePolicy returned error: %v", err)
	}
	outlets := policy["outlets"].(map[string]any)
	if _, ok := outlets["external"]; ok {
		t.Fatal("external outlet should not be persisted")
	}
	if _, ok := outlets["agent_tool"]; !ok {
		t.Fatal("legacy Agent outlets should normalize to agent_tool")
	}
	if _, ok := outlets["workspace"]; !ok {
		t.Fatal("member_search outlet should normalize to workspace")
	}
}
