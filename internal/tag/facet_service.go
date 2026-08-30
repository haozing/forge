package tag

// facet_service.go — workspace tag facets share the asset scope construction
// with asset lists: the caller passes the authorized scope and the same
// structural filters, and counts are always COUNT(DISTINCT asset_id).

import (
	"context"
	"fmt"

	"agentchunzhi/internal/auth"
	"agentchunzhi/internal/store"
)

// FacetScope selects the version pointer the facet counts over.
type FacetScope struct {
	Scope             string // working | published
	ResourceModelID   string
	Visibility        string
	PublicationStatus string
}

// FacetItem is one tag with the distinct asset count under the scope.
type FacetItem struct {
	Tag        Summary `json:"tag"`
	AssetCount int64   `json:"asset_count"`
}

type FacetService struct {
	Store *store.Store
}

// Counts applies any/all/none on top of the scope and returns per-tag counts
// for tags carried by at least one asset in the filtered set. Tag status
// defaults to active. The principal only supplies the organization id — no
// permission check happens here; the caller passes the authorized scope.
func (s FacetService) Counts(ctx context.Context, principal auth.Principal, workspaceID string, scope FacetScope, filter KeyFilter, tagStatus string, limit int) ([]FacetItem, error) {
	return s.CountsForOrganization(ctx, principal.OrganizationID, workspaceID, scope, filter, tagStatus, limit)
}

// CountsForOrganization is the principal-free variant for public-site faces
// (phase 5): the public band is already scoped by the query compiler
// (visibility=public, publication_status=published), so there is no principal
// to authorize — the organization id comes from the resolved site. It reuses
// the exact SQL construction of Counts.
func (s FacetService) CountsForOrganization(ctx context.Context, organizationID, workspaceID string, scope FacetScope, filter KeyFilter, tagStatus string, limit int) ([]FacetItem, error) {
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	if tagStatus == "" {
		tagStatus = StatusActive
	}
	normalized, err := NormalizeFilter(filter)
	if err != nil {
		return nil, err
	}
	lookup := func(keys []string) ([]string, error) {
		if len(keys) == 0 {
			return nil, nil
		}
		rows, err := s.Store.Pool.Query(ctx, `
			SELECT id::text FROM asset.tags
			WHERE organization_id = $1::uuid AND workspace_id = $2::uuid AND normalized_key = ANY($3::text[])
		`, organizationID, workspaceID, keys)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		ids := []string{}
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				return nil, err
			}
			ids = append(ids, id)
		}
		if err := rows.Err(); err != nil {
			return nil, err
		}
		if len(ids) != len(keys) {
			return nil, ErrUnknownTag
		}
		return ids, nil
	}
	anyIDs, err := lookup(normalized.Any)
	if err != nil {
		return nil, err
	}
	allIDs, err := lookup(normalized.All)
	if err != nil {
		return nil, err
	}
	noneIDs, err := lookup(normalized.None)
	if err != nil {
		return nil, err
	}

	pointer := "a.current_working_version_id"
	if scope.Scope == "published" {
		pointer = "a.current_published_version_id"
	}
	args := []any{organizationID, workspaceID}
	arg := func(value any) string {
		args = append(args, value)
		return fmt.Sprintf("$%d", len(args))
	}
	where := []string{
		"a.organization_id = " + arg(organizationID) + "::uuid",
		"a.workspace_id = " + arg(workspaceID) + "::uuid",
		"a.deleted_at IS NULL",
	}
	if scope.ResourceModelID != "" {
		where = append(where, "a.resource_model_id = "+arg(scope.ResourceModelID)+"::uuid")
	}
	if scope.Visibility != "" {
		where = append(where, "a.visibility = "+arg(scope.Visibility))
	}
	if scope.PublicationStatus != "" {
		where = append(where, "a.publication_status = "+arg(scope.PublicationStatus))
	}
	if tagStatus == StatusActive || tagStatus == StatusArchived {
		where = append(where, "t.status = "+arg(tagStatus))
	}
	if len(anyIDs) > 0 {
		where = append(where, fmt.Sprintf(
			"EXISTS (SELECT 1 FROM asset.asset_version_tags fx WHERE fx.asset_version_id = %s AND fx.tag_id = ANY(%s::uuid[]))", pointer, arg(anyIDs)))
	}
	for index, id := range allIDs {
		// UUIDs contain '-', which is illegal in an unquoted identifier: use
		// indexed fixed aliases.
		alias := fmt.Sprintf("fa%d", index)
		where = append(where, fmt.Sprintf(
			"EXISTS (SELECT 1 FROM asset.asset_version_tags %s WHERE %s.asset_version_id = %s AND %s.tag_id = ANY(%s::uuid[]))", alias, alias, pointer, alias, arg([]string{id})))
	}
	if len(noneIDs) > 0 {
		where = append(where, fmt.Sprintf(
			"NOT EXISTS (SELECT 1 FROM asset.asset_version_tags fn WHERE fn.asset_version_id = %s AND fn.tag_id = ANY(%s::uuid[]))", pointer, arg(noneIDs)))
	}
	query := fmt.Sprintf(`
		SELECT t.id::text, t.normalized_key, t.display_name, t.slug, t.status,
		       count(DISTINCT a.id)
		FROM asset.assets a
		JOIN asset.asset_versions v ON v.organization_id = a.organization_id AND v.id = %s
		JOIN asset.asset_version_tags avt ON avt.organization_id = a.organization_id AND avt.asset_version_id = v.id
		JOIN asset.tags t ON t.organization_id = avt.organization_id AND t.id = avt.tag_id
		WHERE %s
		GROUP BY t.id, t.normalized_key, t.display_name, t.slug, t.status
		ORDER BY count(DISTINCT a.id) DESC, t.normalized_key
		LIMIT %d
	`, pointer, joinAnd(where), limit)
	rows, err := s.Store.Pool.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]FacetItem, 0, limit)
	for rows.Next() {
		var item FacetItem
		if err := rows.Scan(&item.Tag.ID, &item.Tag.Key, &item.Tag.DisplayName, &item.Tag.Slug, &item.Tag.Status, &item.AssetCount); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func joinAnd(parts []string) string {
	result := ""
	for index, part := range parts {
		if index > 0 {
			result += " AND "
		}
		result += part
	}
	return result
}
