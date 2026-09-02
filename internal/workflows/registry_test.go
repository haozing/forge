package workflows

import (
	"context"
	"errors"
	"testing"
)

func TestDefaultRegistryUsesFixedGraph(t *testing.T) {
	registry, err := DefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	definition, err := registry.Resolve("asset_prepare")
	if err != nil || definition.CodeVersion != 1 || definition.Checksum == "" {
		t.Fatalf("invalid asset_prepare definition: %+v err=%v", definition, err)
	}
	output, err := definition.Run.Invoke(context.Background(), Input{
		OrganizationID: "org", RunID: "run", AssetIDs: []string{"asset-1"},
		Values: map[string]any{"title": "  title  "},
	})
	if err != nil {
		t.Fatal(err)
	}
	if output.Candidate["title"] != "title" {
		t.Fatalf("unexpected candidate: %#v", output.Candidate)
	}
	if _, has := output.Candidate["workflow_status"]; has {
		t.Fatal("workflow status must not leak into candidates: versions are immutable")
	}
	if _, err := registry.Resolve("graph_json_from_db"); !errors.Is(err, ErrWorkflowNotFound) {
		t.Fatalf("unknown workflow should be rejected, got %v", err)
	}
}

func TestDefaultRegistryCompilesEveryProductWorkflow(t *testing.T) {
	registry, err := DefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range registry.Keys() {
		definition, err := registry.Resolve(key)
		if err != nil {
			t.Fatal(err)
		}
		output, err := definition.Run.Invoke(context.Background(), Input{RunID: "run-1", AssetIDs: []string{"asset-1"}})
		if err != nil {
			t.Fatalf("workflow %s failed: %v", key, err)
		}
		if output.WorkflowKey != key || output.CodeVersion != definition.CodeVersion {
			t.Fatalf("workflow %s returned invalid metadata: %+v", key, output)
		}
	}
}
