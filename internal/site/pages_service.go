package site

// pages_service.go — 自定义页实体（site.site_pages）的服务面。

import (
	"context"
	"strings"
	"time"

	"agentchunzhi/internal/auth"
	"agentchunzhi/internal/authz"

	"github.com/jackc/pgx/v5"
)

// CustomPage 是一个自定义公开页（/p/{slug}）。
type CustomPage struct {
	ID             string    `json:"id"`
	SiteID         string    `json:"site_id"`
	Slug           string    `json:"slug"`
	Title          string    `json:"title"`
	BodyMarkdown   string    `json:"body_markdown"`
	SEODescription string    `json:"seo_description"`
	NavOrder       int       `json:"nav_order"`
	NavHidden      bool      `json:"nav_hidden"`
	Locale         string    `json:"locale,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// SitePageInput 是创建/更新的输入（指针语义：nil = 不变）。
type CustomPageInput struct {
	Slug           *string `json:"slug,omitempty"`
	Title          *string `json:"title,omitempty"`
	BodyMarkdown   *string `json:"body_markdown,omitempty"`
	SEODescription *string `json:"seo_description,omitempty"`
	NavOrder       *int    `json:"nav_order,omitempty"`
	NavHidden      *bool   `json:"nav_hidden,omitempty"`
	Locale         *string `json:"locale,omitempty"`
}

const sitePageColumns = `id::text, site_id::text, slug, title, body_markdown,
	seo_description, nav_order, nav_hidden, COALESCE(locale, ''), created_at, updated_at`

func scanSitePage(row pgx.Row) (CustomPage, error) {
	var p CustomPage
	err := row.Scan(&p.ID, &p.SiteID, &p.Slug, &p.Title, &p.BodyMarkdown,
		&p.SEODescription, &p.NavOrder, &p.NavHidden, &p.Locale, &p.CreatedAt, &p.UpdatedAt)
	return p, err
}

// ListSitePages 列出站点全部自定义页。
func (s *Service) ListSitePages(ctx context.Context, principal auth.Principal, workspaceID, siteID string) ([]CustomPage, error) {
	if err := s.require(ctx, principal, workspaceID, authz.ActionSiteRead); err != nil {
		return nil, err
	}
	rows, err := s.Store.Pool.Query(ctx, `
		SELECT `+sitePageColumns+` FROM site.site_pages
		WHERE organization_id = $1::uuid AND site_id = $2::uuid
		ORDER BY nav_order, slug
	`, principal.OrganizationID, siteID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []CustomPage{}
	for rows.Next() {
		var p CustomPage
		if err := rows.Scan(&p.ID, &p.SiteID, &p.Slug, &p.Title, &p.BodyMarkdown,
			&p.SEODescription, &p.NavOrder, &p.NavHidden, &p.Locale, &p.CreatedAt, &p.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// GetSitePage 读单个自定义页。
func (s *Service) GetSitePage(ctx context.Context, principal auth.Principal, workspaceID, siteID, pageID string) (CustomPage, error) {
	if err := s.require(ctx, principal, workspaceID, authz.ActionSiteRead); err != nil {
		return CustomPage{}, err
	}
	row := s.Store.Pool.QueryRow(ctx, `
		SELECT `+sitePageColumns+` FROM site.site_pages
		WHERE organization_id = $1::uuid AND site_id = $2::uuid AND id = $3::uuid
	`, principal.OrganizationID, siteID, pageID)
	return scanSitePage(row)
}

// CreateSitePage 新建自定义页。
func (s *Service) CreateSitePage(ctx context.Context, principal auth.Principal, workspaceID, siteID string, input CustomPageInput) (CustomPage, error) {
	if err := s.require(ctx, principal, workspaceID, authz.ActionSiteDesign); err != nil {
		return CustomPage{}, err
	}
	slug := ""
	if input.Slug != nil {
		slug = strings.ToLower(strings.TrimSpace(*input.Slug))
	}
	title := ""
	if input.Title != nil {
		title = strings.TrimSpace(*input.Title)
	}
	if slug == "" || !ValidDisplayPath(slug) || strings.Contains(slug, "/") || ReservedPublicSlug(slug) ||
		strings.TrimSpace(title) == "" {
		return CustomPage{}, ErrInvalidInput
	}
	locale := ""
	if input.Locale != nil {
		locale = strings.ToLower(strings.TrimSpace(*input.Locale))
	}
	row := s.Store.Pool.QueryRow(ctx, `
		INSERT INTO site.site_pages
			(organization_id, workspace_id, site_id, slug, title, body_markdown, seo_description, locale, nav_order, nav_hidden)
		VALUES ($1::uuid, $2::uuid, $3::uuid, $4, $5, $6, $7, NULLIF($8, ''), $9, $10)
		RETURNING `+sitePageColumns,
		principal.OrganizationID, workspaceID, siteID, slug, title,
		derefPage(input.BodyMarkdown), derefPage(input.SEODescription), locale,
		derefIntPage(input.NavOrder, 100), derefBoolPage(input.NavHidden, false),
	)
	return scanSitePage(row)
}

// UpdateSitePage 更新自定义页（指针语义）。
func (s *Service) UpdateSitePage(ctx context.Context, principal auth.Principal, workspaceID, siteID, pageID string, input CustomPageInput) (CustomPage, error) {
	if err := s.require(ctx, principal, workspaceID, authz.ActionSiteDesign); err != nil {
		return CustomPage{}, err
	}
	sets := []string{"updated_at = now()"}
	args := []any{principal.OrganizationID, siteID, pageID}
	next := func(v any) string {
		args = append(args, v)
		return "$" + itoa(len(args))
	}
	if input.Slug != nil {
		slug := strings.ToLower(strings.TrimSpace(*input.Slug))
		if slug == "" || !ValidDisplayPath(slug) || strings.Contains(slug, "/") || ReservedPublicSlug(slug) {
			return CustomPage{}, ErrInvalidInput
		}
		sets = append(sets, "slug = "+next(slug))
	}
	if input.Title != nil {
		sets = append(sets, "title = "+next(strings.TrimSpace(*input.Title)))
	}
	if input.BodyMarkdown != nil {
		sets = append(sets, "body_markdown = "+next(*input.BodyMarkdown))
	}
	if input.SEODescription != nil {
		sets = append(sets, "seo_description = "+next(*input.SEODescription))
	}
	if input.NavOrder != nil {
		sets = append(sets, "nav_order = "+next(*input.NavOrder))
	}
	if input.NavHidden != nil {
		sets = append(sets, "nav_hidden = "+next(*input.NavHidden))
	}
	if input.Locale != nil {
		sets = append(sets, "locale = NULLIF("+next(strings.ToLower(strings.TrimSpace(*input.Locale)))+", '')")
	}
	row := s.Store.Pool.QueryRow(ctx, `
		UPDATE site.site_pages SET `+strings.Join(sets, ", ")+`
		WHERE organization_id = $1::uuid AND site_id = $2::uuid AND id = $3::uuid
		RETURNING `+sitePageColumns,
		args...,
	)
	return scanSitePage(row)
}

// DeleteSitePage 删除自定义页。
func (s *Service) DeleteSitePage(ctx context.Context, principal auth.Principal, workspaceID, siteID, pageID string) error {
	if err := s.require(ctx, principal, workspaceID, authz.ActionSiteDesign); err != nil {
		return err
	}
	tag, err := s.Store.Pool.Exec(ctx, `
		DELETE FROM site.site_pages
		WHERE organization_id = $1::uuid AND site_id = $2::uuid AND id = $3::uuid
	`, principal.OrganizationID, siteID, pageID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrSiteNotFound
	}
	return nil
}

func derefPage(v *string) string {
	if v == nil {
		return ""
	}
	return *v
}

func derefIntPage(v *int, d int) int {
	if v == nil {
		return d
	}
	return *v
}

func derefBoolPage(v *bool, d bool) bool {
	if v == nil {
		return d
	}
	return *v
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	if neg {
		return "-" + string(b)
	}
	return string(b)
}
