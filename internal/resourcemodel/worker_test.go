package resourcemodel

import "testing"

func TestTransformFieldsAppliesMappingAndDefaults(t *testing.T) {
	schema := []byte(`{"fields":[{"key":"shot_size","type":"string"},{"key":"camera","type":"string","required":true}]}`)
	result := transformFields(map[string]any{"size": "wide"}, map[string]any{
		"mapping":  map[string]any{"shot_size": "size"},
		"defaults": map[string]any{"camera": "A-cam"},
	}, schema)
	if result["shot_size"] != "wide" || result["camera"] != "A-cam" {
		t.Fatalf("unexpected transformed fields: %#v", result)
	}
	if err := validateMigrationFields(schema, result); err != nil {
		t.Fatalf("transformed fields should validate: %v", err)
	}
}

func TestValidateMigrationFieldsRejectsUnknownRequiredValues(t *testing.T) {
	schema := []byte(`{"properties":{"shot_size":{"type":"string"},"camera":{"type":"string"}},"required":["camera"],"additionalProperties":false}`)
	if err := validateMigrationFields(schema, map[string]any{"shot_size": "wide"}); err == nil {
		t.Fatal("missing required field should fail migration validation")
	}
	if err := validateMigrationFields(schema, map[string]any{"camera": "A-cam", "other": true}); err == nil {
		t.Fatal("unknown field should fail migration validation")
	}
}
