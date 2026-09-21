// Package theme — 公开站点主题引擎（站点主题化与 AI 设计重构）。
// 用户主题以「槽位 → 模板源码」文件集存于 site.site_theme_revisions（jsonb），
// 引擎在运行时 template.Parse 编译（模板是数据，不是代码），渲染时与
// Postgres 取数构造的 VM 接合。安全模型：
//   - agent 产出的模板文件须过 Scan（§5.3）；
//   - 函数白名单（§4.3）之外不可调用，永久无 noescape；
//   - 正文 HTML 经 template.HTML 类型只由服务端净化管线赋值；
//   - query 原语的数据源是派生收录视图（三道闸视图层生效）。
package theme

import (
	"fmt"
	"strings"
	"time"
	"sync"
	"html/template"
)

// 槽位常量（§4.1）。缺失槽位回退内置默认主题。
const (
	SlotLayout     = "layout"
	SlotHome       = "home"
	SlotDetail     = "detail"
	SlotAbout      = "about"
	SlotList       = "list"
	SlotSection    = "section"
	SlotCategory   = "category"
	SlotTags       = "tags"
	SlotTagPage    = "tag_page"
	SlotSearch     = "search"
	SlotArchive    = "archive"
	SlotPage       = "page"
	SlotPartials   = "partials"
	SlotAsk        = "ask"
	SlotTokensCSS  = "tokens.css"
	SlotThemeCSS   = "theme.css"
	ContentDefine  = "content" // layout 必须定义的挂载点名
	SearchIslandFn = "searchIsland"
	ChatIslandFn   = "chatIsland"
)

// Slots 是主题可提供的全部槽位（CSS 与模板分列；partials 可选）。
var Slots = []string{
	SlotLayout, SlotHome, SlotDetail, SlotAbout, SlotList, SlotSection,
	SlotCategory, SlotTags, SlotTagPage, SlotSearch, SlotArchive, SlotPage,
	SlotPartials, SlotAsk,
}

// MaxFileBytes / MaxTotalBytes 是文件集大小上限（§3 要点）。
const (
	MaxFileBytes  = 256 << 10
	MaxTotalBytes = 1 << 20
)

// Theme 是一套已编译的主题。零值不可用；由 Compile 构造。
type Theme struct {
	mu    sync.RWMutex
	slots map[string]*template.Template // slot → 已编译（Clone 自 base + 该槽位文件）
	rev   string                        // 缓存键的修订版标识
}

// CompileError 汇总逐文件逐行的编译/校验问题（agent 可读，供自修复）。
type CompileError struct {
	Problems []Problem
}

// Problem 是一个可定位的编译/扫描问题。
type Problem struct {
	File   string `json:"file"`
	Line   int    `json:"line,omitempty"`
	Rule   string `json:"rule"`
	Detail string `json:"detail,omitempty"`
}

func (e *CompileError) Error() string {
	var b strings.Builder
	b.WriteString("theme compile failed")
	for _, p := range e.Problems {
		fmt.Fprintf(&b, "\n- %s:%d [%s] %s", p.File, p.Line, p.Rule, p.Detail)
	}
	return b.String()
}

// Options 是 Compile 的注入面：站点身份（URL 函数用）与 query 原语的实现。
type Options struct {
	// SiteSlug 用于 assetURL / absURL 的绝对前缀。
	SiteSlug string
	// BaseAbsURL 形如 https://host（可空 = 相对路径）。
	BaseAbsURL string
	// Query 执行公开收录查询；nil = 主题禁用 query 函数。
	Query QueryFunc
}

// QueryFunc 由服务层注入（复用 PublicReader + 三道闸视图），实现见 site 域。
type QueryFunc func(params map[string]any) (*QueryResult, error)

