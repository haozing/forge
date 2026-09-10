package site

// modelview_test.go — the model_views whitelist contract: shape validation,
// fail-closed filtering and publish-time pruning. The DB-facing reference
// checks (model ownership, live field_schema) are covered by QA scripts;
// everything here is pure.

import (
	"encoding/json"
	"testing"
)

func shotSchemas() map[string]map[string]string {
	return map[string]map[string]string{
		"11111111-1111-4111-8111-111111111111": {
			"shot_size": "enum", "camera_angle": "enum", "lens_mm": "integer",
			"location": "string", "shot_date": "date", "internal_note": "string",
		},
	}
}

func TestValidateModelViewShape(t *testing.T) {
	ok := map[string]ModelView{
		"11111111-1111-4111-8111-111111111111": {
			CardFields:   []string{"shot_size", "lens_mm"},
			DetailFields: []string{"shot_size", "camera_angle", "lens_mm", "location", "shot_date"},
		},
	}
	if err := ValidateModelViewShape(ok); err != nil {
		t.Fatalf("valid shape rejected: %v", err)
	}

	badKey := map[string]ModelView{"not-a-uuid": {DetailFields: []string{"shot_size"}}}
	if err := ValidateModelViewShape(badKey); err == nil || err.ModelID != "not-a-uuid" {
		t.Fatalf("non-uuid key must be rejected, got %v", err)
	}

	tooMany := map[string]ModelView{"11111111-1111-4111-8111-111111111111": {
		DetailFields: []string{"f1", "f2", "f3", "f4", "f5", "f6", "f7", "f8", "f9", "f10", "f11", "f12", "f13"},
	}}
	if err := ValidateModelViewShape(tooMany); err == nil {
		t.Fatal("detail whitelist over 12 must be rejected")
	}

	dup := map[string]ModelView{"11111111-1111-4111-8111-111111111111": {
		CardFields: []string{"shot_size", "shot_size"},
	}}
	if err := ValidateModelViewShape(dup); err == nil {
		t.Fatal("duplicate field must be rejected")
	}
}

func TestWhitelistFieldsIsFailClosedAndOrdered(t *testing.T) {
	schemaTypes := map[string]string{"shot_size": "enum", "lens_mm": "integer", "shot_date": "date", "secret": "string"}
	projected := map[string]json.RawMessage{
		"shot_size": json.RawMessage(`"特写"`),
		"lens_mm":   json.RawMessage(`35`),
		"shot_date": json.RawMessage(`"2026-09-10"`),
		"secret":    json.RawMessage(`"do-not-publish"`),
		"ghost":     json.RawMessage(`1`),
		"empty_key": json.RawMessage(`null`),
	}

	empty := WhitelistFields(projected, schemaTypes, ModelView{}, false)
	if len(empty) != 0 {
		t.Fatalf("empty whitelist must publish zero fields, got %v", empty)
	}

	// "secret" is schema-declared but NOT whitelisted: it must not appear.
	view := ModelView{DetailFields: []string{"lens_mm", "missing_field", "shot_date", "empty_key"}}
	fields := WhitelistFields(projected, schemaTypes, view, false)
	if len(fields) != 2 {
		t.Fatalf("whitelist must keep only whitelisted+existing fields, got %v", fields)
	}
	for _, field := range fields {
		if field.Key == "secret" {
			t.Fatal("schema-declared but unwhitelisted field must not be published")
		}
	}

	card := WhitelistFields(projected, schemaTypes, ModelView{CardFields: []string{"lens_mm"}}, true)
	if len(card) != 1 || card[0].Key != "lens_mm" {
		t.Fatalf("card whitelist must use card_fields, got %v", card)
	}
}

func TestPruneModelViews(t *testing.T) {
	schemas := shotSchemas()
	views := map[string]ModelView{
		"11111111-1111-4111-8111-111111111111": {
			CardFields:   []string{"shot_size", "retired_field"},
			DetailFields: []string{"lens_mm", "also_retired"},
		},
		"22222222-2222-4222-8222-222222222222": { // model gone entirely
			DetailFields: []string{"whatever"},
		},
	}
	pruned, count := PruneModelViews(views, schemas)
	if count != 3 {
		t.Fatalf("pruned count = %d, want 3", count)
	}
	view := pruned["11111111-1111-4111-8111-111111111111"]
	if len(view.CardFields) != 1 || view.CardFields[0] != "shot_size" {
		t.Fatalf("card fields not pruned correctly: %v", view.CardFields)
	}
	if len(view.DetailFields) != 1 || view.DetailFields[0] != "lens_mm" {
		t.Fatalf("detail fields not pruned correctly: %v", view.DetailFields)
	}
	if _, exists := pruned["22222222-2222-4222-8222-222222222222"]; exists {
		t.Fatal("view for a gone model must be dropped entirely")
	}
}
