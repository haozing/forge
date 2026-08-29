package retrieval

import (
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/text"
)

// markdownSegment is one block-level element of the Markdown AST with its
// heading path and locator. Block ordinals are assigned after empty blocks
// are dropped so locator numbers stay dense and stable.
type markdownSegment struct {
	Locator map[string]any
	Label   string
	Text    string
}

// MarkdownSegments parses the Markdown with the Goldmark AST (never regex)
// and emits one candidate segment per block element, carrying the heading
// path and the block ordinal in the source locator.
func MarkdownSegments(markdown string) []markdownSegment {
	if strings.TrimSpace(markdown) == "" {
		return nil
	}
	source := []byte(markdown)
	document := goldmark.New().Parser().Parse(text.NewReader(source))
	segments := make([]markdownSegment, 0, 32)
	var headingPath []string
	ordinal := 0

	for block := document.FirstChild(); block != nil; block = block.NextSibling() {
		if block.Kind() == ast.KindHeading {
			heading := block.(*ast.Heading)
			headingText := normalizeSegmentText(extractInlineText(source, heading))
			for len(headingPath) >= heading.Level {
				headingPath = headingPath[:len(headingPath)-1]
			}
			if headingText != "" {
				headingPath = append(headingPath, headingText)
			}
			segments = append(segments, markdownSegment{
				Locator: blockLocator(headingPath, ordinal),
				Label:   headingLabel(headingPath),
				Text:    headingText,
			})
			ordinal++
			continue
		}
		body := normalizeSegmentText(extractBlockText(source, block))
		if body == "" {
			// Empty segments never become chunks and do not consume an
			// ordinal so locators stay stable across runs.
			continue
		}
		segments = append(segments, markdownSegment{
			Locator: blockLocator(headingPath, ordinal),
			Label:   headingLabel(headingPath),
			Text:    body,
		})
		ordinal++
	}
	return segments
}

func blockLocator(headingPath []string, ordinal int) map[string]any {
	path := make([]string, len(headingPath))
	copy(path, headingPath)
	return map[string]any{
		"type":         SourceTypeMarkdown,
		"heading_path": path,
		"block":        ordinal,
	}
}

func headingLabel(path []string) string {
	if len(path) == 0 {
		return ""
	}
	return strings.Join(path, " > ")
}

// extractBlockText renders one block node as plain text: paragraphs and
// headings contribute inline text, code blocks their raw lines, lists and
// quotes fold their children.
func extractBlockText(source []byte, node ast.Node) string {
	switch block := node.(type) {
	case *ast.Paragraph:
		return extractInlineText(source, block)
	case *ast.TextBlock:
		return extractInlineText(source, block)
	case *ast.Heading:
		return extractInlineText(source, block)
	case *ast.FencedCodeBlock:
		return string(block.Lines().Value(source))
	case *ast.CodeBlock:
		return string(block.Lines().Value(source))
	case *ast.HTMLBlock:
		// Raw HTML is treated as text only; it is never rendered.
		return string(block.Lines().Value(source))
	case *ast.Blockquote:
		return joinBlocks(source, block)
	case *ast.List:
		return joinBlocks(source, block)
	default:
		return extractInlineText(source, node)
	}
}

func joinBlocks(source []byte, container ast.Node) string {
	var builder strings.Builder
	for child := container.FirstChild(); child != nil; child = child.NextSibling() {
		line := strings.TrimSpace(extractBlockText(source, child))
		if line == "" {
			continue
		}
		if builder.Len() > 0 {
			builder.WriteByte('\n')
		}
		builder.WriteString(line)
	}
	return builder.String()
}

// extractInlineText concatenates the text-bearing descendants of a node,
// keeping soft/hard line breaks and skipping markup and link URLs.
func extractInlineText(source []byte, node ast.Node) string {
	var builder strings.Builder
	var visit func(current ast.Node)
	visit = func(current ast.Node) {
		for child := current.FirstChild(); child != nil; child = child.NextSibling() {
			switch inline := child.(type) {
			case *ast.Text:
				builder.Write(inline.Segment.Value(source))
				if inline.SoftLineBreak() || inline.HardLineBreak() {
					builder.WriteByte('\n')
				}
			case *ast.String:
				builder.Write(inline.Value)
			case *ast.AutoLink:
				builder.Write(inline.Text(source))
			case *ast.RawHTML:
				// Markup never enters the canonical body.
			default:
				if child.HasChildren() {
					visit(child)
				}
			}
		}
	}
	visit(node)
	return builder.String()
}

func normalizeSegmentText(value string) string {
	var builder strings.Builder
	for _, line := range strings.Split(value, "\n") {
		builder.WriteString(strings.TrimRight(line, " \t"))
		builder.WriteString("\n")
	}
	return strings.TrimSpace(builder.String())
}
