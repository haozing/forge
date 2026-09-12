package mcpserver

// server_test.go — capability-based tool visibility and the tool surface
// contract, exercised over the in-memory transport against an empty Deps
// (tools must gate on capabilities BEFORE touching any service).

import (
	"context"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"agentchunzhi/internal/auth"
	"agentchunzhi/internal/authz"
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
	// 检索走 OpenAPI 通道，要求 query.execute；query.read 只放宽只读资产工具。
	readonly := capabilitiesClient(t, buildServer(Deps{}, agentWith("query.read")))
	wantRead := map[string]bool{"get_asset": true, "list_tables": true}
	if len(readonly) != len(wantRead) {
		t.Errorf("query.read key should see exactly %v, got %v", wantRead, readonly)
	}
	for _, name := range readonly {
		if !wantRead[name] {
			t.Errorf("query.read key sees unexpected tool %q", name)
		}
	}
	readOnly := capabilitiesClient(t, buildServer(Deps{}, agentWith("query.read", "query.execute")))
	wantSearch := map[string]bool{"search_assets": true, "get_asset": true, "list_tables": true}
	if len(readOnly) != len(wantSearch) {
		t.Errorf("query.read+execute key should see exactly %v, got %v", wantSearch, readOnly)
	}
	for _, name := range readOnly {
		if !wantSearch[name] {
			t.Errorf("search key sees unexpected tool %q", name)
		}
	}

	writer := capabilitiesClient(t, buildServer(Deps{}, agentWith("asset.create", "asset.publish")))
	seen := map[string]bool{}
	for _, name := range writer {
		seen[name] = true
	}
	// asset.create / asset.edit / asset.write 是同一写类的三种拼写（设计文档
	// §3.2 把 create/update 系工具记在 asset.write 名下；细分别名仅为兼容），
	// 任一拼写下发全部写工具。
	for _, want := range []string{"create_document", "insert_record", "update_document", "publish_asset"} {
		if !seen[want] {
			t.Errorf("writer key missing tool %q (got %v)", want, seen)
		}
	}
	for _, forbidden := range []string{"archive_asset"} {
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
		"query.read", "query.execute", "asset.read", "asset.create", "asset.edit", "asset.publish", "asset.archive",
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

func TestAgentActionGateCoversConfirm(t *testing.T) {
	// 与 internal/authz 的门禁表保持一致：asset.confirm 必须可授予 agent，
	// 否则 MCP 确认入库工具永远 403（2026-09-11 线上 conformance 发现）。
	if !authz.AgentActionAllowed("asset.confirm") {
		t.Fatal("authz gate must allow asset.confirm for agents")
	}
}
