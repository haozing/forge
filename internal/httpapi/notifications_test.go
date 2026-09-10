package httpapi

// notifications_test.go — the notification presentation contract. The SSE
// stream once queried six columns that do not exist in
// content.notifications and failed on every poll; these tests pin the
// storage-shape → wire-shape mapping and lock the openapi Notification
// schema to the representation struct so field drift fails the suite.

import (
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"
)

func notificationRowFor(kind string, payload string) notificationRow {
	return notificationRow{
		streamID:    7,
		id:          "notif-1",
		workspaceID: "ws-1",
		kind:        kind,
		payload:     []byte(payload),
		createdAt:   time.Date(2026, 9, 10, 8, 0, 0, 0, time.UTC),
	}
}

// Every writer-side kind must render a non-empty headline even when the
// payload predates the title/body contract.
func TestPresentNotificationFallsBackByKind(t *testing.T) {
	for _, kind := range []string{
		"publication.submitted",
		"publication.approved",
		"publication.rejected",
		"publication.cancelled",
		"publication.scheduled_failed",
		"system",
	} {
		item := presentNotification(notificationRowFor(kind, `{"request_id":"r1"}`))
		if item.Title == "" {
			t.Errorf("kind %q: fallback title must not be empty", kind)
		}
		if item.Type != kind {
			t.Errorf("kind %q: Type must mirror the storage kind, got %q", kind, item.Type)
		}
	}
}

func TestPresentNotificationPayloadWinsOverFallback(t *testing.T) {
	item := presentNotification(notificationRowFor("publication.approved", `{
		"request_id": "req-9",
		"asset_id": "asset-9",
		"status": "approved",
		"title": "审核通过",
		"body": "《镜头语言》已通过审核并发布。",
		"object_type": "publication_request",
		"object_id": "req-9"
	}`))
	if item.Title != "审核通过" {
		t.Errorf("payload title must win, got %q", item.Title)
	}
	if item.Body != "《镜头语言》已通过审核并发布。" {
		t.Errorf("payload body must win, got %q", item.Body)
	}
	if item.ObjectType != "publication_request" || item.ObjectID != "req-9" {
		t.Errorf("object pair must come from payload, got %q/%q", item.ObjectType, item.ObjectID)
	}
	if item.Metadata["asset_id"] != "asset-9" {
		t.Errorf("kind-specific business fields must stay in metadata, got %v", item.Metadata)
	}
}

func TestPresentNotificationUnknownKindUsesKindAsTitle(t *testing.T) {
	item := presentNotification(notificationRowFor("custom.future", `{}`))
	if item.Title != "custom.future" {
		t.Errorf("unknown kind must degrade to the kind itself, got %q", item.Title)
	}
	if item.Body != "" {
		t.Errorf("unknown kind must not invent a body, got %q", item.Body)
	}
}

func notificationJSONFields(t *testing.T) map[string]bool {
	t.Helper()
	fields := map[string]bool{}
	structType := reflect.TypeOf(finalNotification{})
	for i := 0; i < structType.NumField(); i++ {
		tag := structType.Field(i).Tag.Get("json")
		name := strings.Split(tag, ",")[0]
		if name != "" && name != "-" {
			fields[name] = true
		}
	}
	return fields
}

var notificationSchemaPropertyRe = regexp.MustCompile(`(?m)^        ([a-z_]+): \{`)

// The wire struct and the openapi Notification schema must agree in both
// directions; the path-level contract gate cannot see field drift.
func TestOpenAPINotificationSchemaMatchesRepresentation(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatalf("resolve repo root: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(root, "openapi.yaml"))
	if err != nil {
		t.Fatalf("read openapi.yaml: %v", err)
	}
	text := string(raw)
	lines := strings.Split(text, "\n")
	start := -1
	for i, line := range lines {
		if line == "    Notification:" {
			start = i
			break
		}
	}
	if start < 0 {
		t.Fatal("Notification schema not found in openapi.yaml")
	}
	// The schema block ends at the next key indented by exactly four spaces.
	var block []string
	for _, line := range lines[start+1:] {
		if strings.HasPrefix(line, "    ") && !strings.HasPrefix(line, "     ") {
			break
		}
		block = append(block, line)
	}
	joined := strings.Join(block, "\n")
	propsIndex := strings.Index(joined, "properties:")
	if propsIndex < 0 {
		t.Fatal("Notification schema has no properties block")
	}
	schemaFields := map[string]bool{}
	for _, m := range notificationSchemaPropertyRe.FindAllStringSubmatch(joined[propsIndex:], -1) {
		schemaFields[m[1]] = true
	}
	if len(schemaFields) == 0 {
		t.Fatal("no properties extracted from the Notification schema")
	}

	structFields := notificationJSONFields(t)
	for field := range structFields {
		if !schemaFields[field] {
			t.Errorf("representation field %q missing from the openapi Notification schema", field)
		}
	}
	for field := range schemaFields {
		if !structFields[field] {
			t.Errorf("openapi Notification schema field %q not present on the representation struct", field)
		}
	}
}
