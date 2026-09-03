package asset

import (
	"context"
	"errors"
	"testing"
	"time"

	"agentchunzhi/internal/auth"
)

func TestAssetCursorRoundTrip(t *testing.T) {
	updated := time.Date(2026, 8, 25, 8, 0, 0, 123000000, time.UTC)
	id := "00000000-0000-4000-8000-000000000001"
	cursor := encodeAssetCursor("updated_at:desc", MemberAsset{ID: id, UpdatedAt: updated})
	got, err := decodeAssetCursor(cursor, "updated_at:desc")
	if err != nil || got.ID != id || got.UpdatedAt == "" {
		t.Fatalf("cursor round trip failed: cursor=%#v err=%v", got, err)
	}
	if _, err := decodeAssetCursor(cursor, "title:asc"); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("cursor sort mismatch should be rejected, got %v", err)
	}
	if _, err := decodeAssetCursor("invalid", "updated_at:desc"); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("invalid cursor should be rejected, got %v", err)
	}
}

func TestValidateFieldsJSONSchema(t *testing.T) {
	schema := []byte(`{"fields":[{"key":"name","type":"string","required":true,"validation":{"min_length":2}},{"key":"count","type":"integer"}],"additional_properties":false}`)
	if err := validateFields(schema, map[string]any{"name": "ok", "count": float64(2)}); err != nil {
		t.Fatalf("expected valid fields, got %v", err)
	}
	if err := validateFields(schema, map[string]any{"count": float64(2)}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("expected required-field error, got %v", err)
	}
	if err := validateFields(schema, map[string]any{"name": "ok", "unknown": true}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("expected unknown-field error, got %v", err)
	}
}

func TestValidateFieldsArraySchema(t *testing.T) {
	schema := []byte(`{"fields":[{"key":"enabled","type":"boolean","required":true}],"additional_properties":false}`)
	if err := validateFields(schema, map[string]any{"enabled": true}); err != nil {
		t.Fatalf("expected valid array schema fields, got %v", err)
	}
	if err := validateFields(schema, map[string]any{"enabled": "yes"}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("expected type error, got %v", err)
	}
}

func TestMemberAssetVisibilityContractLegacyRejected(t *testing.T) {
	if got, err := memberAssetVisibility(nil, ""); err != nil || got != "workspace" {
		t.Fatalf("expected default workspace visibility, got %q err=%v", got, err)
	}
	for _, legacy := range []string{"private", "login", "internal"} {
		if _, err := memberAssetVisibility(nil, legacy); err == nil {
			t.Fatalf("legacy visibility %q must be rejected", legacy)
		}
	}
	if got, err := memberAssetVisibility(nil, "public"); err != nil || got != "public" {
		t.Fatalf("expected public visibility, got %q err=%v", got, err)
	}
	policy := []byte(`{"visibility":{"allowed":["workspace"]}}`)
	if _, err := memberAssetVisibility(policy, "public"); err == nil {
		t.Fatal("policy allowed-set must narrow requests")
	}
}

func TestPublishRejectsInvalidScopeOrIDs(t *testing.T) {
	principal := auth.Principal{OrganizationID: "00000000-0000-4000-8000-000000000001"}
	_, err := (Service{}).Publish(context.Background(), principal, nil, "bad", "bad")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected not found, got %v", err)
	}
}

func TestArchiveRejectsInvalidScopeOrID(t *testing.T) {
	principal := auth.Principal{OrganizationID: "00000000-0000-4000-8000-000000000001"}
	_, err := (Service{}).Archive(context.Background(), principal, nil, "bad")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected not found, got %v", err)
	}
}

func TestApplyDefaultsSemantics(t *testing.T) {
	schema := []byte(`{
		"additional_properties": false,
		"fields": [
			{"key": "status", "type": "enum", "options": [{"value": "draft", "label": "Draft"}, {"value": "done", "label": "Done"}], "default": "draft"},
			{"key": "seats", "type": "integer", "default": 4},
			{"key": "tags", "type": "multiselect", "options": [{"value": "a", "label": "A"}, {"value": "b", "label": "B"}], "default": ["a"]},
			{"key": "notes", "type": "string"},
			{"key": "active", "type": "boolean", "default": true}
		]
	}`)

	t.Run("fills absent keys only", func(t *testing.T) {
		fields := applyDefaults(schema, map[string]any{"notes": "kept"})
		if fields["status"] != "draft" || fields["seats"] != 4.0 || fields["active"] != true {
			t.Fatalf("defaults not filled: %+v", fields)
		}
		if fields["notes"] != "kept" {
			t.Fatalf("explicit value overridden: %+v", fields)
		}
	})

	t.Run("explicit null is user intent", func(t *testing.T) {
		fields := applyDefaults(schema, map[string]any{"seats": nil})
		if value, exists := fields["seats"]; !exists || value != nil {
			t.Fatalf("explicit null must not be replaced, got %+v", fields)
		}
	})

	t.Run("nil map gets all defaults", func(t *testing.T) {
		fields := applyDefaults(schema, nil)
		if fields["status"] != "draft" {
			t.Fatalf("nil map should receive defaults, got %+v", fields)
		}
	})

	t.Run("result passes validation", func(t *testing.T) {
		fields := applyDefaults(schema, nil)
		if err := validateFields(schema, fields); err != nil {
			t.Fatalf("merged fields must validate: %v", err)
		}
	})

	t.Run("invalid default fails write wholesale", func(t *testing.T) {
		bad := []byte(`{"additional_properties": false, "fields": [{"key": "status", "type": "enum", "options": [{"value": "draft", "label": "Draft"}], "default": "gone"}]}`)
		fields := applyDefaults(bad, map[string]any{})
		if err := validateFields(bad, fields); err == nil {
			t.Fatal("default outside options must fail validation")
		}
	})

	t.Run("empty schema is a no-op", func(t *testing.T) {
		fields := map[string]any{"x": 1}
		if got := applyDefaults([]byte("{}"), fields); got["x"] != 1 || len(got) != 1 {
			t.Fatalf("empty schema must not touch fields, got %+v", got)
		}
	})

	t.Run("defaults are deep-copied", func(t *testing.T) {
		fields := applyDefaults(schema, nil)
		list, _ := fields["tags"].([]any)
		list[0] = "mutated"
		fresh := applyDefaults(schema, nil)
		if fresh["tags"].([]any)[0] != "a" {
			t.Fatal("default arrays must be copied per application")
		}
	})
}
