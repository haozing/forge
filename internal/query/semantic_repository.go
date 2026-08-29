package query

import (
	"context"
	"fmt"

	"agentchunzhi/internal/store"
	"agentchunzhi/internal/vectorvalue"
)

// SemanticRecall runs an iterative HNSW cosine scan over the semantic-ready
// embeddings of the active profile (doc §10.4). The HNSW fetch overfetches
// five times the window; scope, policy and filter predicates then narrow the
// candidates. `SET LOCAL hnsw.ef_search` requires the query to run inside a
// transaction, which this repository owns.
func SemanticRecall(ctx context.Context, store *store.Store, scope QueryAccessScope, req Request, plan plan, profileID string, vectorLiteral string, filters []compiledFieldFilter, tags resolvedTagFilter, window int) ([]chunkCandidate, error) {
	if len(plan.SemanticModels) == 0 {
		return []chunkCandidate{}, nil
	}
	overfetch := window * SemanticOverfetchFactor
	if overfetch > MaxCandidateWindow {
		overfetch = MaxCandidateWindow
	}
	builder := &sqlBuilder{}
	// The vector literal is bound first so the template can reference the same
	// parameter in both the ORDER BY and the similarity projection.
	vectorParam := builder.arg(vectorLiteral)
	where := []string{
		"e.organization_id = " + builder.arg(scope.OrganizationID) + "::uuid",
		"e.projection_profile_id = " + builder.arg(profileID) + "::uuid",
		"c.workspace_id = ANY(" + builder.arg(scope.WorkspaceIDs) + "::uuid[])",
		"c.resource_model_id = ANY(" + builder.arg(plan.SemanticModels) + "::uuid[])",
		`EXISTS (SELECT 1 FROM retrieval.projection_heads h
		         WHERE h.organization_id = c.organization_id AND h.asset_id = c.asset_id
		           AND h.projection_profile_id = c.projection_profile_id
		           AND h.active_run_id = c.projection_run_id)`,
		`pr.status IN ('ready', 'degraded')`,
		"a.publication_status = 'published'",
		"a.deleted_at IS NULL",
		"a.current_published_version_id = c.asset_version_id",
		"a.visibility = ANY(" + builder.arg(scope.AllowedVisibilities) + "::text[])",
		"COALESCE(NULLIF(mv.policy #>> '{retrieval,semantic,enabled}'::text[], '')::boolean, false)",
		"w.status = 'active'",
	}
	where = append(where, metadataPredicates(builder, req)...)
	where = append(where, tagPredicates(builder, tags, "c.asset_version_id")...)
	where = append(where, fieldPredicates(builder, filters, "v.fields")...)
	sql := fmt.Sprintf(`
		SELECT c.id::text, c.asset_id::text, c.asset_version_id::text,
		       c.workspace_id::text, c.resource_model_id::text,
		       c.content, c.source_type, c.source_locator, c.char_start, c.char_end,
		       c.source_checksum, c.chunk_checksum, c.canonicalizer_version,
		       1 - (e.embedding <=> `+vectorParam+`::vector(1024))
		FROM retrieval.chunk_embeddings e
		JOIN retrieval.chunks c ON c.id = e.chunk_id
		JOIN retrieval.projection_runs pr
		  ON pr.organization_id = c.organization_id AND pr.id = c.projection_run_id
		JOIN asset.assets a
		  ON a.organization_id = c.organization_id AND a.id = c.asset_id
		JOIN asset.asset_versions v
		  ON v.organization_id = a.organization_id AND v.id = a.current_published_version_id
		JOIN model.resource_model_versions mv
		  ON mv.organization_id = a.organization_id AND mv.id = v.resource_model_version_id
		JOIN content.workspaces w
		  ON w.organization_id = a.organization_id AND w.id = a.workspace_id
		WHERE %s
		ORDER BY e.embedding <=> `+vectorParam+`::vector(1024)
		LIMIT %d
	`, joinAnd(where), overfetch)

	tx, err := store.Pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin semantic recall: %w", err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, "SET LOCAL hnsw.ef_search = 100"); err != nil {
		return nil, fmt.Errorf("configure hnsw iterative scan: %w", err)
	}
	rows, err := tx.Query(ctx, sql, builder.args...)
	if err != nil {
		return nil, fmt.Errorf("semantic recall: %w", err)
	}
	candidates := []chunkCandidate{}
	rank := 0
	for rows.Next() {
		var candidate chunkCandidate
		if err := rows.Scan(&candidate.ChunkID, &candidate.AssetID, &candidate.AssetVersionID,
			&candidate.WorkspaceID, &candidate.ResourceModelID, &candidate.Content,
			&candidate.SourceType, &candidate.SourceLocator, &candidate.CharStart,
			&candidate.CharEnd, &candidate.SourceChecksum, &candidate.ChunkChecksum,
			&candidate.CanonicalizerVersion, &candidate.Score); err != nil {
			rows.Close()
			return nil, fmt.Errorf("scan semantic candidate: %w", err)
		}
		rank++
		candidate.SemanticRank = rank
		candidates = append(candidates, candidate)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit semantic recall: %w", err)
	}
	return candidates, nil
}

// EmbedQueryLiteral embeds the query text through the configured provider and
// renders the pgvector text literal. The provider is mandatory for semantic
// recall: no hash fallback exists (doc §10.4).
func EmbedQueryLiteral(provider interface {
	EmbedQuery(ctx context.Context, text string) ([]float32, error)
}, ctx context.Context, query string) (string, int, error) {
	if provider == nil {
		return "", 0, ErrSemanticProviderUnavailable
	}
	vector, err := provider.EmbedQuery(ctx, query)
	if err != nil {
		return "", 0, ErrSemanticProviderUnavailable
	}
	values := make([]float64, len(vector))
	for index, value := range vector {
		values[index] = float64(value)
	}
	literal, err := vectorvalue.Literal(values)
	if err != nil {
		return "", 0, ErrSemanticProviderUnavailable
	}
	return literal, len(vector), nil
}
