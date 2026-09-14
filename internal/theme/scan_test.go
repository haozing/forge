package theme

import (
	"strings"
	"testing"
)

// scan_test.go — §10 安全验收：对扫描器与运行时边界的对抗样例。
// 扫描器是第一道闸（agent 写入/apply 时拒绝），html/template 自动转义是
// 第二道闸（即使漏网也不产生可执行标记），本文件两层都测。

func layoutWith(content string) map[string]string {
	return map[string]string{
		SlotLayout: DefaultTheme[SlotLayout],
		SlotHome:   "{{define \"content\"}}" + content + "{{end}}",
	}
}

// TestScanRejectsAdversarialTemplates 逐条对抗样例必须被 Compile 拒绝。
func TestScanRejectsAdversarialTemplates(t *testing.T) {
	cases := []struct {
		name string
		src  string
		rule string // 期望命中的规则；空 = 只要求被拒
	}{
		{"script_plain", `<script>alert(1)</script>`, "script_tag"},
		{"script_upper", `<SCRIPT src=x></SCRIPT>`, "script_tag"},
		{"script_space", `< script>alert(1)</ script>`, "script_tag"},
		{"script_slash", `<script/src=data:,alert(1)>`, "script_tag"},
		{"onclick", `<div onclick="alert(1)">x</div>`, "event_attr"},
		{"onmouseover_spaced", `<div onmouseover = "alert(1)">x</div>`, "event_attr"},
		{"onerror_upper", `<img ONERROR="alert(1)">`, "event_attr"},
		{"js_url", `<a href="javascript:alert(1)">x</a>`, "js_url"},
		{"js_url_mixed_case", `<a href="JaVaScRiPt:alert(1)">x</a>`, "js_url"},
		{"js_url_entity", `<a href="javascript&#58;alert(1)">x</a>`, "js_url"},
		{"js_url_entity_hex", `<a href="javascript&#x3a;alert(1)">x</a>`, "js_url"},
		{"js_url_newline", "<a href=\"java\nscript:alert(1)\">x</a>", "js_url"},
		{"data_html_entity", `<a href="data&#58;text/html,<b>x</b>">y</a>`, "data_html"},
		{"data_html", `<a href="data:text/html,<script>alert(1)</script>">x</a>`, "data_html"},
		{"iframe", `<iframe src="//evil.example"></iframe>`, "danger_tag"},
		{"object", `<object data="x"></object>`, "danger_tag"},
		{"embed", `<embed src="x">`, "danger_tag"},
		{"form_post", `<form method="post" action="/x"><input name="q"></form>`, "form_rule"},
		{"form_default_method", `<form action="/sites/s/search"><input name="q"></form>`, "form_rule"},
		{"form_abs_action", `<form method="get" action="https://evil.example/x"></form>`, "form_rule"},
		{"noescape_call", `{{ noescape .Body }}`, "noescape_forbidden"},
		{"noescape_in_comment", `{{/* 用 noescape 输出原始 HTML */}}x`, "noescape_forbidden"},
		{"unknown_fn", `{{ stealSecrets }}`, "unknown_function"},
	}
	for _, tc := range cases {
		_, err := Compile(layoutWith(tc.src), Options{SiteSlug: "demo"})
		if err == nil {
			t.Errorf("%s: expected rejection, compiled ok", tc.name)
			continue
		}
		ce, ok := err.(*CompileError)
		if !ok {
			t.Errorf("%s: expected *CompileError, got %T", tc.name, err)
			continue
		}
		if tc.rule != "" && !strings.Contains(ce.Error(), "["+tc.rule+"]") {
			t.Errorf("%s: expected rule %s, got: %s", tc.name, tc.rule, ce.Error())
		}
	}
}

// TestScanAcceptsLegitForms 合法形态必须放行（防扫描器误伤把正常主题打死）。
func TestScanAcceptsLegitForms(t *testing.T) {
	ok := []string{
		`<form method="get" action="/sites/demo/search"><input name="q"></form>`,
		`<a href="/sites/demo/posts/hello">内链</a>`,
		`<a href="{{absURL .Canonical}}">绝对内链</a>`,
		`<img src="{{assetURL "abc123"}}">`,
		`<a href="/sites/demo/a">x</a> <a href="mailto:a@b.c">信</a>`,
		`onclick 名词出现在正文文字里：我们讨论了 event attribute 的写法`, // 无 onx= 形态
	}
	for i, src := range ok {
		if _, err := Compile(layoutWith(src), Options{SiteSlug: "demo"}); err != nil {
			t.Errorf("case %d should pass, got: %v", i, err)
		}
	}
}

