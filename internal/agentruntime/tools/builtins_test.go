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
