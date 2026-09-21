package delivery

// pages2.go — capability-extension page builders (二期): the about page,
// the archive listing, the public media route and the comment integration
// on detail pages. All of them ride the same pipeline, cache and gates.

import (
	"context"
	"encoding/json"
	"errors"
	"html/template"
	"strings"
	"time"

	"agentchunzhi/internal/auth"
	agentquery "agentchunzhi/internal/query"
	"agentchunzhi/internal/site"
	"agentchunzhi/internal/theme"
)

// About serves the about page (content_type='about' binding).
func (s *Service) About(ctx context.Context, addr string, principal auth.Principal, slug, baseURL string) (*Response, error) {
	routePath := "/sites/" + slug + "/about/"
	return s.pipeline(ctx, addr, principal, slug, routePath, baseURL, func(ctx context.Context, facts site.SiteFacts, band string, queries *theme.Queries) (renderOutput, error) {
		if gated(facts, band) {
			return s.gateOutput(facts)
		}
		content, err := s.Reader.About(ctx, addr, principal, slug)
		if err != nil {
			return renderOutput{}, err
		}
		vm := ResolveDetail(slug, content, s.authorizedBodyImages(ctx, facts, content.Markdown))
		vm.Kind = "about"
		vm.Site = chrome(facts, "about")
		vm.Queries = queries
		vm.Title = content.Title + " · " + facts.Site.Name
		// Keep ResolveDetail's description (summary -> excerpt -> title).
		vm.Canonical = baseURL + routePath
		vm.NoIndex = !vm.Site.ScopePublic
		return renderOutput{kind: "about", vm: vm, noIndex: vm.NoIndex}, nil
	})
}

// Archive serves the year/month archive (二期 §7.2: pure server rendering,
// entries link to details; no client pagination).
func (s *Service) Archive(ctx context.Context, addr string, principal auth.Principal, slug, baseURL string) (*Response, error) {
	routePath := "/sites/" + slug + "/archive/"
	return s.pipeline(ctx, addr, principal, slug, routePath, baseURL, func(ctx context.Context, facts site.SiteFacts, band string, queries *theme.Queries) (renderOutput, error) {
		if gated(facts, band) {
			return s.gateOutput(facts)
		}
		page, err := s.Reader.Posts(ctx, addr, principal, slug, site.PublicPostQuery{Limit: 50})
		if err != nil {
			return renderOutput{}, err
		}
		items := page.Items
		cursor := page.NextCursor
		for round := 0; round < 9 && page.HasMore && cursor != ""; round++ {
			page, err = s.Reader.Posts(ctx, addr, principal, slug, site.PublicPostQuery{Cursor: cursor, Limit: 50})
			if err != nil {
				return renderOutput{}, err
			}
			items = append(items, page.Items...)
			cursor = page.NextCursor
		}
	vm := ArchiveVM{Page: Page{Kind: "archive", Description: facts.Site.Name + " 全部文章按发布时间归档。"}}
	vm.Years = groupArchive(slug, items, 160)
		vm.Site = chrome(facts, "archive")
		vm.Queries = queries
		vm.Title = "归档 · " + facts.Site.Name
		vm.Canonical = baseURL + routePath
		vm.NoIndex = !vm.Site.ScopePublic
		return renderOutput{kind: "archive", vm: vm, noIndex: vm.NoIndex}, nil
	})
}

// groupArchive projects the published list into year → month groups.
func groupArchive(slug string, items []site.PublicPost, summaryRunes int) []ArchiveYearVM {
	type key struct{ year, month string }
	order := []key{}
	grouped := map[key][]CardVM{}
	for _, post := range items {
		published := ""
		if post.PublishedAt != nil && !post.PublishedAt.IsZero() {
			published = post.PublishedAt.UTC().Format("2006-01")
		}
		if published == "" {
			continue
		}
		k := key{year: published[:4], month: published}
		if _, seen := grouped[k]; !seen {
			order = append(order, k)
		}
		grouped[k] = append(grouped[k], cardVM(slug, post, summaryRunes))
	}
	years := []ArchiveYearVM{}
	byYear := map[string]*ArchiveYearVM{}
	for _, k := range order {
		year, ok := byYear[k.year]
		if !ok {
			years = append(years, ArchiveYearVM{Year: k.year})
			year = &years[len(years)-1]
			byYear[k.year] = year
		}
		label := k.month + " 月"
		if len(k.month) >= 7 {
			label = k.month[5:7] + " 月"
		}
		year.Months = append(year.Months, ArchiveMonthVM{Month: k.month, Label: label, Items: grouped[k]})
	}
	return years
}

