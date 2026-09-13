package site

// style.go — 主题化后仅存站点评论策略校验。样式体系已由主题模板取代
// （docs/站点主题化与AI设计重构-2026-09-13.md）；CSS 净化在 css.go。

// ValidCommentsMode reports a valid site comment policy (二期 §8).
func ValidCommentsMode(mode string) bool {
	switch mode {
	case "off", "moderated", "open":
		return true
	}
	return false
}
