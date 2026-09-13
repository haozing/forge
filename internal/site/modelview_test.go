package site

// modelview_test.go — 公开字段白名单投影契约：fail-closed、白名单顺序、
// label 输出（D17）。白名单本体的准入校验在 resourcemodel 包。

import (
	"encoding/json"
	"testing"

	"agentchunzhi/internal/resourcemodel"
)

func TestWhitelistFieldsIsFailClosedAndOrdered(t *testing.T) {
	schemaTypes := map[string]string{"shot_size": "enum", "lens_mm": "integer", "shot_date": "date", "secret": "string"}
	labels := map[string]string{"shot_size": "景别", "lens_mm": "焦距"}
	projected := map[string]json.RawMessage{
		"shot_size": json.RawMessage(`"特写"`),
		"lens_mm":   json.RawMessage(`35`),
		"shot_date": json.RawMessage(`"2026-09-10"`),
		"secret":    json.RawMessage(`"do-not-publish"`),
		"ghost":     json.RawMessage(`1`),
	}
	view := resourcemodel.ModelView{
		CardFields:   []string{"shot_size", "lens_mm"},
		DetailFields: []string{"shot_size", "lens_mm", "shot_date"},
	}

	detail := WhitelistFieldsWithLabels(projected, schemaTypes, labels, view, false)
	if len(detail) != 3 {
		t.Fatalf("detail whitelist must keep 3 whitelisted+present values, got %d", len(detail))
	}
	if detail[0].Key != "shot_size" || detail[0].Label != "景别" {
		t.Fatalf("order/label broken: %+v", detail[0])
	}
	if detail[2].Key != "shot_date" {
		t.Fatalf("shot_date must survive (admitted scalar), got %+v", detail[2])
	}

	empty := WhitelistFields(projected, schemaTypes, resourcemodel.ModelView{}, false)
	if len(empty) != 0 {
		t.Fatalf("fail-closed: empty whitelist must publish zero fields, got %d", len(empty))
	}
}
