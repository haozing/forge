package eventing

import "testing"

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
	if len(consumers) != 1 || consumers[0].Key != "retrieval.projection" {
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
