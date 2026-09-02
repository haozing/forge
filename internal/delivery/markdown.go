package delivery

// markdown.go — server-side markdown rendering of the HTML face (design doc
// §5.2): goldmark GFM (tables, strikethrough, task lists) followed by a
// bluemonday whitelist sanitize (semantic tags only; script/iframe/event
// attributes/style stripped) plus heading extraction for the detail TOC.

import (
	"regexp"
	"strings"

	"github.com/microcosm-cc/bluemonday"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/renderer/html"
	"github.com/yuin/goldmark/text"
)

// Heading is one TOC entry of a detail page.
type Heading struct {
	ID    string
	Text  string
	Level int
}

// MarkdownResult carries the sanitized HTML and the h1–h3 outline.
type MarkdownResult struct {
	HTML     string
	Headings []Heading
}

var markdownEngine = goldmark.New(
	goldmark.WithExtensions(extension.Table, extension.Strikethrough, extension.TaskList, extension.Linkify),
	goldmark.WithParserOptions(parser.WithAutoHeadingID()),
	goldmark.WithRendererOptions(html.WithUnsafe()),
)

// markdownPolicy is the bluemonday whitelist: semantic HTML only. Style
// attributes, event handlers, scripts, iframes and forms never survive.
var markdownPolicy = buildMarkdownPolicy()

func buildMarkdownPolicy() *bluemonday.Policy {
	policy := bluemonday.NewPolicy()
	policy.AllowStandardURLs()
	policy.AllowAttrs("href", "title").OnElements("a")
	policy.AllowAttrs("id").OnElements("h1", "h2", "h3", "h4", "h5", "h6")
	policy.AllowAttrs("src", "alt", "width", "height").OnElements("img")
	policy.AllowAttrs("type").Matching(regexp.MustCompile(`^checkbox$`)).OnElements("input")
	policy.AllowAttrs("disabled", "checked").OnElements("input")
	policy.AllowAttrs("start").OnElements("ol")
	policy.AllowElements(
		"a", "abbr", "b", "blockquote", "br", "caption", "code", "dd", "del", "details", "div",
		"dl", "dt", "em", "figcaption", "figure", "h1", "h2", "h3", "h4", "h5", "h6", "hr", "i",
		"img", "input", "kbd", "li", "mark", "ol", "p", "pre", "q", "s", "summary", "sup", "sub",
		"table", "tbody", "td", "tfoot", "th", "thead", "tr", "ul",
	)
	return policy
}

// RenderMarkdown renders one markdown source into sanitized HTML with the
// extracted heading outline (auto heading IDs feed both anchors and the TOC).
func RenderMarkdown(source string) MarkdownResult {
	if strings.TrimSpace(source) == "" {
		return MarkdownResult{HTML: "", Headings: []Heading{}}
	}
	document := markdownEngine.Parser().Parse(text.NewReader([]byte(source)))
	var builder strings.Builder
	if err := markdownEngine.Renderer().Render(&builder, []byte(source), document); err != nil {
		return MarkdownResult{HTML: "", Headings: []Heading{}}
	}
	headings := []Heading{}
	ast.Walk(document, func(node ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		heading, ok := node.(*ast.Heading)
		if !ok || heading.Level > 3 {
			return ast.WalkContinue, nil
		}
		var textBuilder strings.Builder
		for child := heading.FirstChild(); child != nil; child = child.NextSibling() {
			if segment, ok := child.(*ast.Text); ok {
				textBuilder.Write(segment.Segment.Value([]byte(source)))
			}
		}
		headings = append(headings, Heading{ID: headingID(heading), Text: strings.TrimSpace(textBuilder.String()), Level: heading.Level})
		return ast.WalkSkipChildren, nil
	})
	return MarkdownResult{HTML: markdownPolicy.Sanitize(builder.String()), Headings: headings}
}

// headingID extracts the auto-generated heading id (goldmark stores []byte).
func headingID(heading *ast.Heading) string {
	value, ok := heading.AttributeString("id")
	if !ok || value == nil {
		return ""
	}
	switch typed := value.(type) {
	case string:
		return typed
	case []byte:
		return string(typed)
	default:
		return ""
	}
}
