package tools

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/cloudwego/eino/components/tool"
)

func TestRegisterBuiltinsUsesFixedNamesAndServerBoundHandlers(t *testing.T) {
	called := false
	registry := NewRegistry()
	err := RegisterBuiltins(registry, BuiltinHandlers{
		SearchKnowledge: func(_ context.Context, arguments map[string]any) (any, error) {
			called = arguments["query"] == "policy"
			return map[string]any{"items": []any{}}, nil
		},
		CreateInternalAsset: func(context.Context, map[string]any) (any, error) {
			return map[string]any{"asset_id": "asset-1"}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	available, err := registry.Tools(context.Background(), Policy{
		AllowedCapabilities: map[string]bool{"query.read": true},
	})
	if err != nil || len(available) != 1 {
		t.Fatalf("unexpected built-in selection: count=%d err=%v", len(available), err)
	}
	implementation := available[0].(tool.InvokableTool)
	result, err := implementation.InvokableRun(context.Background(), `{"query":"policy","tenant_id":"other"}`)
	if err != nil {
		t.Fatal(err)
	}
	var rejected map[string]any
	if json.Unmarshal([]byte(result), &rejected) != nil || rejected["code"] != "invalid_tool_arguments" || called {
		t.Fatalf("identity-like unknown fields must be rejected before the handler: %s", result)
	}
	result, err = implementation.InvokableRun(context.Background(), `{"query":"policy","limit":10}`)
	if err != nil || !called {
		t.Fatalf("valid built-in was not invoked: result=%s err=%v", result, err)
	}
}

func TestBuiltinWriteToolsRemainDisabledByDefault(t *testing.T) {
	registry := NewRegistry()
	if err := RegisterBuiltins(registry, BuiltinHandlers{
		CreateInternalAsset: func(context.Context, map[string]any) (any, error) { return nil, nil },
	}); err != nil {
		t.Fatal(err)
	}
	available, err := registry.Tools(context.Background(), Policy{AllowedCapabilities: map[string]bool{"asset.write": true}})
	if err != nil {
		t.Fatal(err)
	}
	if len(available) != 0 {
		t.Fatal("low-risk write tools must require an explicit application policy")
	}
}

func TestModelDraftToolsRequireExplicitCapabilityAndHighWrite(t *testing.T) {
	registry := NewRegistry()
	if err := RegisterBuiltins(registry, BuiltinHandlers{
		SuggestResourceModel: func(context.Context, map[string]any) (any, error) { return nil, nil },
		CreateResourceModel:  func(context.Context, map[string]any) (any, error) { return nil, nil },
		UpdateModelDraft:     func(context.Context, map[string]any) (any, error) { return nil, nil },
	}); err != nil {
		t.Fatal(err)
	}
	// Default read capabilities surface only the suggester.
	names := func(policy Policy) map[string]bool {
		available, err := registry.Tools(context.Background(), policy)
		if err != nil {
			t.Fatal(err)
		}
		result := map[string]bool{}
		for _, item := range available {
			if invokable, ok := item.(tool.InvokableTool); ok {
				info, err := invokable.Info(context.Background())
				if err != nil {
					t.Fatal(err)
				}
				result[info.Name] = true
			}
		}
		return result
	}
	defaults := names(Policy{AllowedCapabilities: map[string]bool{
		"query.read": true, "asset.read": true, "schema.read": true, "attachment.read": true, "task.read": true,
	}})
	if !defaults["suggest_resource_model"] {
		t.Fatal("suggest_resource_model must ride the default schema.read capability")
	}
	if defaults["create_resource_model"] || defaults["update_resource_model_draft"] {
		t.Fatal("model draft write tools must stay hidden under default capabilities")
	}
	// model.manage alone is not enough without high-write.
	capOnly := names(Policy{AllowedCapabilities: map[string]bool{"model.manage": true}, AllowHighWrite: false})
	if capOnly["create_resource_model"] {
		t.Fatal("model.manage capability without allow_high_write must not surface the write tools")
	}
	// Full explicit grant surfaces both write tools.
	full := names(Policy{AllowedCapabilities: map[string]bool{"model.manage": true}, AllowHighWrite: true})
	if !full["create_resource_model"] || !full["update_resource_model_draft"] {
		t.Fatal("explicit model.manage + high-write must surface both model draft tools")
	}
	if full["suggest_resource_model"] {
		t.Fatal("suggest tool needs schema.read, not model.manage")
	}
}