// MediaObject is one authorized public cover image.
type MediaObject struct {
	ObjectKey        string
	MediaType        string
	ByteSize         int64
	ETag             string
	OriginalFilename string
}

// authorizedBodyImages resolves which frozen image references of one page
// body may leave the database as same-origin media: only clean image
// attachments materialized on the current published version of an asset
// bound to this active site qualify — cover parity extended to body
// illustrations. The returned set feeds the render-time rewrite; every
// other image reference is stripped whole.
func (s *Service) authorizedBodyImages(ctx context.Context, facts site.SiteFacts, source string) map[string]bool {
	allowed := map[string]bool{}
	ids := MediaRefIDs(source)
	if len(ids) == 0 || s.Store == nil || s.Store.Pool == nil {
		return allowed
	}
	rows, err := s.Store.Pool.Query(ctx, `
		SELECT DISTINCT cav.attachment_id::text
		FROM asset.asset_version_attachments cav
		JOIN asset.asset_versions v
		  ON v.organization_id = cav.organization_id AND v.id = cav.asset_version_id
		JOIN asset.assets a
		  ON a.organization_id = v.organization_id AND a.id = v.asset_id
		 AND a.current_published_version_id = v.id
		JOIN asset.attachments att
		  ON att.organization_id = cav.organization_id AND att.id = cav.attachment_id
		JOIN site.site_content_bindings b
		  ON b.organization_id = a.organization_id AND b.asset_id = a.id
		WHERE cav.organization_id = $1::uuid AND b.site_id = $2::uuid
		  AND cav.attachment_id = ANY($3::uuid[])
		  AND att.deleted_at IS NULL AND att.status = 'clean'
		  AND att.media_type LIKE 'image/%'
	`, facts.Site.OrganizationID, facts.Site.ID, ids)
	if err != nil {
		return allowed
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return allowed
		}
		allowed[id] = true
	}
	return allowed
}

