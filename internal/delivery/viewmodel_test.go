package delivery

import (
	"testing"

	"agentchunzhi/internal/site"
)

// 主题化后的 HomeVM 语义：单一"最新内容"卡片流 + 标签云（设计面在主题）。
func TestResolveHomeProjectsLatestStream(t *testing.T) {
	view := site.PublicHomeView{
		Site: site.PublicSiteInfo{Slug: "demo"},
		Sections: []site.PublicSection{{
			Type:  site.HomepageSectionLatest,
			Title: "最新内容",
			Items: []site.PublicPost{
				{Title: "Hello World", DisplayPath: "hello", Summary: "First post"},
				{Title: "Second", DisplayPath: "second", Summary: "Another"},
			},
		}},
	}
	vm := ResolveHome(view, nil)
	if len(vm.Items) != 2 {
		t.Fatalf("items = %d, want 2", len(vm.Items))
	}
	if vm.Items[0].Title != "Hello World" {
		t.Fatalf("first item = %q", vm.Items[0].Title)
	}
	if vm.Items[0].Href != "/sites/demo/posts/hello" {
		t.Fatalf("href = %q", vm.Items[0].Href)
	}
	if vm.ShowTagCloud {
		t.Fatal("no tag facet must not show cloud")
	}
}
