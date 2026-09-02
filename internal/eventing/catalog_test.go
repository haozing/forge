package eventing

import (
	"encoding/json"
	"testing"
)

func TestCatalogPayloadsRoundTrip(t *testing.T) {
	payloads := []struct {
		name string
		item any
	}{
		{EventAssetVersionCreated, AssetVersionCreatedPayload{AssetID: "a", VersionID: "v", VersionNo: 3, WorkspaceID: "w"}},
		{EventAssetPublished, AssetPublishedPayload{AssetID: "a", VersionID: "v", PreviousVersionID: "p", WorkspaceID: "w"}},
		{EventAssetArchived, AssetArchivedPayload{AssetID: "a", PreviousVersionID: "p", WorkspaceID: "w"}},
		{EventPublicationSubmitted, PublicationRequestPayload{RequestID: "r", AssetID: "a", AssetVersionID: "v", WorkspaceID: "w"}},
		{EventTagUpdated, TagUpdatedPayload{TagID: "t", WorkspaceID: "w", ChangedFields: []string{"display_name"}}},
		{EventWorkspaceMembershipChanged, WorkspaceMembershipChangedPayload{WorkspaceID: "w", UserID: "u", Operation: "granted", NewRole: "editor"}},
	}
	for _, tc := range payloads {
		raw, err := EncodePayload(tc.item)
		if err != nil {
			t.Fatalf("encode %s: %v", tc.name, err)
		}
		if !json.Valid(raw) {
			t.Fatalf("payload %s is not valid JSON", tc.name)
		}
	}
}

func TestEveryCatalogEventIsRegisteredAtV1(t *testing.T) {
	known := KnownEvents()
	required := []string{
		EventAssetVersionCreated, EventAssetPublished, EventAssetArchived,
		EventAssetRestored, EventAssetVisibilityChanged,
		EventPublicationSubmitted, EventPublicationApproved, EventPublicationRejected,
		EventPublicationCancelled,
		EventTagCreated, EventTagUpdated, EventTagArchived, EventTagRestored,
		EventResourceModelPolicyPublished, EventWorkspaceMembershipChanged,
		EventSiteBindingChanged,
	}
	for _, name := range required {
		if version, ok := known[name]; !ok || version != PayloadVersionV1 {
			t.Fatalf("event %s must be registered at payload version 1", name)
		}
	}
}

func TestRegistryRefusesUnknownPayloadVersion(t *testing.T) {
	registry, err := DefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	if consumers := registry.ConsumersFor(EventAssetPublished, 1); len(consumers) == 0 {
		t.Fatal("asset.published@1 must reach the projection consumer")
	}
	if consumers := registry.ConsumersFor(EventAssetPublished, 2); len(consumers) != 0 {
		t.Fatalf("unknown payload version must be rejected, got %v", consumers)
	}
	if consumers := registry.ConsumersFor("asset.retrieval_projection_requested", 1); len(consumers) != 0 {
		t.Fatal("the downstream command event must have no consumers")
	}
}

func TestEventNamesStateFactsNotCommands(t *testing.T) {
	for name := range KnownEvents() {
		if len(name) > 9 && name[len(name)-9:] == "requested" {
			t.Fatalf("event %s exposes a downstream command; use past-tense facts", name)
		}
	}
}
