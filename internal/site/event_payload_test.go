package site

// event_payload_test.go — locks the wire shape of the site domain events at
// the pure-function level: the typed catalog payloads must marshal into JSON
// objects carrying exactly their declared identifier keys (the phase 4
// lesson: never pre-encode payloads as []byte), and both site events must
// stay registered with payload version 1 in the closed catalog.

import (
	"encoding/json"
	"testing"

	"agentchunzhi/internal/eventing"
)

func decodePayloadObject(t *testing.T, payload any) map[string]any {
	t.Helper()
	raw, err := eventing.EncodePayload(payload)
	if err != nil {
		t.Fatalf("encode payload: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("payload must decode into a JSON object, got %s: %v", raw, err)
	}
	return decoded
}

func TestSiteChangedPayloadIsJSONObject(t *testing.T) {
	decoded := decodePayloadObject(t, eventing.SiteChangedPayload{
		SiteID:      "11111111-1111-1111-1111-111111111111",
		WorkspaceID: "22222222-2222-2222-2222-222222222222",
		Action:      "created",
	})
	for _, key := range []string{"site_id", "workspace_id", "action"} {
		if _, ok := decoded[key]; !ok {
			t.Fatalf("site.site_changed payload missing key %q: %v", key, decoded)
		}
	}
	if len(decoded) != 3 {
		t.Fatalf("site.site_changed payload must carry identifiers only, got %v", decoded)
	}
}

func TestSiteBindingChangedPayloadIsJSONObject(t *testing.T) {
	decoded := decodePayloadObject(t, eventing.SiteBindingChangedPayload{
		SiteID:    "11111111-1111-1111-1111-111111111111",
		AssetID:   "33333333-3333-3333-3333-333333333333",
		Operation: "created",
	})
	for _, key := range []string{"site_id", "asset_id", "operation"} {
		if _, ok := decoded[key]; !ok {
			t.Fatalf("site.binding_changed payload missing key %q: %v", key, decoded)
		}
	}
	if len(decoded) != 3 {
		t.Fatalf("site.binding_changed payload must carry identifiers only, got %v", decoded)
	}
}

func TestSiteEventsRegisteredWithPayloadVersionV1(t *testing.T) {
	known := eventing.KnownEvents()
	if known[eventing.EventSiteChanged] != eventing.PayloadVersionV1 {
		t.Fatal("site.site_changed must be registered with payload version 1")
	}
	if known[eventing.EventSiteBindingChanged] != eventing.PayloadVersionV1 {
		t.Fatal("site.binding_changed must be registered with payload version 1")
	}
}
