package site

// category.go — 分类树公开化（站点方案 C5/D14，2026-09-12）。
// folder（content.containers）子树开 public_flag 后生成 /c/{path…} 层级罗列页：
// 每级 = 子分类区 + 收录内容卡片 + 分类级 SEO 元数据。这是"分门别类、罗列"
// 权重金字塔的骨架。

import (
	"context"
	"fmt"
	"strings"
	"time"

	"agentchunzhi/internal/auth"
	agentquery "agentchunzhi/internal/query"
)

// 公开路由保留字：分类 slug 与自定义页 slug 不得占用，否则前缀路由无法判定。
var publicReservedSlugs = map[string]bool{
	"posts": true, "sections": true, "c": true, "p": true, "tags": true,
	"search": true, "archive": true, "about": true, "media": true, "static": true,
	"rss.xml": true, "sitemap.xml": true, "robots.txt": true,
	// 全部 ISO 639-1 两字母语言码（K7）：为多语言前缀让路。
	"aa": true, "ab": true, "af": true, "ak": true, "am": true, "ar": true, "as": true, "av": true,
	"ay": true, "az": true, "ba": true, "be": true, "bg": true, "bh": true, "bi": true, "bm": true,
	"bn": true, "bo": true, "br": true, "bs": true, "ca": true, "ce": true, "ch": true, "co": true,
	"cr": true, "cs": true, "cu": true, "cv": true, "cy": true, "da": true, "de": true, "dv": true,
	"dz": true, "ee": true, "el": true, "en": true, "eo": true, "es": true, "et": true, "eu": true,
	"fa": true, "ff": true, "fi": true, "fj": true, "fo": true, "fr": true, "fy": true, "ga": true,
	"gd": true, "gl": true, "gn": true, "gu": true, "gv": true, "ha": true, "he": true, "hi": true,
	"ho": true, "hr": true, "ht": true, "hu": true, "hy": true, "hz": true, "ia": true, "id": true,
	"ie": true, "ig": true, "ii": true, "ik": true, "io": true, "is": true, "it": true, "iu": true,
	"ja": true, "jv": true, "ka": true, "kg": true, "ki": true, "kj": true, "kk": true, "kl": true,
	"km": true, "kn": true, "ko": true, "kr": true, "ks": true, "ku": true, "kv": true, "kw": true,
	"ky": true, "la": true, "lb": true, "lg": true, "li": true, "ln": true, "lo": true, "lt": true,
	"lu": true, "lv": true, "mg": true, "mh": true, "mi": true, "mk": true, "ml": true, "mn": true,
	"mr": true, "ms": true, "mt": true, "my": true, "na": true, "nb": true, "nd": true, "ne": true,
	"ng": true, "nl": true, "nn": true, "no": true, "nr": true, "nv": true, "ny": true, "oc": true,
	"oj": true, "om": true, "or": true, "os": true, "pa": true, "pi": true, "pl": true, "ps": true,
	"pt": true, "qu": true, "rm": true, "rn": true, "ro": true, "ru": true, "rw": true, "sa": true,
	"sc": true, "sd": true, "se": true, "sg": true, "si": true, "sk": true, "sl": true, "sm": true,
	"sn": true, "so": true, "sq": true, "sr": true, "ss": true, "st": true, "su": true, "sv": true,
	"sw": true, "ta": true, "te": true, "tg": true, "th": true, "ti": true, "tk": true, "tl": true,
	"tn": true, "to": true, "tr": true, "ts": true, "tt": true, "tw": true, "ty": true, "ug": true,
	"uk": true, "ur": true, "uz": true, "ve": true, "vi": true, "vo": true, "wa": true, "wo": true,
	"xh": true, "yi": true, "yo": true, "za": true, "zh": true, "zu": true,
}

// ReservedPublicSlug reports whether the slug collides with a fixed route
// segment or a language prefix (K7)。
func ReservedPublicSlug(slug string) bool {
	return publicReservedSlugs[strings.ToLower(strings.TrimSpace(slug))]
}

// CategoryCrumb is one breadcrumb entry of a category path.
type CategoryCrumb struct {
	Name string
	Href string
}

// PublicCategory is the category listing page payload: the resolved category,
// its public children and the included posts of the subtree.
type PublicCategory struct {
	Site          Site
	CategoryID    string
	Slug          string
	Path          string
	Title         string
	Description   string
	ParentHref    string
	Crumbs        []CategoryCrumb
	Subcategories []CategoryLink
	Posts         []PublicPost
	GeneratedAt   time.Time
}

// CategoryLink is one public child category.
type CategoryLink struct {
	Name  string
	Href  string
	Count int
}