// Media authorizes and opens one public image: the attachment must be a
// clean image materialized (cover or body illustration) on the current
// published version of an asset bound to this active site, AND visible to
// the requesting visitor — the visibility decision itself runs through the
// shared authorizer (audit B-07: no second visibility judgment lives here).
// Missing/foreign/below-tier targets all collapse into the same not-found
// error (anti-probing parity).
func (s *Service) Media(ctx context.Context, addr string, principal auth.Principal, slug, attachmentID string) (MediaObject, error) {
	if s.Store == nil || s.Store.Pool == nil || s.Reader == nil {
		return MediaObject{}, errMediaNotFound
	}
	if err := s.Reader.AllowPublic(ctx, addr); err != nil {
		return MediaObject{}, err
	}
	facts, err := s.Reader.SiteFacts(ctx, slug)
	if err != nil {
		return MediaObject{}, errMediaNotFound
	}
	var media MediaObject
	var assetID, versionID string

	// 站点品牌媒体（Logo/Favicon/分享图）直接挂在 site 行上，不走资产
	// 绑定链；站点是公开读的，品牌图对访客可见即站点的公开配置。
	brandErr := s.Store.Pool.QueryRow(ctx, `
		SELECT media.object_key, media.media_type, media.byte_size,
		       COALESCE(media.sha256::text, ''), ''::text, ''::text
		FROM asset.attachments media
		JOIN site.public_sites site_row
		  ON site_row.organization_id = media.organization_id
		 AND (site_row.logo_attachment_id = media.id
		   OR site_row.favicon_attachment_id = media.id
		   OR site_row.social_image_attachment_id = media.id)
		WHERE media.organization_id = $1::uuid
		  AND media.id = $2::uuid
		  AND media.deleted_at IS NULL
		  AND media.status = 'clean'
		  AND media.media_type LIKE 'image/%'
		  AND site_row.id = $3::uuid
		  AND site_row.status = 'active'
		LIMIT 1
	`, facts.Site.OrganizationID, attachmentID, facts.Site.ID).Scan(
		&media.ObjectKey, &media.MediaType, &media.ByteSize, &media.ETag,
		&assetID, &versionID)
	if brandErr == nil {
		return media, nil
	}

	err = s.Store.Pool.QueryRow(ctx, `
		SELECT media.object_key, media.media_type, media.byte_size,
		       COALESCE(media.sha256::text, ''), COALESCE(media.original_filename, ''),
		       a.id::text, v.id::text
		FROM asset.attachments media
		JOIN asset.asset_version_attachments cav
		  ON cav.organization_id = media.organization_id AND cav.attachment_id = media.id
		 AND cav.role IN ('cover', 'body')
		JOIN asset.asset_versions v
		  ON v.organization_id = cav.organization_id AND v.id = cav.asset_version_id
		JOIN asset.assets a
		  ON a.organization_id = v.organization_id AND a.id = v.asset_id
		  AND a.current_published_version_id = v.id
		JOIN site.site_content_bindings b
		  ON b.organization_id = a.organization_id AND b.asset_id = a.id
		WHERE media.organization_id = $1::uuid
		  AND media.id = $2::uuid
		  AND media.deleted_at IS NULL
		  AND media.status = 'clean'
		  AND b.site_id = $3::uuid
		LIMIT 1
	`, facts.Site.OrganizationID, attachmentID, facts.Site.ID).Scan(
		&media.ObjectKey, &media.MediaType, &media.ByteSize, &media.ETag,
		&media.OriginalFilename, &assetID, &versionID)
	if err != nil {
		return MediaObject{}, errMediaNotFound
	}
	// The one visibility judgment: same authorizer, same visitor band as
	// every other public read (member covers stay member-only on gated
	// sites, matching what the cards render).
	visitor := s.Reader.VisitorFor(ctx, facts.Site, principal)
	authorized, err := agentquery.AuthorizePublicSiteAsset(ctx, s.Store,
		agentquery.PublicSiteRef{
			OrganizationID: facts.Site.OrganizationID,
			WorkspaceID:    facts.Site.WorkspaceID,
			DefaultScope:   facts.Site.DefaultContentScope,
		}, visitor, assetID, versionID)
	if err != nil || !authorized {
		return MediaObject{}, errMediaNotFound
	}
	return media, nil
}

var errMediaNotFound = errors.New("delivery: media not found")

// MediaCacheControl: cover objects are content-addressed and immutable.
const mediaCacheControl = "public, max-age=31536000, immutable"

// attachDetailComments fills the comment section of one detail VM from the
// site facts and the effective mode (二期 §8).
func (s *Service) attachDetailComments(ctx context.Context, vm *DetailVM, facts site.SiteFacts, band, slug string) {
	mode := facts.CommentsMode
	if mode == "" {
		mode = "moderated"
	}
	if mode == "off" {
		return
	}
	vm.CommentsEnabled = true
	vm.Moderation = mode
	vm.CanComment = band == "member"
	if s.Sites == nil {
		return
	}
	comments, err := s.Sites.VisibleComments(ctx, facts.Site.OrganizationID, facts.Site.ID, vm.AssetID, 100)
	if err != nil {
		s.Logf("delivery: comments degraded slug=%s err=%v", slug, err)
		return
	}
	vm.Comments = make([]CommentVM, 0, len(comments))
	for _, comment := range comments {
		vm.Comments = append(vm.Comments, CommentVM{
			Author:  comment.AuthorName,
			Body:    comment.Body,
			Created: comment.CreatedAt.UTC().Format("2006-01-02"),
		})
	}
}

