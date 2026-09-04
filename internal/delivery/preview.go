package delivery

// preview.go — the real-render preview of the management face (design doc
// §8.2): one member-authenticated POST renders the requested page through
// the exact Renderer + StyleEngine the live face uses, with a candidate
// style patch merged over the working style. Preview output is never cached
// and always noindex + no-store.

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"agentchunzhi/internal/auth"
	"agentchunzhi/internal/site"
	"agentchunzhi/internal/tag"
)

// PreviewInput carries the preview request body.
type PreviewInput struct {
	StyleConfig json.RawMessage `json:"style_config"`
	CustomCss   string          `json:"custom_css"`
	Page        string          `json:"page"`
	DisplayPath string          `json:"display_path"`
}

// RenderPreview renders one candidate page. The site service call enforces
// site.read; style patch validation failures return the wrapped site error so
// the handler answers 422.
func (s *Service) RenderPreview(ctx context.Context, principal auth.Principal, workspaceID, siteID string, input PreviewInput) (*Response, error) {
	if s.Sites == nil || s.Reader == nil {
		return nil, fmt.Errorf("delivery preview is not wired")
	}
	row, err := s.Sites.GetSite(ctx, principal, workspaceID, siteID)
	if err != nil {
		return nil, err
	}
	// Candidate style = working style ⊕ patch, fully re-validated.
	styleDocument := row.StyleConfig
	if len(input.StyleConfig) > 0 {
		merged, err := site.MergeStylePatch(styleDocument, input.StyleConfig)
		if err != nil {
			return nil, err
		}
		styleDocument = merged
	}
	config, err := site.ParseStyleConfig(styleDocument)
	if err != nil {
		return nil, err
	}
	customCss := row.CustomCss
	if strings.TrimSpace(input.CustomCss) != "" {
		clean, stripped := site.SanitizeCSS(input.CustomCss)
		if len(stripped) > 0 && strings.TrimSpace(clean) == "" {
			return nil, fmt.Errorf("%w: custom_css was entirely removed by the sanitizer", site.ErrInvalidInput)
		}
		customCss = clean
	}
	facts := site.SiteFacts{
		Site:             row,
		HomepageConfig:   row.HomepageConfig,
		NavigationConfig: row.NavigationConfig,
		StyleConfig:      styleDocument,
		CustomCss:        customCss,
		Template:         row.Template,
	}
	// Previews bypass the page cache by construction (no pipeline).
	render := func(kind string, vm any) (*Response, error) {
		body, err := s.Render.RenderPage(kind, vm)
		if err != nil {
			return nil, err
		}
		return &Response{Body: body, ContentType: contentHTML, CacheControl: noStorePolicy, NoIndex: true, Status: 200}, nil
	}
	switch input.Page {
	case "", "home":
		view, err := s.Reader.HomeWithConfig(ctx, previewAddr, principal, row.Slug, facts.HomepageConfig)
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
		vm := ResolveHome(view, config, facets)
		vm.Site = chrome(facts, config, "home")
		vm.Title = facts.Site.Name + "（预览）"
		vm.Canonical = vm.Site.HomeHref
		vm.NoIndex = true
		return render("home", vm)
	case "posts":
		page, err := s.Reader.Posts(ctx, previewAddr, principal, row.Slug, site.PublicPostQuery{Limit: config.PostsPerPage})
		if err != nil {
			return nil, err
		}
		vm := ResolveList(row.Slug, "文章（预览）", "/sites/"+row.Slug+"/posts/", page, config, page.NextCursor)
		vm.Site = chrome(facts, config, "list")
		vm.Title = "文章 · " + row.Name + "（预览）"
		vm.NoIndex = true
		return render("list", vm)
	case "detail":
		if input.DisplayPath == "" {
			return nil, site.ErrPathInvalid
		}
		content, err := s.Reader.Post(ctx, previewAddr, principal, row.Slug, input.DisplayPath)
		if err != nil {
			return nil, err
		}
		vm := ResolveDetail(row.Slug, content, s.authorizedBodyImages(ctx, facts, content.Markdown))
		vm.Site = chrome(facts, config, "detail")
		vm.Title = content.Title + " · " + row.Name + "（预览）"
		vm.NoIndex = true
		s.attachDetailRelated(ctx, previewAddr, principal, row.Slug, content, &vm)
		return render("detail", vm)
	default:
		return nil, site.ErrInvalidInput
	}
}

// previewAddr is the synthetic client address of preview reads (the preview
// is member-gated; the shared anonymous budget is not consumed).
const previewAddr = "preview"
