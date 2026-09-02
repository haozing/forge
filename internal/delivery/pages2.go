package delivery

// pages2.go — capability-extension page builders (二期): the about page,
// the archive listing, the public media route and the comment integration
// on detail pages. All of them ride the same pipeline, cache and gates.

import (
	"context"
	"errors"

	"agentchunzhi/internal/auth"
	"agentchunzhi/internal/site"
)

// About serves the about page (content_type='about' binding).
func (s *Service) About(ctx context.Context, addr string, principal auth.Principal, slug, baseURL string) (*Response, error) {
	routePath := "/sites/" + slug + "/about/"
	return s.pipeline(ctx, addr, principal, slug, routePath, baseURL, func(ctx context.Context, facts site.SiteFacts, band string) (renderOutput, error) {
		if gated(facts, band) {
			return s.gateOutput(facts)
		}
		config := style(facts)
		content, err := s.Reader.About(ctx, addr, principal, slug)
		if err != nil {
			return renderOutput{}, err
		}
		vm := ResolveDetail(slug, content)
		vm.Kind = "about"
		vm.Site = chrome(facts, config, "about")
		vm.Title = content.Title + " · " + facts.Site.Name
		vm.Description = content.Summary
		vm.Canonical = baseURL + routePath
		vm.NoIndex = !vm.Site.ScopePublic
		return renderOutput{kind: "about", vm: vm, noIndex: vm.NoIndex}, nil
	})
}

// Archive serves the year/month archive (二期 §7.2: pure server rendering,
// entries link to details; no client pagination).
func (s *Service) Archive(ctx context.Context, addr string, principal auth.Principal, slug, baseURL string) (*Response, error) {
	routePath := "/sites/" + slug + "/archive/"
	return s.pipeline(ctx, addr, principal, slug, routePath, baseURL, func(ctx context.Context, facts site.SiteFacts, band string) (renderOutput, error) {
		if gated(facts, band) {
			return s.gateOutput(facts)
		}
		config := style(facts)
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
		vm := struct {
			Page
			Years []ArchiveYearVM
		}{Page: Page{Kind: "archive"}}
		vm.Years = groupArchive(slug, items, config.SummaryLength)
		vm.Site = chrome(facts, config, "archive")
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
	ObjectKey  string
	MediaType  string
	ByteSize   int64
	ETag       string
}

// Media authorizes and opens one public cover image: the attachment must be
// the cover of the current published version of an asset bound to this
// active site (二期 §6.2). Missing/foreign targets collapse into the same
// not-found error (anti-probing parity).
func (s *Service) Media(ctx context.Context, slug, attachmentID string) (MediaObject, error) {
	if s.Store == nil || s.Store.Pool == nil || s.Reader == nil {
		return MediaObject{}, errMediaNotFound
	}
	if err := s.Reader.AllowPublic(ctx, previewAddr); err != nil {
		return MediaObject{}, err
	}
	facts, err := s.Reader.SiteFacts(ctx, slug)
	if err != nil {
		return MediaObject{}, errMediaNotFound
	}
	var media MediaObject
	err = s.Store.Pool.QueryRow(ctx, `
		SELECT cover.object_key, cover.media_type, cover.byte_size, COALESCE(cover.sha256::text, '')
		FROM asset.attachments cover
		JOIN asset.asset_version_attachments cav
		  ON cav.organization_id = cover.organization_id AND cav.attachment_id = cover.id
		  AND cav.role = 'cover'
		JOIN asset.asset_versions v
		  ON v.organization_id = cav.organization_id AND v.id = cav.asset_version_id
		JOIN asset.assets a
		  ON a.organization_id = v.organization_id AND a.id = v.asset_id
		  AND a.current_published_version_id = v.id
		JOIN site.site_content_bindings b
		  ON b.organization_id = a.organization_id AND b.asset_id = a.id
		WHERE cover.organization_id = $1::uuid
		  AND cover.id = $2::uuid
		  AND cover.deleted_at IS NULL
		  AND cover.status = 'clean'
		  AND cover.media_type LIKE 'image/%'
		  AND b.site_id = $3::uuid
		LIMIT 1
	`, facts.Site.OrganizationID, attachmentID, facts.Site.ID).Scan(
		&media.ObjectKey, &media.MediaType, &media.ByteSize, &media.ETag)
	if err != nil {
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
