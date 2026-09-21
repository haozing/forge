package delivery

// 临时性能定位测试（勿提交）：读 slow_md.md 逐阶段计时。
// 运行：go test ./internal/delivery/ -run TestScratchSlowMD -v

import (
	"os"
	"testing"
	"time"
)

func TestScratchSlowMD(t *testing.T) {
	src, err := os.ReadFile("../../tmp_deploy/slow_md.md")
	if err != nil {
		t.Skip("no sample file")
	}
	source := string(src)
	t.Logf("markdown bytes: %d", len(source))

	step := func(name string, fn func()) {
		t0 := time.Now()
		fn()
		t.Logf("%-22s %v", name, time.Since(t0))
	}

	refs := map[string]AssetRefView{}
	var applied string
	step("applyAssetRefs", func() { applied = applyAssetRefs(source, refs) })

	var html string
	var result MarkdownResult
	step("Parse+Walk+Render+Sanitize", func() {
		result = RenderSiteMarkdown(applied, "x", map[string]bool{})
		html = result.HTML
	})
	t.Logf("html bytes: %d", len(html))

	var excerpt string
	step("PlainTextExcerpt220", func() { excerpt = PlainTextExcerpt(applied, 220) })
	t.Logf("excerpt: %q", excerpt[:min(60, len(excerpt))])

	step("SanitizeAgain(html)", func() { markdownPolicy.Sanitize(html) })
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
