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
	"unicode/utf8"

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
	// PreviewHashSecret 一次性预览 token 的 HMAC 密钥（空 = 禁签发）。
	PreviewHashSecret string
}

// Site is the workspace-scoped public site aggregate. HomepageConfig,
// NavigationConfig and StyleConfig are presentation extension points carried
// verbatim (style_config is the L1 parameter space, validated on write).
type Site struct {
	ID                  string          `json:"id"`
	OrganizationID      string          `json:"organization_id"`
	WorkspaceID         string          `json:"workspace_id"`
	Slug                string          `json:"slug"`
	Name                string          `json:"name"`
	Domain              string          `json:"domain"`
	DefaultContentScope string          `json:"default_content_scope"`
	Status              string          `json:"status"`
	// CommentsMode gates the comment section (二期 §8).
	CommentsMode string `json:"comments_mode"`
	// 品牌媒体（CMS §10.7）：站点 Logo、Favicon 与社交分享图，引用
	// asset.attachments（image/*，交付面媒体路由校验后对外服务）。
	LogoAttachmentID        string `json:"logo_attachment_id"`
	FaviconAttachmentID     string `json:"favicon_attachment_id"`
	SocialImageAttachmentID string `json:"social_image_attachment_id"`
	// 站点级描述（§1.3/§7.3）：公开首页 meta description 的兜底来源。
	Description string `json:"description"`
	// 站点简报（内容治理 F1，0043）：站点的"为什么"（定位/受众/范围），
	// 注入 react run 指令供 agent 对齐；内部工作文档，不进公开渲染。
	Brief string `json:"brief"`
	// 多语言（D11）：默认语言 + 启用语言集合 + 回退开关。
	DefaultLocale     string   `json:"default_locale"`
	EnabledLocales    []string `json:"enabled_locales"`
	FallbackToDefault bool     `json:"fallback_to_default"`
	// 主题修订版指针（站点主题化与 AI 设计重构）：draft = 工作台副本，
	// published = 对外渲染所用文件集。0040 起 UI 设计面的唯一事实源。
	DraftThemeRevisionID     string `json:"-"`
	PublishedThemeRevisionID string `json:"-"`
	// PublishedReleaseID points at the live immutable config snapshot; NULL
	// means the public render falls back to the working columns above.
	PublishedReleaseID *string   `json:"published_release_id"`
	Revision           int64     `json:"revision"`
	CreatedAt          time.Time `json:"created_at"`
	UpdatedAt          time.Time `json:"updated_at"`
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
	DefaultContentScope string
	HomepageConfig      json.RawMessage
	NavigationConfig    json.RawMessage
	PagesConfig         json.RawMessage
	StyleConfig         json.RawMessage
}