// TestScanCSSAdversarial CSS 文件的对抗样例。
func TestScanCSSAdversarial(t *testing.T) {
	bad := []struct {
		src  string
		rule string
	}{
		{`.x { background: url(https://evil.example/track.png) }`, "css_url"},
		{`.x { background: url("//evil.example/x.png") }`, "css_url"},
		{`@import url("https://evil.example/x.css");`, "css_import"},
		{`.x { background: url(data:text/html,<script>) }`, "css_url"},
	}
	for i, tc := range bad {
		_, err := Compile(map[string]string{
			SlotLayout: DefaultTheme[SlotLayout],
			"theme.css": tc.src,
		}, Options{SiteSlug: "demo"})
		if err == nil || !strings.Contains(err.Error(), "["+tc.rule+"]") {
			t.Errorf("css case %d expected %s, got: %v", i, tc.rule, err)
		}
	}
	good := []string{
		`.hero { background: url("/sites/demo/media/abc") }`,
		`.dot { background: url(data:image/png;base64,iVBOR) }`,
		`.none { background: none }`,
	}
	for i, src := range good {
		if _, err := Compile(map[string]string{
			SlotLayout:  DefaultTheme[SlotLayout],
			"theme.css": src,
		}, Options{SiteSlug: "demo"}); err != nil {
			t.Errorf("css good case %d should pass, got: %v", i, err)
		}
	}
}

// TestStructuralRejects 结构性拒绝：未知槽位、超限文件、缺挂载点。
func TestStructuralRejects(t *testing.T) {
	if _, err := Compile(map[string]string{"evil_slot": "x"}, Options{SiteSlug: "demo"}); err == nil {
		t.Error("unknown slot should be rejected")
	}
	if _, err := Compile(map[string]string{
		SlotLayout: DefaultTheme[SlotLayout],
		SlotHome:   strings.Repeat("a", MaxFileBytes+1),
	}, Options{SiteSlug: "demo"}); err == nil {
		t.Error("oversize file should be rejected")
	}
	if _, err := Compile(map[string]string{SlotLayout: `<html>{{define "layout"}}<body></body>{{end}}`},
		Options{SiteSlug: "demo"}); err == nil {
		t.Error("layout without content mount should be rejected")
	}
}

// TestRuntimeAutoescapeDefense 第二道闸：转义与 URL 过滤。即使恶意数据
// 进入 VM，html/template 也按上下文转义/降级，不产生可执行标记。
func TestRuntimeAutoescapeDefense(t *testing.T) {
	theme, err := Compile(map[string]string{
		SlotLayout: DefaultTheme[SlotLayout],
		SlotHome:   `{{define "content"}}<a href="{{.Link}}">{{.Title}}</a><p>{{.Title}}</p>{{end}}`,
	}, Options{SiteSlug: "demo"})
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	var sb strings.Builder
	err = theme.Render(&sb, SlotHome, map[string]any{
		"Title": `<script>alert(1)</script>`,
		"Link":  `javascript:alert(1)`,
		"Site":  map[string]any{"SiteLang": "zh"},
	})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	out := sb.String()
	if strings.Contains(out, "<script>alert(1)</script>") {
		t.Fatalf("executable script leaked into output")
	}
	if !strings.Contains(out, "&lt;script&gt;") {
		t.Fatalf("expected escaped title in output")
	}
	if strings.Contains(out, `href="javascript:`) {
		t.Fatalf("javascript URL leaked into href")
	}
}

// TestQueryBudgetAdversarial query 原语对抗：预算、limit 上限、空参数。
func TestQueryBudgetAdversarial(t *testing.T) {
	calls := 0
	q := NewQueries(func(params map[string]any) (*QueryResult, error) {
		calls++
		return &QueryResult{}, nil
	})
	// 第 9 次必须失败（MaxQueryCalls=8）。
	for i := 0; i < MaxQueryCalls; i++ {
		if _, err := q.Run(map[string]any{"limit": 1}); err != nil {
			t.Fatalf("call %d should succeed: %v", i+1, err)
		}
	}
	if _, err := q.Run(map[string]any{"limit": 1}); err == nil {
		t.Fatal("call beyond budget should fail")
	}

	// limit 超上限必须被钳制（impl 收到的参数已裁剪）。
	var seen int
	q2 := NewQueries(func(params map[string]any) (*QueryResult, error) {
		p, err := ParseParams(params)
		if err != nil {
			return nil, err
		}
		seen = p.Limit
		return &QueryResult{}, nil
	})
	if _, err := q2.Run(map[string]any{"limit": 100000}); err != nil {
		t.Fatalf("oversize limit run: %v", err)
	}
	if seen != MaxQueryLimit {
		t.Fatalf("limit should clamp to %d, got %d", MaxQueryLimit, seen)
	}

	// 空参数与负数 limit：ParseParams 拒绝 / 兜底。
	if _, err := ParseParams(nil); err == nil {
		t.Fatal("nil params should be rejected")
	}
	p, err := ParseParams(map[string]any{"limit": -5})
	if err != nil || p.Limit <= 0 {
		t.Fatalf("negative limit should fall back to default, got %d err %v", p.Limit, err)
	}

	// nil Queries / nil impl 都是空结果而不是 panic。
	var nq *Queries
	if r, err := nq.Run(map[string]any{}); err != nil || r == nil {
		t.Fatal("nil Queries should return empty result")
	}
	empty := NewQueries(nil)
	if r, err := empty.Run(map[string]any{"model": "x"}); err != nil || r == nil {
		t.Fatal("nil impl should return empty result")
	}
}
