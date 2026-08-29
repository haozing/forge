package query

import (
	"context"
	"fmt"
	"strings"

	"agentchunzhi/internal/retrieval"
	"agentchunzhi/internal/store"
)

// chunkCandidate is one lexical/semantic chunk hit before asset collapse.
type chunkCandidate struct {
	ChunkID              string
	AssetID              string
	AssetVersionID       string
	WorkspaceID          string
	ResourceModelID      string
	Content              string
	SourceType           string
	SourceLocator        []byte
	CharStart            int
	CharEnd              int
	SourceChecksum       string
	ChunkChecksum        string
	CanonicalizerVersion string
	Score                float64
	LexicalRank          int
	SemanticRank         int
}

// pgroongaSpecials are the operator characters of the PGroonga query syntax.
// User input is always treated as plain text: every special character is
// backslash-escaped so `&@~` cannot be injected with operators (doc §10.3).
const pgroongaSpecials = `"'()+-*.:^$~|&!\[\]{}?<>_%/`

// EscapePGroongaQuery renders the user input as an escaped plain-text query.
func EscapePGroongaQuery(input string) string {
	var builder strings.Builder
	builder.Grow(len(input))
	for _, char := range input {
		if strings.ContainsRune(pgroongaSpecials, char) {
			builder.WriteByte('\\')
		}
		builder.WriteRune(char)
	}
	return builder.String()
}

// activeProfile loads the organization's active projection profile. Warming
// profiles never serve queries (doc §10.3).
func activeProfile(ctx context.Context, store *store.Store, organizationID string) (retrieval.Profile, error) {
	repo := retrieval.ProfileRepository{Store: store}
	profile, err := repo.GetActiveProfile(ctx, organizationID)
	if err != nil {
		return retrieval.Profile{}, err
	}
	return profile, nil
}

// LexicalRecall queries the lexical-ready chunks of the active profile's
// serving runs with PGroonga. Scope, policy, tag and field filters apply
// before ranking; the caller collapses chunks per asset afterwards.
func LexicalRecall(ctx context.Context, store *store.Store, scope QueryAccessScope, req Request, plan plan, profile retrieval.Profile, filters []compiledFieldFilter, tags resolvedTagFilter, window int) ([]chunkCandidate, error) {
	if len(plan.FulltextModels) == 0 {
		return []chunkCandidate{}, nil
	}
	builder := &sqlBuilder{}
	where := []string{
		"c.organization_id = " + builder.arg(scope.OrganizationID) + "::uuid",
		"c.projection_profile_id = " + builder.arg(profile.ID) + "::uuid",
		"c.workspace_id = ANY(" + builder.arg(scope.WorkspaceIDs) + "::uuid[])",
		"c.resource_model_id = ANY(" + builder.arg(plan.FulltextModels) + "::uuid[])",
		// The head must point at the chunk's run: only the serving run of the
		// current published version is visible.
		`EXISTS (SELECT 1 FROM retrieval.projection_heads h
		         WHERE h.organization_id = c.organization_id AND h.asset_id = c.asset_id
		           AND h.projection_profile_id = c.projection_profile_id
		           AND h.active_run_id = c.projection_run_id)`,
		`pr.status IN ('ready', 'degraded')`,
		"a.publication_status = 'published'",
		"a.deleted_at IS NULL",
		"a.current_published_version_id = c.asset_version_id",
		"a.visibility = ANY(" + builder.arg(scope.AllowedVisibilities) + "::text[])",
		"COALESCE(NULLIF(mv.policy #>> '{retrieval,fulltext,enabled}'::text[], '')::boolean, false)",
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
		       pgroonga_score(c.tableoid, c.ctid)
		FROM retrieval.chunks c
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
		  AND c.content &@~ %s
		ORDER BY pgroonga_score(c.tableoid, c.ctid) DESC, c.asset_id, c.ordinal
		LIMIT %d
	`, joinAnd(where), builder.arg(EscapePGroongaQuery(req.Query)), window)
	rows, err := store.Pool.Query(ctx, sql, builder.args...)
	if err != nil {
		return nil, fmt.Errorf("lexical recall: %w", err)
	}
	defer rows.Close()
	candidates := []chunkCandidate{}
	rank := 0
	for rows.Next() {
		var candidate chunkCandidate
		if err := rows.Scan(&candidate.ChunkID, &candidate.AssetID, &candidate.AssetVersionID,
			&candidate.WorkspaceID, &candidate.ResourceModelID, &candidate.Content,
			&candidate.SourceType, &candidate.SourceLocator, &candidate.CharStart,
			&candidate.CharEnd, &candidate.SourceChecksum, &candidate.ChunkChecksum,
			&candidate.CanonicalizerVersion, &candidate.Score); err != nil {
			return nil, fmt.Errorf("scan lexical candidate: %w", err)
		}
		rank++
		candidate.LexicalRank = rank
		candidates = append(candidates, candidate)
	}
	return candidates, rows.Err()
}

// SemanticProjectionIncomplete reports whether the active profile still has
// runs in flight for scope workspaces (doc §10.4: partial projection stays
// visible as degraded, never as an empty guarantee).
func SemanticProjectionIncomplete(ctx context.Context, store *store.Store, organizationID string, profile retrieval.Profile, workspaceIDs []string) (bool, error) {
	var incomplete bool
	err := store.Pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM retrieval.projection_runs pr
			WHERE pr.organization_id = $1::uuid
			  AND pr.projection_profile_id = $2::uuid
			  AND pr.workspace_id = ANY($3::uuid[])
			  AND pr.status IN ('queued', 'building', 'lexical_ready', 'embedding')
		)
	`, organizationID, profile.ID, workspaceIDs).Scan(&incomplete)
	if err != nil {
		return false, fmt.Errorf("check semantic projection progress: %w", err)
	}
	return incomplete, nil
}
