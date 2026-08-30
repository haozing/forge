package site

// binding.go — the site_content_bindings aggregate: write-time binding gate
// (plan section 3.1), display metadata CRUD and the preview snapshot. A
// binding stores asset identity plus presentation metadata only; the served
// version is always resolved live from the asset's published pointer (D1).

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

// Binding is one display entry of a site: which asset appears at which
// public path with which presentation metadata.
type Binding struct {
	ID                 string          `json:"id"`
	SiteID             string          `json:"site_id"`
	AssetID            string          `json:"asset_id"`
	DisplayPath        string          `json:"display_path"`
	ContentType        string          `json:"content_type"`
	SectionSlug        string          `json:"section_slug"`
	SortOrder          int             `json:"sort_order"`
	OnHomepage         bool            `json:"on_homepage"`
	OnNavigation       bool            `json:"on_navigation"`
	DisplayConfig      json.RawMessage `json:"display_config"`
	DisplayPublishedAt *time.Time      `json:"display_published_at,omitempty"`
	CreatedAt          time.Time       `json:"created_at"`
	UpdatedAt          time.Time       `json:"updated_at"`
}

// CreateBindingInput carries the POST .../bindings body. Empty enums fall
// back to the 0010 defaults (article / ” / 0 / false / {}).
type CreateBindingInput struct {
	AssetID       string
	DisplayPath   string
	ContentType   string
	SectionSlug   string
	SortOrder     int
	OnHomepage    bool
	OnNavigation  bool
	DisplayConfig json.RawMessage
}

// UpdateBindingInput carries the PATCH .../bindings/{bindingId} body; nil
// pointers stay unchanged. The bound asset is immutable: re-targeting means
// delete + create so the write-time gate runs again.
type UpdateBindingInput struct {
	DisplayPath   *string
	ContentType   *string
	SectionSlug   *string
	SortOrder     *int
	OnHomepage    *bool
	OnNavigation  *bool
	DisplayConfig *json.RawMessage
}

// BindingPage is one keyset page of the site binding catalog.
type BindingPage struct {
	Items      []Binding
	HasMore    bool
	NextCursor string
}

// PreviewSnapshot is the management preview payload: the site plus its
// bindings plus the homepage configuration in one no-store JSON document.
// The P5-3 wave extends it into the full public-face preview.
type PreviewSnapshot struct {
	Site        Site      `json:"site"`
	Bindings    []Binding `json:"bindings"`
	GeneratedAt time.Time `json:"generated_at"`
}

const bindingColumns = `b.id::text, b.site_id::text, b.asset_id::text, b.display_path,
	b.content_type, b.section_slug, b.sort_order, b.on_homepage, b.on_navigation,
	b.display_config, b.display_published_at, b.created_at, b.updated_at`

func scanBindingRow(row interface{ Scan(...any) error }) (Binding, error) {
	var item Binding
	if err := row.Scan(&item.ID, &item.SiteID, &item.AssetID, &item.DisplayPath,
		&item.ContentType, &item.SectionSlug, &item.SortOrder, &item.OnHomepage,
		&item.OnNavigation, &item.DisplayConfig, &item.DisplayPublishedAt,
		&item.CreatedAt, &item.UpdatedAt); err != nil {
		return Binding{}, err
	}
	return item, nil
}

// keysetCursor is the shared (created_at, id) cursor of the site domain
// lists; base64url(JSON) keeps the pair opaque and delimiter-free.
type keysetCursor struct {
	CreatedAt time.Time `json:"created_at"`
	ID        string    `json:"id"`
}

