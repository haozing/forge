package theme

import (
	"embed"
	"fmt"
	"io/fs"
	"strings"
)

//go:embed defaults/*.tmpl defaults/*.css
var defaultsFS embed.FS

// DefaultTheme 是内置默认主题：缺失槽位的回退与新建站点的起点。
// 它是可信基线，不参与安全扫描（§5.3）。
var DefaultTheme = loadDefaults()

func loadDefaults() map[string]string {
	out := make(map[string]string)
	entries, err := defaultsFS.ReadDir("defaults")
	if err != nil {
		// embed 保证非空；防御式兜底为空集（全部回退到布局缺失报错）。
		return out
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		src, readErr := fs.ReadFile(defaultsFS, "defaults/"+name)
		if readErr != nil {
			continue
		}
		// 槽位键不含 .tmpl 扩展名；CSS 保留全名（tokens.css / theme.css）。
		name = strings.TrimSuffix(name, ".tmpl")
		out[name] = string(src)
	}
	// layout 与 partials 必须存在，否则 Compile 无法建树。
	if _, ok := out[SlotLayout]; !ok {
		panic("theme: defaults missing layout")
	}
	if _, ok := out[SlotPartials]; !ok {
		panic("theme: defaults missing partials")
	}
	_ = fmt.Sprintf
	return out
}
