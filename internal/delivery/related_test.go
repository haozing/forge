package delivery

import (
	"strings"
	"testing"

	"agentchunzhi/internal/site"
)

func TestRelatedTerms(t *testing.T) {
	terms := relatedTerms("云原生架构实践 cloud native 架构指南", 8)
	joined := strings.Join(terms, ",")
	for _, want := range []string{`"云原"`, `"原生"`, `"架构"`, `"cloud"`, `"native"`} {
		if !strings.Contains(joined, want) {
			t.Errorf("relatedTerms missing %s in %v", want, terms)
		}
	}
	if len(relatedTerms("!", 8)) != 0 {
		t.Error("noise-only text should yield no terms")
	}
	// Distinct terms beyond the cap are dropped; repeated runs fold.
	if got := relatedTerms("架构 设计 模式 服务 检索 索引 发布 订阅 缓存 队列", 8); len(got) != 8 {
		t.Errorf("term cap not enforced: %d terms", len(got))
	}
	if got := relatedTerms("同词同词同词", 8); len(got) != 2 {
		t.Errorf("repeated bigrams fold to their alternation (同词/词同), got %v", got)
	}
}

func TestRelatedQueryEscapesQuotes(t *testing.T) {
	content := site.PublicPostContent{Title: `Ti"tle 用"引号"测试`, Summary: "摘要"}
	query := relatedQuery(content)
	// The quote acts as a separator, so every emitted term is a quoted atom
	// without embedded quotes — no way to break out of the PGroonga string.
	for _, atom := range strings.Split(query, " OR ") {
		if !strings.HasPrefix(atom, `"`) || !strings.HasSuffix(atom, `"`) || strings.Count(atom, `"`) != 2 {
			t.Errorf("malformed PGroonga atom %q in %q", atom, query)
		}
	}
	if content := (site.PublicPostContent{Title: "  ", Summary: ""}); relatedQuery(content) != "" {
		t.Error("empty content must yield an empty query (tier 1 skipped)")
	}
}
