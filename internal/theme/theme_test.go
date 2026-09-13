package theme

import (
	"strings"
	"testing"
)

func TestCompileAndRender(t *testing.T) {
	files := map[string]string{
		SlotLayout: DefaultTheme[SlotLayout],
		SlotHome:   `{{define "content"}}<h1>{{.Title}}</h1>{{range .Items}}<a href="{{.Href}}">{{.Title}}</a>{{end}}{{end}}`,
	}
	theme, err := Compile(files, Options{SiteSlug: "demo"})
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	var sb strings.Builder
	err = theme.Render(&sb, SlotHome, map[string]any{
		"Title": "测试站", "Description": "", "Site": map[string]any{"SiteLang": "zh"},
	})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if !strings.Contains(sb.String(), "<h1>测试站</h1>") {
		t.Fatalf("output missing title: %s", sb.String()[:200])
	}
}

func TestCompileRejectsScript(t *testing.T) {
	files := map[string]string{
		SlotLayout: DefaultTheme[SlotLayout],
		SlotHome:   `{{define "content"}}<script>alert(1)</script>{{end}}`,
	}
	if _, err := Compile(files, Options{SiteSlug: "demo"}); err == nil {
		t.Fatal("expected compile error for <script>")
	}
}

func TestCompileRejectsNoescape(t *testing.T) {
	files := map[string]string{
		SlotLayout: DefaultTheme[SlotLayout],
		SlotDetail: `{{define "content"}}{{noescape "x"}}{{end}}`,
	}
	if _, err := Compile(files, Options{SiteSlug: "demo"}); err == nil {
		t.Fatal("expected compile error for noescape")
	}
}

func TestLayoutMissingContentMount(t *testing.T) {
	files := map[string]string{SlotLayout: `<html>{{define "layout"}}{{end}}`}
	if _, err := Compile(files, Options{SiteSlug: "demo"}); err == nil {
		t.Fatal("expected error for missing content mount")
	}
}

func TestDefaultThemeCompilesAlone(t *testing.T) {
	if _, err := Compile(map[string]string{}, Options{SiteSlug: "demo"}); err != nil {
		t.Fatalf("default theme compile: %v", err)
	}
}
