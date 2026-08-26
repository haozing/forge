package tools

import (
	"context"
	"errors"
	"testing"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

type testTool struct{ name string }

func (t testTool) Info(context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{Name: t.name}, nil
}

func (testTool) InvokableRun(context.Context, string, ...tool.Option) (string, error) {
	return "ok", nil
}

func TestRegistryFiltersAndGuardsTools(t *testing.T) {
	registry := NewRegistry()
	if err := registry.Register(Definition{Name: "search_knowledge", Risk: ReadOnly, Capabilities: []string{"query.read"}, Tool: testTool{name: "search_knowledge"}}); err != nil {
		t.Fatal(err)
	}
	if err := registry.Register(Definition{Name: "create_internal_asset", Risk: LowWrite, Capabilities: []string{"asset.write"}, Tool: testTool{name: "create_internal_asset"}}); err != nil {
		t.Fatal(err)
	}
	tools, err := registry.Tools(context.Background(), Policy{AllowedCapabilities: map[string]bool{"query.read": true}})
	if err != nil || len(tools) != 1 {
		t.Fatalf("unexpected filtered tools: len=%d err=%v", len(tools), err)
	}
	guardedTool, ok := tools[0].(tool.InvokableTool)
	if !ok {
		t.Fatal("filtered tool must remain invokable")
	}
	if _, err := guardedTool.InvokableRun(context.Background(), `{}`); err != nil {
		t.Fatal(err)
	}

	writeTools, err := registry.Tools(context.Background(), Policy{AllowLowWrite: true, AllowedCapabilities: map[string]bool{"asset.write": true}, MaxCalls: 1})
	if err != nil || len(writeTools) != 1 {
		t.Fatalf("unexpected write tools: len=%d err=%v", len(writeTools), err)
	}
	writeTool := writeTools[0].(tool.InvokableTool)
	if _, err := writeTool.InvokableRun(context.Background(), `{}`); !errors.Is(err, ErrApprovalNeeded) {
		t.Fatalf("write tool should require approval, got %v", err)
	}
}
