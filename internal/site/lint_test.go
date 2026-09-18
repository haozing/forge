package site

import (
	"strings"
	"testing"
)

func TestLintDeadRefs(t *testing.T) {
	snap := lintSnapshot{
		Included: []lintIncluded{
			{AssetID: "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa", Slug: "a", Title: "A",
				Markdown: "见 chunzhi-asset://bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb 与 chunzhi-asset://aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"},
			{AssetID: "cccccccc-cccc-cccc-cccc-cccccccccccc", Slug: "c", Title: "C", Markdown: "干净正文"},
		},
	}
	findings := lintDeadRefs(snap)
	if len(findings) != 1 {
		t.Fatalf("want 1 dead ref, got %d: %+v", len(findings), findings)
	}
	f := findings[0]
	if f.Severity != LintError || f.ResourceID != "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa" {
		t.Fatalf("unexpected finding: %+v", f)
	}
	if !strings.Contains(f.Message, "bbbbbbbb") {
		t.Fatalf("finding should name the dead target: %s", f.Message)
	}
}

func TestLintStaleRedirects(t *testing.T) {
	snap := lintSnapshot{
		Included: []lintIncluded{{AssetID: "a", Slug: "live"}},
		Redirects: []lintRedirect{
			{FromPath: "old", ToPath: "live"},
			{FromPath: "older", ToPath: "old"},   // 链到另一条重定向：合法
			{FromPath: "broken", ToPath: "gone"}, // 落点无内容且无链：报
		},
	}
	findings := lintStaleRedirects(snap)
	if len(findings) != 1 {
		t.Fatalf("want 1 stale redirect, got %d", len(findings))
	}
	if findings[0].ResourceID != "broken" {
		t.Fatalf("wrong redirect flagged: %+v", findings[0])
	}
}

func TestLintOrphans(t *testing.T) {
	id := "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
	other := "cccccccc-cccc-cccc-cccc-cccccccccccc"
	snap := lintSnapshot{
		Included: []lintIncluded{
			{AssetID: id, Slug: "orphan", Title: "孤儿"},
			{AssetID: other, Slug: "linked", Title: "被引用",
				Markdown: "chunzhi-asset://" + other},
			{AssetID: "dddddddd-dddd-dddd-dddd-dddddddddddd", Slug: "featured", Title: "精选", Featured: true},
		},
	}
	findings := lintOrphans(snap)
	if len(findings) != 1 || findings[0].ResourceID != id {
		t.Fatalf("want only the true orphan flagged: %+v", findings)
	}
}

func TestLintEmptyCategories(t *testing.T) {
	snap := lintSnapshot{
		Included: []lintIncluded{{AssetID: "a", Slug: "a", CategoryID: "leaf1"}},
		Categories: []lintCategory{
			{ID: "root", ParentID: "", Slug: "root"},
			{ID: "leaf1", ParentID: "root", Slug: "leaf-1"},
			{ID: "leaf2", ParentID: "root", Slug: "leaf-2"},
			{ID: "lone", ParentID: "", Slug: "lone"},
		},
	}
	findings := lintEmptyCategories(snap)
	got := map[string]bool{}
	for _, f := range findings {
		got[f.ResourceID] = true
	}
	if got["root"] || got["leaf1"] {
		t.Fatalf("root is populated via leaf1: %+v", findings)
	}
	if !got["leaf2"] || !got["lone"] {
		t.Fatalf("empty leaves should be flagged: %+v", findings)
	}
}

func TestLintSEOBaseline(t *testing.T) {
	findings := lintSEOBaseline("", []lintPage{
		{ID: "p1", Slug: "about", SEODescription: "", BodyMarkdown: "# 大标题\n正文"},
	})
	checks := map[string]int{}
	for _, f := range findings {
		checks[f.Check]++
	}
	if checks["site_description_missing"] != 1 || checks["page_seo_description_missing"] != 1 || checks["page_body_h1"] != 1 {
		t.Fatalf("unexpected findings: %+v", findings)
	}
	// 干净站点零发现。
	if again := lintSEOBaseline("有描述", []lintPage{{ID: "p", Slug: "s", SEODescription: "d", BodyMarkdown: "## 二级"}}); len(again) != 0 {
		t.Fatalf("clean site should pass: %+v", again)
	}
}

func TestLintLocaleBreaks(t *testing.T) {
	group := "11111111-1111-1111-1111-111111111111"
	snap := lintSnapshot{
		EnabledLocales: []string{"zh", "en"},
		Included: []lintIncluded{
			{AssetID: "a", Slug: "a", Title: "中文版", Locale: "zh", TranslationGroupID: group},
			{AssetID: "b", Slug: "b", Title: "无组", Locale: "en"},
		},
	}
	findings := lintLocaleBreaks(snap)
	if len(findings) != 1 || !strings.Contains(findings[0].Message, "en") {
		t.Fatalf("want one missing-en finding: %+v", findings)
	}
	if single := lintLocaleBreaks(lintSnapshot{EnabledLocales: []string{"zh"}, Included: snap.Included}); len(single) != 0 {
		t.Fatalf("single-locale site skips check: %+v", single)
	}
}

func TestLintMissingAltAndRefIDs(t *testing.T) {
	snap := lintSnapshot{
		Included: []lintIncluded{
			{AssetID: "a", Slug: "a", Title: "A",
				Markdown: "![](chunzhi-media://eeeeeeee-eeee-eeee-eeee-eeeeeeeeeeee) ![描述](chunzhi-media://eeeeeeee-eeee-eeee-eeee-eeeeeeeeeeee)"},
		},
	}
	findings := lintMissingAlt(snap)
	if len(findings) != 1 || !strings.Contains(findings[0].Message, "1 张") {
		t.Fatalf("want 1 missing-alt finding: %+v", findings)
	}
	ids := lintRefIDs("x chunzhi-asset://aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa y chunzhi-asset://aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa")
	if len(ids) != 1 {
		t.Fatalf("ref ids should dedup: %v", ids)
	}
}

func TestLintFromSnapshotNeverNil(t *testing.T) {
	findings := lintFromSnapshot("", lintSnapshot{})
	if findings == nil {
		t.Fatal("findings must never be nil (nil serializes as null)")
	}
	if len(findings) != 1 { // 仅 site_description_missing
		t.Fatalf("empty site still has description warning: %+v", findings)
	}
}

func TestLintReportCounts(t *testing.T) {
	report := LintReport{SiteID: "s", Findings: []LintFinding{
		{Check: "a", Severity: LintError}, {Check: "b", Severity: LintWarn}, {Check: "c", Severity: LintWarn},
	}}
	counts := report.Counts()
	if counts["error"] != 1 || counts["warn"] != 2 || counts["info"] != 0 {
		t.Fatalf("bad counts: %v", counts)
	}
}
