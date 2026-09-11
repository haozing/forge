// Package mcpserver exposes the asset-hub open surface as an MCP (Model
// Context Protocol) server: local agents connect with an API key over
// Streamable HTTP and discover tools mapped onto the same services the
// open REST face uses. Design doc: docs/MCP-Server设计文档-2026-09-11.md.
package mcpserver

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"agentchunzhi/internal/asset"
	"agentchunzhi/internal/auth"
	"agentchunzhi/internal/authz"
	agentquery "agentchunzhi/internal/query"
	"agentchunzhi/internal/resourcemodel"
)

// QueryService is the slice of the unified query service the search tool
// needs; defined here (consumer side) to avoid an httpapi import cycle.
type QueryService interface {
	OpenAPIQuery(ctx context.Context, principal auth.Principal, input agentquery.Request) (agentquery.Response, error)
}

// Deps carries the services the tools delegate to. It mirrors the subset of
// httpapi.Dependencies the open face uses — no new business logic lives here.
type Deps struct {
	Authenticator        auth.APIKeyAuthenticator
	QueryService         QueryService
	AssetService         asset.Service
	MemberAssetService   asset.MemberService
	ResourceModelService resourcemodel.Service
	ScopeResolver        authz.ScopeResolver
	Logger               *slog.Logger
}

// NewHandler builds the /mcp HTTP handler. Each request authenticates its
// bearer key; the per-request server (stateless mode) only lists tools the
// key's capabilities allow, so tools/list already reflects least privilege.
func NewHandler(deps Deps) *mcp.StreamableHTTPHandler {
	return mcp.NewStreamableHTTPHandler(func(r *http.Request) *mcp.Server {
		principal, err := deps.Authenticator.Authenticate(r.Context(), r)
		if err != nil {
			// The handler still completes the JSON-RPC exchange; tools are
			// withheld so the client sees an empty (but valid) server. Calls
			// from an unauthenticated session fail in the tool middleware.
			return buildServer(deps, auth.Principal{})
		}
		return buildServer(deps, principal)
	}, &mcp.StreamableHTTPOptions{
		Stateless:    true,
		JSONResponse: true,
		Logger:       deps.Logger,
	})
}

const serverName = "asset-hub"
const serverVersion = "1.0.0"

func buildServer(deps Deps, principal auth.Principal) *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{Name: serverName, Version: serverVersion}, nil)
	registerTools(server, deps, principal)
	return server
}

// can reports whether the principal holds the capability. An empty
// principal (unauthenticated) holds nothing.
func can(p auth.Principal, capability string) bool {
	return p.UserType == "agent" && p.HasCapability(capability)
}

// sessionID produces a crypto-random id for diagnostics; stateless mode does
// not advertise it, but keeping generation here documents the contract.
func sessionID() string {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "session-unavailable"
	}
	return hex.EncodeToString(buf[:])
}

// toolError converts domain errors into an MCP tool error result so agents
// receive readable guidance instead of a protocol-level failure.
func toolError(err error) (*mcp.CallToolResult, any, error) {
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: "error: " + err.Error()}},
		IsError: true,
	}, nil, nil
}

// textResult renders the standard dual output: a human-readable summary plus
// structured JSON payload for downstream parsing.
func textResult(summary string, structured any) (*mcp.CallToolResult, any, error) {
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: summary}},
	}, structured, nil
}

var _ = fmt.Sprintf // keep fmt for summaries added per-tool below
