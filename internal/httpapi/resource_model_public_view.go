package httpapi

// resource_model_public_view.go — 模型公开字段白名单（站点方案 C4/D5）。
// GET  返回当前白名单 + 候选字段清单（key/label/type + 准入判断）供向导渲染；
// PUT  整体替换白名单（model.manage 权限，D5：向导默认全关）。

import (
	"context"
	"encoding/json"
	"net/http"

	"agentchunzhi/internal/auth"
	"agentchunzhi/internal/resourcemodel"
)

type publicViewFieldDef struct {
	Key   string `json:"key"`
	Label string `json:"label,omitempty"`
	Type  string `json:"type"`
}

type publicViewCandidate struct {
	Key      string `json:"key"`
	Label    string `json:"label,omitempty"`
	Type     string `json:"type"`
	Card     bool   `json:"card"`
	Detail   bool   `json:"detail"`
	Selected bool   `json:"selected"`
}

func resourceModelPublicView(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		principal, ok := requireMemberSession(w, r, deps)
		if !ok {
			return
		}
		modelID := r.PathValue("resourceModelId")
		switch r.Method {
		case http.MethodGet:
			view, fields, err := loadPublicViewContext(r.Context(), deps, principal, modelID)
			if err != nil {
				writeResourceModelError(w, err, "resource_model_public_view_failed")
				return
			}
			candidates := buildPublicViewCandidates(fields, view)
			// nil slice 会序列化成 null（或省略），前端按数组消费——补空数组。
			cardFields := view.CardFields
			if cardFields == nil {
				cardFields = []string{}
			}
			detailFields := view.DetailFields
			if detailFields == nil {
				detailFields = []string{}
			}
			writeJSON(w, http.StatusOK, map[string]any{
				"card_fields":   cardFields,
				"detail_fields": detailFields,
				"candidates":    candidates,
			})
		case http.MethodPut:
			var input struct {
				CardFields   []string `json:"card_fields"`
				DetailFields []string `json:"detail_fields"`
			}
			decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64*1024))
			decoder.DisallowUnknownFields()
			if err := decoder.Decode(&input); err != nil {
				writeError(w, http.StatusUnprocessableEntity, "validation_failed")
				return
			}
			if _, err := deps.ResourceModelService.SetModelPublicView(r.Context(), principal, modelID, resourcemodel.ModelView{
				CardFields:   input.CardFields,
				DetailFields: input.DetailFields,
			}); err != nil {
				writeResourceModelError(w, err, "resource_model_public_view_failed")
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"ok": true})
		default:
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
		}
	}
}

// loadPublicViewContext resolves the view plus the model's field catalog
// (key/label/type) from the current version's field_schema.
func loadPublicViewContext(ctx context.Context, deps Dependencies, principal auth.Principal, modelID string) (resourcemodel.ModelView, []publicViewFieldDef, error) {
	view, err := deps.ResourceModelService.GetModelPublicView(ctx, principal, modelID)
	if err != nil {
		return resourcemodel.ModelView{}, nil, err
	}
	model, err := deps.ResourceModelService.Get(ctx, principal, modelID)
	if err != nil {
		return resourcemodel.ModelView{}, nil, err
	}
	var fields []publicViewFieldDef
	if model.CurrentVersion != nil {
		raw, _ := json.Marshal(model.CurrentVersion.FieldSchema)
		var schema struct {
			Fields []publicViewFieldDef `json:"fields"`
		}
		if err := json.Unmarshal(raw, &schema); err == nil {
			fields = schema.Fields
		}
	}
	return view, fields, nil
}

func buildPublicViewCandidates(fields []publicViewFieldDef, view resourcemodel.ModelView) []publicViewCandidate {
	selected := func(list []string, key string) bool {
		for _, item := range list {
			if item == key {
				return true
			}
		}
		return false
	}
	out := make([]publicViewCandidate, 0, len(fields))
	for _, field := range fields {
		out = append(out, publicViewCandidate{
			Key:      field.Key,
			Label:    field.Label,
			Type:     field.Type,
			Card:     isCardAdmitted(field.Type),
			Detail:   isDetailAdmitted(field.Type),
			Selected: selected(view.CardFields, field.Key) || selected(view.DetailFields, field.Key),
		})
	}
	return out
}

func isDetailAdmitted(fieldType string) bool {
	switch fieldType {
	case "string", "integer", "number", "boolean", "date", "datetime", "enum", "multiselect":
		return true
	}
	return false
}

func isCardAdmitted(fieldType string) bool {
	switch fieldType {
	case "string", "integer", "number", "boolean", "date", "enum":
		return true
	}
	return false
}
