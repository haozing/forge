package site

// release.go — immutable site configuration releases (design doc §7.4).
// A release pins one config snapshot {homepage_config, navigation_config,
// style_config, template}; content is never pinned (bindings keep resolving
// the live current_published_version_id pointer, v2 §7.2). The published
// pointer on public_sites selects the live snapshot; rollback republishes a
// historical snapshot as a new revision (audit intact, no destructive write).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"agentchunzhi/internal/auth"
	"agentchunzhi/internal/authz"

	"github.com/jackc/pgx/v5"
)

// Release is one immutable config snapshot of a site.
type Release struct {
	ID          string          `json:"id"`
	SiteID      string          `json:"site_id"`
	Revision    int64           `json:"revision"`
	Config      json.RawMessage `json:"config"`
	PublishedBy string          `json:"published_by"`
	CreatedAt   time.Time       `json:"created_at"`
}

// ReleasePage is one keyset page of the release history (revision DESC).
type ReleasePage struct {
	Items      []Release
	HasMore    bool
	NextCursor string
}

// ReleaseConfig is the snapshot document of one release.
type ReleaseConfig struct {
	HomepageConfig   json.RawMessage `json:"homepage_config"`
	NavigationConfig json.RawMessage `json:"navigation_config"`
	StyleConfig      json.RawMessage `json:"style_config"`
	Template         string          `json:"template"`
}

const releaseColumns = `id::text, site_id::text, revision, config, published_by::text, created_at`

func scanReleaseRow(row interface{ Scan(...any) error }) (Release, error) {
	var item Release
	if err := row.Scan(&item.ID, &item.SiteID, &item.Revision, &item.Config,
		&item.PublishedBy, &item.CreatedAt); err != nil {
		return Release{}, err
	}
	return item, nil
}

// ListReleases pages the release history of one site (revision DESC keyset).
// Reads need site.read like every management read.
func (s Service) ListReleases(ctx context.Context, principal auth.Principal, workspaceID, siteID, cursor string, limit int) (ReleasePage, error) {
	if !validID(workspaceID) || !validID(siteID) {
		return ReleasePage{}, ErrInvalidInput
	}
	if err := s.require(ctx, principal, workspaceID, authz.ActionSiteRead); err != nil {
		return ReleasePage{}, err
	}
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	// cursorValue rides as interface{}: an empty cursor must bind SQL NULL,
	// a zero int64 would bind 0 and the (revision < 0) filter would starve
	// the first page (same defect family as the binding list cursor).
	var cursorValue any
	if cursor != "" {
		parsed, err := strconv.ParseInt(cursor, 10, 64)
		if err != nil || parsed <= 0 {
			return ReleasePage{}, ErrInvalidInput
		}
		cursorValue = parsed
	}
	rows, err := s.Store.Pool.Query(ctx, `
		SELECT `+releaseColumns+`
		FROM site.site_releases
		WHERE organization_id = $1::uuid AND workspace_id = $2::uuid AND site_id = $3::uuid
		  AND ($4::bigint IS NULL OR revision < $4::bigint)
		ORDER BY revision DESC
		LIMIT $5::int
	`, principal.OrganizationID, workspaceID, siteID, cursorValue, limit+1)
	if err != nil {
		return ReleasePage{}, fmt.Errorf("list site releases: %w", err)
	}
	defer rows.Close()
	page := ReleasePage{Items: make([]Release, 0, limit+1)}
	for rows.Next() {
		item, err := scanReleaseRow(rows)
		if err != nil {
			return ReleasePage{}, err
		}
		page.Items = append(page.Items, item)
	}
	if err := rows.Err(); err != nil {
		return ReleasePage{}, fmt.Errorf("iterate site releases: %w", err)
	}
	if len(page.Items) > limit {
		page.HasMore = true
		page.Items = page.Items[:limit]
		page.NextCursor = strconv.FormatInt(page.Items[len(page.Items)-1].Revision, 10)
	}
	return page, nil
}

