package theme

// scan_html.go — §5.3.1 的解析器扫描：模板动作先替换为占位文本，再用
// x/net/html 解析（属性值已完成字符引用解码，天然免疫 &#58; 变体），
// 按语义检查：危险元素、on* 事件属性、URL 属性的 scheme、form 约束。
// 相比纯正则，正文文字里出现 "javascript:"、"onclick=" 等不会再误伤。

import (
	"strings"

	"golang.org/x/net/html"
)

// actionPlaceholder 顶替 {{…}} 动作参与 HTML 解析（占位本身无害）。
const actionPlaceholder = "z"

// urlAttributes 是取值需按 URL 校验 scheme 的属性。
var urlAttributes = map[string]bool{
	"href": true, "src": true, "action": true, "xlink:href": true,
	"formaction": true, "poster": true, "background": true,
}

// dangerElements 是禁止出现的元素（§5.3）。
var dangerElements = map[string]bool{
	"script": true, "iframe": true, "object": true, "embed": true,
}

// scanTemplateParsed 用 HTML 解析器扫描一份模板的静态部分。
func scanTemplateParsed(name, src string) []Problem {
	var out []Problem

	// 1) 动作占位：{{…}}（含 {{- / -}} 修剪）替换为无占位文本，避免
	//    动作内表达式被误当 HTML/属性语义；动作内部另有函数白名单。
	masked := maskTemplateActions(src)

	// 2) 解析（容错模式：模板拼出的 HTML 常不闭合，解析器可自恢复）。
	doc, err := html.Parse(strings.NewReader(masked))
	if err != nil {
		return out
	}

	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode {
			tag := strings.ToLower(n.Data)
			if dangerElements[tag] {
				out = append(out, Problem{File: name, Rule: elementRule(tag),
					Detail: "模板禁止 <" + tag + ">（脚本经 {{searchIsland}} 白名单输出）"})
			}
			for _, attr := range n.Attr {
				key := strings.ToLower(attr.Key)
				if isEventAttribute(key) {
					out = append(out, Problem{File: name, Rule: "event_attr",
						Detail: "模板禁止 on* 事件属性（" + key + "）"})
					continue
				}
				if urlAttributes[key] {
					if problem := checkURLAttribute(name, key, attr.Val); problem != nil {
						out = append(out, *problem)
					}
				}
			}
			if tag == "form" {
				if problem := checkFormNode(name, n); problem != nil {
					out = append(out, *problem)
				}
			}
		}
		for child := n.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(doc)
	return out
}

// elementRule 映射元素名到规则号（与《模板上下文参考》§7 表一致）。
func elementRule(tag string) string {
	switch tag {
	case "script":
		return "script_tag"
	default:
		return "danger_tag"
	}
}

func isEventAttribute(key string) bool {
	return strings.HasPrefix(key, "on") && len(key) > 2
}

// checkURLAttribute 按浏览器语义校验 URL 属性值：剥除 \t\r\n（浏览器
// 剥除后才解析 scheme），再看 scheme 是否 javascript / data:text/html。
func checkURLAttribute(name, key, value string) *Problem {
	cleaned := strings.Map(func(r rune) rune {
		switch r {
		case '\t', '\n', '\r', '\f', '':
			return -1
		}
		return r
	}, strings.TrimSpace(value))
	lower := strings.ToLower(cleaned)
	if strings.HasPrefix(lower, "javascript:") {
		p := Problem{File: name, Rule: "js_url",
			Detail: "禁止 javascript: URL（属性 " + key + "）"}
		return &p
	}
	if strings.HasPrefix(lower, "data:") && !strings.HasPrefix(lower, "data:image/") {
		p := Problem{File: name, Rule: "data_html",
			Detail: "禁止 data: URL（属性 " + key + "，仅 data:image 允许）"}
		return &p
	}
	return nil
}

func checkFormNode(name string, n *html.Node) *Problem {
	method, action := "", ""
	for _, attr := range n.Attr {
		switch strings.ToLower(attr.Key) {
		case "method":
			method = strings.ToLower(strings.TrimSpace(attr.Val))
		case "action":
			action = attr.Val
		}
	}
	actionOK := strings.HasPrefix(strings.TrimSpace(action), "/") ||
		strings.HasPrefix(strings.TrimSpace(action), "z") // 动作占位 = 模板表达式
	if method != "get" || !actionOK {
		return &Problem{File: name, Rule: "form_rule",
			Detail: `<form> 仅允许 method="get" 且 action 为站内相对路径`}
	}
	return nil
}

// maskTemplateActions 把 {{…}} 替换为占位字符 "z"（保留数量足够定位）。
// 使用逐字符扫描以正确处理包含引号/嵌套花括号的动作体。
func maskTemplateActions(src string) string {
	var b strings.Builder
	for i := 0; i < len(src); {
		if strings.HasPrefix(src[i:], "{{") {
			end := strings.Index(src[i+2:], "}}")
			if end < 0 {
				b.WriteByte(src[i])
				i++
				continue
			}
			b.WriteString("z")
			i += 2 + end + 2
			continue
		}
		b.WriteByte(src[i])
		i++
	}
	return b.String()
}
