package site

// comments.go — site comments v1 (二期 §8): members only, flat, plain text,
// moderated by default. Public writes land on the public face; moderation
// runs on the management face behind site.manage. The comment_created fact
// drives the delivery cache invalidation chain.

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"agentchunzhi/internal/auth"
	"agentchunzhi/internal/authz"
	"agentchunzhi/internal/eventing"

	"github.com/jackc/pgx/v5"
)

// Comment is one rendered or moderated comment row.
type Comment struct {
	ID          string    `json:"id"`
	AssetID     string    `json:"asset_id"`
	DisplayPath string    `json:"display_path"`
	AuthorName  string    `json:"author_name"`
	Body        string    `json:"body"`
	Status      string    `json:"status"`
	CreatedAt   time.Time `json:"created_at"`
}

// CommentPage is one page of the moderation queue.
type CommentPage struct {
	Items []Comment
	HasMore bool
	NextCursor string
}

// CommentCooldownBounds the per-(member, asset) write frequency (public
// face carries no idempotency contract; the cooldown is the backstop).
const CommentCooldown = 10 * time.Second

// CreateComment writes one member comment on a bound published asset of the
// site. Moderated sites queue it as pending; open sites publish directly.
func (s Service) CreateComment(ctx context.Context, principal auth.Principal, slug, displayPath, body string) (Comment, error) {
	if s.Store == nil || s.Store.Pool == nil {
		return Comment{}, errors.New("database store is not initialized")
	}
	if principal.UserType != auth.UserTypeMember {
		return Comment{}, ErrForbidden
	}
	body = strings.TrimSpace(body)
	if len(body) == 0 || len(body) > 2000 {
		return Comment{}, ErrInvalidInput
	}
	reader := PublicReader{Store: s.Store}
	item, err := reader.loadSite(ctx, slug)
	if err != nil {
		return Comment{}, err
	}
	if item.CommentsMode == "" {
		item.CommentsMode = "moderated"
	}
	if item.CommentsMode == "off" {
		return Comment{}, ErrSiteDisabled
	}
	displayPath = strings.Trim(displayPath, "/")
	if !ValidDisplayPath(displayPath) {
		return Comment{}, ErrSiteNotFound
	}
	status := "pending"
	if item.CommentsMode == "open" {
		status = "visible"
	}
	var comment Comment
	err = s.Store.Pool.QueryRow(ctx, `
		WITH bound AS (
			SELECT b.asset_id, b.display_path
			FROM site.site_content_bindings b
			JOIN asset.assets a
			  ON a.organization_id = b.organization_id AND a.id = b.asset_id
			  AND a.current_published_version_id IS NOT NULL
			WHERE b.organization_id = $1::uuid AND b.site_id = $2::uuid
			  AND b.display_path = $3
			LIMIT 1
		)
		INSERT INTO site.site_comments
			(organization_id, site_id, asset_id, display_path, author_user_id, body, status)
		SELECT $1::uuid, $2::uuid, bound.asset_id, bound.display_path, $4::uuid, $5, $6
		FROM bound
		WHERE NOT EXISTS (
			SELECT 1 FROM site.site_comments recent
			WHERE recent.site_id = $2::uuid AND recent.asset_id = bound.asset_id
			  AND recent.author_user_id = $4::uuid
			  AND recent.created_at > now() - $7::interval
		)
		RETURNING id::text, asset_id::text, display_path, body, status, created_at
	`, item.OrganizationID, item.ID, displayPath, principal.UserID, body, status,
		fmt.Sprint(CommentCooldown.Seconds())+" seconds").Scan(
		&comment.ID, &comment.AssetID, &comment.DisplayPath, &comment.Body,
		&comment.Status, &comment.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		// Either the path is not a bound published asset, or the cooldown.
		// The distinction stays hidden (anti-probing parity).
		return Comment{}, ErrSiteNotFound
	}
	if err != nil {
		return Comment{}, fmt.Errorf("create comment: %w", err)
	}
	// The fact rides its own short transaction (AppendCommentEventPool); a
	// failed append falls back to the cache TTL backstop.
	_ = s.AppendCommentEventPool(ctx, item, principal, comment)
	return comment, nil
}