// PublishRelease snapshots the site's current working configuration (or the
// configuration of baseReleaseID — the rollback path) as a new immutable
// release and moves the published pointer. Publishing bumps the site revision
// so D4 ETags and the delivery release_rev cache keys rotate. site.site_changed
// is emitted with action "released"/"rolled_back".
func (s Service) PublishRelease(ctx context.Context, principal auth.Principal, workspaceID, siteID, baseReleaseID string) (Release, error) {
	if !validID(workspaceID) || !validID(siteID) {
		return Release{}, ErrInvalidInput
	}
	if baseReleaseID != "" && !validID(baseReleaseID) {
		return Release{}, ErrInvalidInput
	}
	if err := s.require(ctx, principal, workspaceID, authz.ActionSiteManage); err != nil {
		return Release{}, err
	}
	if s.Events == nil {
		return Release{}, errors.New("event store is not initialized")
	}
	tx, err := s.Store.Pool.Begin(ctx)
	if err != nil {
		return Release{}, err
	}
	defer tx.Rollback(ctx)
	current, err := lockSite(ctx, tx, principal.OrganizationID, workspaceID, siteID)
	if err != nil {
		return Release{}, err
	}
	var snapshot ReleaseConfig
	action := "released"
	if baseReleaseID != "" {
		base, err := scanReleaseRow(tx.QueryRow(ctx, `
			SELECT `+releaseColumns+`
			FROM site.site_releases
			WHERE organization_id = $1::uuid AND workspace_id = $2::uuid AND site_id = $3::uuid
			  AND id = $4::uuid
		`, principal.OrganizationID, workspaceID, siteID, baseReleaseID))
		if errors.Is(err, pgx.ErrNoRows) {
			return Release{}, ErrReleaseNotFound
		}
		if err != nil {
			return Release{}, fmt.Errorf("load release base: %w", err)
		}
		if err := json.Unmarshal(base.Config, &snapshot); err != nil {
			return Release{}, fmt.Errorf("decode release base: %w", err)
		}
		action = "rolled_back"
	} else {
		snapshot = ReleaseConfig{
			HomepageConfig:   current.HomepageConfig,
			NavigationConfig: current.NavigationConfig,
			StyleConfig:      current.StyleConfig,
			Template:         current.Template,
		}
	}
	// Render-side re-validation of the snapshot style document (design doc
	// §7.2: both write and render reject invalid values).
	if _, err := ParseStyleConfig(snapshot.StyleConfig); err != nil {
		return Release{}, err
	}
	config, err := json.Marshal(snapshot)
	if err != nil {
		return Release{}, err
	}
	var nextRevision int64
	if err := tx.QueryRow(ctx, `
		SELECT COALESCE(MAX(revision), 0) + 1 FROM site.site_releases WHERE site_id = $1::uuid
	`, siteID).Scan(&nextRevision); err != nil {
		return Release{}, fmt.Errorf("allocate release revision: %w", err)
	}
	item, err := scanReleaseRow(tx.QueryRow(ctx, `
		INSERT INTO site.site_releases
			(organization_id, workspace_id, site_id, revision, config, published_by)
		VALUES ($1::uuid, $2::uuid, $3::uuid, $4, $5::jsonb, $6::uuid)
		RETURNING `+releaseColumns+`
	`, principal.OrganizationID, workspaceID, siteID, nextRevision, string(config), principal.UserID))
	if err != nil {
		return Release{}, fmt.Errorf("insert site release: %w", err)
	}
	updated, err := scanSiteRow(tx.QueryRow(ctx, `
		UPDATE site.public_sites
		SET published_release_id = $4::uuid, revision = site.public_sites.revision + 1, updated_at = now()
		WHERE organization_id = $1::uuid AND workspace_id = $2::uuid AND id = $3::uuid
		RETURNING `+siteColumns+`
	`, principal.OrganizationID, workspaceID, siteID, item.ID))
	if err != nil {
		return Release{}, fmt.Errorf("publish site release: %w", err)
	}
	recordSiteAudit(ctx, tx, principal, workspaceID, "site.release_published", siteID, map[string]any{
		"release_id": item.ID, "revision": item.Revision, "action": action,
		"base_release_id": baseReleaseID,
	})
	if err := appendSiteEvent(ctx, tx, s.Events, principal, workspaceID, updated, action); err != nil {
		return Release{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Release{}, err
	}
	return item, nil
}
