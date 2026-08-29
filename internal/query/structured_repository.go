package query

import (
	"context"
	"fmt"
	"time"

	"agentchunzhi/internal/store"
)

// structuredCandidate is one asset-level structured row. Structured reads the
// authoritative main data only: assets, the current published version and the
// tag relation (doc §10.2). It never touches the projection schema and never
// degrades on embedding/PGroonga failures.
type structuredCandidate struct {
	AssetID            string
	AssetVersionID     string
	WorkspaceID        string
	ResourceModelID    string
	Title              string
	Summary            string
	Visibility         string
	Origin             string
	ConfirmationStatus string
	PublishedAt        *time.Time
}

// StructuredRecall returns up to MaxSessionAssets published assets matching
// scope, tags, typed fields, origin/confirmation and the publication window,
// ordered by `published_at DESC, asset_id DESC`.
func StructuredRecall(ctx context.Context, store *store.Store, scope QueryAccessScope, req Request, filters []compiledFieldFilter, tags resolvedTagFilter) ([]structuredCandidate, error) {
	builder := &sqlBuilder{}
	where := []string{
		"a.organization_id = " + builder.arg(scope.OrganizationID) + "::uuid",
		"a.workspace_id = ANY(" + builder.arg(scope.WorkspaceIDs) + "::uuid[])",
		"a.resource_model_id = ANY(" + builder.arg(scope.ResourceModelIDs) + "::uuid[])",
		"a.publication_status = 'published'",
		"a.deleted_at IS NULL",
		"a.current_published_version_id IS NOT NULL",
		"a.visibility = ANY(" + builder.arg(scope.AllowedVisibilities) + "::text[])",
		"w.status = 'active'",
	}
	where = append(where, metadataPredicates(builder, req)...)
	where = append(where, tagPredicates(builder, tags, "v.id")...)
	where = append(where, fieldPredicates(builder, filters, "v.fields")...)
	sql := fmt.Sprintf(`
		SELECT a.id::text, v.id::text, a.workspace_id::text, a.resource_model_id::text,
		       v.title, v.summary, a.visibility, v.origin, v.confirmation_status,
		       a.published_at
		FROM asset.assets a
		JOIN asset.asset_versions v
		  ON v.organization_id = a.organization_id AND v.id = a.current_published_version_id
		JOIN content.workspaces w
		  ON w.organization_id = a.organization_id AND w.id = a.workspace_id
		WHERE %s
		ORDER BY a.published_at DESC, a.id DESC
		LIMIT %d
	`, joinAnd(where), MaxSessionAssets)
	rows, err := store.Pool.Query(ctx, sql, builder.args...)
	if err != nil {
		return nil, fmt.Errorf("structured recall: %w", err)
	}
	defer rows.Close()
	candidates := make([]structuredCandidate, 0, MaxSessionAssets)
	for rows.Next() {
		var candidate structuredCandidate
		if err := rows.Scan(&candidate.AssetID, &candidate.AssetVersionID, &candidate.WorkspaceID,
			&candidate.ResourceModelID, &candidate.Title, &candidate.Summary,
			&candidate.Visibility, &candidate.Origin, &candidate.ConfirmationStatus,
			&candidate.PublishedAt); err != nil {
			return nil, fmt.Errorf("scan structured candidate: %w", err)
		}
		candidates = append(candidates, candidate)
	}
	return candidates, rows.Err()
}

// loadTagSummaries resolves the TagSummary list for the given asset versions
// (doc §6.5: tags come from the relation, rendered with the phase 2 shape).
func loadTagSummaries(ctx context.Context, store *store.Store, organizationID string, versionIDs []string) (map[string][]TagSummary, error) {
	result := make(map[string][]TagSummary, len(versionIDs))
	if len(versionIDs) == 0 {
		return result, nil
	}
	rows, err := store.Pool.Query(ctx, `
		SELECT avt.asset_version_id::text, t.id::text, t.normalized_key, t.display_name, t.slug
		FROM asset.asset_version_tags avt
		JOIN asset.tags t ON t.organization_id = avt.organization_id AND t.id = avt.tag_id
		WHERE avt.organization_id = $1::uuid AND avt.asset_version_id = ANY($2::uuid[])
		ORDER BY t.normalized_key
	`, organizationID, versionIDs)
	if err != nil {
		return nil, fmt.Errorf("load tag summaries: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var versionID string
		var summary TagSummary
		if err := rows.Scan(&versionID, &summary.ID, &summary.Key, &summary.DisplayName, &summary.Slug); err != nil {
			return nil, err
		}
		result[versionID] = append(result[versionID], summary)
	}
	return result, rows.Err()
}

func joinAnd(parts []string) string {
	out := ""
	for index, part := range parts {
		if index > 0 {
			out += "\n\t\t  AND "
		}
		out += part
	}
	return out
}
