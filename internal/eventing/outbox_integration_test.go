package eventing

// outbox_integration_test.go — verifies the stored outbox payload shape
// against a real database. The database is provided via
// AGENTCHUNZHI_TEST_DATABASE_URL; without it the test is skipped.

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"agentchunzhi/internal/store"
)

func TestAppendTxStoresObjectPayloadIntegration(t *testing.T) {
	databaseURL := os.Getenv("AGENTCHUNZHI_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("AGENTCHUNZHI_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	db, err := store.Open(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	var organizationID string
	if err := db.Pool.QueryRow(ctx, `
		SELECT id::text FROM organization.organizations ORDER BY created_at, id LIMIT 1
	`).Scan(&organizationID); err != nil {
		t.Fatalf("load outbox integration scope: %v", err)
	}
	registry, err := DefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	events := EventStore{Registry: registry}
	tx, err := db.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	// EncodePayload-style caller: the payload reaches AppendTx as bytes.
	raw, err := EncodePayload(map[string]any{"organization_id": organizationID})
	if err != nil {
		t.Fatal(err)
	}
	eventID, err := events.AppendTx(ctx, tx, Event{
		OrganizationID:   organizationID,
		EventType:        EventOrganizationUpdated,
		AggregateType:    "organization",
		AggregateID:      organizationID,
		AggregateVersion: 1,
		PayloadVersion:   PayloadVersionV1,
		Actor:            map[string]any{"type": "system"},
		Payload:          raw,
	})
	if err != nil {
		t.Fatalf("AppendTx: %v", err)
	}
	var stored []byte
	if err := tx.QueryRow(ctx, `
		SELECT payload FROM audit.outbox_events WHERE id = $1::uuid
	`, eventID).Scan(&stored); err != nil {
		t.Fatalf("load stored payload: %v", err)
	}
	// A double-encoded byte slice is stored as a base64 JSON string; consumers
	// require a JSON object.
	var object map[string]any
	if err := json.Unmarshal(stored, &object); err != nil {
		t.Fatalf("stored payload is not a JSON object: %q (%v)", stored, err)
	}
	if len(object) == 0 {
		t.Fatalf("stored payload object is empty: %q", stored)
	}
}
