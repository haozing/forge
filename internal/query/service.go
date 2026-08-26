package query

import (
	"context"
	"errors"
	"fmt"
	"regexp"

	"agentchunzhi/internal/auth"
	"agentchunzhi/internal/retrieval"
	"agentchunzhi/internal/store"

	"github.com/jackc/pgx/v5"
)

type Service struct {
	Store        *store.Store
	Embeddings   retrieval.EmbeddingProvider
	Reranker     Reranker
	CursorSecret string
}

var ErrInvalidQuery = errors.New("invalid query")
var ErrReferenceNotFound = errors.New("asset reference not found")

var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[1-5][0-9a-fA-F]{3}-[89abAB][0-9a-fA-F]{3}-[0-9a-fA-F]{12}$`)

func ValidUUID(value string) bool {
	return uuidPattern.MatchString(value)
}

type AssetReference struct {
	AssetID        string `json:"asset_id"`
	AssetVersionID string `json:"asset_version_id"`
	Title          string `json:"title"`
	URL            string `json:"url"`
	SourceExcerpt  string `json:"source_excerpt,omitempty"`
	UpdatedAt      string `json:"updated_at"`
}

// Reference returns only the current published version visible to the Agent
// user and enabled for the agent_tool outlet.
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
		JOIN asset.asset_versions v ON v.id = a.current_published_version_id
		JOIN model.resource_model_versions mv ON mv.id = v.resource_model_version_id
		WHERE a.id = $1::uuid
		  AND a.organization_id = $2::uuid
		  AND a.resource_model_id::text = ANY($3::text[])
		  AND a.publication_status = 'published'
		  AND a.current_published_version_id IS NOT NULL
		  AND COALESCE(NULLIF(mv.policy #>> '{outlets,agent_tool,enabled}', '')::boolean, false)
		  AND CASE v.quality
				WHEN 'raw' THEN 1
					WHEN 'ai_generated' THEN 2
						WHEN 'human_confirmed' THEN 3
			END >= CASE COALESCE(NULLIF(mv.policy #>> '{outlets,agent_tool,min_quality}', ''), 'raw')
				WHEN 'raw' THEN 1
						WHEN 'ai_generated' THEN 2
						WHEN 'human_confirmed' THEN 3
				ELSE 99
			END
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
