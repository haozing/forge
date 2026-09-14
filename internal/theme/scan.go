package theme

// scan.go — 模板/CSS 源码安全扫描（§5.3）。作用域：只扫 agent 写入与 apply
// 的文件；内置默认主题是可信基线不参与。主扫描走 HTML 解析器
//（scan_html.go，§5.3.1：属性值按「浏览器解码后」语义判定，正文文字不误伤），
// 叠加模板动作函数白名单与 noescape 禁令；错误按「文件:行:规则」结构化
// 返回供 agent 自修复。

import (
	"regexp"
	"strings"
)

var (
	reCSSImport  = regexp.MustCompile(`(?i)@\s*import`)
	reCSSURL     = regexp.MustCompile(`(?i)url\s*\(\s*['"]?([^'")]+)`)
	reNoescape   = regexp.MustCompile(`noescape`)
	reLine       = regexp.MustCompile(`\n`)
	reTemplateFn = regexp.MustCompile(`\{\{-?\s*([a-zA-Z_][a-zA-Z0-9_]*)`)
)

func lineOf(src string, offset int) int {
	return 1 + strings.Count(src[:offset], "\n")
}

func scanTemplate(name, src string) []Problem {
	var out []Problem
	// §5.3.1：主扫描走 HTML 解析器——属性值已完成字符引用解码、事件属性
	// /URL scheme/form 约束按语义判断。正文文字里的 "javascript:"、
	// "onclick=" 不是威胁（浏览器只在属性/标签位解析它们），不叠加正则
	// 兜底，避免把文档性文字误判为攻击。
	out = append(out, scanTemplateParsed(name, src)...)
	// noescape 永久不在白名单（XSS 边界，§4.2）：任何位置出现即拒。
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
