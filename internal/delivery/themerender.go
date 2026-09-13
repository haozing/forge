package delivery

// themerender.go — 主题页面渲染桥：按站点加载已发布主题文件集，经
// internal/theme 引擎编译（带进程内缓存）并渲染 HTML。主题是数据
// （site.site_theme_revisions.files），这里不含任何页面设计。

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sync"

	"agentchunzhi/internal/theme"
)

// themeCacheEntry 是编译缓存条目。
type themeCacheEntry struct {
	theme *theme.Theme
}

var themeMu sync.Mutex
var themeCache = map[string]*themeCacheEntry{}

// loadThemeFiles 读站点已发布主题文件集（SiteFacts.ThemeFiles 已带）。
func parseThemeFiles(raw json.RawMessage) map[string]string {
	out := map[string]string{}
	if len(raw) == 0 {
		return out
	}
	var m map[string]string
	if err := json.Unmarshal(raw, &m); err != nil {
		return out
	}
	for k, v := range m {
		out[k] = v
	}
	return out
}

// themeFor 编译（或命中缓存）站点主题。cacheKey 须随修订版变化。
func themeFor(cacheKey string, files map[string]string, siteSlug, baseAbsURL string, query theme.QueryFunc) (*theme.Theme, error) {
	themeMu.Lock()
	defer themeMu.Unlock()
	if entry, ok := themeCache[cacheKey]; ok {
		return entry.theme, nil
	}
	compiled, err := theme.Compile(files, theme.Options{
		SiteSlug:   siteSlug,
		BaseAbsURL: baseAbsURL,
		Query:      query,
	})
	if err != nil {
		return nil, err
	}
	themeCache[cacheKey] = &themeCacheEntry{theme: compiled}
	if len(themeCache) > 512 { // 简单防胀：超出即全清（低频发布场景可接受）
		themeCache = map[string]*themeCacheEntry{}
		themeCache[cacheKey] = &themeCacheEntry{theme: compiled}
	}
	return compiled, nil
}

// renderThemed 渲染一个主题槽位为 HTML 字节流。
// cacheKey 必须随（站点, 修订版/指纹）变化；queries 是本渲染的预算环境。
func renderThemed(files json.RawMessage, cacheKey, siteSlug, kind string, vm any, queries *theme.Queries, baseAbsURL string) ([]byte, error) {
	compiled, err := themeFor(cacheKey, parseThemeFiles(files), siteSlug, baseAbsURL, queries.QueryFor)
	if err != nil {
		return nil, fmt.Errorf("delivery: theme compile: %w", err)
	}
	var buffer bytes.Buffer
	if err := compiled.Render(&buffer, kind, vm); err != nil {
		return nil, fmt.Errorf("delivery: render themed page %s: %w", kind, err)
	}
	return buffer.Bytes(), nil
}
