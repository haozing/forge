package site

// theme_query.go — query 数据原语的 site 层实现（§4.5）。
// 数据源是派生收录视图（三道闸视图层生效）：经 PublicReader.latestPosts
// （绑定视图 + 可见性 + 访客 band 重查），模板不存在查出非公开数据的语法。

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"agentchunzhi/internal/auth"
	"agentchunzhi/internal/authz"
	"agentchunzhi/internal/theme"
)

// loadSiteByID 按 ID 加载站点（query 原语绑定用）。
func (r *PublicReader) loadSiteByID(ctx context.Context, organizationID, id string) (Site, error) {
	var item Site
	err := r.Store.Pool.QueryRow(ctx, `SELECT `+siteColumns+`
		FROM site.public_sites
		WHERE organization_id = $1::uuid AND id = $2::uuid AND status = 'active'
	`, organizationID, id).Scan(&item.ID, &item.OrganizationID, &item.WorkspaceID, &item.Slug, &item.Name,
		&item.Domain, &item.DefaultContentScope, &item.Status, &item.Revision,
		&item.DefaultLocale, &item.EnabledLocales, &item.FallbackToDefault,
		&item.CommentsMode, &item.PublishedReleaseID, &item.CreatedAt, &item.UpdatedAt,
		&item.LogoAttachmentID, &item.FaviconAttachmentID, &item.SocialImageAttachmentID,
		&item.DraftThemeRevisionID, &item.PublishedThemeRevisionID)
	return item, err
}

// ThemePublishedFiles 返回站点已发布主题的文件集（空集 = 默认主题）。
func (s *Service) ThemePublishedFiles(ctx context.Context, principal auth.Principal, workspaceID, siteID string) (json.RawMessage, error) {
	if err := s.require(ctx, principal, workspaceID, authz.ActionSiteRead); err != nil {
		return nil, err
	}
	site, err := s.GetSite(ctx, principal, workspaceID, siteID)
	if err != nil {
		return nil, err
	}
	if site.PublishedThemeRevisionID == "" {
		return json.RawMessage("{}"), nil
	}
	var files json.RawMessage
	err = s.Store.Pool.QueryRow(ctx, `
		SELECT files FROM site.site_theme_revisions
		WHERE organization_id = $1::uuid AND id = $2::uuid
	`, principal.OrganizationID, site.PublishedThemeRevisionID).Scan(&files)
	if err != nil {
		return nil, err
	}
	if files == nil || string(files) == "null" {
		files = json.RawMessage("{}")
	}
	return files, nil
}

// ThemeQuery 构造绑定到站点与访客身份的 query 原语实现（引擎 FuncMap 注入）。
// 预算（调用次数/limit）由 theme.Queries 管；这里只负责数据面：
// 查询走 latestPosts（绑定视图），模板拿到的每一条都已过三道闸。
func (s *Service) ThemeQuery(ctx context.Context, principal auth.Principal, organizationID, workspaceID, siteID string, reader *PublicReader) (theme.QueryFunc, error) {
	if reader == nil || s.Store == nil {
		return nil, fmt.Errorf("theme query: store not wired")
	}
	item, err := reader.loadSiteByID(ctx, organizationID, siteID)
	if err != nil {
		return nil, err
	}
	return func(raw map[string]any) (*theme.QueryResult, error) {
		params, err := theme.ParseParams(raw)
		if err != nil {
			return nil, err
		}
		visitor := reader.visitor(ctx, item, principal)
		posts, err := reader.latestPosts(ctx, item, visitor, params.Model, params.Limit, "")
		if err != nil {
			return nil, err
		}
		out := &theme.QueryResult{Items: make([]theme.QueryItem, 0, len(posts))}
		for _, post := range posts {
			fields := map[string]string{}
			for _, f := range post.Fields {
				fields[f.Label] = string(f.Value)
			}
			tags := make([]string, 0, len(post.Tags))
			for _, t := range post.Tags {
				tags = append(tags, t.DisplayName)
			}
			out.Items = append(out.Items, theme.QueryItem{
				Title:       post.Title,
				Href:        "/sites/" + item.Slug + "/posts/" + post.DisplayPath,
				Summary:     post.Summary,
				PublishedOn: fmtTimePtr(post.PublishedAt),
				CoverURL:    post.CoverAttachmentID,
				Fields:      fields,
				Tags:        tags,
			})
		}
		out.Total = len(out.Items)
		return out, nil
	}, nil
}

func fmtTimePtr(t *time.Time) string {
	if t == nil {
		return ""
	}
	return t.Format("2006-01-02")
}
