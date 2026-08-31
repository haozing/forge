package site

// service.go — the site aggregate of the phase 5 public-site domain:
// workspace-scoped CRUD with revision + If-Match semantics, audit entries and
// site.site_changed facts. Zero body copying (plan D1): sites reference
// assets through bindings only.

import (
	"context"
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
	"github.com/jackc/pgx/v5/pgconn"
)

// Service implements the site lifecycle. Permission decisions run through
// the workspace policy (site.read / site.manage); the service never compares
// role strings.
type Service struct {
	Store  *store.Store
	Events *eventing.EventStore
	Policy authz.WorkspacePolicyService
}

// Site is the workspace-scoped public site aggregate. HomepageConfig and
// NavigationConfig are presentation extension points carried verbatim.
type Site struct {
	ID                  string          `json:"id"`
	OrganizationID      string          `json:"organization_id"`
	WorkspaceID         string          `json:"workspace_id"`
	Slug                string          `json:"slug"`
	Name                string          `json:"name"`
	Domain              string          `json:"domain"`
	Template            string          `json:"template"`
	DefaultContentScope string          `json:"default_content_scope"`
	Status              string          `json:"status"`
	HomepageConfig      json.RawMessage `json:"homepage_config"`
	NavigationConfig    json.RawMessage `json:"navigation_config"`
	Revision            int64           `json:"revision"`
	CreatedAt           time.Time       `json:"created_at"`
	UpdatedAt           time.Time       `json:"updated_at"`
	// ETag is the representation version (the revision); handlers emit it for
	// the If-Match contract.
	ETag string `json:"etag"`
}

// CreateSiteInput carries the POST /sites body. Empty enums fall back to the
// 0010 defaults (blog / public); configs default to {}.
type CreateSiteInput struct {
	Slug                string
	Name                string
	Domain              string
	Template            string
	DefaultContentScope string
	HomepageConfig      json.RawMessage
	NavigationConfig    json.RawMessage
}

// UpdateSiteInput carries the PATCH /sites/{siteId} body; nil pointers stay
// unchanged. Slug is identity and never updatable.
type UpdateSiteInput struct {
	Name                *string
	Domain              *string
	Template            *string
	DefaultContentScope *string
	HomepageConfig      *json.RawMessage
	NavigationConfig    *json.RawMessage
	Status              *string
}

// SitePage is one keyset page of the workspace site catalog.
type SitePage struct {
	Items      []Site
	HasMore    bool
	NextCursor string
}

