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
	cursor := encodeAssetCursor("updated_at:desc", MemberAsset{ID: id, UpdatedAt: updated, sortValue: updated.String()})
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

func TestMemberAssetVisibilityDefaultsWhenPolicyOmitsVisibility(t *testing.T) {
	if got, err := memberAssetVisibility([]byte(`{"outlets":{}}`), ""); err != nil || got != "workspace" {
		t.Fatalf("expected default workspace visibility, got %q err=%v", got, err)
	}
	if got, err := memberAssetVisibility([]byte(`{"outlets":{}}`), "private"); err != nil || got != "private" {
		t.Fatalf("expected requested private visibility, got %q err=%v", got, err)
	}
	if got, err := memberAssetVisibility([]byte(`{"outlets":{}}`), "public"); err != nil || got != "public" {
		t.Fatalf("expected public visibility, got %q err=%v", got, err)
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