func encodeKeysetCursor(created time.Time, id string) (string, error) {
	raw, err := json.Marshal(keysetCursor{CreatedAt: created.UTC(), ID: id})
	if err != nil {
		return "", fmt.Errorf("encode cursor: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func decodeKeysetCursor(token string) (keysetCursor, error) {
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return keysetCursor{}, err
	}
	var cursor keysetCursor
	if err := json.Unmarshal(raw, &cursor); err != nil {
		return keysetCursor{}, err
	}
	if cursor.CreatedAt.IsZero() || !validID(cursor.ID) {
		return keysetCursor{}, errors.New("cursor fields invalid")
	}
	return cursor, nil
}

// queryRunner abstracts pool and transaction reads for the shared list query.
type queryRunner interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

// listBindingsPage runs the keyset binding list over a pool or transaction.
func listBindingsPage(ctx context.Context, db queryRunner, organizationID, workspaceID, siteID, cursor string, limit int) (BindingPage, error) {
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	var cursorTime *time.Time
	// cursorID rides as interface{}: an empty string would fail uuid Bind
	// even when the NULL comparison short-circuits. cursorTime stays a typed
	// *time.Time so pgx encodes it natively as timestamptz — a NULLIF text
	// cast would force the parameter to text, which time.Time cannot
	// encode into (paged lists would fail).
	var cursorID any
	if strings.TrimSpace(cursor) != "" {
		parsed, err := decodeKeysetCursor(cursor)
		if err != nil {
			return BindingPage{}, ErrInvalidInput
		}
		cursorTime = &parsed.CreatedAt
		cursorID = parsed.ID
	}
	rows, err := db.Query(ctx, `
		SELECT `+bindingColumns+`
		FROM site.site_content_bindings b
		WHERE b.organization_id = $1::uuid AND b.workspace_id = $2::uuid AND b.site_id = $3::uuid
		  AND ($4::timestamptz IS NULL OR (b.created_at, b.id) < ($4::timestamptz, $5::uuid))
		ORDER BY b.created_at DESC, b.id DESC
		LIMIT $6::int
	`, organizationID, workspaceID, siteID, cursorTime, cursorID, limit+1)
	if err != nil {
		return BindingPage{}, fmt.Errorf("list bindings: %w", err)
	}
	defer rows.Close()
	page := BindingPage{Items: make([]Binding, 0, limit+1)}
	for rows.Next() {
		item, err := scanBindingRow(rows)
		if err != nil {
			return BindingPage{}, err
		}
		page.Items = append(page.Items, item)
	}
	if err := rows.Err(); err != nil {
		return BindingPage{}, fmt.Errorf("iterate bindings: %w", err)
	}
	if len(page.Items) > limit {
		page.HasMore = true
		page.Items = page.Items[:limit]
		last := page.Items[len(page.Items)-1]
		if page.NextCursor, err = encodeKeysetCursor(last.CreatedAt, last.ID); err != nil {
			return BindingPage{}, err
		}
	}
	return page, nil
}

// ListBindings pages the bindings of one site (created_at, id) keyset. The
// stage 5 management matrix gates even the listing behind site.manage.
func (s Service) ListBindings(ctx context.Context, principal auth.Principal, workspaceID, siteID, cursor string, limit int) (BindingPage, error) {
	if !validID(workspaceID) || !validID(siteID) {
		return BindingPage{}, ErrInvalidInput
	}
	if err := s.require(ctx, principal, workspaceID, authz.ActionSiteManage); err != nil {
		return BindingPage{}, err
	}
	// The site scope check runs before the list so a foreign site id answers
	// site_not_found instead of an empty page.
	if _, err := s.GetSite(ctx, principal, workspaceID, siteID); err != nil {
		return BindingPage{}, err
	}
	return listBindingsPage(ctx, s.Store.Pool, principal.OrganizationID, workspaceID, siteID, cursor, limit)
}

// bindingTargetFactsTx fetches the binding gate facts with one JOIN over the
// asset, its published version, the model version that version binds and the
// model head. Missing pointers decode as ineligible facts, never as SQL
// misses, so one query decides the whole gate.
func bindingTargetFactsTx(ctx context.Context, tx pgx.Tx, organizationID, workspaceID, assetID string) (BindingTargetFacts, *time.Time, error) {
	var facts BindingTargetFacts
	var publishedAt *time.Time
	err := tx.QueryRow(ctx, `
		SELECT a.visibility, a.publication_status,
		       (a.current_published_version_id IS NOT NULL),
		       COALESCE(rm.status = 'active', false),
		       COALESCE(mv.policy #>> '{channels,public_site,enabled}', 'false') = 'true',
		       a.published_at
		FROM asset.assets a
		JOIN model.resource_models rm
		  ON rm.organization_id = a.organization_id AND rm.id = a.resource_model_id
		LEFT JOIN asset.asset_versions pv
		  ON pv.organization_id = a.organization_id AND pv.id = a.current_published_version_id
		LEFT JOIN model.resource_model_versions mv
		  ON mv.organization_id = pv.organization_id AND mv.id = pv.resource_model_version_id
		WHERE a.organization_id = $1::uuid AND a.id = $2::uuid
		  AND a.workspace_id = $3::uuid AND a.deleted_at IS NULL
	`, organizationID, assetID, workspaceID).Scan(
		&facts.Visibility, &facts.PublicationStatus, &facts.HasPublishedVersion,
		&facts.ModelActive, &facts.PublicSiteChannel, &publishedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return BindingTargetFacts{}, nil, ErrBindingTargetInvalid
	}
	if err != nil {
		return BindingTargetFacts{}, nil, fmt.Errorf("load binding target facts: %w", err)
	}
	return facts, publishedAt, nil
}

// CreateBinding attaches a published asset to the site under the write-time
// gate: publication status, visibility against the site scope ceiling,
// public_site channel of the bound model policy and active model head. The
// binding mirror of published_at is set once here; the worker keeps it fresh.
func (s Service) CreateBinding(ctx context.Context, principal auth.Principal, workspaceID, siteID string, input CreateBindingInput) (Binding, error) {
	if !validID(workspaceID) || !validID(siteID) || !validID(input.AssetID) {
		return Binding{}, ErrInvalidInput
	}
	if err := s.require(ctx, principal, workspaceID, authz.ActionSiteManage); err != nil {
		return Binding{}, err
	}
	displayPath := strings.TrimSpace(input.DisplayPath)
	if !ValidDisplayPath(displayPath) {
		return Binding{}, ErrPathInvalid
	}
	contentType := input.ContentType
	if contentType == "" {
		contentType = ContentTypeArticle
	}
	if !ValidContentType(contentType) {
		return Binding{}, ErrInvalidInput
	}
	config, err := defaultConfig(input.DisplayConfig)
	if err != nil {
		return Binding{}, err
	}
	if s.Events == nil {
		return Binding{}, errors.New("event store is not initialized")
	}
	tx, err := s.Store.Pool.Begin(ctx)
	if err != nil {
		return Binding{}, err
	}
	defer tx.Rollback(ctx)
	site, err := lockSite(ctx, tx, principal.OrganizationID, workspaceID, siteID)
	if err != nil {
		return Binding{}, err
	}
	if site.Status == StatusDisabled {
		return Binding{}, ErrSiteDisabled
	}
	facts, publishedAt, err := bindingTargetFactsTx(ctx, tx, principal.OrganizationID, workspaceID, input.AssetID)
	if err != nil {
		return Binding{}, err
	}
	if !BindingTargetEligible(site.DefaultContentScope, facts) {
		return Binding{}, ErrBindingTargetInvalid
	}
	var exists bool
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM site.site_content_bindings
			WHERE organization_id = $1::uuid AND site_id = $2::uuid AND display_path = $3
		)
	`, principal.OrganizationID, siteID, displayPath).Scan(&exists); err != nil {
		return Binding{}, err
	}
	if exists {
		return Binding{}, ErrConflict
	}
	item, err := scanBindingRow(tx.QueryRow(ctx, `
		INSERT INTO site.site_content_bindings AS b
			(organization_id, workspace_id, site_id, asset_id, display_path, content_type,
			 section_slug, sort_order, on_homepage, on_navigation, display_config,
			 display_published_at, created_by)
		VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid, $5, $6, $7, $8::int, $9, $10, $11::jsonb, $12::timestamptz, $13::uuid)
		RETURNING `+bindingColumns+`
	`, principal.OrganizationID, workspaceID, siteID, input.AssetID, displayPath, contentType,
		input.SectionSlug, input.SortOrder, input.OnHomepage, input.OnNavigation,
		[]byte(config), publishedAt, principal.UserID))
	if err != nil {
		if uniqueViolation(err) {
			return Binding{}, ErrConflict
		}
		return Binding{}, fmt.Errorf("insert binding: %w", err)
	}
	// Binding changes re-render the site: bump the revision so the D4 list
	// and homepage ETags rotate with the binding catalog.
	site, err = bumpSiteRevision(ctx, tx, principal.OrganizationID, workspaceID, siteID)
	if err != nil {
		return Binding{}, err
	}
	recordBindingAudit(ctx, tx, principal, workspaceID, "site.binding.created", item, map[string]any{
		"site_slug": site.Slug, "display_path": item.DisplayPath,
	})
	if err := appendBindingEvents(ctx, tx, s.Events, principal, workspaceID, site, item, "created"); err != nil {
		return Binding{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		if uniqueViolation(err) {
			return Binding{}, ErrConflict
		}
		return Binding{}, err
	}
	return item, nil
}

// UpdateBinding patches the presentation metadata of one binding. The bound
// asset is immutable here and the gate is not re-run: the plan fixes
// validation at write time with the read layer as the safety net.
func (s Service) UpdateBinding(ctx context.Context, principal auth.Principal, workspaceID, siteID, bindingID string, input UpdateBindingInput) (Binding, error) {
	if !validID(workspaceID) || !validID(siteID) || !validID(bindingID) {
		return Binding{}, ErrInvalidInput
	}
	if err := s.require(ctx, principal, workspaceID, authz.ActionSiteManage); err != nil {
		return Binding{}, err
	}
	if input.DisplayPath != nil && !ValidDisplayPath(strings.TrimSpace(*input.DisplayPath)) {
		return Binding{}, ErrPathInvalid
	}
	if input.ContentType != nil && !ValidContentType(*input.ContentType) {
		return Binding{}, ErrInvalidInput
	}
	var config json.RawMessage
	if input.DisplayConfig != nil {
		if !validConfigObject(*input.DisplayConfig) {
			return Binding{}, ErrInvalidInput
		}
		config = *input.DisplayConfig
	}
	if s.Events == nil {
		return Binding{}, errors.New("event store is not initialized")
	}
	tx, err := s.Store.Pool.Begin(ctx)
	if err != nil {
		return Binding{}, err
	}
	defer tx.Rollback(ctx)
	site, err := lockSite(ctx, tx, principal.OrganizationID, workspaceID, siteID)
	if err != nil {
		return Binding{}, err
	}
	if site.Status == StatusDisabled {
		return Binding{}, ErrSiteDisabled
	}
	current, err := lockBinding(ctx, tx, principal.OrganizationID, workspaceID, siteID, bindingID)
	if err != nil {
		return Binding{}, err
	}
	if input.DisplayPath != nil {
		displayPath := strings.TrimSpace(*input.DisplayPath)
		if displayPath != current.DisplayPath {
			var exists bool
			if err := tx.QueryRow(ctx, `
				SELECT EXISTS (
					SELECT 1 FROM site.site_content_bindings
					WHERE organization_id = $1::uuid AND site_id = $2::uuid
					  AND display_path = $3 AND id <> $4::uuid
				)
			`, principal.OrganizationID, siteID, displayPath, bindingID).Scan(&exists); err != nil {
				return Binding{}, err
			}
			if exists {
				return Binding{}, ErrConflict
			}
		}
	}
	sets := []string{"updated_at = now()"}
	args := []any{principal.OrganizationID, bindingID}
	arg := func(value any) string {
		args = append(args, value)
		return fmt.Sprintf("$%d", len(args))
	}
	if input.DisplayPath != nil {
		sets = append(sets, "display_path = "+arg(strings.TrimSpace(*input.DisplayPath)))
	}
	if input.ContentType != nil {
		sets = append(sets, "content_type = "+arg(*input.ContentType))
	}
	if input.SectionSlug != nil {
		sets = append(sets, "section_slug = "+arg(*input.SectionSlug))
	}
	if input.SortOrder != nil {
		sets = append(sets, "sort_order = "+arg(*input.SortOrder)+"::int")
	}
	if input.OnHomepage != nil {
		sets = append(sets, "on_homepage = "+arg(*input.OnHomepage))
	}
	if input.OnNavigation != nil {
		sets = append(sets, "on_navigation = "+arg(*input.OnNavigation))
	}
	if input.DisplayConfig != nil {
		sets = append(sets, "display_config = "+arg(string(config))+"::jsonb")
	}
	item, err := scanBindingRow(tx.QueryRow(ctx, `
		UPDATE site.site_content_bindings b
		SET `+strings.Join(sets, ", ")+`
		WHERE b.organization_id = $1::uuid AND b.id = $2::uuid
		RETURNING `+bindingColumns+`
	`, args...))
	if err != nil {
		if uniqueViolation(err) {
			return Binding{}, ErrConflict
		}
		return Binding{}, fmt.Errorf("update binding: %w", err)
	}
	site, err = bumpSiteRevision(ctx, tx, principal.OrganizationID, workspaceID, siteID)
	if err != nil {
		return Binding{}, err
	}
	recordBindingAudit(ctx, tx, principal, workspaceID, "site.binding.updated", item, map[string]any{
		"site_slug": site.Slug, "display_path": item.DisplayPath,
	})
	if err := appendBindingEvents(ctx, tx, s.Events, principal, workspaceID, site, item, "updated"); err != nil {
		return Binding{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		if uniqueViolation(err) {
			return Binding{}, ErrConflict
		}
		return Binding{}, err
	}
	return item, nil
}

// DeleteBinding detaches the asset from the site. The asset itself is never
// touched: the site domain holds references only (D1).
func (s Service) DeleteBinding(ctx context.Context, principal auth.Principal, workspaceID, siteID, bindingID string) (Binding, error) {
	if !validID(workspaceID) || !validID(siteID) || !validID(bindingID) {
		return Binding{}, ErrInvalidInput
	}
	if err := s.require(ctx, principal, workspaceID, authz.ActionSiteManage); err != nil {
		return Binding{}, err
	}
	if s.Events == nil {
		return Binding{}, errors.New("event store is not initialized")
	}
	tx, err := s.Store.Pool.Begin(ctx)
	if err != nil {
		return Binding{}, err
	}
	defer tx.Rollback(ctx)
	site, err := lockSite(ctx, tx, principal.OrganizationID, workspaceID, siteID)
	if err != nil {
		return Binding{}, err
	}
	if site.Status == StatusDisabled {
		return Binding{}, ErrSiteDisabled
	}
	item, err := scanBindingRow(tx.QueryRow(ctx, `
		DELETE FROM site.site_content_bindings b
		WHERE b.organization_id = $1::uuid AND b.workspace_id = $2::uuid
		  AND b.site_id = $3::uuid AND b.id = $4::uuid
		RETURNING `+bindingColumns+`
	`, principal.OrganizationID, workspaceID, siteID, bindingID))
	if errors.Is(err, pgx.ErrNoRows) {
		return Binding{}, ErrBindingNotFound
	}
	if err != nil {
		return Binding{}, fmt.Errorf("delete binding: %w", err)
	}
	site, err = bumpSiteRevision(ctx, tx, principal.OrganizationID, workspaceID, siteID)
	if err != nil {
		return Binding{}, err
	}
	recordBindingAudit(ctx, tx, principal, workspaceID, "site.binding.deleted", item, map[string]any{
		"site_slug": site.Slug, "display_path": item.DisplayPath,
	})
	if err := appendBindingEvents(ctx, tx, s.Events, principal, workspaceID, site, item, "deleted"); err != nil {
		return Binding{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Binding{}, err
	}
	return item, nil
}

// Preview assembles the management preview snapshot: the site, its bindings
// and the homepage configuration. Member-gated with site.read; the handler
// serves it with no-store headers.
func (s Service) Preview(ctx context.Context, principal auth.Principal, workspaceID, siteID string) (PreviewSnapshot, error) {
	item, err := s.GetSite(ctx, principal, workspaceID, siteID)
	if err != nil {
		return PreviewSnapshot{}, err
	}
	page, err := listBindingsPage(ctx, s.Store.Pool, principal.OrganizationID, workspaceID, siteID, "", 100)
	if err != nil {
		return PreviewSnapshot{}, err
	}
	return PreviewSnapshot{Site: item, Bindings: page.Items, GeneratedAt: time.Now().UTC()}, nil
}

// lockBinding loads and locks one binding of the site FOR UPDATE.
func lockBinding(ctx context.Context, tx pgx.Tx, organizationID, workspaceID, siteID, bindingID string) (Binding, error) {
	item, err := scanBindingRow(tx.QueryRow(ctx, `
		SELECT `+bindingColumns+`
		FROM site.site_content_bindings b
		WHERE b.organization_id = $1::uuid AND b.workspace_id = $2::uuid
		  AND b.site_id = $3::uuid AND b.id = $4::uuid
		FOR UPDATE
	`, organizationID, workspaceID, siteID, bindingID))
	if errors.Is(err, pgx.ErrNoRows) {
		return Binding{}, ErrBindingNotFound
	}
	if err != nil {
		return Binding{}, fmt.Errorf("lock binding: %w", err)
	}
	return item, nil
}

// bumpSiteRevision advances the site revision after a binding change so the
// D4 ETags rotate. The row is already locked by the caller.
func bumpSiteRevision(ctx context.Context, tx pgx.Tx, organizationID, workspaceID, siteID string) (Site, error) {
	item, err := scanSiteRow(tx.QueryRow(ctx, `
		UPDATE site.public_sites
		SET revision = site.public_sites.revision + 1, updated_at = now()
		WHERE organization_id = $1::uuid AND workspace_id = $2::uuid AND id = $3::uuid
		RETURNING `+siteColumns+`
	`, organizationID, workspaceID, siteID))
	if err != nil {
		return Site{}, fmt.Errorf("bump site revision: %w", err)
	}
	return item, nil
}

// appendBindingEvents records the binding fact and its site-level mirror:
// site.binding_changed for binding consumers, site.site_changed so cache
// invalidators watching the site aggregate also rotate.
func appendBindingEvents(ctx context.Context, tx pgx.Tx, events *eventing.EventStore, principal auth.Principal, workspaceID string, site Site, item Binding, operation string) error {
	if events == nil {
		return errors.New("event store is not initialized")
	}
	if _, err := events.AppendTx(ctx, tx, eventing.Event{
		OrganizationID:   principal.OrganizationID,
		WorkspaceID:      workspaceID,
		EventType:        eventing.EventSiteBindingChanged,
		AggregateType:    "site_binding",
		AggregateID:      item.ID,
		AggregateVersion: site.Revision,
		PayloadVersion:   eventing.PayloadVersionV1,
		Actor:            eventing.ActorFromPrincipal(principal),
		Payload: eventing.SiteBindingChangedPayload{
			SiteID:    site.ID,
			AssetID:   item.AssetID,
			Operation: operation,
		},
	}); err != nil {
		return err
	}
	return appendSiteEvent(ctx, tx, events, principal, workspaceID, site, "binding_"+operation)
}

// recordBindingAudit writes the binding audit entry; resource_type stays
// "site" with the binding id so the audit trail groups under the domain.
func recordBindingAudit(ctx context.Context, tx pgx.Tx, principal auth.Principal, workspaceID, action string, item Binding, metadata map[string]any) {
	if metadata == nil {
		metadata = map[string]any{}
	}
	metadata["workspace_id"] = workspaceID
	metadata["site_id"] = item.SiteID
	entry := store.NewAuditEntry(action, principal.OrganizationID, principal.UserID, "site_binding", item.ID, metadata)
	_ = store.AppendAuditTx(ctx, tx, entry, workspaceID)
}
