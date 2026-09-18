package textutil

import (
	"strings"
	"testing"
)

func TestEscapeLike(t *testing.T) {
	cases := map[string]string{
		`100%`:       `100\%`,
		`a_b`:        `a\_b`,
		`back\slash`: `back\\slash`,
		`plain`:      `plain`,
	}
	for input, want := range cases {
		if got := EscapeLike(input); got != want {
			t.Fatalf("EscapeLike(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestExtractTerms(t *testing.T) {
	terms := ExtractTerms("SEO 关键词挖掘 Keyword Research 工具", 20)
	joined := strings.Join(terms, ",")
	for _, want := range []string{"seo", "keyword", "research", "关键", "词挖", "工具"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing term %q in %v", want, terms)
		}
	}
	// 截断生效且去重。
	if few := ExtractTerms("关键词 关键词挖掘", 2); len(few) != 2 {
		t.Fatalf("limit respected: %v", few)
	}
	if ExtractTerms("x", 0) != nil {
		t.Fatal("limit 0 yields nil")
	}
}

func TestRRFFusesAndRanks(t *testing.T) {
	lexical := []RankedList[string]{
		{ID: "a", Value: "a"}, {ID: "b", Value: "b"}, {ID: "c", Value: "c"},
	}
	semantic := []RankedList[string]{
		{ID: "b", Value: "b"}, {ID: "a", Value: "a"}, {ID: "d", Value: "d"},
	}
	fused := RRF(60, lexical, semantic)
	// a、b 两路都出现，应领先只出现一路的 c、d；a、b 总分并列时按输入顺序稳定保序。
	if len(fused) != 4 {
		t.Fatalf("want 4 fused, got %d", len(fused))
	}
	if fused[0].ID != "a" || fused[1].ID != "b" {
		t.Fatalf("top-2 = %s,%s want a,b", fused[0].ID, fused[1].ID)
	}
	// 单路出现者总分相同，按输入顺序稳定保序：c（词法路）在前。
	if fused[2].ID != "c" || fused[3].ID != "d" {
		t.Fatalf("single-appearance tail = %s,%s want c,d", fused[2].ID, fused[3].ID)
	}
	// 载荷取首次出现。
	if fused[0].Value != "a" {
		t.Fatalf("payload lost: %v", fused[0].Value)
	}
}

func TestFingerprintStableAndDistinct(t *testing.T) {
	a1 := Fingerprint("md", "内容一")
	a2 := Fingerprint("md", "内容一")
	b := Fingerprint("md", "内容二")
	if a1 != a2 {
		t.Fatal("same content must produce same fingerprint")
	}
	if a1 == b {
		t.Fatal("different content must differ")
	}
}
