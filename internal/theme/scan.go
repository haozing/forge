package theme

// scan.go — 模板/CSS 源码安全扫描（§5.3）。作用域：只扫 agent 写入与 apply
// 的文件；内置默认主题是可信基线不参与。M1 用规则扫描（字符串 + 正则），
// 错误按「文件:行:规则」结构化返回供 agent 自修复；误伤升级路径是换
// golang.org/x/net/html 解析器，调用方无感。

import (
	"regexp"
	"strings"
)

var (
	reScript     = regexp.MustCompile(`(?i)<\s*script`)
	reEventAttr  = regexp.MustCompile(`(?i)\son[a-z]+\s*=`)
	reJSURL      = regexp.MustCompile(`(?i)javascript\s*:`)
	reDataHTML   = regexp.MustCompile(`(?i)data:\s*text/html`)
	reDangerTag  = regexp.MustCompile(`(?i)<\s*(iframe|object|embed)\b`)
	reFormTag    = regexp.MustCompile(`(?i)<\s*form\b`)
	reFormGet    = regexp.MustCompile(`(?i)method\s*=\s*["']?get["']?`)
	reFormAction = regexp.MustCompile(`(?i)action\s*=\s*["']?(/|\{\{)`)
	reCSSImport  = regexp.MustCompile(`(?i)@\s*import`)
	reCSSURL     = regexp.MustCompile(`(?i)url\s*\(\s*['"]?([^'")]+)`)
	reNoescape   = regexp.MustCompile(`noescape`)
	reLine       = regexp.MustCompile(`\n`)
	reTemplateFn = regexp.MustCompile(`\{\{-?\s*([a-zA-Z_][a-zA-Z0-9_]*)`)
)

// blockedHandlers 是禁止出现的标签/属性规则（模板文件）。
var blockedRules = []struct {
	rule string
	re   *regexp.Regexp
	deny string
}{
	{"script_tag", reScript, "模板禁止 <script>（脚本经 {{searchIsland}} 白名单输出）"},
	{"event_attr", reEventAttr, "模板禁止 on* 事件属性"},
	{"js_url", reJSURL, "禁止 javascript: URL"},
	{"data_html", reDataHTML, "禁止 data:text/html"},
	{"danger_tag", reDangerTag, "模板禁止 iframe/object/embed"},
}

func lineOf(src string, offset int) int {
	return 1 + strings.Count(src[:offset], "\n")
}

func scanTemplate(name, src string) []Problem {
	var out []Problem
	for _, rule := range blockedRules {
		for _, loc := range rule.re.FindAllStringIndex(src, -1) {
			out = append(out, Problem{File: name, Line: lineOf(src, loc[0]), Rule: rule.rule, Detail: rule.deny})
		}
	}
	// <form>：仅允许 method=get + 站内相对 action（默认主题搜索框形态）。
	for _, loc := range reFormTag.FindAllStringIndex(src, -1) {
		end := loc[1]
		if close := strings.Index(src[loc[1]:], ">"); close >= 0 {
			end = loc[1] + close
		}
		tag := src[loc[0]:end]
		if !reFormGet.MatchString(tag) || !reFormAction.MatchString(tag) {
			out = append(out, Problem{File: name, Line: lineOf(src, loc[0]), Rule: "form_rule",
				Detail: `<form> 仅允许 method="get" 且 action 为站内相对路径`})
		}
	}
	// noescape 永久不在白名单（XSS 边界，§4.2）。
	for _, loc := range reNoescape.FindAllStringIndex(src, -1) {
		out = append(out, Problem{File: name, Line: lineOf(src, loc[0]), Rule: "noescape_forbidden",
			Detail: "正文 HTML 由服务端净化并已按 template.HTML 传入，禁止 noescape"})
	}
	return out
}

func scanCSS(name, src string) []Problem {
	var out []Problem
	for _, loc := range reCSSImport.FindAllStringIndex(src, -1) {
		out = append(out, Problem{File: name, Line: lineOf(src, loc[0]), Rule: "css_import", Detail: "CSS 禁止 @import（外部资源）"})
	}
	for _, loc := range reCSSURL.FindAllStringSubmatchIndex(src, -1) {
		url := strings.TrimSpace(src[loc[2]:loc[3]])
		if url == "" || strings.HasPrefix(url, "data:image") ||
			strings.HasPrefix(url, "/sites/") || url == "none" {
			continue
		}
		out = append(out, Problem{File: name, Line: lineOf(src, loc[0]), Rule: "css_url",
			Detail: "url() 仅允许 data:image、/sites/ 或 none"})
	}
	return out
}

// Scan 扫描一个主题文件，返回全部违规。
func Scan(name, src string) []Problem {
	if strings.HasSuffix(name, ".css") {
		return scanCSS(name, src)
	}
	var out []Problem
	out = append(out, scanTemplate(name, src)...)
	// 模板函数白名单：{{- funcName …}} 形式的一元函数名先过一遍已知集合；
	// 管道/字段访问不在本检查范围（Parse 阶段会拒绝未知函数）。
	for _, loc := range reTemplateFn.FindAllStringSubmatchIndex(src, -1) {
		fn := src[loc[2]:loc[3]]
		switch fn {
		case "if", "else", "end", "range", "with", "template", "define", "block",
			"assetURL", "absURL", "dateFmt", "truncate", "json", "i18n", "dict", "themeCSS",
			SearchIslandFn, "query", "printf", "print", "println", "len", "index",
			"slice", "not", "and", "or", "eq", "ne", "lt", "le", "gt", "ge", "urlquery", "html", "js":
			continue
		}
		out = append(out, Problem{File: name, Line: lineOf(src, loc[0]), Rule: "unknown_function",
			Detail: "函数 " + fn + " 不在白名单（见《模板上下文参考》）"})
	}
	return out
}
