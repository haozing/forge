package delivery

// external_links_test.go — 模型公开目录页（外链板块 v2）的纯函数回归：
// 配置解析、分类分组、提交者站点去重、nofollow 开关。

import (
	"strings"
	"testing"
	"time"

	"agentchunzhi/internal/site"
)

func TestDirectoryConfigFor(t *testing.T) {
	facts := site.SiteFacts{Site: site.Site{DirectoryConfig: []byte(`[
		{"slug":"other","model_key":"x"},
		{"slug":"external-links","model_key":"external_site","page_size":0,
		 "title":"T","intro":"I","submission_enabled":true,"outlink_nofollow":true}
	]`)}}
	cfg, ok := directoryConfigFor(facts, "external-links")
	if !ok {
		t.Fatal("expected config to resolve")
	}
	if cfg.ModelKey != "external_site" || cfg.PageSize != 20 || !cfg.SubmissionEnabled || !cfg.OutlinkNofollow {
		t.Fatalf("unexpected config: %+v", cfg)
	}
	if _, ok := directoryConfigFor(facts, "missing"); ok {
		t.Fatal("missing slug must not resolve")
	}
	empty := site.SiteFacts{}
	if _, ok := directoryConfigFor(empty, "external-links"); ok {
		t.Fatal("empty config must not resolve")
	}
}

func TestDirectoryCategoryValid(t *testing.T) {
	for key := range directoryCategoryLabels {
		if !DirectoryCategoryValid(key) {
			t.Fatalf("expected %q valid", key)
		}
	}
	for _, invalid := range []string{"", "excel", "SEO-Tools", "../etc"} {
		if DirectoryCategoryValid(invalid) {
			t.Fatalf("expected %q invalid", invalid)
		}
	}
}

func TestGroupExternalEntries(t *testing.T) {
	records := []site.DirectoryRecord{
		{Title: "B Tool", Category: "monetization", URL: "https://b.example"},
		{Title: "A Tool", Category: "seo-tools", URL: "https://a.example"},
		{Title: "A Tool 2", Category: "seo-tools", URL: "https://a2.example"},
		{Title: "No Category", URL: "https://x.example"},
	}
	groups := groupExternalEntries(records, false)
	if len(groups) != 2 {
		t.Fatalf("expected 2 groups, got %d", len(groups))
	}
	if groups[0].Key != "seo-tools" || groups[0].Label != "SEO & Keyword Tools" || len(groups[0].Items) != 2 {
		t.Fatalf("unexpected first group: %+v", groups[0])
	}
	if groups[1].Key != "monetization" {
		t.Fatalf("unexpected second group: %+v", groups[1])
	}
}

func TestDedupeSubmitterSites(t *testing.T) {
	records := []site.DirectoryRecord{
		{SubmitterSiteName: "Old", SubmitterSiteURL: "https://dup.example"},
		{SubmitterSiteName: "Keep", SubmitterSiteURL: "https://keep.example"},
		{SubmitterSiteName: "Dup Again", SubmitterSiteURL: "https://dup.example"},
		{SubmitterSiteName: "No URL"},
	}
	items := dedupeSubmitterSites(records, true)
	if len(items) != 2 {
		t.Fatalf("expected 2 deduped entries, got %d", len(items))
	}
	if items[0].URL != "https://dup.example" || items[0].Rel != "nofollow ugc" {
		t.Fatalf("unexpected first item: %+v", items[0])
	}
	if items[1].Rel != "nofollow ugc" {
		t.Fatalf("switch must apply to community picks: %+v", items[1])
	}
	if dedupe := dedupeSubmitterSites(records, false); dedupe[0].Rel != "" {
		t.Fatalf("switch off must render dofollow: %+v", dedupe[0])
	}
}

func TestExternalLinkEntryRel(t *testing.T) {
	rec := site.DirectoryRecord{Title: "T", URL: "https://x.example", AgentSkill: strings.Repeat("s", 10), UpdatedAt: time.Now()}
	if entry := externalLinkEntry(rec, true); entry.Rel != "nofollow ugc" {
		t.Fatalf("expected nofollow ugc, got %q", entry.Rel)
	}
	if entry := externalLinkEntry(rec, false); entry.Rel != "" {
		t.Fatalf("expected dofollow (empty rel), got %q", entry.Rel)
	}
}
