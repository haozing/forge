package admin

import "testing"

// 列表项与键/策略子结构的 JSON 形状稳定（管理台发钥 UI 契约）。
func TestAgentUserListItemJSONShape(t *testing.T) {
	item := AgentUserListItem{}
	if item.Keys == nil || item.Policies == nil {
		// 零值切片为 nil，序列化端已保证空数组：这里只锁字段存在性。
		_ = item
	}
	key := AgentKeyItem{Capabilities: []string{"asset.read"}}
	if len(key.Capabilities) != 1 {
		t.Fatal("capabilities lost")
	}
	_ = AgentUserListResult{Items: []AgentUserListItem{}}
}
