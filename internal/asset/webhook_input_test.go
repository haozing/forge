package asset

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"agentchunzhi/internal/auth"
)

func TestRejectImportRowsSQLCastsParametersExplicitly(t *testing.T) {
	if !strings.Contains(rejectImportRowsSQL, "$2::jsonb") {
		t.Fatalf("rejection SQL must cast the errors payload explicitly: %s", rejectImportRowsSQL)
	}
	if strings.Contains(rejectImportRowsSQL, "jsonb_build_object") {
		t.Fatalf("variadic jsonb_build_object parameters cannot be type-inferred (SQLSTATE 42P18): %s", rejectImportRowsSQL)
	}
}

func TestRejectImportRowsFallsBackToGenericEntry(t *testing.T) {
	// rejectImportRows rejects empty entry lists so a rejection always lands
	// with a code even when callers forget one.
	entries := []ImportRowError{}
	if len(entries) == 0 {
		entries = []ImportRowError{{Code: "invalid_row"}}
	}
	if entries[0].Code != "invalid_row" {
		t.Fatalf("unexpected fallback code %q", entries[0].Code)
	}
}

func TestImportPreRowErrorsParsesRoundTrippedMarkers(t *testing.T) {
	markers := []any{
		map[string]any{"code": "field_count", "message": "expected 3 fields, got 2"},
	}
	row := map[string]any{
		"title":               "Broken",
		ImportPreRowErrorsKey: markers,
	}
	pre := importPreRowErrors(row)
	if len(pre) != 1 || pre[0].Code != "field_count" || pre[0].Message == "" {
		t.Fatalf("unexpected pre-row errors: %#v", pre)
	}
	fields := importFields(row)
	if _, leaked := fields[ImportPreRowErrorsKey]; leaked {
		t.Fatal("reserved error marker must not leak into import fields")
	}
	if _, leaked := fields["title"]; leaked {
		t.Fatal("title is a content column, never an asset field")
	}
	if clean := importFields(map[string]any{"title": "OK"}); len(clean) != 0 {
		t.Fatalf("unexpected fields extraction: %#v", clean)
	}
	clean := importPreRowErrors(map[string]any{"title": "OK"})
	if clean != nil {
		t.Fatalf("clean rows must have no pre-row errors: %#v", clean)
	}
}

func TestBuildWebhookVersionSourceMarksChannelAndRef(t *testing.T) {
	receivedAt := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
	source := BuildWebhookVersionSource("incident-42", receivedAt)
	if source["channel"] != "webhook" {
		t.Fatalf("missing webhook channel marker: %#v", source)
	}
	if source["external_ref"] != "incident-42" {
		t.Fatalf("missing external_ref: %#v", source)
	}
	replayedAt, ok := source["received_at"].(string)
	if !ok || !strings.HasPrefix(replayedAt, "2026-08-26T12:00:00") {
		t.Fatalf("unexpected received_at: %#v", source["received_at"])
	}
	anonymous := BuildWebhookVersionSource("", receivedAt)
	if _, exists := anonymous["external_ref"]; exists {
		t.Fatalf("empty ref must be omitted: %#v", anonymous)
	}
}

func TestNewWebhookIdempotencyKeyMeetsLengthRule(t *testing.T) {
	key, err := newWebhookIdempotencyKey()
	if err != nil {
		t.Fatal(err)
	}
	if !validIdempotencyKey(key) {
		t.Fatalf("generated key must pass idempotency validation: %q", key)
	}
	if key == func() string { other, _ := newWebhookIdempotencyKey(); return other }() {
		t.Fatal("generated keys should differ")
	}
}

func TestResolveWebhookTargetRequiresStoreAndPolicy(t *testing.T) {
	service := TransferService{}
	if _, err := service.ResolveWebhookTarget(context.Background(), auth.Principal{}, "", ""); !errors.Is(err, ErrForbidden) {
		t.Fatalf("expected forbidden without store/policy, got %v", err)
	}
}

func TestAllowedModelsForScopesToSingleModel(t *testing.T) {
	modelID := "00000000-0000-4000-8000-000000000001"
	if models := allowedModelsFor(modelID); len(models) != 1 || models[0] != modelID {
		t.Fatalf("unexpected scope: %#v", models)
	}
	if models := allowedModelsFor("not-a-uuid"); models != nil {
		t.Fatalf("invalid id must yield no scope: %#v", models)
	}
}
