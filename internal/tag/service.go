package tag

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"agentchunzhi/internal/auth"
	"agentchunzhi/internal/authz"
	"agentchunzhi/internal/eventing"
	"agentchunzhi/internal/store"

	"github.com/jackc/pgx/v5"
)

// Tag is the workspace-scoped definition aggregate.
type Tag struct {
	ID             string     `json:"id"`
	WorkspaceID    string     `json:"workspace_id"`
	Key            string     `json:"key"`
	DisplayName    string     `json:"display_name"`
	Slug           string     `json:"slug"`
	Status         string     `json:"status"`
	Revision       int64      `json:"revision"`
	CreatedBy      string     `json:"created_by,omitempty"`
	UpdatedBy      string     `json:"updated_by,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
	ArchivedAt     *time.Time `json:"archived_at,omitempty"`
	ETag           string     `json:"etag"`
	// sortValue is the catalog sort key of the row (never serialized); List
	// embeds it into the next-page cursor.
	sortValue string `json:"-"`
}

// Summary is the DTO embedded in draft/version/query responses.
type Summary struct {
	ID          string `json:"id"`
	Key         string `json:"key"`
	DisplayName string `json:"display_name"`
	Slug        string `json:"slug"`
	Status      string `json:"status"`
}

func (t Tag) Summary() Summary {
	return Summary{ID: t.ID, Key: t.Key, DisplayName: t.DisplayName, Slug: t.Slug, Status: t.Status}
}

type ListInput struct {
	Query         string
	Status        string // active|archived|all, default active
	Sort          string // key:asc|display_name:asc|created_at:desc
	IncludeUsage  bool
	Limit         int
	Cursor        string
}

type Usage struct {
	WorkingAssets   int64 `json:"working_assets"`
	PublishedAssets int64 `json:"published_assets"`
	OpenDrafts      int64 `json:"open_drafts"`
}

type Page struct {
	Items      []Tag
	HasMore    bool
	NextCursor string
}

// Service implements the definition lifecycle. Permission decisions are made
// by the caller through authz actions; the service never compares roles.
type Service struct {
	Store  *store.Store
	Events *eventing.EventStore
}

func (s Service) validID(value string) bool {
	if len(value) != 36 {
		return false
	}
	for index, char := range value {
		if index == 8 || index == 13 || index == 18 || index == 23 {
			if char != '-' {
				return false
			}
			continue
		}
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') && (char < 'A' || char > 'F') {
			return false
		}
	}
	return true
}

// Create registers a new active tag. Concurrent same-key creates surface as
// ErrKeyExists for the loser — member APIs never paper over conflicts.
func (s Service) Create(ctx context.Context, principal auth.Principal, workspaceID, key, displayName string) (Tag, error) {
	if !s.validID(workspaceID) {
		return Tag{}, ErrInvalidInput
	}
	displayName, err := ValidateDisplayName(displayName)
	if err != nil {
		return Tag{}, ErrInvalidInput
	}
	if strings.TrimSpace(key) == "" {
		key = displayName
	}
	normalized, err := NormalizeKey(key)
	if err != nil {
		return Tag{}, ErrInvalidInput
	}
	if s.Events == nil {
		return Tag{}, errors.New("event store is not initialized")
	}
	tx, err := s.Store.Pool.Begin(ctx)
	if err != nil {
		return Tag{}, err
	}
	defer tx.Rollback(ctx)
	var id string
	if err := tx.QueryRow(ctx, `SELECT gen_random_uuid()::text`).Scan(&id); err != nil {
		return Tag{}, err
	}
	slug := DeriveSlug(normalized, id)
	var archivedOnly int
	if err := tx.QueryRow(ctx, `
		SELECT count(*) FROM asset.tags
		WHERE organization_id = $1::uuid AND workspace_id = $2::uuid
		  AND normalized_key = $3 AND status = 'archived'
	`, principal.OrganizationID, workspaceID, normalized).Scan(&archivedOnly); err != nil {
		return Tag{}, err
	}
	if archivedOnly > 0 {
		// Same-key archived tags keep their identity: restore instead of recreate.
		return Tag{}, ErrArchived
	}
	var keyConflict bool
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM asset.tags
			WHERE organization_id = $1::uuid AND workspace_id = $2::uuid AND normalized_key = $3
		)
	`, principal.OrganizationID, workspaceID, normalized).Scan(&keyConflict); err != nil {
		return Tag{}, err
	}
	if keyConflict {
		return Tag{}, ErrKeyExists
	}
	var slugConflict bool
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM asset.tags
			WHERE organization_id = $1::uuid AND workspace_id = $2::uuid AND slug = $3
		)
	`, principal.OrganizationID, workspaceID, slug).Scan(&slugConflict); err != nil {
		return Tag{}, err
	}
	if slugConflict {
		slug = DeriveSlug(normalized, id) + "-" + id[:8]
	}
	var item Tag
	err = tx.QueryRow(ctx, `
		INSERT INTO asset.tags
			(id, organization_id, workspace_id, normalized_key, display_name, slug, status, revision, created_by, updated_by)
		VALUES ($1::uuid, $2::uuid, $3::uuid, $4, $5, $6, 'active', 1, $7::uuid, $7::uuid)
		RETURNING id::text, workspace_id::text, normalized_key, display_name, slug, status, revision, created_by::text, updated_by::text, created_at, updated_at, archived_at
	`, id, principal.OrganizationID, workspaceID, normalized, displayName, slug, principal.UserID).Scan(
		&item.ID, &item.WorkspaceID, &item.Key, &item.DisplayName, &item.Slug, &item.Status,
		&item.Revision, &item.CreatedBy, &item.UpdatedBy, &item.CreatedAt, &item.UpdatedAt, &item.ArchivedAt)
	if err != nil {
		return Tag{}, fmt.Errorf("insert tag: %w", err)
	}
	item.ETag = fmt.Sprint(item.Revision)
	store.AppendAuditTx(ctx, tx, store.NewAuditEntry("tag.create", principal.OrganizationID, principal.UserID, "tag", item.ID, map[string]any{
		"workspace_id": workspaceID, "key": normalized, "display_name": displayName,
	}), workspaceID)
	if err := appendTagEvent(ctx, tx, s.Events, principal, workspaceID, eventing.EventTagCreated, item, eventing.TagLifecyclePayload{
		TagID: item.ID, WorkspaceID: workspaceID,
	}); err != nil {
		return Tag{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Tag{}, err
	}
	return item, nil
}

func appendTagEvent(ctx context.Context, tx pgx.Tx, events *eventing.EventStore, principal auth.Principal, workspaceID, eventType string, item Tag, payload any) error {
	raw, err := eventing.EncodePayload(payload)
	if err != nil {
		return err
	}
	_, err = events.AppendTx(ctx, tx, eventing.Event{
		OrganizationID:   principal.OrganizationID,
		WorkspaceID:      workspaceID,
		EventType:        eventType,
		AggregateType:    "tag",
		AggregateID:      item.ID,
		AggregateVersion: item.Revision,
		PayloadVersion:   eventing.PayloadVersionV1,
		Actor:            eventing.ActorFromPrincipal(principal),
		Payload:          raw,
	})
	return err
}

// Get reads one tag; archived tags remain readable for management and
// historical context.
func (s Service) Get(ctx context.Context, principal auth.Principal, workspaceID, tagID string) (Tag, error) {
	if !s.validID(tagID) {
		return Tag{}, ErrInvalidInput
	}
	var item Tag
	err := s.Store.Pool.QueryRow(ctx, `
		SELECT id::text, workspace_id::text, normalized_key, display_name, slug, status, revision,
		       created_by::text, updated_by::text, created_at, updated_at, archived_at
		FROM asset.tags
		WHERE organization_id = $1::uuid AND workspace_id = $2::uuid AND id = $3::uuid
	`, principal.OrganizationID, workspaceID, tagID).Scan(
		&item.ID, &item.WorkspaceID, &item.Key, &item.DisplayName, &item.Slug, &item.Status,
		&item.Revision, &item.CreatedBy, &item.UpdatedBy, &item.CreatedAt, &item.UpdatedAt, &item.ArchivedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Tag{}, ErrNotFound
	}
	item.ETag = fmt.Sprint(item.Revision)
	return item, err
}

// tagListCursorVersion is the payload schema version of the catalog cursor.
const tagListCursorVersion = 1

// tagListCursor is the opaque keyset cursor of one sort option: the sort key
// value plus the id tie-breaker of the last returned row.
type tagListCursor struct {
	Version int    `json:"v"`
	Sort    string `json:"sort"`
	Value   string `json:"value"`
	ID      string `json:"id"`
}

// encodeTagListCursor renders base64url(JSON) so key values never depend on a
// delimiter that display names could contain.
func encodeTagListCursor(sort, value, id string) (string, error) {
	payload, err := json.Marshal(tagListCursor{Version: tagListCursorVersion, Sort: sort, Value: value, ID: id})
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(payload), nil
}

// decodeTagListCursor validates the framing, the version and — for the
// created_at sort — the timestamp value; sort must match the request the page
// continues. Malformed cursors fail with ErrInvalidInput (422).
func decodeTagListCursor(token, sort string) (tagListCursor, error) {
	if token == "" {
		return tagListCursor{}, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return tagListCursor{}, ErrInvalidInput
	}
	var cursor tagListCursor
	if err := json.Unmarshal(raw, &cursor); err != nil {
		return tagListCursor{}, ErrInvalidInput
	}
	if cursor.Version != tagListCursorVersion || cursor.Sort != sort || cursor.ID == "" || cursor.Value == "" {
		return tagListCursor{}, ErrInvalidInput
	}
	if sort == "created_at:desc" {
		if _, err := time.Parse(time.RFC3339Nano, cursor.Value); err != nil {
			return tagListCursor{}, ErrInvalidInput
		}
	}
	return cursor, nil
}

// List returns the workspace catalog with cursor pagination.
func (s Service) List(ctx context.Context, principal auth.Principal, workspaceID string, input ListInput) (Page, error) {
	if !s.validID(workspaceID) {
		return Page{}, ErrInvalidInput
	}
	limit := input.Limit
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	status := input.Status
	if status == "" {
		status = StatusActive
	}
	switch status {
	case StatusActive, StatusArchived, "all":
	default:
		return Page{}, ErrInvalidInput
	}
	sort := input.Sort
	if sort == "" {
		sort = "key:asc"
	}
	order := "normalized_key ASC, id ASC"
	sortExpr := "normalized_key"
	switch sort {
	case "key:asc":
	case "display_name:asc":
		order = "lower(display_name) ASC, id ASC"
		sortExpr = "lower(display_name)"
	case "created_at:desc":
		order = "created_at DESC, id DESC"
		sortExpr = "created_at"
	default:
		return Page{}, ErrInvalidInput
	}
	cursor, err := decodeTagListCursor(input.Cursor, sort)
	if err != nil {
		return Page{}, err
	}
	if cursor.ID != "" && !s.validID(cursor.ID) {
		return Page{}, ErrInvalidInput
	}
	where := []string{"organization_id = $1::uuid", "workspace_id = $2::uuid"}
	args := []any{principal.OrganizationID, workspaceID}
	arg := func(value any) string {
		args = append(args, value)
		return fmt.Sprintf("$%d", len(args))
	}
	if status != "all" {
		where = append(where, "status = "+arg(status))
	}
	if q := strings.TrimSpace(input.Query); q != "" {
		where = append(where, "(normalized_key ILIKE "+arg("%"+q+"%")+" OR display_name ILIKE "+arg("%"+q+"%")+")")
	}
	if cursor.ID != "" {
		// Keyset continuation on the exact sort key plus the id tie-breaker.
		switch sort {
		case "key:asc":
			where = append(where, "(normalized_key, id) > ("+arg(cursor.Value)+", "+arg(cursor.ID)+"::uuid)")
		case "display_name:asc":
			where = append(where, "(lower(display_name), id) > ("+arg(cursor.Value)+", "+arg(cursor.ID)+"::uuid)")
		case "created_at:desc":
			where = append(where, "(created_at, id) < ("+arg(cursor.Value)+"::timestamptz, "+arg(cursor.ID)+"::uuid)")
		}
	}
	query := fmt.Sprintf(`
		SELECT id::text, workspace_id::text, normalized_key, display_name, slug, status, revision,
		       created_by::text, updated_by::text, created_at, updated_at, archived_at,
		       %s::text
		FROM asset.tags
		WHERE %s
		ORDER BY %s
		LIMIT %d
	`, sortExpr, strings.Join(where, " AND "), order, limit+1)
	rows, err := s.Store.Pool.Query(ctx, query, args...)
	if err != nil {
		return Page{}, err
	}
	defer rows.Close()
	page := Page{Items: make([]Tag, 0, limit+1)}
	for rows.Next() {
		var item Tag
		var sortValue string
		if err := rows.Scan(&item.ID, &item.WorkspaceID, &item.Key, &item.DisplayName, &item.Slug,
			&item.Status, &item.Revision, &item.CreatedBy, &item.UpdatedBy, &item.CreatedAt,
			&item.UpdatedAt, &item.ArchivedAt, &sortValue); err != nil {
			return Page{}, err
		}
		item.ETag = fmt.Sprint(item.Revision)
		if sort == "created_at:desc" {
			// RFC 3339 travels through the cursor verbatim; the database text
			// rendering depends on the session timezone.
			item.sortValue = item.CreatedAt.UTC().Format(time.RFC3339Nano)
		} else {
			item.sortValue = sortValue
		}
		page.Items = append(page.Items, item)
	}
	if err := rows.Err(); err != nil {
		return Page{}, err
	}
	if len(page.Items) > limit {
		page.HasMore = true
		page.Items = page.Items[:limit]
		last := page.Items[len(page.Items)-1]
		page.NextCursor, err = encodeTagListCursor(sort, last.sortValue, last.ID)
		if err != nil {
			return Page{}, err
		}
	}
	return page, nil
}

// Rename changes only the display name; key and slug are immutable identity.
func (s Service) Rename(ctx context.Context, principal auth.Principal, workspaceID, tagID, expectedRevision, displayName string) (Tag, error) {
	displayName, err := ValidateDisplayName(displayName)
	if err != nil {
		return Tag{}, ErrInvalidInput
	}
	tx, err := s.Store.Pool.Begin(ctx)
	if err != nil {
		return Tag{}, err
	}
	defer tx.Rollback(ctx)
	item, err := lockTag(ctx, tx, principal.OrganizationID, workspaceID, tagID, expectedRevision)
	if err != nil {
		return Tag{}, err
	}
	var oldName string
	if err := tx.QueryRow(ctx, `UPDATE asset.tags SET display_name = $4, revision = revision + 1, updated_by = $5::uuid, updated_at = now()
		WHERE organization_id = $1::uuid AND workspace_id = $2::uuid AND id = $3::uuid
		RETURNING revision`, principal.OrganizationID, workspaceID, tagID, displayName, principal.UserID).Scan(&item.Revision); err != nil {
		return Tag{}, err
	}
	_ = oldName
	item.DisplayName = displayName
	item.ETag = fmt.Sprint(item.Revision)
	store.AppendAuditTx(ctx, tx, store.NewAuditEntry("tag.update", principal.OrganizationID, principal.UserID, "tag", tagID, map[string]any{
		"workspace_id": workspaceID, "display_name": displayName,
	}), workspaceID)
	if err := appendTagEvent(ctx, tx, s.Events, principal, workspaceID, eventing.EventTagUpdated, item, eventing.TagUpdatedPayload{
		TagID: item.ID, WorkspaceID: workspaceID, ChangedFields: []string{"display_name"},
	}); err != nil {
		return Tag{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Tag{}, err
	}
	return item, nil
}

// Archive retires a tag: it can no longer enter drafts or new versions, but
// historical relations stay untouched.
func (s Service) Archive(ctx context.Context, principal auth.Principal, workspaceID, tagID, expectedRevision string) (Tag, error) {
	tx, err := s.Store.Pool.Begin(ctx)
	if err != nil {
		return Tag{}, err
	}
	defer tx.Rollback(ctx)
	item, err := lockTag(ctx, tx, principal.OrganizationID, workspaceID, tagID, expectedRevision)
	if err != nil {
		return Tag{}, err
	}
	if item.Status == StatusArchived {
		return Tag{}, ErrAlreadyArchived
	}
	if err := tx.QueryRow(ctx, `
		UPDATE asset.tags
		SET status = 'archived', archived_at = now(), archived_by = $4::uuid, revision = revision + 1, updated_at = now()
		WHERE organization_id = $1::uuid AND workspace_id = $2::uuid AND id = $3::uuid
		RETURNING revision
	`, principal.OrganizationID, workspaceID, tagID, principal.UserID).Scan(&item.Revision); err != nil {
		return Tag{}, err
	}
	item.Status = StatusArchived
	now := time.Now().UTC()
	item.ArchivedAt = &now
	item.ETag = fmt.Sprint(item.Revision)
	store.AppendAuditTx(ctx, tx, store.NewAuditEntry("tag.archive", principal.OrganizationID, principal.UserID, "tag", tagID, map[string]any{"workspace_id": workspaceID}), workspaceID)
	if err := appendTagEvent(ctx, tx, s.Events, principal, workspaceID, eventing.EventTagArchived, item, eventing.TagLifecyclePayload{
		TagID: item.ID, WorkspaceID: workspaceID,
	}); err != nil {
		return Tag{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Tag{}, err
	}
	return item, nil
}

// Restore returns an archived tag to active; identity never changed so no
// merge is needed.
func (s Service) Restore(ctx context.Context, principal auth.Principal, workspaceID, tagID, expectedRevision string) (Tag, error) {
	tx, err := s.Store.Pool.Begin(ctx)
	if err != nil {
		return Tag{}, err
	}
	defer tx.Rollback(ctx)
	item, err := lockTag(ctx, tx, principal.OrganizationID, workspaceID, tagID, expectedRevision)
	if err != nil {
		return Tag{}, err
	}
	if item.Status == StatusActive {
		return Tag{}, ErrAlreadyActive
	}
	if err := tx.QueryRow(ctx, `
		UPDATE asset.tags
		SET status = 'active', archived_at = NULL, archived_by = NULL, revision = revision + 1, updated_at = now()
		WHERE organization_id = $1::uuid AND workspace_id = $2::uuid AND id = $3::uuid
		RETURNING revision
	`, principal.OrganizationID, workspaceID, tagID).Scan(&item.Revision); err != nil {
		return Tag{}, err
	}
	item.Status = StatusActive
	item.ArchivedAt = nil
	item.ETag = fmt.Sprint(item.Revision)
	store.AppendAuditTx(ctx, tx, store.NewAuditEntry("tag.restore", principal.OrganizationID, principal.UserID, "tag", tagID, map[string]any{"workspace_id": workspaceID}), workspaceID)
	if err := appendTagEvent(ctx, tx, s.Events, principal, workspaceID, eventing.EventTagRestored, item, eventing.TagLifecyclePayload{
		TagID: item.ID, WorkspaceID: workspaceID,
	}); err != nil {
		return Tag{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Tag{}, err
	}
	return item, nil
}

// lockTag loads and locks the tag row, verifying the optimistic revision.
func lockTag(ctx context.Context, tx pgx.Tx, organizationID, workspaceID, tagID, expectedRevision string) (Tag, error) {
	var item Tag
	err := tx.QueryRow(ctx, `
		SELECT id::text, workspace_id::text, normalized_key, display_name, slug, status, revision,
		       created_by::text, updated_by::text, created_at, updated_at, archived_at
		FROM asset.tags
		WHERE organization_id = $1::uuid AND workspace_id = $2::uuid AND id = $3::uuid
		FOR UPDATE
	`, organizationID, workspaceID, tagID).Scan(
		&item.ID, &item.WorkspaceID, &item.Key, &item.DisplayName, &item.Slug, &item.Status,
		&item.Revision, &item.CreatedBy, &item.UpdatedBy, &item.CreatedAt, &item.UpdatedAt, &item.ArchivedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Tag{}, ErrNotFound
	}
	if err != nil {
		return Tag{}, err
	}
	// An empty expected revision or the If-Match wildcard "*" only demands the
	// tag exists; any concrete revision must match exactly.
	trimmed := strings.Trim(expectedRevision, "\"")
	if expectedRevision != "" && trimmed != "*" && fmt.Sprint(item.Revision) != trimmed {
		return Tag{}, ErrRevisionMismatch
	}
	return item, nil
}

// Usage aggregates per-page usage counts in one grouped query set (no N+1).
// The published counter follows the current published pointer only — the
// working counter stays per working version (标签系统最终设计架构: 当前发布
// 版本决定公开标签集合).
func (s Service) Usage(ctx context.Context, principal auth.Principal, workspaceID string, tagIDs []string) (map[string]Usage, error) {
	result := make(map[string]Usage, len(tagIDs))
	if len(tagIDs) == 0 {
		return result, nil
	}
	rows, err := s.Store.Pool.Query(ctx, `
		SELECT avt.tag_id::text,
		       count(DISTINCT CASE WHEN a.publication_status <> 'archived' THEN a.id END),
		       count(DISTINCT CASE WHEN a.current_published_version_id = avt.asset_version_id
		                             AND a.publication_status = 'published' THEN a.id END)
		FROM asset.asset_version_tags avt
		JOIN asset.asset_versions v ON v.organization_id = avt.organization_id AND v.id = avt.asset_version_id
		JOIN asset.assets a ON a.organization_id = v.organization_id AND a.id = v.asset_id
		WHERE avt.organization_id = $1::uuid AND avt.workspace_id = $2::uuid
		  AND avt.tag_id = ANY($3::uuid[])
		GROUP BY avt.tag_id
	`, principal.OrganizationID, workspaceID, tagIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var tagID string
		var usage Usage
		if err := rows.Scan(&tagID, &usage.WorkingAssets, &usage.PublishedAssets); err != nil {
			return nil, err
		}
		result[tagID] = usage
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	draftRows, err := s.Store.Pool.Query(ctx, `
		SELECT adt.tag_id::text, count(DISTINCT d.asset_id)
		FROM asset.asset_draft_tags adt
		JOIN asset.asset_drafts d ON d.organization_id = adt.organization_id AND d.id = adt.asset_draft_id
		JOIN asset.assets a ON a.organization_id = d.organization_id AND a.id = d.asset_id
		WHERE adt.organization_id = $1::uuid AND adt.workspace_id = $2::uuid
		  AND adt.tag_id = ANY($3::uuid[])
		  AND a.publication_status <> 'archived'
		  AND d.revision <> d.committed_revision
		GROUP BY adt.tag_id
	`, principal.OrganizationID, workspaceID, tagIDs)
	if err != nil {
		return nil, err
	}
	defer draftRows.Close()
	for draftRows.Next() {
		var tagID string
		var open int64
		if err := draftRows.Scan(&tagID, &open); err != nil {
			return nil, err
		}
		usage := result[tagID]
		usage.OpenDrafts = open
		result[tagID] = usage
	}
	return result, draftRows.Err()
}

var _ = authz.ActionTagManage