// PublicCategoryPath walks a slash-separated category path (each segment a
// category slug) down the public container tree of the site's workspace.
// Returns the resolved container id chain (for breadcrumb + subtree query).
func (r *PublicReader) PublicCategoryPath(ctx context.Context, visitorAddr string, principal auth.Principal, siteSlug, path, locale string) (PublicCategory, error) {
	if err := r.allow(ctx, visitorAddr); err != nil {
		return PublicCategory{}, err
	}
	item, err := r.loadSite(ctx, siteSlug)
	if err != nil {
		return PublicCategory{}, err
	}
	segments := []string{}
	for _, seg := range strings.Split(strings.Trim(path, "/"), "/") {
		seg = strings.TrimSpace(strings.ToLower(seg))
		if seg != "" {
			segments = append(segments, seg)
		}
	}
	if len(segments) == 0 || len(segments) > 3 {
		return PublicCategory{}, ErrSiteNotFound
	}

	type node struct {
		id, slug, title, publicTitle, publicDescription string
	}
	current := node{}
	var parentID *string
	href := "/sites/" + item.Slug + "/c"
	crumbs := []CategoryCrumb{{Name: "首页", Href: "/sites/" + item.Slug}}

	for _, seg := range segments {
		var n node
		var parentRef *string
		args := []any{item.OrganizationID, item.WorkspaceID, seg}
		parentClause := " AND parent_id IS NULL"
		if parentID != nil {
			parentClause = " AND parent_id = $4::uuid"
			args = append(args, *parentID)
		}
		err := r.Store.Pool.QueryRow(ctx, `
			SELECT id::text, slug, title, COALESCE(NULLIF(public_title, ''), title), COALESCE(public_description, ''), parent_id
			FROM content.containers
			WHERE organization_id = $1::uuid AND workspace_id = $2::uuid
			  AND slug = $3 AND public_flag = true AND status = 'active'`+parentClause,
			args...).Scan(&n.id, &n.slug, &n.title, &n.publicTitle, &n.publicDescription, &parentRef)
		if err != nil {
			return PublicCategory{}, ErrSiteNotFound
		}
		current = n
		parentID = &n.id
		href = href + "/" + n.slug
		crumbs = append(crumbs, CategoryCrumb{Name: n.publicTitle, Href: href})
	}

	// 子分类（下一层公开分类）+ 各自收录数。
	subcategories := []CategoryLink{}
	subRows, err := r.Store.Pool.Query(ctx, `
		SELECT c.slug, COALESCE(NULLIF(c.public_title, ''), c.title),
		       (SELECT count(DISTINCT sl.asset_id)
		          FROM content.containers sub
		          LEFT JOIN content.containers sub2 ON sub2.parent_id = sub.id AND sub2.public_flag
		         JOIN asset.assets a2 ON a2.category_container_id IN (sub.id, sub2.id)
		         JOIN site.site_slugs sl ON sl.organization_id = a2.organization_id
		           AND sl.asset_id = a2.id AND sl.site_id = $3::uuid AND sl.is_current
		         WHERE sub.parent_id = c.id AND sub.public_flag = true) AS cnt
		FROM content.containers c
		WHERE c.organization_id = $1::uuid AND c.workspace_id = $2::uuid
		  AND c.public_flag = true AND c.status = 'active' AND c.parent_id = $4::uuid
		ORDER BY c.sort_key, c.title
	`, item.OrganizationID, item.WorkspaceID, item.ID, current.id)
	if err == nil {
		defer subRows.Close()
		for subRows.Next() {
			var link CategoryLink
			var slug string
			if err := subRows.Scan(&slug, &link.Name, &link.Count); err == nil {
				link.Href = href + "/" + slug
				subcategories = append(subcategories, link)
			}
		}
	}

	// 收录内容 = 子树内分类挂载的资产 ∩ 站点收录（slug 当前 + 未排除 + 发布）。
	posts := []PublicPost{}
	postRows, err := r.Store.Pool.Query(ctx, `
		WITH RECURSIVE subtree AS (
			SELECT id FROM content.containers WHERE id = $3::uuid
			UNION ALL
			SELECT c.id FROM content.containers c
			JOIN subtree st ON c.parent_id = st.id
			WHERE c.public_flag = true
		)
		SELECT sl.slug, COALESCE(pv.title, ''), COALESCE(pv.summary, ''),
		       COALESCE(a.updated_at, now()), a.published_at
		FROM site.site_slugs sl
		JOIN asset.assets a
		  ON a.organization_id = sl.organization_id AND a.id = sl.asset_id
		 AND a.deleted_at IS NULL AND a.current_published_version_id IS NOT NULL
		LEFT JOIN asset.asset_versions pv
		  ON pv.organization_id = a.organization_id AND pv.id = a.current_published_version_id
		WHERE sl.organization_id = $1::uuid AND sl.site_id = $2::uuid AND sl.is_current
		  AND `+item.localePredicate("a", locale)+`
		  AND NOT EXISTS (SELECT 1 FROM site.site_exclusions x
		                  WHERE x.site_id = sl.site_id AND x.asset_id = sl.asset_id)
		  AND (a.category_container_id IN (SELECT id FROM subtree)
		       OR EXISTS (SELECT 1 FROM content.container_assets ca
		                  WHERE ca.asset_id = a.id AND ca.container_id IN (SELECT id FROM subtree)))
		ORDER BY a.published_at DESC
		LIMIT 60
	`, item.OrganizationID, item.ID, current.id)
	if err != nil {
		return PublicCategory{}, fmt.Errorf("load category posts: %w", err)
	}
	defer postRows.Close()
	for postRows.Next() {
		var slug, title, summary string
		var updated time.Time
		var published *time.Time
		if err := postRows.Scan(&slug, &title, &summary, &updated, &published); err != nil {
			continue
		}
		posts = append(posts, PublicPost{
			DisplayPath: slug,
			Title:       title,
			Summary:     SafeSummary(summary, 120),
			Tags:        []agentquery.TagSummary{},
			UpdatedAt:   &updated,
			PublishedAt: published,
		})
	}

	seoTitle := current.publicTitle
	if seoTitle == "" {
		seoTitle = current.title
	}
	return PublicCategory{
		Site:          item,
		CategoryID:    current.id,
		Slug:          current.slug,
		Path:          strings.Join(segments, "/"),
		Title:         seoTitle,
		Description:   current.publicDescription,
		Crumbs:        crumbs,
		Subcategories: subcategories,
		Posts:         posts,
		GeneratedAt:   time.Now().UTC(),
	}, nil
}

// Authorize nothing here: the delivery category page is public-face read; the
// inclusion derivation already gates content by the three doors.
var _ = auth.Principal{}
