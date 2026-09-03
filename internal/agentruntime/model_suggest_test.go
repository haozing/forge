package agentruntime

import (
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"agentchunzhi/internal/resourcemodel"
)

func TestValidateSuggestionAcceptsCleanDraft(t *testing.T) {
	suggestion := &modelSuggestionEnvelope{
		ModelKey:    "shot_library",
		Name:        "镜头库",
		ContentKind: "record",
		FieldSchema: map[string]any{
			"additional_properties": false,
			"fields": []any{
				map[string]any{"key": "shot_size", "type": "enum", "options": []any{map[string]any{"value": "wide", "label": "远景"}}},
				map[string]any{"key": "duration", "type": "number", "default": 3.5},
			},
		},
	}
	if issues := validateSuggestion(suggestion); len(issues) > 0 {
		t.Fatalf("clean draft must validate, got %+v", issues)
	}
}

func TestValidateSuggestionReturnsStructuredIssues(t *testing.T) {
	suggestion := &modelSuggestionEnvelope{
		ModelKey:    "broken",
		Name:        "坏模型",
		ContentKind: "book",
		FieldSchema: map[string]any{
			"additional_properties": false,
			"fields": []any{
				map[string]any{"key": "Bad_Key", "type": "no_such_type"},
				map[string]any{"key": "title", "type": "string"},
			},
		},
	}
	issues := validateSuggestion(suggestion)
	if len(issues) < 3 {
		t.Fatalf("expected content_kind, key pattern, reserved key and type issues, got %+v", issues)
	}
	codes := map[string]bool{}
	for _, item := range issues {
		codes[item.Code] = true
	}
	if !codes["invalid_content_kind"] || !codes["invalid_key"] || !codes["reserved_key"] || !codes["unsupported_type"] {
		t.Fatalf("missing expected issue codes: %+v", issues)
	}
}

func TestDefaultModelSchemasProduceValidDocument(t *testing.T) {
	form, list, policy := defaultModelSchemas(map[string]any{
		"additional_properties": false,
		"fields":                []any{map[string]any{"key": "note", "type": "string", "default": ""}},
	})
	if err := resourcemodel.Validate("record", map[string]any{
		"additional_properties": false,
		"fields":                []any{map[string]any{"key": "note", "type": "string", "default": ""}},
	}, form, list, policy); err != nil {
		t.Fatalf("assembled defaults must validate: %v", err)
	}
}

func TestModelDraftToolErrorRendersSchemaIssuesAsJSON(t *testing.T) {
	inner := resourcemodel.Validate("record",
		map[string]any{"fields": []any{map[string]any{"key": "demo", "type": "nope"}}, "additional_properties": false},
		map[string]any{"sections": []any{}}, map[string]any{"columns": []any{}, "filters": []any{}},
		map[string]any{})
	var schemaErr *resourcemodel.SchemaValidationError
	if !errors.As(inner, &schemaErr) {
		t.Fatalf("expected schema error, got %v", inner)
	}
	rendered := modelDraftToolError(fmt.Errorf("wrap: %w", inner)).Error()
	if len(rendered) == 0 || rendered[:20] != "model_schema_invalid" {
		t.Fatalf("schema issues must render as model_schema_invalid JSON, got %q", rendered)
	}
	var issues []resourcemodel.ValidationIssue
	payload := rendered[len("model_schema_invalid: "):]
	if err := json.Unmarshal([]byte(payload), &issues); err != nil || len(issues) == 0 {
		t.Fatalf("rendered payload must be decodable JSON issues, got %q", payload)
	}
}
