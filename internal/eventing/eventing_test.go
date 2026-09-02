package eventing

import (
	"encoding/json"
	"testing"
)

func TestEncodeEventPayload(t *testing.T) {
	raw := []byte(`{"asset_id":"00000000-0000-4000-8000-000000000001"}`)
	cases := []struct {
		name      string
		payload   any
		wantEmpty bool
	}{
		{"byte slice passes through", raw, false},
		{"json raw message passes through", json.RawMessage(`{"asset_id":"00000000-0000-4000-8000-000000000001"}`), false},
		{"typed struct is marshalled", AssetPublishedPayload{
			AssetID:     "00000000-0000-4000-8000-000000000001",
			VersionID:   "00000000-0000-4000-8000-000000000002",
			WorkspaceID: "00000000-0000-4000-8000-000000000003",
		}, false},
		{"map is marshalled", map[string]any{"job_id": "j1"}, false},
		{"nil becomes empty object", nil, true},
		{"empty byte slice becomes empty object", []byte(nil), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			encoded, err := encodeEventPayload(tc.payload)
			if err != nil {
				t.Fatalf("encodeEventPayload: %v", err)
			}
			// A double-encoded byte slice lands as a base64 JSON string, which
			// no consumer can decode into a payload object.
			var object map[string]any
			if err := json.Unmarshal(encoded, &object); err != nil {
				t.Fatalf("payload is not a JSON object: %q (%v)", encoded, err)
			}
			if tc.wantEmpty {
				if string(encoded) != "{}" {
					t.Fatalf("payload = %q, want {}", encoded)
				}
				return
			}
			if len(object) == 0 {
				t.Fatalf("payload object is empty: %q", encoded)
			}
		})
	}
}

func TestDefaultRegistryMatchesDeclaredConsumers(t *testing.T) {
	registry, err := DefaultRegistry()
	if err != nil {
		t.Fatalf("DefaultRegistry() error = %v", err)
	}
	consumers := registry.ConsumersFor("agent.task.prepare_requested", 1)
	if len(consumers) != 0 {
		t.Fatalf("agent.task.prepare_requested consumers = %#v", consumers)
	}
	consumers = registry.ConsumersFor("asset.published", 1)
	// The SSR delivery cache invalidator joined the asset facts (design doc
	// §6.2); consumers are key-sorted: delivery.cache then the projector.
	if len(consumers) != 2 || consumers[0].Key != "delivery.cache" || consumers[1].Key != "retrieval.projection" {
		t.Fatalf("projection consumers = %#v", consumers)
	}
	if consumers := registry.ConsumersFor("asset.retrieval_projection_requested", 1); len(consumers) != 0 {
		t.Fatalf("retired command event must have no consumers, got %#v", consumers)
	}
	consumers = registry.ConsumersFor("attachment.created", 1)
	if len(consumers) != 1 || consumers[0].Key != "attachment.scan" {
		t.Fatalf("attachment.created consumers = %#v", consumers)
	}
}

func TestDispatchEventArgsAreUniqueAndQueued(t *testing.T) {
	opts := DispatchEventArgs{}.InsertOpts()
	if opts.Queue != QueueEvents {
		t.Fatalf("queue = %q, want %q", opts.Queue, QueueEvents)
	}
	if !opts.UniqueOpts.ByArgs {
		t.Fatal("dispatch jobs must be unique by event arguments")
	}
	if opts.MaxAttempts != DefaultMaxDeliveryAttempts {
		t.Fatalf("max attempts = %d, want %d", opts.MaxAttempts, DefaultMaxDeliveryAttempts)
	}
}

func TestRegistryRejectsDuplicateConsumer(t *testing.T) {
	_, err := NewRegistry([]ConsumerManifest{
		{Key: "duplicate", EventVersions: map[string]int{"event.one": 1}},
		{Key: "duplicate", EventVersions: map[string]int{"event.two": 1}},
	})
	if err == nil {
		t.Fatal("NewRegistry() error = nil, want duplicate key error")
	}
}
