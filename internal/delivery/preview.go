package delivery

// preview.go — 沙盒主题的实时预览渲染（主题化与 AI 设计重构）。
// 输入 = 沙盒主题文件集（agent 会话），输出 = 指定槽位的完整 HTML。
// 预览不进页缓存、一律 noindex + no-store；跨源 iframe 由 base_url 重写
// 根相对引用。安全：渲染走主题引擎（编译门禁 + 扫描已在上游完成）。

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	"agentchunzhi/internal/auth"
	"agentchunzhi/internal/site"
	"agentchunzhi/internal/tag"
	"agentchunzhi/internal/theme"
)

// PreviewInput carries the preview request body: the candidate theme file
// set plus the page to render.
type PreviewInput struct {
	Files       json.RawMessage `json:"files"`
	Slot        string          `json:"slot"`
	DisplayPath string          `json:"display_path"`
	// BaseURL optionally rewrites the rendered site's root-relative links
	// (href/src/action starting with "/") to this origin, so the management
	// UI can show the preview inside a cross-origin iframe with working
	// media and script references. Must be an absolute http(s) origin.
	BaseURL string `json:"base_url"`
}

// RenderPreview renders one candidate page from the sandbox files. The site
// service call enforces site.read; theme compile failures return a structured
// error the agent can consume.
func (s *Service) RenderPreview(ctx context.Context, principal auth.Principal, workspaceID, siteID string, input PreviewInput) (*Response, error) {
	if s.Sites == nil || s.Reader == nil {
		return nil, fmt.Errorf("delivery preview is not wired")
	}
	row, err := s.Sites.GetSite(ctx, principal, workspaceID, siteID)
	if err != nil {
		return nil, err
	}
	files := parseThemeFiles(input.Files)
	if len(files) == 0 {
		// 空沙盒 = 当前 published 主题。
		published, ferr := s.Sites.ThemePublishedFiles(ctx, principal, workspaceID, siteID)
		if ferr != nil {
			return nil, ferr
		}
		files = parseThemeFiles(published)
	}
	slot := input.Slot
	if slot == "" {
		slot = theme.SlotHome
	}

	// The base URL must validate before any rendering happens; the closure
	// below rewrites root-relative references so cross-origin iframe
	// previews resolve media and script references.
	baseURL := ""
	if raw := strings.TrimSpace(input.BaseURL); raw != "" {
		parsed, err := url.Parse(raw)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
			return nil, fmt.Errorf("%w: base_url must be an absolute http(s) origin", site.ErrInvalidInput)
		}
		baseURL = parsed.Scheme + "://" + parsed.Host
	}

	// Previews bypass the page cache by construction (no pipeline).
	render := func(kind string, vm any) (*Response, error) {
		queryFn, qerr := s.Sites.ThemeQuery(ctx, principal, workspaceID, siteID, s.Reader)
		if qerr != nil {
			return nil, qerr
		}
		compiled, err := theme.Compile(files, theme.Options{SiteSlug: row.Slug, BaseAbsURL: baseURL, Query: queryFn})
		if err != nil {
			return nil, err
		}
		var buffer bytes.Buffer
		if err := compiled.Render(&buffer, kind, vm); err != nil {
			return nil, err
		}
		body := buffer.Bytes()
		if baseURL != "" {
			body = []byte(absolutizePreviewBody(string(body), baseURL))
		}
		return &Response{Body: body, ContentType: contentHTML, CacheControl: noStorePolicy, NoIndex: true, Status: 200}, nil
	}

	switch slot {
	case "", theme.SlotHome:
		view, err := s.Reader.HomeWithConfig(ctx, previewAddr, principal, row.Slug, "")
		if err != nil {
			return nil, err
		}
		if len(view.Sections) == 0 {
			page, err := s.Reader.Posts(ctx, previewAddr, principal, row.Slug, site.PublicPostQuery{Limit: 10})
			if err != nil {
				return nil, err
			}
			view.Sections = []site.PublicSection{{Type: site.HomepageSectionLatest, Title: "最新", Items: page.Items}}
		}
		var facets []tag.FacetItem
		if tags, err := s.Reader.Tags(ctx, previewAddr, principal, row.Slug, 24); err == nil {
			facets = tags
		}
		vm := ResolveHome(view, facets)
		vm.Site = chrome(factsFrom(row), "home")
		vm.Title = row.Name + "（预览）"
		vm.Canonical = vm.Site.HomeHref
		vm.NoIndex = true
		return render(theme.SlotHome, vm)
	case theme.SlotList:
		page, err := s.Reader.Posts(ctx, previewAddr, principal, row.Slug, site.PublicPostQuery{Limit: 10})
		if err != nil {
			return nil, err
		}
		vm := ResolveList(row.Slug, "文章（预览）", "/sites/"+row.Slug+"/posts/", page, page.NextCursor)
		vm.Site = chrome(factsFrom(row), "list")
		vm.Title = "文章 · " + row.Name + "（预览）"
		vm.NoIndex = true
		return render(theme.SlotList, vm)
	case theme.SlotDetail:
		if input.DisplayPath == "" {
			return nil, site.ErrPathInvalid
		}
		content, err := s.Reader.Post(ctx, previewAddr, principal, row.Slug, input.DisplayPath, "")
		if err != nil {
			return nil, err
		}
		vm := ResolveDetail(row.Slug, content, s.authorizedBodyImages(ctx, factsFrom(row), content.Markdown))
		vm.Site = chrome(factsFrom(row), "detail")
		vm.Title = content.Title + " · " + row.Name + "（预览）"
		vm.NoIndex = true
		return render(theme.SlotDetail, vm)
	default:
		return nil, site.ErrInvalidInput
	}
}

// factsFrom 把站点行包装为渲染所需的 facts（预览路径：无 release 快照）。
func factsFrom(row site.Site) site.SiteFacts {
	return site.SiteFacts{Site: row, CommentsMode: row.CommentsMode, ThemeFiles: json.RawMessage("{}")}
}

// previewAddr is the synthetic client address of preview reads (the preview
// is member-gated; the shared anonymous budget is not consumed).
const previewAddr = "preview"

// absolutizePreviewBody rewrites root-relative href/src/action references to
// the given origin. Only the preview face uses this; the live face renders on
// its own origin and never rewrites.
func absolutizePreviewBody(body, baseURL string) string {
	body = strings.ReplaceAll(body, `href="/`, `href="`+baseURL+`/`)
	body = strings.ReplaceAll(body, `src="/`, `src="`+baseURL+`/`)
	body = strings.ReplaceAll(body, `action="/`, `action="`+baseURL+`/`)
	return body
}
