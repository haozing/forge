package delivery

import (
	"testing"
)

// TestRendererCompilesSystemTemplates asserts the embedded system template
// sets parse at boot (NewRenderer panics otherwise). 主题化后内置模板只剩
// 系统页（gate/error）与协议输出（rss/sitemap/robots）。
func TestRendererCompilesSystemTemplates(t *testing.T) {
	renderer := NewRenderer()
	for _, kind := range []string{"gate", "error"} {
		if _, ok := renderer.pages[kind]; !ok {
			t.Fatalf("system page template %s not compiled", kind)
		}
	}
	for _, kind := range []string{"rss", "sitemap", "robots"} {
		if _, ok := renderer.xml[kind]; !ok {
			t.Fatalf("xml template %s not compiled", kind)
		}
	}
}

// TestRenderSystemPages 冒烟：gate/error 能渲染出完整 HTML。
func TestRenderSystemPages(t *testing.T) {
	renderer := NewRenderer()
	for _, kind := range []string{"gate", "error"} {
		vm := ErrorVM{Page: Page{Kind: kind, Title: kind, NoIndex: true}, Status: 404}
		if kind == "gate" {
			vm = ErrorVM{Page: Page{Kind: kind, Title: "站点", NoIndex: true}}
		}
		body, err := renderer.RenderSystemPage(kind, vm)
		if err != nil {
			t.Fatalf("%s render: %v", kind, err)
		}
		if len(body) == 0 {
			t.Fatalf("%s rendered empty", kind)
		}
	}
}
