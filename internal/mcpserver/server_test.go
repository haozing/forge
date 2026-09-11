package mcpserver

// server_test.go — capability-based tool visibility and the tool surface
// contract, exercised over the in-memory transport against an empty Deps
// (tools must gate on capabilities BEFORE touching any service).

import (
	"context"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"agentchunzhi/internal/auth"
)

func capabilitiesClient(t *testing.T, server *mcp.Server) []string {
	t.Helper()
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0.0.0"}, nil)
	ctx := context.Background()
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	serverSession, err := server.Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatalf("server connect: %v", err)
	}
	t.Cleanup(func() { _ = serverSession.Close() })
	clientSession, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	t.Cleanup(func() { _ = clientSession.Close() })

	response, err := clientSession.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("tools/list: %v", err)
	}
	names := make([]string, 0, len(response.Tools))
	for _, tool := range response.Tools {
		names = append(names, tool.Name)
	}
	return names
}

func agentWith(capabilities ...string) auth.Principal {
	return auth.Principal{
		UserType:     "agent",
		Capabilities: capabilities,
	}
}

func TestToolVisibilityFollowsCapabilities(t *testing.T) {
	// query.read 是检索能力，读资产只读工具（get_asset/list_tables）随之可见。
	readOnly := capabilitiesClient(t, buildServer(Deps{}, agentWith("query.read")))
	wantRead := map[string]bool{"search_assets": true, "get_asset": true, "list_tables": true}
	if len(readOnly) != len(wantRead) {
		t.Errorf("query.read key should see exactly %v, got %v", wantRead, readOnly)
	}
	for _, name := range readOnly {
		if !wantRead[name] {
			t.Errorf("read-only key sees unexpected tool %q", name)
		}
	}

	writer := capabilitiesClient(t, buildServer(Deps{}, agentWith("asset.create", "asset.publish")))
	seen := map[string]bool{}
	for _, name := range writer {
		seen[name] = true
	}
	for _, want := range []string{"create_document", "insert_record", "publish_asset"} {
		if !seen[want] {
			t.Errorf("writer key missing tool %q (got %v)", want, seen)
		}
	}
	for _, forbidden := range []string{"archive_asset", "update_document"} {
		if seen[forbidden] {
			t.Errorf("writer key should not see %q", forbidden)
		}
	}
}

func TestUnauthenticatedAndMemberPrincipalsSeeNoTools(t *testing.T) {
	for name, principal := range map[string]auth.Principal{
		"empty":  {},
		"member": {UserType: "member", Capabilities: []string{"query.read", "asset.create"}},
	} {
		tools := capabilitiesClient(t, buildServer(Deps{}, principal))
		if len(tools) != 0 {
			t.Errorf("%s principal must see zero tools, got %v", name, tools)
		}
	}
}

func TestFullWriteKeySeesCompleteChain(t *testing.T) {
	tools := capabilitiesClient(t, buildServer(Deps{}, agentWith(
		"query.read", "asset.read", "asset.create", "asset.edit", "asset.publish", "asset.archive",
	)))
	want := []string{
		"search_assets", "get_asset", "list_tables",
		"create_document", "insert_record", "update_document",
		"confirm_asset_version", "publish_asset", "archive_asset",
	}
	seen := map[string]bool{}
	for _, name := range tools {
		seen[name] = true
	}
	for _, name := range want {
		if !seen[name] {
			t.Errorf("full key missing %q (got %v)", name, seen)
		}
	}
}