// UpdateSiteInput carries the PATCH /sites/{siteId} body; nil pointers stay
// unchanged. Slug is identity and never updatable. StyleConfig is a partial
// document deep-merged over the stored one (null leaves reset to preset).
type UpdateSiteInput struct {
	Name                *string
	Description         *string
	Brief               *string
	Domain              *string
	DefaultContentScope *string
	HomepageConfig      *json.RawMessage
	NavigationConfig    *json.RawMessage
	DefaultLocale       *string
	EnabledLocales      *[]string
	FallbackToDefault   *bool
	StyleConfig         *json.RawMessage
	CommentsMode        *string
	Status              *string
	// 品牌媒体附件（image/*）：整体更新语义（nil = 不动；空串 = 清除）。
	LogoAttachmentID        *string
	FaviconAttachmentID     *string
	SocialImageAttachmentID *string
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
	// agent 成员（统一方案 A/B）与人同域判权：agent 路径在 Require 内
	// 走角色预设 ± 覆写并剥 human_only 动作；这里只拦未授权主体类型。
	if principal.UserType != auth.UserTypeMember && principal.UserType != auth.UserTypeAgent {
		return ErrForbidden
	}
	if s.Store == nil || s.Store.Pool == nil {
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

// MaxBriefRunes caps the site brief (F1): long enough for a real positioning
// statement, short enough to inject verbatim into a react run instruction.
const MaxBriefRunes = 2000

const siteColumns = `id::text, organization_id::text, workspace_id::text, slug, name, description, brief,
	COALESCE(domain, ''), default_content_scope, status, revision,
	default_locale, enabled_locales, fallback_to_default, comments_mode,
	published_release_id::text, created_at, updated_at,
	COALESCE(logo_attachment_id::text, ''), COALESCE(favicon_attachment_id::text, ''),
	COALESCE(social_image_attachment_id::text, ''),
	COALESCE(draft_theme_revision_id::text, ''), COALESCE(published_theme_revision_id::text, '')`

func scanSiteRow(row interface{ Scan(...any) error }) (Site, error) {
	var item Site
	err := row.Scan(&item.ID, &item.OrganizationID, &item.WorkspaceID, &item.Slug, &item.Name,
		&item.Description, &item.Brief, &item.Domain, &item.DefaultContentScope, &item.Status, &item.Revision,
		&item.DefaultLocale, &item.EnabledLocales, &item.FallbackToDefault,
		&item.CommentsMode, &item.PublishedReleaseID, &item.CreatedAt, &item.UpdatedAt,
		&item.LogoAttachmentID, &item.FaviconAttachmentID, &item.SocialImageAttachmentID,
		&item.DraftThemeRevisionID, &item.PublishedThemeRevisionID)
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
	// G: 建站是站点生命周期权（admin）；handler 层已做同款检查。
	if err := s.require(ctx, principal, workspaceID, authz.ActionSiteLifecycle); err != nil {
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
	scope := input.DefaultContentScope
	if scope == "" {
		scope = ScopePublic
	}
	if !ValidScope(scope) {
		return Site{}, ErrInvalidInput
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
			(organization_id, workspace_id, slug, name, domain,
			 default_content_scope, status, revision, created_by)
		VALUES ($1::uuid, $2::uuid, $3, $4, NULLIF($5, ''), $6, 'active', 1, $7::uuid)
		RETURNING `+siteColumns+`
	`, principal.OrganizationID, workspaceID, slug, name, domain,
		scope, principal.UserID))
	if err != nil {
		if uniqueViolation(err) {
			return Site{}, ErrConflict
		}
		return Site{}, fmt.Errorf("insert site: %w", err)
	}
	recordSiteAudit(ctx, tx, principal, workspaceID, "site.created", item.ID, map[string]any{
		"slug":                  item.Slug,
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
	if err := s.require(ctx, principal, workspaceID, authz.ActionSiteDesign); err != nil {
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
	if input.DefaultContentScope != nil && !ValidScope(*input.DefaultContentScope) {
		return Site{}, ErrInvalidInput
	}
	if input.Status != nil && *input.Status != StatusActive && *input.Status != StatusDisabled {
		return Site{}, ErrInvalidInput
	}
	if input.DefaultLocale != nil || input.EnabledLocales != nil {
		if err := validLocalePair(deref(input.DefaultLocale), derefStringSlice(input.EnabledLocales)); err != nil {
			return Site{}, err
		}
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
	if input.CommentsMode != nil && !ValidCommentsMode(*input.CommentsMode) {
		return Site{}, ErrInvalidInput
	}
	if err := s.validateBrandingAttachments(ctx, principal.OrganizationID, input); err != nil {
		return Site{}, err
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
	item, err := applySiteUpdate(ctx, tx, principal, workspaceID, siteID, input, name, domain)
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

// validateBrandingAttachments checks the branding media pointers: every
// non-empty id must reference an image/*, clean attachment of this
// organization (same safety bar the delivery media route applies).
func (s Service) validateBrandingAttachments(ctx context.Context, organizationID string, input UpdateSiteInput) error {
	for _, entry := range []struct {
		name string
		id   *string
	}{
		{"logo_attachment_id", input.LogoAttachmentID},
		{"favicon_attachment_id", input.FaviconAttachmentID},
		{"social_image_attachment_id", input.SocialImageAttachmentID},
	} {
		if entry.id == nil {
			continue
		}
		id := strings.TrimSpace(*entry.id)
		if id == "" {
			continue // 空串 = 清除
		}
		if !validID(id) {
			return fmt.Errorf("%w: %s 不是合法的附件 UUID", ErrInvalidInput, entry.name)
		}
		var ok bool
		if err := s.Store.Pool.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1 FROM asset.attachments
				WHERE organization_id = $1::uuid AND id = $2::uuid
				  AND deleted_at IS NULL AND status = 'clean'
				  AND media_type LIKE 'image/%'
			)
		`, organizationID, id).Scan(&ok); err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("%w: %s 必须是本组织已上传的图片附件", ErrInvalidInput, entry.name)
		}
	}
	return nil
}

// applySiteUpdate renders the dynamic SET clause from the non-nil pointers
// and bumps the revision inside the caller's transaction.
func applySiteUpdate(ctx context.Context, tx pgx.Tx, principal auth.Principal, workspaceID, siteID string, input UpdateSiteInput, name, domain string) (Site, error) {
	sets := []string{"revision = site.public_sites.revision + 1", "updated_at = now()"}
	args := []any{principal.OrganizationID, workspaceID, siteID}
	arg := func(value any) string {
		args = append(args, value)
		return fmt.Sprintf("$%d", len(args))
	}
	if input.Name != nil {
		sets = append(sets, "name = "+arg(name))
	}
	if input.Description != nil {
		sets = append(sets, "description = "+arg(strings.TrimSpace(*input.Description)))
	}
	if input.Brief != nil {
		if utf8.RuneCountInString(*input.Brief) > MaxBriefRunes {
			return Site{}, fmt.Errorf("%w: 站点简报不能超过 %d 字", ErrInvalidInput, MaxBriefRunes)
		}
		sets = append(sets, "brief = "+arg(strings.TrimSpace(*input.Brief)))
	}
	if input.Domain != nil {
		sets = append(sets, "domain = NULLIF("+arg(domain)+", '')")
	}
	if input.DefaultContentScope != nil {
		sets = append(sets, "default_content_scope = "+arg(*input.DefaultContentScope))
	}
	if input.DefaultLocale != nil {
		sets = append(sets, "default_locale = "+arg(strings.ToLower(strings.TrimSpace(*input.DefaultLocale))))
	}
	if input.EnabledLocales != nil {
		sets = append(sets, "enabled_locales = "+arg(*input.EnabledLocales)+"::text[]")
	}
	if input.FallbackToDefault != nil {
		sets = append(sets, "fallback_to_default = "+arg(*input.FallbackToDefault))
	}
	if input.CommentsMode != nil {
		sets = append(sets, "comments_mode = "+arg(*input.CommentsMode))
	}
	if input.LogoAttachmentID != nil {
		sets = append(sets, "logo_attachment_id = NULLIF("+arg(strings.TrimSpace(*input.LogoAttachmentID))+", '')::uuid")
	}
	if input.FaviconAttachmentID != nil {
		sets = append(sets, "favicon_attachment_id = NULLIF("+arg(strings.TrimSpace(*input.FaviconAttachmentID))+", '')::uuid")
	}
	if input.SocialImageAttachmentID != nil {
		sets = append(sets, "social_image_attachment_id = NULLIF("+arg(strings.TrimSpace(*input.SocialImageAttachmentID))+", '')::uuid")
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

func mustMarshalJSON(value any) []byte {
	raw, err := json.Marshal(value)
	if err != nil {
		return []byte("{}")
	}
	return raw
}

// validLocalePair 校验默认语言与启用集合：全部归一为两位字母码，默认语言
// 必须在启用集合内（D11）。
func validLocalePair(defaultLocale string, enabled []string) error {
	twoLetter := func(value string) bool {
		value = strings.ToLower(strings.TrimSpace(value))
		if len(value) != 2 {
			return false
		}
		for _, char := range value {
			if char < 'a' || char > 'z' {
				return false
			}
		}
		return true
	}
	if defaultLocale != "" && !twoLetter(defaultLocale) {
		return ErrInvalidInput
	}
	normalized := map[string]bool{}
	for _, locale := range enabled {
		if !twoLetter(locale) {
			return ErrInvalidInput
		}
		normalized[strings.ToLower(strings.TrimSpace(locale))] = true
	}
	if defaultLocale != "" && len(normalized) > 0 && !normalized[strings.ToLower(strings.TrimSpace(defaultLocale))] {
		return ErrInvalidInput
	}
	return nil
}

func derefStringSlice(value *[]string) []string {
	if value == nil {
		return nil
	}
	return *value
}
