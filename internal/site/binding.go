package site

// binding.go — the inclusion read model (站点方案 D2/B2，2026-09-12)。
// site_content_bindings 已由物理表改为**派生视图**（迁移 0033）：收录 =
// 已发布 + 可见性 ∈ scope 天花板 + 模型 public_site 通道 − 单条排除，
// slug 即公开路径。本文件只保留读取面（收录清单 / 预览）；绑定写路径已随
// 「自动收录 + 单条排除」模型整体移除。

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
	err := row.Scan(&item.ID, &item.SiteID, &item.AssetID, &item.DisplayPath,
		&item.ContentType, &item.SectionSlug, &item.SortOrder, &item.OnHomepage,
		&item.OnNavigation, &item.DisplayConfig, &item.DisplayPublishedAt,
		&item.CreatedAt, &item.UpdatedAt)
	if err != nil {
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

// ListBindings pages the derived inclusion rows of one site (created_at, id)
// keyset. Gated behind site.design (统一方案 G)。
func (s Service) ListBindings(ctx context.Context, principal auth.Principal, workspaceID, siteID, cursor string, limit int) (BindingPage, error) {
	if !validID(workspaceID) || !validID(siteID) {
		return BindingPage{}, ErrInvalidInput
	}
	if err := s.require(ctx, principal, workspaceID, authz.ActionSiteDesign); err != nil {
		return BindingPage{}, err
	}
	// The site scope check runs before the list so a foreign site id answers
	// site_not_found instead of an empty page.
	if _, err := s.GetSite(ctx, principal, workspaceID, siteID); err != nil {
		return BindingPage{}, err
	}
	return listBindingsPage(ctx, s.Store.Pool, principal.OrganizationID, workspaceID, siteID, cursor, limit)
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

// SetExcluded marks (active=true) or clears (active=false) the single-asset
// exclusion of one site (站点方案 D2/B4)。排除是持久意愿：重新发布不解除。
func (s Service) SetExcluded(ctx context.Context, principal auth.Principal, workspaceID, siteID, assetID string, active bool) error {
	if !validID(workspaceID) || !validID(siteID) || !validID(assetID) {
		return ErrInvalidInput
	}
	if err := s.require(ctx, principal, workspaceID, authz.ActionSiteDesign); err != nil {
		return err
	}
	site, err := s.GetSite(ctx, principal, workspaceID, siteID)
	if err != nil {
		return err
	}
	tx, err := s.Store.Pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("exclude tx: %w", err)
	}
	defer tx.Rollback(ctx)
	action := "inclusion_restored"
	if active {
		action = "inclusion_excluded"
		if _, err := tx.Exec(ctx, `
			INSERT INTO site.site_exclusions (site_id, asset_id, organization_id, excluded_by)
			VALUES ($2::uuid, $3::uuid, $1::uuid, $4::uuid)
			ON CONFLICT (site_id, asset_id) DO NOTHING
		`, principal.OrganizationID, siteID, assetID, principal.UserID); err != nil {
			return fmt.Errorf("exclude asset: %w", err)
		}
	} else if _, err := tx.Exec(ctx, `
		DELETE FROM site.site_exclusions
		WHERE organization_id = $1::uuid AND site_id = $2::uuid AND asset_id = $3::uuid
	`, principal.OrganizationID, siteID, assetID); err != nil {
		return fmt.Errorf("restore asset: %w", err)
	}
	// 排除/恢复即时改变派生收录视图的行集，必须失效页面缓存，
	// 否则已下线内容在缓存 TTL 内仍可访问。
	if err := appendSiteEvent(ctx, tx, s.Events, principal, workspaceID, site, action); err != nil {
		return err
	}
	recordSiteAudit(ctx, tx, principal, workspaceID, "site.inclusion_changed", siteID, map[string]any{
		"asset_id": assetID, "excluded": active,
	})
	return tx.Commit(ctx)
}

// SetFeatured marks (active=true) or clears (active=false) the featured flag
// of one included asset (站点方案 D2/B4)。精选供首页 featured 模块消费。
func (s Service) SetFeatured(ctx context.Context, principal auth.Principal, workspaceID, siteID, assetID string, active bool) error {
	if !validID(workspaceID) || !validID(siteID) || !validID(assetID) {
		return ErrInvalidInput
	}
	if err := s.require(ctx, principal, workspaceID, authz.ActionSiteDesign); err != nil {
		return err
	}
	site, err := s.GetSite(ctx, principal, workspaceID, siteID)
	if err != nil {
		return err
	}
	tx, err := s.Store.Pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("feature tx: %w", err)
	}
	defer tx.Rollback(ctx)
	action := "feature_cleared"
	if active {
		action = "featured"
		if _, err := tx.Exec(ctx, `
			INSERT INTO site.site_featured (site_id, asset_id, organization_id, marked_by)
			VALUES ($2::uuid, $3::uuid, $1::uuid, $4::uuid)
			ON CONFLICT (site_id, asset_id) DO NOTHING
		`, principal.OrganizationID, siteID, assetID, principal.UserID); err != nil {
			return fmt.Errorf("feature asset: %w", err)
		}
	} else if _, err := tx.Exec(ctx, `
		DELETE FROM site.site_featured
		WHERE organization_id = $1::uuid AND site_id = $2::uuid AND asset_id = $3::uuid
	`, principal.OrganizationID, siteID, assetID); err != nil {
		return fmt.Errorf("unfeature asset: %w", err)
	}
	// 首页 featured 模块消费精选位，与排除同理需要页面缓存失效。
	if err := appendSiteEvent(ctx, tx, s.Events, principal, workspaceID, site, action); err != nil {
		return err
	}
	recordSiteAudit(ctx, tx, principal, workspaceID, "site.featured_changed", siteID, map[string]any{
		"asset_id": assetID, "featured": active,
	})
	return tx.Commit(ctx)
}
