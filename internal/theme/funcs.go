package theme

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// fmtDate 是 dateFmt 的实现：layout 用 Go 参考时间，value 支持
// time.Time / *time.Time / ISO 字符串。
func fmtDate(layout string, value any) string {
	switch v := value.(type) {
	case time.Time:
		return v.Format(layout)
	case *time.Time:
		if v != nil {
			return v.Format(layout)
		}
	case string:
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			return t.Format(layout)
		}
		return v
	}
	return ""
}

func marshalJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return "null"
	}
	return string(b)
}

// i18nText 是站点侧的最小文案表；站点级多语言文案后续挂 enabled_locales。
func i18nText(key string) string {
	table := map[string]string{
		"home":        "首页",
		"posts":       "文章",
		"tags":        "标签",
		"search":      "搜索",
		"about":       "关于",
		"archive":     "归档",
		"published":   "发布于",
		"updated":     "更新于",
		"read_more":   "阅读全文",
		"next":        "下一篇",
		"prev":        "上一篇",
		"categories":  "分类",
		"latest":      "最新内容",
		"empty_list":  "暂无内容",
		"back_home":   "返回首页",
		"comment":     "评论",
		"ask":              "出海agent",
		"ask_note":         "基于本站公开文章回答，回答下方标注来源。",
		"ask_quota_label":  "今日额度",
		"ask_greeting":     "你好！我是七渡出海的 AI 助手，基于本站 755+ 篇实战文章回答问题。输入你的问题开始。",
		"ask_placeholder":  "输入你的问题…",
		"ask_send":         "发送",
		"ask_login_required": "AI 问答需要登录后使用。登录后每位成员每日可提问 20 次。",
		"ask_go_login":     "登录后使用",
		"attachments": "附件",
	}
	if text, ok := table[key]; ok {
		return text
	}
	return key
}

// Fingerprint 对文件集做内容指纹（缓存键成分）。保持与 theme.go 的
// Revision 一致的语义：内容变则指纹变。
func Fingerprint(files map[string]string) string {
	keys := make([]string, 0, len(files))
	for k := range files {
		keys = append(keys, k)
	}
	// 稳定排序拼接后取长度与内容的简单哈希——指纹只用于缓存键，不涉安全。
	var b strings.Builder
	for i := 0; i < len(keys); i++ {
		for j := i + 1; j < len(keys); j++ {
			if keys[j] < keys[i] {
				keys[i], keys[j] = keys[j], keys[i]
			}
		}
	}
	for _, k := range keys {
		fmt.Fprintf(&b, "%s\x00%d\x00%s\x00", k, len(files[k]), files[k])
	}
	return fmt.Sprintf("%x", len(b.String())) + fmt.Sprintf("-%d", len(keys))
}
