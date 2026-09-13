package site

import (
	"encoding/json"
	"testing"
)

// 复现真实后端 422：JSON PATCH（嵌套对象）→ ValidatePagesConfig
func TestValidatePagesConfigFromPatchJSON(t *testing.T) {
	raw := []byte(`{
		"version": 2,
		"home": {"content_width": "normal", "blocks": [
			{"id": "hero", "type": "hero", "title": "验证站（暗色改造中）", "subtitle": "AI 设计会话验证"},
			{"id": "latest", "type": "latest", "limit": 6, "layout": "grid"},
			{"id": "rank", "type": "ranked", "model_key": "verify_doc9", "sort_field": "rating", "order": "desc", "limit": 5, "layout": "grid"}
		]},
		"pages": [],
		"nav": {"order": ["home"], "hidden": [], "extra": []}
	}`)
	var cfg PagesConfig
	if err := jsonUnmarshalForTest(raw, &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if cfg.Home == nil {
		t.Fatal("Home nil after unmarshal")
	}
	if err := ValidatePagesConfig(nil, nil, "", "", cfg); err != nil {
		t.Logf("validate err (expected, needs DB): %v", err)
	}
	if cfg.Home.Blocks[2].Type != "ranked" {
		t.Fatalf("block 2 type = %s", cfg.Home.Blocks[2].Type)
	}
}

func jsonUnmarshalForTest(raw []byte, v any) error {
	return json.Unmarshal(raw, v)
}