// commentEvent renders the site.comment_created fact (consumed by the
// delivery cache invalidator). It uses a pool-level append (no surrounding
// business transaction); a failed event falls back to the TTL backstop.
func commentEvent(item Site, principal auth.Principal, comment Comment) eventing.Event {
	return eventing.Event{
		OrganizationID:   item.OrganizationID,
		WorkspaceID:      item.WorkspaceID,
		EventType:        eventing.EventSiteCommentCreated,
		AggregateType:    "site",
		AggregateID:      item.ID,
		AggregateVersion: item.Revision,
		PayloadVersion:   eventing.PayloadVersionV1,
		Actor:            eventing.ActorFromPrincipal(principal),
		Payload: map[string]any{
			"site_id":  item.ID,
			"asset_id": comment.AssetID,
		},
	}
}

// AppendCommentEventPool appends the comment fact outside a transaction
// (EventStore.AppendTx requires one; this helper owns a short tx).
func (s Service) AppendCommentEventPool(ctx context.Context, item Site, principal auth.Principal, comment Comment) error {
	if s.Events == nil || s.Store == nil || s.Store.Pool == nil {
		return nil
	}
	tx, err := s.Store.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := s.Events.AppendTx(ctx, tx, commentEvent(item, principal, comment)); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// VisibleComments lists the comments rendered on a detail page (public read:
// only visible ones, newest last for a natural reading flow).
func (s Service) VisibleComments(ctx context.Context, organizationID, siteID, assetID string, limit int) ([]Comment, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.Store.Pool.Query(ctx, `
		SELECT c.id::text, c.asset_id::text, c.display_path, u.display_name, c.body, c.created_at
		FROM site.site_comments c
		JOIN identity.users u ON u.id = c.author_user_id
		WHERE c.organization_id = $1::uuid AND c.site_id = $2::uuid
		  AND c.asset_id = $3::uuid AND c.status = 'visible'
		ORDER BY c.created_at ASC
		LIMIT $4::int
	`, organizationID, siteID, assetID, limit)
	if err != nil {
		return nil, fmt.Errorf("list visible comments: %w", err)
	}
	defer rows.Close()
	items := []Comment{}
	for rows.Next() {
		var comment Comment
		if err := rows.Scan(&comment.ID, &comment.AssetID, &comment.DisplayPath,
			&comment.AuthorName, &comment.Body, &comment.CreatedAt); err != nil {
			return nil, err
		}
		comment.Status = "visible"
		items = append(items, comment)
	}
	return items, rows.Err()
}

// ListComments pages the moderation queue (management face).
func (s Service) ListComments(ctx context.Context, principal auth.Principal, workspaceID, siteID, status string) (CommentPage, error) {
	if err := s.require(ctx, principal, workspaceID, authz.ActionSiteRead); err != nil {
		return CommentPage{}, err
	}
	if status == "" {
		status = "pending"
	}
	if status != "pending" && status != "visible" && status != "rejected" && status != "all" {
		return CommentPage{}, ErrInvalidInput
	}
	filter := ""
	args := []any{principal.OrganizationID, workspaceID, siteID}
	if status != "all" {
		args = append(args, status)
		filter = fmt.Sprintf(" AND c.status = $%d", len(args))
	}
	rows, err := s.Store.Pool.Query(ctx, `
		SELECT c.id::text, c.asset_id::text, c.display_path, u.display_name, c.body, c.status, c.created_at
		FROM site.site_comments c
		JOIN identity.users u ON u.id = c.author_user_id
		WHERE c.organization_id = $1::uuid
		  AND c.site_id = (SELECT id FROM site.public_sites
		                   WHERE organization_id = $1::uuid AND workspace_id = $2::uuid AND id = $3::uuid)
	`+filter+`
		ORDER BY c.created_at DESC
		LIMIT 100
	`, args...)
	if err != nil {
		return CommentPage{}, fmt.Errorf("list comments: %w", err)
	}
	defer rows.Close()
	page := CommentPage{Items: []Comment{}}
	for rows.Next() {
		var comment Comment
		if err := rows.Scan(&comment.ID, &comment.AssetID, &comment.DisplayPath,
			&comment.AuthorName, &comment.Body, &comment.Status, &comment.CreatedAt); err != nil {
			return CommentPage{}, err
		}
		page.Items = append(page.Items, comment)
	}
	return page, rows.Err()
}

// ModerateComment sets the status (visible/rejected) behind site.manage.
func (s Service) ModerateComment(ctx context.Context, principal auth.Principal, workspaceID, siteID, commentID, status string) error {
	if status != "visible" && status != "rejected" && status != "pending" {
		return ErrInvalidInput
	}
	if err := s.require(ctx, principal, workspaceID, authz.ActionSiteManage); err != nil {
		return err
	}
	tag, err := s.Store.Pool.Exec(ctx, `
		UPDATE site.site_comments c
		SET status = $4, moderated_by = $5::uuid, moderated_at = now()
		WHERE c.organization_id = $1::uuid
		  AND c.site_id = (SELECT id FROM site.public_sites
		                   WHERE organization_id = $1::uuid AND workspace_id = $2::uuid AND id = $3::uuid)
		  AND c.id = $6::uuid
	`, principal.OrganizationID, workspaceID, siteID, status, principal.UserID, commentID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrCommentNotFound
	}
	// The moderation changed what the detail page renders — emit the site
	// fact so the delivery cache invalidation chain runs (design §8.4).
	s.emitSiteCommentFact(ctx, principal, workspaceID, siteID)
	return nil
}

// DeleteComment removes one comment behind site.manage.
func (s Service) DeleteComment(ctx context.Context, principal auth.Principal, workspaceID, siteID, commentID string) error {
	if err := s.require(ctx, principal, workspaceID, authz.ActionSiteManage); err != nil {
		return err
	}
	tag, err := s.Store.Pool.Exec(ctx, `
		DELETE FROM site.site_comments c
		WHERE c.organization_id = $1::uuid
		  AND c.site_id = (SELECT id FROM site.public_sites
		                   WHERE organization_id = $1::uuid AND workspace_id = $2::uuid AND id = $3::uuid)
		  AND c.id = $4::uuid
	`, principal.OrganizationID, workspaceID, siteID, commentID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrCommentNotFound
	}
	s.emitSiteCommentFact(ctx, principal, workspaceID, siteID)
	return nil
}

// emitSiteCommentFact records site.site_changed (action=comment_moderated)
// in its own short transaction; a failed append falls back to the cache TTL.
func (s Service) emitSiteCommentFact(ctx context.Context, principal auth.Principal, workspaceID, siteID string) {
	if s.Events == nil || s.Store == nil || s.Store.Pool == nil {
		return
	}
	var item Site
	if err := s.Store.Pool.QueryRow(ctx, `
		SELECT `+siteColumns+` FROM site.public_sites
		WHERE organization_id = $1::uuid AND workspace_id = $2::uuid AND id = $3::uuid
	`, principal.OrganizationID, workspaceID, siteID).Scan(
		&item.ID, &item.OrganizationID, &item.WorkspaceID, &item.Slug, &item.Name,
		&item.Domain, &item.Template, &item.DefaultContentScope, &item.Status, &item.Revision,
		&item.HomepageConfig, &item.NavigationConfig, &item.StyleConfig, &item.CustomCss,
		&item.CommentsMode, &item.PublishedReleaseID, &item.CreatedAt, &item.UpdatedAt); err != nil {
		return
	}
	tx, err := s.Store.Pool.Begin(ctx)
	if err != nil {
		return
	}
	defer tx.Rollback(ctx)
	if _, err := s.Events.AppendTx(ctx, tx, eventing.Event{
		OrganizationID:   item.OrganizationID,
		WorkspaceID:      item.WorkspaceID,
		EventType:        eventing.EventSiteChanged,
		AggregateType:    "site",
		AggregateID:      item.ID,
		AggregateVersion: item.Revision,
		PayloadVersion:   eventing.PayloadVersionV1,
		Actor:            eventing.ActorFromPrincipal(principal),
		Payload: eventing.SiteChangedPayload{
			SiteID:      item.ID,
			WorkspaceID: item.WorkspaceID,
			Action:      "comment_moderated",
		},
	}); err != nil {
		return
	}
	_ = tx.Commit(ctx)
}
