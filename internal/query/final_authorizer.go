package query

import (
	"context"
	"errors"
	"fmt"

	"agentchunzhi/internal/auth"
	"agentchunzhi/internal/store"
)

// FinalAuthorize re-checks a batch of (asset, version) candidates against the
// current scope, the current published pointer, the channel/retrieval policy
// and the original tag/field filters (doc §5.5). Candidates missing from the
// result set were filtered; the caller silently skips them — no 403, no skip
// counts, no reason leakage.
func FinalAuthorize(ctx context.Context, store *store.Store, scope QueryAccessScope, req Request, executedMode string, executedModels []string, filters []compiledFieldFilter, tags resolvedTagFilter, pairs [][2]string) (map[string]bool, error) {
	if len(pairs) == 0 {
		return map[string]bool{}, nil
	}
	assetIDs := make([]string, 0, len(pairs))
	versionIDs := make([]string, 0, len(pairs))
	for _, pair := range pairs {
		assetIDs = append(assetIDs, pair[0])
		versionIDs = append(versionIDs, pair[1])
	}
	builder := &sqlBuilder{}
	where := []string{
		"a.organization_id = " + builder.arg(scope.OrganizationID) + "::uuid",
		"a.id = ANY(" + builder.arg(assetIDs) + "::uuid[])",
		"a.publication_status = 'published'",
		"a.deleted_at IS NULL",
		"a.visibility = ANY(" + builder.arg(scope.AllowedVisibilities) + "::text[])",
		"w.status = 'active'",
	}
	// Version scope is fixed to published: the candidate version must still be
	// the asset's current published pointer.
	where = append(where, "a.current_published_version_id = v.id")
	where = append(where, "v.id = ANY("+builder.arg(versionIDs)+"::uuid[])")
	// Channel and retrieval-mode policy still gate the response. Structured
	// carries no retrieval-mode switch (doc §10.2).
	switch executedMode {
	case ModeFulltext:
		where = append(where, "COALESCE(NULLIF(mv.policy #>> '{retrieval,fulltext,enabled}'::text[], '')::boolean, false)")
	case ModeSemantic:
		where = append(where, "COALESCE(NULLIF(mv.policy #>> '{retrieval,semantic,enabled}'::text[], '')::boolean, false)")
	case ModeHybrid:
		where = append(where, "(COALESCE(NULLIF(mv.policy #>> '{retrieval,fulltext,enabled}'::text[], '')::boolean, false) OR COALESCE(NULLIF(mv.policy #>> '{retrieval,semantic,enabled}'::text[], '')::boolean, false))")
	}
	if len(executedModels) > 0 {
		where = append(where, "a.resource_model_id = ANY("+builder.arg(executedModels)+"::uuid[])")
	}
	where = append(where, tagPredicates(builder, tags, "v.id")...)
	where = append(where, fieldPredicates(builder, filters, "v.fields")...)
	where = append(where, metadataPredicates(builder, req)...)
	sql := fmt.Sprintf(`
		SELECT a.id::text, v.id::text
		FROM asset.assets a
		JOIN asset.asset_versions v
		  ON v.organization_id = a.organization_id AND v.id = a.current_published_version_id
		JOIN model.resource_model_versions mv
		  ON mv.organization_id = a.organization_id AND mv.id = v.resource_model_version_id
		JOIN content.workspaces w
		  ON w.organization_id = a.organization_id AND w.id = a.workspace_id
		WHERE %s
	`, joinAnd(where))
	rows, err := store.Pool.Query(ctx, sql, builder.args...)
	if err != nil {
		return nil, fmt.Errorf("final authorization: %w", err)
	}
	defer rows.Close()
	authorized := make(map[string]bool, len(pairs))
	for rows.Next() {
		var assetID, versionID string
		if err := rows.Scan(&assetID, &versionID); err != nil {
			return nil, err
		}
		authorized[assetID+"\x00"+versionID] = true
	}
	return authorized, rows.Err()
}

