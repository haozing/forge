package query

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"agentchunzhi/internal/auth"

	"github.com/jackc/pgx/v5"
)

// agent_bridge.go — the controlled compatibility surface for the in-process
// agent runtime until phase 4 rewires it onto Service.AgentQuery (doc §11.2:
// the built-in agent calls the service directly, never HTTP self-loopback).
// The legacy request/response shapes are bridged onto the unified engine; no
// second query implementation exists behind them.

// QueryRequest is the legacy bridge request.
type QueryRequest struct {
	Mode     string
	Query    string
	ModelIDs []string
	TopK     int
	Cursor   string
}

// SearchItem is the legacy bridge result item.
type SearchItem struct {
	AssetID              string          `json:"asset_id"`
	AssetVersionID       string          `json:"asset_version_id"`
	Score                float64         `json:"score"`
	Snippet              string          `json:"snippet"`
	Source               string          `json:"source"`
	SourceType           string          `json:"source_type,omitempty"`
	SourceLocator        json.RawMessage `json:"source_locator,omitempty"`
	CharStart            int             `json:"char_start,omitempty"`
	CharEnd              int             `json:"char_end,omitempty"`
	SourceChecksum       string          `json:"source_checksum,omitempty"`
	ChunkChecksum        string          `json:"chunk_checksum,omitempty"`
	CanonicalizerVersion string          `json:"canonicalizer_version,omitempty"`
	RankingMethod        string          `json:"ranking_method,omitempty"`
	Title                string          `json:"title"`
	Summary              string          `json:"summary"`
	Tags                 []string        `json:"tags"`
}

// QueryResponse is the legacy bridge response.
type QueryResponse struct {
	Mode           string       `json:"mode"`
	Degraded       bool         `json:"degraded"`
	Items          []SearchItem `json:"items"`
	NextCursor     string       `json:"next_cursor,omitempty"`
	HasMore        bool         `json:"has_more"`
	SessionID      string       `json:"session_id,omitempty"`
	RankingMethod  string       `json:"ranking_method"`
	PolicyRevision int64        `json:"policy_revision"`
}

// AssetReference is the validated single-asset reference the agent tools and
// chat surfaces consume.
type AssetReference struct {
	AssetID        string `json:"asset_id"`
	AssetVersionID string `json:"asset_version_id"`
	Title          string `json:"title"`
	URL            string `json:"url"`
	SourceExcerpt  string `json:"source_excerpt,omitempty"`
	UpdatedAt      string `json:"updated_at"`
}

// Query bridges the legacy call shape onto the unified engine. Agent
// principals compile ForAgent; member principals reach every workspace with an
// explicit membership (the pre-phase-3 behavior, narrowed by the model
// allowlist the caller already resolved).
func (s Service) Query(ctx context.Context, principal auth.Principal, req QueryRequest, allowedModels []string) (QueryResponse, error) {
	if len(allowedModels) == 0 {
		return QueryResponse{}, ErrModelAccessDenied
	}
	for _, modelID := range allowedModels {
		if !ValidUUID(modelID) {
			return QueryResponse{}, fmt.Errorf("%w: model ids must be UUIDs", ErrInvalidQuery)
		}
	}
	unified := Request{
		Mode:             req.Mode,
		Query:            req.Query,
		ResourceModelIDs: req.ModelIDs,
		TopK:             req.TopK,
		Cursor:           req.Cursor,
	}
	var response Response
	var err error
	if principal.UserType == auth.UserTypeAgent {
		response, err = s.AgentQuery(ctx, principal, unified)
	} else {
		response, err = s.Execute(ctx, principal, ChannelWorkspace,
			func(ctx context.Context) (QueryAccessScope, error) {
				return s.compiler().ForMemberCompat(ctx, principal, allowedModels)
			}, unified)
	}
	if err != nil {
		if errors.Is(err, ErrInvalidQueryMode) || errors.Is(err, ErrQueryTextRequired) ||
			errors.Is(err, ErrInvalidVisibility) || errors.Is(err, ErrInvalidRequest) ||
			errors.Is(err, ErrInvalidTagFilter) || errors.Is(err, ErrInvalidFieldFilter) {
			return QueryResponse{}, fmt.Errorf("%w: %v", ErrInvalidQuery, err)
		}
		return QueryResponse{}, err
	}
	items := make([]SearchItem, 0, len(response.Items))
	for _, item := range response.Items {
		bridge := SearchItem{
			AssetID:        item.AssetID,
			AssetVersionID: item.AssetVersionID,
			Snippet:        item.Summary,
			Source:         "asset",
			RankingMethod:  response.RankingMethod,
			Title:          item.Title,
			Summary:        item.Summary,
			Tags:           make([]string, 0, len(item.Tags)),
		}
		for _, tag := range item.Tags {
			bridge.Tags = append(bridge.Tags, tag.Key)
		}
		if item.Score != nil {
			bridge.Score = *item.Score
		}
		if item.Citation != nil {
			bridge.Snippet = item.Citation.Excerpt
			bridge.SourceType = item.Citation.SourceType
			bridge.SourceLocator = item.Citation.SourceLocator
			bridge.CharStart = item.Citation.CharStart
			bridge.CharEnd = item.Citation.CharEnd
			bridge.SourceChecksum = item.Citation.SourceChecksum
			bridge.ChunkChecksum = item.Citation.ChunkChecksum
			bridge.CanonicalizerVersion = item.Citation.CanonicalizerVersion
		}
		items = append(items, bridge)
	}
	return QueryResponse{
		Mode:           response.RequestedMode,
		Degraded:       response.Degraded,
		Items:          items,
		NextCursor:     response.Page.NextCursor,
		HasMore:        response.Page.HasMore,
		SessionID:      response.SessionID,
		RankingMethod:  response.RankingMethod,
		PolicyRevision: 0,
	}, nil
}

// Reference returns only the current published version visible to the caller
// and enabled for the agent channel (the agent data-access boundary).
func (s Service) Reference(ctx context.Context, principal auth.Principal, assetID string, allowedModelIDs []string) (AssetReference, error) {
	if !ValidUUID(assetID) || len(allowedModelIDs) == 0 {
		return AssetReference{}, ErrReferenceNotFound
	}
	if s.Store == nil || s.Store.Pool == nil {
		return AssetReference{}, errors.New("database store is not initialized")
	}
	var result AssetReference
	err := s.Store.Pool.QueryRow(ctx, `
		SELECT a.id::text, v.id::text, COALESCE(v.title, ''),
		       LEFT(COALESCE(v.markdown, ''), 500),
		       a.updated_at::text
		FROM asset.assets a
		JOIN asset.asset_versions v ON v.organization_id = a.organization_id AND v.id = a.current_published_version_id
		JOIN model.resource_model_versions mv ON mv.organization_id = a.organization_id AND mv.id = v.resource_model_version_id
		WHERE a.id = $1::uuid
		  AND a.organization_id = $2::uuid
		  AND a.resource_model_id::text = ANY($3::text[])
		  AND a.publication_status = 'published'
		  AND a.current_published_version_id IS NOT NULL
		  AND a.deleted_at IS NULL
		  AND COALESCE(NULLIF(mv.policy #>> '{channels,agent,enabled}', '')::boolean, false)
	`, assetID, principal.OrganizationID, allowedModelIDs).Scan(
		&result.AssetID, &result.AssetVersionID, &result.Title,
		&result.SourceExcerpt, &result.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return AssetReference{}, ErrReferenceNotFound
	}
	if err != nil {
		return AssetReference{}, fmt.Errorf("load asset reference: %w", err)
	}
	result.URL = "/assets/" + result.AssetID
	return result, nil
}
