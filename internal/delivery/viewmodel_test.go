package delivery

import (
	"testing"

	"agentchunzhi/internal/site"
)

func homePost(title string) site.PublicPost {
	return site.PublicPost{
		Title:       title,
		DisplayPath: "posts/" + title,
		Summary:     title + " summary",
	}
}

func homeView(sections ...site.PublicSection) site.PublicHomeView {
	return site.PublicHomeView{
		Site:     site.PublicSiteInfo{Slug: "demo", Name: "Demo"},
		Sections: sections,
	}
}

func sectionTitles(vm HomeVM) []string {
	titles := make([]string, 0, len(vm.Sections))
	for _, section := range vm.Sections {
		titles = append(titles, section.Title)
	}
	return titles
}

// A model-scoped latest slot must render as its own titled section instead of
// being merged into the generic "最新" bucket assembled from home_components.
func TestResolveHomeModelScopedLatestStaysSeparate(t *testing.T) {
	style := mustStyleConfig(t, `{"preset":"calm","ia":{"home_components":["latest"]}}`)
	view := homeView(
		site.PublicSection{
			Type:  site.HomepageSectionLatest,
			Title: "最新",
			Items: []site.PublicPost{homePost("generic")},
		},
		site.PublicSection{
			Type:     site.HomepageSectionLatest,
			Title:    "镜头精选",
			ModelKey: "builtin_shot",
			Items:    []site.PublicPost{homePost("shot-a")},
		},
	)

	vm := ResolveHome(view, style, nil)

	if len(vm.Sections) != 2 {
		t.Fatalf("want 2 sections (generic latest + model slot), got %d: %v", len(vm.Sections), sectionTitles(vm))
	}
	generic, scoped := vm.Sections[0], vm.Sections[1]
	if generic.Title != "最新" || generic.ModelKey != "" {
		t.Fatalf("generic latest wrong: %+v", generic)
	}
	if scoped.Title != "镜头精选" || scoped.ModelKey != "builtin_shot" {
		t.Fatalf("model slot wrong: %+v", scoped)
	}
	if len(scoped.Items) != 1 || scoped.Items[0].Title != "shot-a" {
		t.Fatalf("model slot items wrong: %+v", scoped.Items)
	}
}

// A model-scoped slot with no title falls back to the model key so it is not
// rendered as an untitled block.
func TestResolveHomeModelScopedTitleFallsBackToModelKey(t *testing.T) {
	style := mustStyleConfig(t, `{"preset":"calm","ia":{"home_components":["latest"]}}`)
	view := homeView(site.PublicSection{
		Type:     site.HomepageSectionLatest,
		ModelKey: "builtin_document",
		Items:    []site.PublicPost{homePost("doc")},
	})

	vm := ResolveHome(view, style, nil)

	if len(vm.Sections) != 1 {
		t.Fatalf("want 1 section, got %d", len(vm.Sections))
	}
	if vm.Sections[0].Title != "builtin_document" {
		t.Fatalf("title fallback wrong: %q", vm.Sections[0].Title)
	}
	// The model-scoped slot must not be suppressed by home_components (which
	// only gates the generic latest/featured buckets).
	if vm.Sections[0].ModelKey != "builtin_document" {
		t.Fatalf("model key lost: %+v", vm.Sections[0])
	}
}

// Columns are keyed by (section_slug, model_key): the same column slug serving
// two models must not collapse into one column.
func TestResolveHomeColumnsSplitByModel(t *testing.T) {
	style := mustStyleConfig(t, `{"preset":"calm"}`)
	view := homeView(
		site.PublicSection{
			Type:        site.HomepageSectionColumn,
			SectionSlug: "news",
			Title:       "新闻",
			Items:       []site.PublicPost{homePost("plain")},
		},
		site.PublicSection{
			Type:        site.HomepageSectionColumn,
			SectionSlug: "news",
			Title:       "镜头新闻",
			ModelKey:    "builtin_shot",
			Items:       []site.PublicPost{homePost("shot-news")},
		},
	)

	vm := ResolveHome(view, style, nil)

	if len(vm.Sections) != 2 {
		t.Fatalf("want 2 distinct columns, got %d: %v", len(vm.Sections), sectionTitles(vm))
	}
	if vm.Sections[0].ModelKey != "" || vm.Sections[0].Items[0].Title != "plain" {
		t.Fatalf("generic column wrong: %+v", vm.Sections[0])
	}
	if vm.Sections[1].ModelKey != "builtin_shot" || vm.Sections[1].Items[0].Title != "shot-news" {
		t.Fatalf("model column wrong: %+v", vm.Sections[1])
	}
}

// Generic latest/featured buckets still follow home_components order while
// model-scoped slots append after them.
func TestResolveHomeKeepsComponentOrderForGenericBuckets(t *testing.T) {
	style := mustStyleConfig(t, `{"preset":"calm","ia":{"home_components":["featured","latest"]}}`)
	view := homeView(
		site.PublicSection{
			Type:  site.HomepageSectionLatest,
			Title: "最新",
			Items: []site.PublicPost{homePost("l")},
		},
		site.PublicSection{
			Type:     site.HomepageSectionFeatured,
			Title:    "镜头精选",
			ModelKey: "builtin_shot",
			Items:    []site.PublicPost{homePost("f")},
		},
	)

	vm := ResolveHome(view, style, nil)

	if len(vm.Sections) != 2 {
		t.Fatalf("want 2 sections, got %d: %v", len(vm.Sections), sectionTitles(vm))
	}
	if vm.Sections[0].Title != "最新" || vm.Sections[0].ModelKey != "" {
		t.Fatalf("generic bucket should come first: %+v", vm.Sections[0])
	}
	if vm.Sections[1].Title != "镜头精选" || vm.Sections[1].ModelKey != "builtin_shot" {
		t.Fatalf("model slot should append: %+v", vm.Sections[1])
	}
}

// TestResolveDetailDescriptionFallsBackToExcerpt pins the meta-description
// fallback: a post without a summary derives its description from the body
// plain text instead of shipping an empty meta tag.
func TestResolveDetailDescriptionFallsBackToExcerpt(t *testing.T) {
	withSummary := ResolveDetail("demo", site.PublicPostContent{
		Title: "T", Summary: "编辑写的摘要", Markdown: "很长的正文",
	}, nil)
	if withSummary.Description != "编辑写的摘要" {
		t.Fatalf("summary must win, got %q", withSummary.Description)
	}
	without := ResolveDetail("demo", site.PublicPostContent{
		Title: "T", Markdown: "正文第一段，足够作为摘要使用。",
	}, nil)
	if without.Description == "" || without.Description == "很长的正文" {
		t.Fatalf("expected plain-text excerpt fallback, got %q", without.Description)
	}
}