// loadAuthorizedAssets renders the main-data fields of the authorized pairs
// for the final page. Only rows that pass the same batch predicate are
// returned.
func loadAuthorizedAssets(ctx context.Context, store *store.Store, scope QueryAccessScope, req Request, executedMode string, executedModels []string, filters []compiledFieldFilter, tags resolvedTagFilter, pairs [][2]string) ([]structuredCandidate, error) {
	if len(pairs) == 0 {
		return []structuredCandidate{}, nil
	}
	assetIDs := make([]string, 0, len(pairs))
	for _, pair := range pairs {
		assetIDs = append(assetIDs, pair[0])
	}
	builder := &sqlBuilder{}
	where := []string{
		"a.organization_id = " + builder.arg(scope.OrganizationID) + "::uuid",
		"a.id = ANY(" + builder.arg(assetIDs) + "::uuid[])",
		"a.publication_status = 'published'",
		"a.deleted_at IS NULL",
		"a.current_published_version_id = v.id",
		"a.visibility = ANY(" + builder.arg(scope.AllowedVisibilities) + "::text[])",
		"w.status = 'active'",
	}
	switch executedMode {
	case ModeFulltext:
		where = append(where, "COALESCE(NULLIF(mv.policy #>> '{retrieval,fulltext,enabled}'::text[], '')::boolean, false)")
	case ModeSemantic:
		where = append(where, "COALESCE(NULLIF(mv.policy #>> '{retrieval,semantic,enabled}'::text[], '')::boolean, false)")
	case ModeHybrid:
		where = append(where, "(COALESCE(NULLIF(mv.policy #>> '{retrieval,fulltext,enabled}'::text[], '')::boolean, false) OR COALESCE(NULLIF(mv.policy #>> '{retrieval,semantic,enabled}'::text[], '')::boolean, false))")
	}
	if len(executedModels) > 0 {
		where = append(where, "a.resource_model_id = ANY("+builder.arg(executedModels)+"::uuid[])")
	}
	where = append(where, tagPredicates(builder, tags, "v.id")...)
	where = append(where, fieldPredicates(builder, filters, "v.fields")...)
	where = append(where, metadataPredicates(builder, req)...)
	sql := fmt.Sprintf(`
		SELECT a.id::text, v.id::text, a.workspace_id::text, a.resource_model_id::text,
		       v.title, v.summary, a.visibility, v.origin, v.confirmation_status,
		       a.published_at
		FROM asset.assets a
		JOIN asset.asset_versions v
		  ON v.organization_id = a.organization_id AND v.id = a.current_published_version_id
		JOIN model.resource_model_versions mv
		  ON mv.organization_id = a.organization_id AND mv.id = v.resource_model_version_id
		JOIN content.workspaces w
		  ON w.organization_id = a.organization_id AND w.id = a.workspace_id
		WHERE %s
	`, joinAnd(where))
	rows, err := store.Pool.Query(ctx, sql, builder.args...)
	if err != nil {
		return nil, fmt.Errorf("load authorized assets: %w", err)
	}
	defer rows.Close()
	candidates := []structuredCandidate{}
	for rows.Next() {
		var candidate structuredCandidate
		if err := rows.Scan(&candidate.AssetID, &candidate.AssetVersionID, &candidate.WorkspaceID,
			&candidate.ResourceModelID, &candidate.Title, &candidate.Summary,
			&candidate.Visibility, &candidate.Origin, &candidate.ConfirmationStatus,
			&candidate.PublishedAt); err != nil {
			return nil, fmt.Errorf("scan authorized asset: %w", err)
		}
		candidates = append(candidates, candidate)
	}
	return candidates, rows.Err()
}

// AuthorizePublicSiteAsset is the extracted single-pair re-check predicate for
// the public-site detail path (doc phase 5 §3.3, audit item A3): the query
// service has no detail mode, so the site domain reads the published version
// from the main data and re-checks it here before serving. The pair must
// still be the asset's current published pointer, the asset must stay
// published and visible inside the visitor's tiered band (D5'), the workspace
// must stay active and the bound model must keep the public_site channel
// enabled on the policy of the served version. The band reuses the exact
// membership resolution and tiering the ForPublicSite compiler applies, so a
// detail answer can never exceed what a list query of the same site would
// have served. The boolean is the only signal: callers hide every failure
// behind a not-found so existence never leaks.
func AuthorizePublicSiteAsset(ctx context.Context, store *store.Store, site PublicSiteRef, visitor VisitorIdentity, assetID, versionID string) (bool, error) {
	if store == nil || store.Pool == nil {
		return false, errors.New("database store is not initialized")
	}
	if !ValidUUID(site.OrganizationID) || !ValidUUID(site.WorkspaceID) ||
		!ValidUUID(assetID) || !ValidUUID(versionID) {
		return false, nil
	}
	// Membership is re-derived from the presented user id, never trusted from
	// the wire (doc phase 5 D5').
	if err := newCompiler(store, "").resolvePublicSiteVisitor(ctx, site, &visitor); err != nil {
		return false, err
	}
	allowed := publicSiteVisibilities(site.DefaultScope, visitor.UserType == auth.UserTypeMember, visitor.WorkspaceMember)
	var authorized bool
	err := store.Pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM asset.assets a
			JOIN asset.asset_versions v
			  ON v.organization_id = a.organization_id AND v.id = a.current_published_version_id
			JOIN model.resource_model_versions mv
			  ON mv.organization_id = a.organization_id AND mv.id = v.resource_model_version_id
			JOIN model.resource_models rm
			  ON rm.organization_id = a.organization_id AND rm.id = a.resource_model_id
			JOIN content.workspaces w
			  ON w.organization_id = a.organization_id AND w.id = a.workspace_id
			WHERE a.organization_id = $1::uuid
			  AND a.workspace_id = $2::uuid
			  AND a.id = $3::uuid
			  AND v.id = $4::uuid
			  AND a.publication_status = 'published'
			  AND a.deleted_at IS NULL
			  AND a.visibility = ANY($5::text[])
			  AND w.status = 'active'
			  AND rm.status = 'active'
			  AND COALESCE(NULLIF(mv.policy #>> '{channels,public_site,enabled}', '')::boolean, false)
		)
	`, site.OrganizationID, site.WorkspaceID, assetID, versionID, allowed).Scan(&authorized)
	if err != nil {
		return false, fmt.Errorf("authorize public site asset: %w", err)
	}
	return authorized, nil
}
