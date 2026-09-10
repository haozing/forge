package site

// summary.go — small aggregate counts for the site management face (design
// doc §6.4): the overview tab needs binding/pending-comment numbers without
// pulling full lists. Read-only, behind site.read.

import (
	"context"

	"agentchunzhi/internal/auth"
	"agentchunzhi/internal/authz"
)

// SiteSummary is the site-level counter block.
type SiteSummary struct {
	BindingsCount        int64 `json:"bindings_count"`
	PendingCommentsCount int64 `json:"pending_comments_count"`
}

// Summary returns the counts for one site of the workspace.
func (s Service) Summary(ctx context.Context, principal auth.Principal, workspaceID, siteID string) (SiteSummary, error) {
	if err := s.require(ctx, principal, workspaceID, authz.ActionSiteRead); err != nil {
		return SiteSummary{}, err
	}
	var summary SiteSummary
	var exists bool
	if err := s.Store.Pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM site.public_sites
			WHERE organization_id = $1::uuid AND workspace_id = $2::uuid AND id = $3::uuid
		)
	`, principal.OrganizationID, workspaceID, siteID).Scan(&exists); err != nil {
		return SiteSummary{}, err
	}
	if !exists {
		return SiteSummary{}, ErrSiteNotFound
	}
	err := s.Store.Pool.QueryRow(ctx, `
		SELECT
		  (SELECT count(*) FROM site.site_content_bindings b
		   WHERE b.organization_id = $1::uuid
		     AND b.site_id = (SELECT id FROM site.public_sites
		                      WHERE organization_id = $1::uuid AND workspace_id = $2::uuid AND id = $3::uuid)),
		  (SELECT count(*) FROM site.site_comments c
		   WHERE c.organization_id = $1::uuid
		     AND c.site_id = (SELECT id FROM site.public_sites
		                      WHERE organization_id = $1::uuid AND workspace_id = $2::uuid AND id = $3::uuid)
		     AND c.status = 'pending')
	`, principal.OrganizationID, workspaceID, siteID).Scan(&summary.BindingsCount, &summary.PendingCommentsCount)
	if err != nil {
		return SiteSummary{}, err
	}
	return summary, nil
}
