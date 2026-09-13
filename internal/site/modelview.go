package site

// modelview.go — 公开字段白名单投影（站点方案 C4 后的读取半场）。
// 白名单本体（resourcemodel.ModelView）存模型版本并随 asset pin 冻结；
// 本文件只做投影：按白名单顺序输出 label 化的字段值，空白名单 = 零字段
// （fail-closed）。准入校验/上限见 internal/resourcemodel/publicview.go。

import (
	"encoding/json"

	"agentchunzhi/internal/resourcemodel"
)

// PublicFieldValue is one whitelisted field rendered for the public faces:
// ordered by the whitelist, typed by the version's frozen field_schema.
// Label 是字段的人读展示名（D17），缺省由渲染层回退 key。
type PublicFieldValue struct {
	Key   string          `json:"key"`
	Label string          `json:"label,omitempty"`
	Type  string          `json:"type"`
	Value json.RawMessage `json:"value"`
}

// WhitelistFields projects schema-declared fields onto the whitelist (无 label 版本).
func WhitelistFields(projected map[string]json.RawMessage, schemaTypes map[string]string, view resourcemodel.ModelView, card bool) []PublicFieldValue {
	return WhitelistFieldsWithLabels(projected, schemaTypes, nil, view, card)
}

// WhitelistFieldsWithLabels 是 WhitelistFields 的 label 版本（D17）。
func WhitelistFieldsWithLabels(projected map[string]json.RawMessage, schemaTypes map[string]string, labels map[string]string, view resourcemodel.ModelView, card bool) []PublicFieldValue {
	fields := view.DetailFields
	if card {
		fields = view.CardFields
	}
	if len(fields) == 0 {
		return []PublicFieldValue{}
	}
	out := make([]PublicFieldValue, 0, len(fields))
	for _, key := range fields {
		value, ok := projected[key]
		if !ok || len(value) == 0 || string(value) == "null" {
			continue
		}
		fieldType := schemaTypes[key]
		if fieldType == "" {
			continue
		}
		out = append(out, PublicFieldValue{Key: key, Label: labels[key], Type: fieldType, Value: value})
	}
	return out
}
