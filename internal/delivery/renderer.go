package delivery

// renderer.go — template compilation and rendering (design doc §5.1). All
// templates ship embedded in the binary; every page set is parsed with the
// layout and partials at process start and a parse failure panics (a broken
// template must never boot). Templates receive ViewModel structs only.

import (
	"bufio"
	"bytes"
	"embed"
	"encoding/xml"
	"fmt"
	"html/template"
	"path"
	"sync"
)

//go:embed templates
var templateFS embed.FS

// baseStyleSheet is read once from the embedded FS.
var baseStyleSheet = readBaseStyles()

func readBaseStyles() string {
	body, err := templateFS.ReadFile("templates/static/site.css")
	if err != nil {
		panic("delivery: base stylesheet missing: " + err.Error())
	}
	return string(body)
}

// CarouselScript serves the carousel enhancement script bytes.
func CarouselScript() []byte {
	body, err := templateFS.ReadFile("templates/static/carousel.js")
	if err != nil {
		panic("delivery: carousel script missing: " + err.Error())
	}
	return body
}

// SearchJavaScript serves the search island script bytes.
func SearchJavaScript() []byte {
	body, err := templateFS.ReadFile("templates/static/search.js")
	if err != nil {
		panic("delivery: search island script missing: " + err.Error())
	}
	return body
}

var rendererFuncs = template.FuncMap{
	// noescape marks server-sanitized HTML (markdown pipeline output).
	"noescape": func(value string) template.HTML { return template.HTML(value) },
}

// pageSets enumerates every HTML page template with its content file.
var pageSets = map[string]string{
	"home":      "templates/pages/home.html",
	"list":      "templates/pages/list.html",
	"detail":    "templates/pages/detail.html",
	"tags":      "templates/pages/tag_index.html",
	"tag_page":  "templates/pages/tag_page.html",
	"search":    "templates/pages/search.html",
	"gate":      "templates/pages/gate.html",
	"about":     "templates/pages/about.html",
	"archive":   "templates/pages/archive.html",
	"error":     "templates/errors/error.html",
}

// xmlSets enumerates the non-HTML serializations.
var xmlSets = map[string]string{
	"rss":    "templates/xml/rss.xml",
	"sitemap": "templates/xml/sitemap.xml",
	"robots": "templates/xml/robots.txt",
}

// Renderer holds the compiled template sets.
type Renderer struct {
	pages map[string]*template.Template
	xml   map[string]*template.Template
}

// NewRenderer compiles every template set; a malformed template panics.
func NewRenderer() *Renderer {
	renderer := &Renderer{pages: map[string]*template.Template{}, xml: map[string]*template.Template{}}
	for kind, file := range pageSets {
		set, err := template.New("layout").Funcs(rendererFuncs).ParseFS(templateFS,
			"templates/layout.html",
			"templates/partials/header.html",
			"templates/partials/footer.html",
			"templates/partials/card.html",
			"templates/partials/pagination.html",
			"templates/partials/tag_chips.html",
			file,
		)
		if err != nil {
			panic(fmt.Sprintf("delivery: parse page template %s: %v", kind, err))
		}
		renderer.pages[kind] = set
	}
	for kind, file := range xmlSets {
		set, err := template.New(kind).Funcs(rendererFuncs).ParseFS(templateFS, file)
		if err != nil {
			panic(fmt.Sprintf("delivery: parse xml template %s: %v", kind, err))
		}
		renderer.xml[kind] = set
	}
	return renderer
}

// RenderPage executes one HTML page set.
func (r *Renderer) RenderPage(kind string, vm any) ([]byte, error) {
	set, ok := r.pages[kind]
	if !ok {
		return nil, fmt.Errorf("delivery: unknown page template %q", kind)
	}
	var buffer bytes.Buffer
	writer := bufio.NewWriter(&buffer)
	if err := set.ExecuteTemplate(writer, "layout", vm); err != nil {
		return nil, fmt.Errorf("delivery: render page %s: %w", kind, err)
	}
	if err := writer.Flush(); err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
}

// RenderXML executes one non-HTML template (file-content templates carry
// their file base name, not the set name). The XML declaration is prepended
// raw: html/template would escape a literal prolog inside the template.
func (r *Renderer) RenderXML(kind string, vm any) ([]byte, error) {
	file, ok := xmlSets[kind]
	if !ok {
		return nil, fmt.Errorf("delivery: unknown xml template %q", kind)
	}
	var buffer bytes.Buffer
	if err := r.xml[kind].ExecuteTemplate(&buffer, path.Base(file), vm); err != nil {
		return nil, fmt.Errorf("delivery: render xml %s: %w", kind, err)
	}
	if kind == "rss" || kind == "sitemap" {
		return append([]byte(xml.Header), buffer.Bytes()...), nil
	}
	return buffer.Bytes(), nil
}

// PageKinds lists every registered HTML page kind (route table test truth).
func (r *Renderer) PageKinds() []string {
	kinds := make([]string, 0, len(r.pages))
	for kind := range r.pages {
		kinds = append(kinds, kind)
	}
	return kinds
}

// once guards the shared default renderer (handlers and the preview share it).
var rendererOnce struct {
	sync.Once
	renderer *Renderer
}

// SharedRenderer lazily compiles the process-wide renderer.
func SharedRenderer() *Renderer {
	rendererOnce.Do(func() { rendererOnce.renderer = NewRenderer() })
	return rendererOnce.renderer
}