// validID mirrors the tag domain's hand-rolled UUID shape check: it keeps
// malformed identifiers away from ::uuid casts without importing a parser.
func validID(value string) bool {
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

// require enforces the workspace policy for the action. Policy denials
// collapse into ErrForbidden (the HTTP layer already distinguished unknown
// workspace 404 from denial 403 through requireWorkspaceAction); every
// site method calls it before touching SQL.
func (s Service) require(ctx context.Context, principal auth.Principal, workspaceID, action string) error {
	if principal.UserType != auth.UserTypeMember || s.Store == nil || s.Store.Pool == nil {
		return ErrForbidden
	}
	if s.Policy.Store == nil {
		return ErrForbidden
	}
	if _, err := s.Policy.Require(ctx, principal, workspaceID, "", action); err != nil {
		if errors.Is(err, authz.ErrWorkspaceForbidden) || errors.Is(err, authz.ErrWorkspaceNotFound) {
			return ErrForbidden
		}
		return err
	}
	return nil
}

const siteColumns = `id::text, organization_id::text, workspace_id::text, slug, name,
	COALESCE(domain, ''), template, default_content_scope, status, revision,
	homepage_config, navigation_config, created_at, updated_at`

func scanSiteRow(row interface{ Scan(...any) error }) (Site, error) {
	var item Site
	err := row.Scan(&item.ID, &item.OrganizationID, &item.WorkspaceID, &item.Slug, &item.Name,
		&item.Domain, &item.Template, &item.DefaultContentScope, &item.Status, &item.Revision,
		&item.HomepageConfig, &item.NavigationConfig, &item.CreatedAt, &item.UpdatedAt)
	if err != nil {
		return Site{}, err
	}
	item.ETag = fmt.Sprint(item.Revision)
	return item, nil
}

// uniqueViolation reports whether the error is a PostgreSQL unique-constraint
// loss; the service maps it to ErrConflict instead of a 500.
func uniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

// appendSiteEvent records one site.site_changed fact with the typed catalog
// payload passed straight through (never pre-encoded as []byte).
func appendSiteEvent(ctx context.Context, tx pgx.Tx, events *eventing.EventStore, principal auth.Principal, workspaceID string, site Site, action string) error {
	if events == nil {
		return errors.New("event store is not initialized")
	}
	_, err := events.AppendTx(ctx, tx, eventing.Event{
		OrganizationID:   principal.OrganizationID,
		WorkspaceID:      workspaceID,
		EventType:        eventing.EventSiteChanged,
		AggregateType:    "site",
		AggregateID:      site.ID,
		AggregateVersion: site.Revision,
		PayloadVersion:   eventing.PayloadVersionV1,
		Actor:            eventing.ActorFromPrincipal(principal),
		Payload: eventing.SiteChangedPayload{
			SiteID:      site.ID,
			WorkspaceID: workspaceID,
			Action:      action,
		},
	})
	return err
}

// recordSiteAudit writes the audit entry inside the business transaction;
// governance writes must not lose their audit row after commit.
func recordSiteAudit(ctx context.Context, tx pgx.Tx, principal auth.Principal, workspaceID, action, siteID string, metadata map[string]any) {
	if metadata == nil {
		metadata = map[string]any{}
	}
	metadata["workspace_id"] = workspaceID
	entry := store.NewAuditEntry(action, principal.OrganizationID, principal.UserID, "site", siteID, metadata)
	_ = store.AppendAuditTx(ctx, tx, entry, workspaceID)
}

// ListSites pages the workspace site catalog (created_at, id) keyset. The
// page size caps at 100 with a default of 50.
func (s Service) ListSites(ctx context.Context, principal auth.Principal, workspaceID, cursor string, limit int) (SitePage, error) {
	if !validID(workspaceID) {
		return SitePage{}, ErrInvalidInput
	}
	if err := s.require(ctx, principal, workspaceID, authz.ActionSiteRead); err != nil {
		return SitePage{}, err
	}
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	var cursorTime *time.Time
	// cursorID rides as interface{}: an empty string would fail the uuid Bind
	// even when the NULL comparison short-circuits (same defect family the
	// binding list hit; nil binds SQL NULL cleanly).
	var cursorID any
	if strings.TrimSpace(cursor) != "" {
		parsed, err := decodeKeysetCursor(cursor)
		if err != nil {
			return SitePage{}, ErrInvalidInput
		}
		cursorTime = &parsed.CreatedAt
		cursorID = parsed.ID
	}
	rows, err := s.Store.Pool.Query(ctx, `
		SELECT `+siteColumns+`
		FROM site.public_sites
		WHERE organization_id = $1::uuid AND workspace_id = $2::uuid
		  AND ($3::timestamptz IS NULL OR (created_at, id) < ($3::timestamptz, $4::uuid))
		ORDER BY created_at DESC, id DESC
		LIMIT $5::int
	`, principal.OrganizationID, workspaceID, cursorTime, cursorID, limit+1)
	if err != nil {
		return SitePage{}, fmt.Errorf("list sites: %w", err)
	}
	defer rows.Close()
	page := SitePage{Items: make([]Site, 0, limit+1)}
	for rows.Next() {
		item, err := scanSiteRow(rows)
		if err != nil {
			return SitePage{}, err
		}
		page.Items = append(page.Items, item)
	}
	if err := rows.Err(); err != nil {
		return SitePage{}, fmt.Errorf("iterate sites: %w", err)
	}
	if len(page.Items) > limit {
		page.HasMore = true
		page.Items = page.Items[:limit]
		last := page.Items[len(page.Items)-1]
		if page.NextCursor, err = encodeKeysetCursor(last.CreatedAt, last.ID); err != nil {
			return SitePage{}, err
		}
	}
	return page, nil
}

// CreateSite registers a new active site. Slug format and uniqueness follow
// the stage 5 plan: malformed input fails with ErrSlugInvalid, a lost unique
// race (slug or domain) with ErrConflict.
func (s Service) CreateSite(ctx context.Context, principal auth.Principal, workspaceID string, input CreateSiteInput) (Site, error) {
	if !validID(workspaceID) {
		return Site{}, ErrInvalidInput
	}
	if err := s.require(ctx, principal, workspaceID, authz.ActionSiteManage); err != nil {
		return Site{}, err
	}
	slug := strings.TrimSpace(input.Slug)
	if !ValidSlug(slug) {
		return Site{}, ErrSlugInvalid
	}
	name := strings.TrimSpace(input.Name)
	if name == "" {
		return Site{}, ErrInvalidInput
	}
	domain := strings.TrimSpace(input.Domain)
	if domain != "" && !ValidDomain(domain) {
		return Site{}, ErrInvalidInput
	}
	template := input.Template
	if template == "" {
		template = TemplateBlog
	}
	if !ValidTemplate(template) {
		return Site{}, ErrInvalidInput
	}
	scope := input.DefaultContentScope
	if scope == "" {
		scope = ScopePublic
	}
	if !ValidScope(scope) {
		return Site{}, ErrInvalidInput
	}
	homepage, err := defaultConfig(input.HomepageConfig)
	if err != nil {
		return Site{}, err
	}
	navigation, err := defaultConfig(input.NavigationConfig)
	if err != nil {
		return Site{}, err
	}
	if s.Events == nil {
		return Site{}, errors.New("event store is not initialized")
	}
	tx, err := s.Store.Pool.Begin(ctx)
	if err != nil {
		return Site{}, err
	}
	defer tx.Rollback(ctx)
	// Pre-checks give the friendly ErrConflict mapping; the unique indexes
	// remain the concurrency backstop (mapped from 23505 below).
	var exists bool
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM site.public_sites WHERE slug = $1)
	`, slug).Scan(&exists); err != nil {
		return Site{}, err
	}
	if exists {
		return Site{}, ErrConflict
	}
	if domain != "" {
		if err := tx.QueryRow(ctx, `
			SELECT EXISTS (SELECT 1 FROM site.public_sites WHERE domain = $1)
		`, domain).Scan(&exists); err != nil {
			return Site{}, err
		}
		if exists {
			return Site{}, ErrConflict
		}
	}
	item, err := scanSiteRow(tx.QueryRow(ctx, `
		INSERT INTO site.public_sites
			(organization_id, workspace_id, slug, name, domain, template,
			 default_content_scope, homepage_config, navigation_config, status, revision, created_by)
		VALUES ($1::uuid, $2::uuid, $3, $4, NULLIF($5, ''), $6, $7, $8::jsonb, $9::jsonb, 'active', 1, $10::uuid)
		RETURNING `+siteColumns+`
	`, principal.OrganizationID, workspaceID, slug, name, domain, template,
		scope, []byte(homepage), []byte(navigation), principal.UserID))
	if err != nil {
		if uniqueViolation(err) {
			return Site{}, ErrConflict
		}
		return Site{}, fmt.Errorf("insert site: %w", err)
	}
	recordSiteAudit(ctx, tx, principal, workspaceID, "site.created", item.ID, map[string]any{
		"slug":                  item.Slug,
		"template":              item.Template,
		"default_content_scope": item.DefaultContentScope,
	})
	if err := appendSiteEvent(ctx, tx, s.Events, principal, workspaceID, item, "created"); err != nil {
		return Site{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		if uniqueViolation(err) {
			return Site{}, ErrConflict
		}
		return Site{}, err
	}
	return item, nil
}

// GetSite reads one site inside the workspace scope; disabled sites stay
// readable for management (only the public face hides them).
func (s Service) GetSite(ctx context.Context, principal auth.Principal, workspaceID, siteID string) (Site, error) {
	if !validID(workspaceID) || !validID(siteID) {
		return Site{}, ErrInvalidInput
	}
	if err := s.require(ctx, principal, workspaceID, authz.ActionSiteRead); err != nil {
		return Site{}, err
	}
	item, err := scanSiteRow(s.Store.Pool.QueryRow(ctx, `
		SELECT `+siteColumns+`
		FROM site.public_sites
		WHERE organization_id = $1::uuid AND workspace_id = $2::uuid AND id = $3::uuid
	`, principal.OrganizationID, workspaceID, siteID))
	if errors.Is(err, pgx.ErrNoRows) {
		return Site{}, ErrSiteNotFound
	}
	return item, err
}

// UpdateSite patches the site under If-Match revision semantics: an empty
// expected revision (or the "*" wildcard) skips the check, a mismatch fails
// with ErrConflict. Slug is never updatable.
func (s Service) UpdateSite(ctx context.Context, principal auth.Principal, workspaceID, siteID, expectedRevision string, input UpdateSiteInput) (Site, error) {
	if !validID(workspaceID) || !validID(siteID) {
		return Site{}, ErrInvalidInput
	}
	if err := s.require(ctx, principal, workspaceID, authz.ActionSiteManage); err != nil {
		return Site{}, err
	}
	name := strings.TrimSpace(deref(input.Name))
	if input.Name != nil && name == "" {
		return Site{}, ErrInvalidInput
	}
	domain := strings.TrimSpace(deref(input.Domain))
	if input.Domain != nil && domain != "" && !ValidDomain(domain) {
		return Site{}, ErrInvalidInput
	}
	if input.Template != nil && !ValidTemplate(*input.Template) {
		return Site{}, ErrInvalidInput
	}
	if input.DefaultContentScope != nil && !ValidScope(*input.DefaultContentScope) {
		return Site{}, ErrInvalidInput
	}
	if input.Status != nil && *input.Status != StatusActive && *input.Status != StatusDisabled {
		return Site{}, ErrInvalidInput
	}
	var homepage, navigation json.RawMessage
	if input.HomepageConfig != nil {
		if !validConfigObject(*input.HomepageConfig) {
			return Site{}, ErrInvalidInput
		}
		homepage = *input.HomepageConfig
	}
	if input.NavigationConfig != nil {
		if !validConfigObject(*input.NavigationConfig) {
			return Site{}, ErrInvalidInput
		}
		navigation = *input.NavigationConfig
	}
	if s.Events == nil {
		return Site{}, errors.New("event store is not initialized")
	}
	tx, err := s.Store.Pool.Begin(ctx)
	if err != nil {
		return Site{}, err
	}
	defer tx.Rollback(ctx)
	current, err := lockSite(ctx, tx, principal.OrganizationID, workspaceID, siteID)
	if err != nil {
		return Site{}, err
	}
	if !revisionMatches(current.Revision, expectedRevision) {
		return Site{}, ErrConflict
	}
	if domain != "" && domain != current.Domain {
		var exists bool
		if err := tx.QueryRow(ctx, `
			SELECT EXISTS (SELECT 1 FROM site.public_sites WHERE domain = $1 AND id <> $2::uuid)
		`, domain, siteID).Scan(&exists); err != nil {
			return Site{}, err
		}
		if exists {
			return Site{}, ErrConflict
		}
	}
	item, err := applySiteUpdate(ctx, tx, principal, workspaceID, siteID, input, name, domain, homepage, navigation)
	if err != nil {
		return Site{}, err
	}
	recordSiteAudit(ctx, tx, principal, workspaceID, "site.updated", item.ID, map[string]any{
		"slug": item.Slug, "status": item.Status, "revision": item.Revision,
	})
	if err := appendSiteEvent(ctx, tx, s.Events, principal, workspaceID, item, "updated"); err != nil {
		return Site{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		if uniqueViolation(err) {
			return Site{}, ErrConflict
		}
		return Site{}, err
	}
	return item, nil
}

// applySiteUpdate renders the dynamic SET clause from the non-nil pointers
// and bumps the revision inside the caller's transaction.
func applySiteUpdate(ctx context.Context, tx pgx.Tx, principal auth.Principal, workspaceID, siteID string, input UpdateSiteInput, name, domain string, homepage, navigation json.RawMessage) (Site, error) {
	sets := []string{"revision = site.public_sites.revision + 1", "updated_at = now()"}
	args := []any{principal.OrganizationID, workspaceID, siteID}
	arg := func(value any) string {
		args = append(args, value)
		return fmt.Sprintf("$%d", len(args))
	}
	if input.Name != nil {
		sets = append(sets, "name = "+arg(name))
	}
	if input.Domain != nil {
		sets = append(sets, "domain = NULLIF("+arg(domain)+", '')")
	}
	if input.Template != nil {
		sets = append(sets, "template = "+arg(*input.Template))
	}
	if input.DefaultContentScope != nil {
		sets = append(sets, "default_content_scope = "+arg(*input.DefaultContentScope))
	}
	if input.HomepageConfig != nil {
		sets = append(sets, "homepage_config = "+arg(string(homepage))+"::jsonb")
	}
	if input.NavigationConfig != nil {
		sets = append(sets, "navigation_config = "+arg(string(navigation))+"::jsonb")
	}
	if input.Status != nil {
		sets = append(sets, "status = "+arg(*input.Status))
	}
	item, err := scanSiteRow(tx.QueryRow(ctx, `
		UPDATE site.public_sites
		SET `+strings.Join(sets, ", ")+`
		WHERE organization_id = $1::uuid AND workspace_id = $2::uuid AND id = $3::uuid
		RETURNING `+siteColumns+`
	`, args...))
	if err != nil {
		if uniqueViolation(err) {
			return Site{}, ErrConflict
		}
		return Site{}, fmt.Errorf("update site: %w", err)
	}
	return item, nil
}

// DisableSite is the soft DELETE of the site resource: status flips to
// 'disabled' and the public face starts answering 404. Bindings and configs
// stay intact so a later re-enable restores the site verbatim.
func (s Service) DisableSite(ctx context.Context, principal auth.Principal, workspaceID, siteID, expectedRevision string) (Site, error) {
	status := StatusDisabled
	return s.UpdateSite(ctx, principal, workspaceID, siteID, expectedRevision, UpdateSiteInput{Status: &status})
}

// lockSite loads and locks the site row FOR UPDATE inside a transaction.
func lockSite(ctx context.Context, tx pgx.Tx, organizationID, workspaceID, siteID string) (Site, error) {
	item, err := scanSiteRow(tx.QueryRow(ctx, `
		SELECT `+siteColumns+`
		FROM site.public_sites
		WHERE organization_id = $1::uuid AND workspace_id = $2::uuid AND id = $3::uuid
		FOR UPDATE
	`, organizationID, workspaceID, siteID))
	if errors.Is(err, pgx.ErrNoRows) {
		return Site{}, ErrSiteNotFound
	}
	if err != nil {
		return Site{}, fmt.Errorf("lock site: %w", err)
	}
	return item, nil
}

// defaultConfig normalizes an optional config field: empty stays {}, invalid
// JSON or non-object values fail with ErrInvalidInput.
func defaultConfig(raw json.RawMessage) (json.RawMessage, error) {
	if !validConfigObject(raw) {
		return nil, ErrInvalidInput
	}
	if len(strings.TrimSpace(string(raw))) == 0 {
		return json.RawMessage("{}"), nil
	}
	return raw, nil
}

func deref(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
