package delivery

// renderer.go — 双轨渲染设施：
//   - 主题页面（home/detail/list/…）经 internal/theme 引擎按站点渲染，
//     入口是 Service.renderThemed（service.go）；
//   - 系统页（gate/error）与协议输出（rss/sitemap/robots）保持内置模板。
// 内置模板只服务于系统页，agent 不可编辑；用户主题全部来自数据库。

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

// 系统页集合：gate（内容不足门）与 error。xml 三件套与 static 脚本同样内置。
var systemPageSets = map[string]string{
	"gate":  "templates/pages/gate.html",
	"error": "templates/errors/error.html",
}

var xmlSets = map[string]string{
	"rss":     "templates/xml/rss.xml",
	"sitemap": "templates/xml/sitemap.xml",
	"robots":  "templates/xml/robots.txt",
}

// Renderer holds the compiled system template sets.
type Renderer struct {
	pages map[string]*template.Template
	xml   map[string]*template.Template
}

// NewRenderer compiles every system template set; a malformed template panics.
func NewRenderer() *Renderer {
	renderer := &Renderer{pages: map[string]*template.Template{}, xml: map[string]*template.Template{}}
	for kind, file := range systemPageSets {
		set, err := template.New("layout").ParseFS(templateFS,
			"templates/layout.html",
			"templates/partials/header.html",
			"templates/partials/footer.html",
			file,
		)
		if err != nil {
			panic(fmt.Sprintf("delivery: parse system template %s: %v", kind, err))
		}
		renderer.pages[kind] = set
	}
	for kind, file := range xmlSets {
		set, err := template.New(kind).ParseFS(templateFS, file)
		if err != nil {
			panic(fmt.Sprintf("delivery: parse xml template %s: %v", kind, err))
		}
		renderer.xml[kind] = set
	}
	return renderer
}

// RenderSystemPage executes one system page set (gate / error).
func (r *Renderer) RenderSystemPage(kind string, vm any) ([]byte, error) {
	set, ok := r.pages[kind]
	if !ok {
		return nil, fmt.Errorf("delivery: unknown system template %q", kind)
	}
	var buffer bytes.Buffer
	writer := bufio.NewWriter(&buffer)
	if err := set.ExecuteTemplate(writer, "layout", vm); err != nil {
		return nil, fmt.Errorf("delivery: render system page %s: %w", kind, err)
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

// ChatJavaScript serves the chat island script bytes.
func ChatJavaScript() []byte {
	body, err := templateFS.ReadFile("templates/static/chat.js")
	if err != nil {
		panic("delivery: chat island script missing: " + err.Error())
	}
	return body
}
