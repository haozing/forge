package site

// theme_query.go — query 数据原语的 site 层实现（§4.5）。
// 数据源是派生收录视图（三道闸视图层生效）：经 PublicReader.latestPosts
// （绑定视图 + 可见性 + 访客 band 重查），模板不存在查出非公开数据的语法。

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"agentchunzhi/internal/auth"
	agentquery "agentchunzhi/internal/query"
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
		posts, err := reader.queryPosts(ctx, item, visitor, params)
		if err != nil {
			fmt.Fprintf(os.Stderr, "THEMEQ model=%s limit=%d err=%v", params.Model, params.Limit, err)
			return nil, err
		}
		out := &theme.QueryResult{Items: make([]theme.QueryItem, 0, len(posts))}
		for _, post := range posts {
			fields := map[string]string{}
			for _, f := range post.Fields {
				fields[f.Label] = formatQueryFieldValue(f)
			}
			tags := make([]string, 0, len(post.Tags))
			for _, t := range post.Tags {
				tags = append(tags, t.DisplayName)
			}
			cover := ""
			if post.CoverAttachmentID != "" {
				cover = "/sites/" + item.Slug + "/media/" + post.CoverAttachmentID
			}
			out.Items = append(out.Items, theme.QueryItem{
				Title:       post.Title,
				Href:        "/sites/" + item.Slug + "/posts/" + post.DisplayPath,
				Summary:     post.Summary,
				PublishedOn: fmtTimePtr(post.PublishedAt),
				CoverURL:    cover,
				Fields:      fields,
				Tags:        tags,
			})
		}
		out.Total = len(out.Items)
		return out, nil
	}, nil
}

// queryPosts 执行一次收录查询：filter 下推为统一查询的 FieldFilters
//（public_view 白名单字段，引擎层校验），sort=oldest 在取回后反转
//（≤20 条的页面级语义，不值得下沉引擎）。
func (r *PublicReader) queryPosts(ctx context.Context, item Site, visitor agentquery.VisitorIdentity, params theme.QueryParams) ([]PublicPost, error) {
	modelID, err := r.modelIDByKey(ctx, item, params.Model)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(params.Model) != "" && modelID == "" {
		// Unknown model key: empty rather than silently falling back to
		// every model (a wrong key must not leak content).
		return []PublicPost{}, nil
	}
	req := agentquery.Request{Mode: agentquery.ModeStructured, TopK: normalizePublicLimit(params.Limit)}
	if modelID != "" {
		req.ResourceModelIDs = []string{modelID}
	}
	for key, value := range params.Filter {
		req.FieldFilters = append(req.FieldFilters, agentquery.FieldFilter{
			Field: key, Operator: "eq", Value: value,
		})
	}
	response, err := r.Query.PublicSiteQuery(ctx, siteRef(item), visitor, req)
	if err != nil {
		return nil, err
	}
	page, err := r.mergeResponse(ctx, item, response)
	if err != nil {
		return nil, err
	}
	posts := r.filterByLocale(ctx, item, page.Items, "")
	if params.Sort == "oldest" {
		for i, j := 0, len(posts)-1; i < j; i, j = i+1, j-1 {
			posts[i], posts[j] = posts[j], posts[i]
		}
	}
	return posts, nil
}

// formatQueryFieldValue 把白名单字段的 JSON 值格式化为展示文本
//（与 delivery 侧字段行同语义；site 层不能反向 import delivery）。
func formatQueryFieldValue(field PublicFieldValue) string {
	switch field.Type {
	case "boolean":
		if string(field.Value) == "true" {
			return "是"
		}
		return "否"
	case "multiselect":
		var items []string
		if err := json.Unmarshal(field.Value, &items); err == nil {
			return strings.Join(items, "、")
		}
		return ""
	case "string", "enum", "date", "datetime":
		var text string
		if err := json.Unmarshal(field.Value, &text); err == nil {
			return text
		}
		return string(field.Value)
	default:
		return string(field.Value)
	}
}

func fmtTimePtr(t *time.Time) string {
	if t == nil {
		return ""
	}
	return t.Format("2006-01-02")
}
