package workflows

import (
	"context"
	"errors"
	"reflect"
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
	if output.Candidate["title"] != "title" || output.Candidate["workflow_status"] != "submitted" {
		t.Fatalf("unexpected candidate: %#v", output.Candidate)
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

func TestFixedWorkflowTopologies(t *testing.T) {
	want := map[string][]string{
		"asset_publish":    {"load_reviewed_version", "authz", "publish_transaction", "enqueue_projection"},
		"asset_archive":    {"load_published_version", "authz", "archive_transaction", "delete_projection"},
		"asset_import":     {"parse_input", "validate_schema", "write_raw", "enqueue_prepare"},
		"asset_reindex":    {"resolve_scope", "enqueue_projection", "collect_results"},
		"asset_transcribe": {"load_media", "request_asr", "write_content", "optional_prepare"},
		"note_sync":        {"load_conversation", "build_note_version", "idempotent_write"},
	}
	if got := fixedWorkflowSteps(); !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected fixed workflow topology:\n got: %#v\nwant: %#v", got, want)
	}
}