// postAttachments lists the downloadable file attachments (non-inline
// images) materialized on the asset's current published version, bound to
// this site. Same publish+binding+clean contract the media route enforces.
func (s *Service) postAttachments(ctx context.Context, facts site.SiteFacts, assetID string) ([]AttachmentVM, error) {
	rows, err := s.Store.Pool.Query(ctx, `
		SELECT media.original_filename, media.id::text, media.media_type, media.byte_size
		FROM asset.asset_version_attachments cav
		JOIN asset.attachments media
		  ON media.organization_id = cav.organization_id AND media.id = cav.attachment_id
		JOIN asset.asset_versions v
		  ON v.organization_id = cav.organization_id AND v.id = cav.asset_version_id
		JOIN asset.assets a
		  ON a.organization_id = v.organization_id AND a.id = v.asset_id
		 AND a.current_published_version_id = v.id
		JOIN site.site_content_bindings b
		  ON b.organization_id = a.organization_id AND b.asset_id = a.id
		WHERE cav.organization_id = $1::uuid
		  AND v.asset_id = $2::uuid
		  AND b.site_id = $3::uuid
		  AND media.deleted_at IS NULL AND media.status = 'clean'
		  AND media.media_type NOT LIKE 'image/%'
		GROUP BY media.id, media.original_filename, media.media_type, media.byte_size
		ORDER BY media.original_filename
	`, facts.Site.OrganizationID, assetID, facts.Site.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AttachmentVM{}
	for rows.Next() {
		var att AttachmentVM
		var name string
		if err := rows.Scan(&name, &att.URL, &att.MediaType, &att.ByteSize); err != nil {
			return nil, err
		}
		att.Name = name
		att.URL = "/sites/" + facts.Site.Slug + "/media/" + att.URL
		out = append(out, att)
	}
	return out, rows.Err()
}

// postNeighbors resolves the previous/next published posts of one site by
// (published_at, id) around the current asset — for the detail page's
// sequential reading navigation (product doc §11.2).
func (s *Service) postNeighbors(ctx context.Context, facts site.SiteFacts, assetID string) (prev, next *NeighborLink) {
	current := struct {
		PublishedAt *time.Time
		ID          string
	}{}
	err := s.Store.Pool.QueryRow(ctx, `
		SELECT published_at, id::text FROM asset.assets
		WHERE organization_id = $1::uuid AND id = $2::uuid AND deleted_at IS NULL
	`, facts.Site.OrganizationID, assetID).Scan(&current.PublishedAt, &current.ID)
	if err != nil || current.PublishedAt == nil {
		return nil, nil
	}
	neighbor := func(direction string) *NeighborLink {
		order := "DESC"
		cmp := "<"
		if direction == "next" {
			order, cmp = "ASC", ">"
		}
		q := `
			SELECT a.id::text, b.display_path, COALESCE(v.title, '')
			FROM site.site_content_bindings b
			JOIN asset.assets a
			  ON a.organization_id = b.organization_id AND a.id = b.asset_id
			 AND a.deleted_at IS NULL AND a.current_published_version_id IS NOT NULL
			JOIN asset.asset_versions v
			  ON v.organization_id = a.organization_id AND v.id = a.current_published_version_id
			WHERE b.organization_id = $1::uuid AND b.site_id = $2::uuid
			  AND a.id <> $3::uuid
			  AND (v.published_at, a.id) ` + cmp + ` ((SELECT published_at FROM asset.assets WHERE id = $3::uuid), $3::uuid)
			ORDER BY v.published_at ` + order + `, a.id ` + order + `
			LIMIT 1
		`
		var id, path, title string
		if err := s.Store.Pool.QueryRow(ctx, q,
			facts.Site.OrganizationID, facts.Site.ID, assetID).Scan(&id, &path, &title); err != nil {
			return nil
		}
		href := "/sites/" + facts.Site.Slug + "/posts/" + path
		return &NeighborLink{Title: title, Href: href}
	}
	return neighbor("prev"), neighbor("next")
}

// Category serves the hierarchical public category listing (站点方案 C5/D14):
// /c/{path...} walks the public container tree, renders subcategories plus
// the included posts of the subtree, with CollectionPage + BreadcrumbList
// structured data and category-level SEO title/description.
func (s *Service) Category(ctx context.Context, addr string, principal auth.Principal, siteSlug, path, baseURL string, locale string) (*Response, error) {
	routePath := "/sites/" + siteSlug + "/c/" + strings.Trim(path, "/")
	return s.pipeline(ctx, addr, principal, siteSlug, routePath, baseURL, func(ctx context.Context, facts site.SiteFacts, band string, queries *theme.Queries) (renderOutput, error) {
		if gated(facts, band) {
			return s.gateOutput(facts)
		}
		// /c/ 根路径 = 分类总览页：全部分类入口（对标分析的"分类导航落地页"）。
		if strings.Trim(path, "/") == "" {
			cats, err := s.Reader.PublicCategoryIndex(ctx, addr, principal, siteSlug)
			if err != nil {
				return renderOutput{}, err
			}
			vm := CategoryVM{
				Page: Page{
					Kind:        "category",
					Title:       "分类 · " + facts.Site.Name,
					Description: facts.Site.Name + " 内容分类总览。",
					Canonical:   baseURL + "/sites/" + siteSlug + "/c/",
					NoIndex:     facts.Site.DefaultContentScope != site.ScopePublic,
				},
				Site:       chrome(facts, "category"),
				Heading:    "分类",
				IsIndex:    true,
				Categories: []SubcategoryVM{},
			}
			for _, cat := range cats {
				vm.Categories = append(vm.Categories, SubcategoryVM{Name: cat.Name, Href: cat.Href, Count: cat.Count})
			}
			return renderOutput{kind: "category", vm: vm, noIndex: vm.NoIndex}, nil
		}
		category, err := s.Reader.PublicCategoryPath(ctx, addr, principal, siteSlug, path, locale)
		if err != nil {
			return renderOutput{}, err
		}
		description := category.Description
		if description == "" {
			description = category.Title
		}
		vm := CategoryVM{
			Page: Page{
				Kind:        "category",
				Title:       category.Title + " · " + facts.Site.Name,
				Description: description,
				Canonical:   baseURL + routePath,
				NoIndex:     facts.Site.DefaultContentScope != site.ScopePublic,
			},
			Site:          chrome(facts, "category"),
			Heading:       category.Title,
			Crumbs:        []CrumbVM{},
			Subcategories: []SubcategoryVM{},
			Items:         []CardVM{},
		}
		for _, crumb := range category.Crumbs {
			vm.Crumbs = append(vm.Crumbs, CrumbVM{Name: crumb.Name, Href: crumb.Href})
		}
		for _, sub := range category.Subcategories {
			vm.Subcategories = append(vm.Subcategories, SubcategoryVM{Name: sub.Name, Href: sub.Href, Count: sub.Count})
		}
		for _, post := range category.Posts {
			vm.Items = append(vm.Items, cardVM(siteSlug, post, 160))
		}
		vm.Filter = s.buildFilterPanel(ctx, addr, principal, siteSlug, strings.Trim(path, "/"), nil)
		breadcrumbs := map[string]any{
			"@context":        "https://schema.org",
			"@type":           "BreadcrumbList",
			"itemListElement": buildBreadcrumbItems(baseURL+routePath, category.Title, category.Crumbs),
		}
		ld, _ := json.Marshal([]map[string]any{
			{"@context": "https://schema.org", "@type": "CollectionPage", "name": category.Title, "description": description},
			breadcrumbs,
		})
		vm.JSONLD = template.JS(ld)
		return renderOutput{kind: "category", vm: vm, noIndex: vm.NoIndex}, nil
	})
}

// CustomPage serves /sites/{slug}/p/{pageSlug} (C1)：pages_config v2 自定义
// 页的全模块渲染。canonical 与缓存走同一 pipeline（发布快照冻结）。
func (s *Service) CustomPage(ctx context.Context, addr string, principal auth.Principal, siteSlug, pageSlug, baseURL string, locale string) (*Response, error) {
	routePath := "/sites/" + siteSlug + "/p/" + strings.Trim(pageSlug, "/")
	return s.pipeline(ctx, addr, principal, siteSlug, routePath, baseURL, func(ctx context.Context, facts site.SiteFacts, band string, queries *theme.Queries) (renderOutput, error) {
		if gated(facts, band) {
			return s.gateOutput(facts)
		}
		page, err := s.Reader.CustomPage(ctx, addr, principal, siteSlug, pageSlug)
		if err != nil {
			return renderOutput{}, err
		}
		vm := CustomPageVM{
			Page: Page{
				Kind:        "page",
				Title:       page.Title + " · " + facts.Site.Name,
				Description: page.Description,
				Canonical:   baseURL + routePath,
				NoIndex:     facts.Site.DefaultContentScope != site.ScopePublic,
			},
			Site:        chrome(facts, "page"),
			Heading:     page.Title,
			ContentHTML: template.HTML(page.Markdown), // 由 RenderSiteMarkdown 净化管线产出
		}
		// 正文经与资产同一净化管线（注入不可绕过）。
		vm.ContentHTML = template.HTML(RenderSiteMarkdown(page.Markdown, siteSlug, s.authorizedBodyImages(ctx, facts, page.Markdown)).HTML)
		return renderOutput{kind: "page", vm: vm, noIndex: vm.NoIndex}, nil
	})
}