// Compile 把文件集编译为可渲染主题。files 的键为槽位名；未知槽位与超限文件
// 直接拒绝。layout 缺失或未定义 content 挂载点时报错。错误按文件聚合返回。
func Compile(files map[string]string, opts Options) (*Theme, error) {
	var problems []Problem
	clean := make(map[string]string, len(files))
	var total int
	for name, src := range files {
		if !validSlotName(name) {
			problems = append(problems, Problem{File: name, Rule: "unknown_slot", Detail: "未知槽位"})
			continue
		}
		if len(src) > MaxFileBytes {
			problems = append(problems, Problem{File: name, Rule: "file_too_large", Detail: "单文件超过 256KB"})
			continue
		}
		total += len(src)
		if total > MaxTotalBytes {
			problems = append(problems, Problem{File: name, Rule: "set_too_large", Detail: "文件集超过 1MB"})
			break
		}
		clean[name] = src
	}

	// 安全扫描只针对模板与 CSS 文本；布局为必备可信挂载点之外的文件一律扫。
	for name, src := range clean {
		problems = append(problems, Scan(name, src)...)
	}

	layoutSrc := clean[SlotLayout]
	if layoutSrc == "" {
		layoutSrc = DefaultTheme[SlotLayout]
	}
	if !strings.Contains(layoutSrc, `{{template "`+ContentDefine+`"`) {
		problems = append(problems, Problem{File: SlotLayout, Rule: "missing_content_mount",
			Detail: `layout 必须包含 {{template "content" .}} 挂载点`})
	}
	if len(problems) > 0 {
		return nil, &CompileError{Problems: problems}
	}

	themeCSS := clean[SlotThemeCSS]
	if themeCSS == "" {
		themeCSS = DefaultTheme[SlotThemeCSS]
	}
	tokensCSS := clean[SlotTokensCSS]
	if tokensCSS == "" {
		tokensCSS = DefaultTheme[SlotTokensCSS]
	}

	funcs := template.FuncMap{
		"themeCSS": func() template.CSS {
			return template.CSS(tokensCSS + "\n" + themeCSS)
		},
		"assetURL": func(id string) string {
			return "/sites/" + opts.SiteSlug + "/media/" + id
		},
		"absURL": func(path string) string {
			if opts.BaseAbsURL == "" {
				return path
			}
			return opts.BaseAbsURL + path
		},
		"dateFmt": func(layout string, value any) string { return fmtDate(layout, value) },
		"now": func() time.Time { return time.Now() },
		"truncate": func(n int, s string) string {
			r := []rune(s)
			if len(r) <= n {
				return s
			}
			return string(r[:n]) + "…"
		},
		"json":  marshalJSON,
		"i18n":  func(key string) string { return i18nText(key) },
		"dict": func(pairs ...any) map[string]any {
			out := make(map[string]any, len(pairs)/2)
			for i := 0; i+1 < len(pairs); i += 2 {
				key, _ := pairs[i].(string)
				out[key] = pairs[i+1]
			}
			return out
		},
		"query": opts.Query,
		SearchIslandFn: func() string {
			return `<script src="/static/delivery-search.js" defer></script>`
		},
		ChatIslandFn: func() string {
			return `<script src="/static/delivery-chat.js" defer></script>`
		},
	}

	// base = 默认主题全量 + 用户 layout/partials 覆盖：保证未覆盖槽位的
	// define（如 header/footer 片段被引用时）也存在于同一棵树。
	merged := map[string]string{}
	for name, src := range DefaultTheme {
		merged[name] = src
	}
	for name, src := range clean {
		merged[name] = src
	}

	t := template.New("site").Funcs(funcs)
	baseSrc := merged[SlotLayout] + "\n" + merged[SlotPartials]
	base, err := t.Parse(baseSrc)
	if err != nil {
		return nil, &CompileError{Problems: []Problem{{File: SlotLayout, Rule: "parse", Detail: err.Error()}}}
	}

	theme := &Theme{slots: map[string]*template.Template{}}
	for _, slot := range Slots {
		if slot == SlotLayout || slot == SlotPartials ||
			slot == SlotTokensCSS || slot == SlotThemeCSS {
			continue
		}
		src := clean[slot]
		if src == "" {
			src = DefaultTheme[slot]
		}
		clone, cloneErr := base.Clone()
		if cloneErr != nil {
			return nil, &CompileError{Problems: []Problem{{File: slot, Rule: "clone", Detail: cloneErr.Error()}}}
		}
		page, perr := clone.Parse(src)
		if perr != nil {
			return nil, &CompileError{Problems: []Problem{{File: slot, Rule: "parse", Detail: perr.Error()}}}
		}
		theme.slots[slot] = page
	}
	theme.rev = Fingerprint(clean)
	return theme, nil
}

// Render 执行槽位模板（经 layout 包裹）。
func (t *Theme) Render(sb interface{ Write([]byte) (int, error) }, slot string, vm any) error {
	t.mu.RLock()
	page := t.slots[slot]
	t.mu.RUnlock()
	if page == nil {
		return fmt.Errorf("theme: slot %q not compiled", slot)
	}
	return page.ExecuteTemplate(sb, "layout", vm)
}

// Revision 返回文件集指纹（缓存键）。
func (t *Theme) Revision() string { return t.rev }

func validSlotName(name string) bool {
	for _, slot := range Slots {
		if slot == name {
			return true
		}
	}
	return name == SlotTokensCSS || name == SlotThemeCSS
}
