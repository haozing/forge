package resourcemodel

// publicview.go — 字段公开白名单（站点方案 C4/D5，2026-09-12）。
// 白名单归属从站点（原 site.model_views）下沉到模型版本：随
// asset_versions.resource_model_version_id 的 pin 语义天然冻结。准入规则
// （标量类型、卡片 4 / 详情 12）与原 site.modelview 一致，仅换了归属地。

import (
	"context"
	"encoding/json"
	"fmt"

	"agentchunzhi/internal/auth"
)

// ModelView is one model's public field whitelist. Array order is render order.
type ModelView struct {
	CardFields   []string `json:"card_fields,omitempty"`
	DetailFields []string `json:"detail_fields,omitempty"`
}

const (
	maxCardFields   = 4
	maxDetailFields = 12
)

// detailFieldTypes are the scalar types presentable on the detail page.
var detailFieldTypes = map[string]bool{
	"string": true, "integer": true, "number": true, "boolean": true,
	"date": true, "datetime": true, "enum": true, "multiselect": true,
}

// cardFieldTypes are the scalar types presentable in the one-line card meta.
var cardFieldTypes = map[string]bool{
	"string": true, "integer": true, "number": true, "boolean": true,
	"date": true, "enum": true,
}

// PublicViewError carries the offending field/list so the caller can answer
// 422 with actionable details.
type PublicViewError struct {
	Field  string `json:"field,omitempty"`
	List   string `json:"list,omitempty"`
	Reason string `json:"reason"`
}

func (e *PublicViewError) Error() string {
	if e.Field != "" {
		return fmt.Sprintf("field %s (%s): %s", e.Field, e.List, e.Reason)
	}
	if e.List != "" {
		return fmt.Sprintf("%s: %s", e.List, e.Reason)
	}
	return e.Reason
}

// ValidatePublicViewShape enforces structure: lists ≤ 4/12, no dupes/empties.
func ValidatePublicViewShape(view ModelView) *PublicViewError {
	if err := validatePublicList(view.CardFields, maxCardFields, "card_fields"); err != nil {
		return err
	}
	return validatePublicList(view.DetailFields, maxDetailFields, "detail_fields")
}

func validatePublicList(fields []string, max int, list string) *PublicViewError {
	if len(fields) > max {
		return &PublicViewError{List: list, Reason: fmt.Sprintf("at most %d fields", max)}
	}
	seen := map[string]bool{}
	for _, field := range fields {
		if field == "" {
			return &PublicViewError{List: list, Reason: "empty field key"}
		}
		if seen[field] {
			return &PublicViewError{Field: field, List: list, Reason: "duplicate field in " + list}
		}
		seen[field] = true
	}
	return nil
}

// DecodePublicView parses the stored JSON document; malformed documents
// decode as empty (fail-closed).
func DecodePublicView(raw []byte) ModelView {
	view := ModelView{}
	if len(raw) == 0 {
		return view
	}
	_ = json.Unmarshal(raw, &view)
	return view
}

// SetModelPublicView replaces the public field whitelist on the model's
// current version (draft or published — the row is the freeze point for
// assets pinned to it). Validation: structure + field existence + scalar
// type admission against the model's own field_schema. 管理权 =
// model.manage（与模型设计同权；发布内容始终是人）。
func (s Service) SetModelPublicView(ctx context.Context, principal auth.Principal, modelID string, view ModelView) (Model, error) {
	if !validID(modelID) {
		return Model{}, ErrInvalidInput
	}
	model, err := s.Get(ctx, principal, modelID)
	if err != nil {
		return Model{}, err
	}
	if err := s.requireModelAction(ctx, principal, model, "model.manage"); err != nil {
		return Model{}, err
	}
	if err := ValidatePublicViewShape(view); err != nil {
		return Model{}, err
	}
	if s.Store == nil || s.Store.Pool == nil {
		return Model{}, ErrInvalidInput
	}
	var fieldSchema []byte
	err = s.Store.Pool.QueryRow(ctx, `
		SELECT field_schema FROM model.resource_model_versions
		WHERE organization_id = $1::uuid AND id = $2::uuid
	`, principal.OrganizationID, model.CurrentVersion.ID).Scan(&fieldSchema)
	if err != nil {
		return Model{}, fmt.Errorf("load model field schema: %w", err)
	}
	if err := validatePublicViewReferences(view, ParseFieldSchemaTypes(fieldSchema)); err != nil {
		return Model{}, err
	}
	raw, err := json.Marshal(view)
	if err != nil {
		return Model{}, err
	}
	if _, err := s.Store.Pool.Exec(ctx, `
		UPDATE model.resource_model_versions SET public_view = $3::jsonb
		WHERE organization_id = $1::uuid AND id = $2::uuid
	`, principal.OrganizationID, model.CurrentVersion.ID, raw); err != nil {
		return Model{}, fmt.Errorf("update model public view: %w", err)
	}
	return s.Get(ctx, principal, modelID)
}

// GetModelPublicView reads the current whitelist document of one model.
func (s Service) GetModelPublicView(ctx context.Context, principal auth.Principal, modelID string) (ModelView, error) {
	model, err := s.Get(ctx, principal, modelID)
	if err != nil {
		return ModelView{}, err
	}
	if err := s.requireModelAction(ctx, principal, model, "model.read"); err != nil {
		return ModelView{}, err
	}
	if s.Store == nil || s.Store.Pool == nil {
		return ModelView{}, ErrInvalidInput
	}
	var raw []byte
	err = s.Store.Pool.QueryRow(ctx, `
		SELECT public_view FROM model.resource_model_versions
		WHERE organization_id = $1::uuid AND id = $2::uuid
	`, principal.OrganizationID, model.CurrentVersion.ID).Scan(&raw)
	if err != nil {
		return ModelView{}, fmt.Errorf("load model public view: %w", err)
	}
	return DecodePublicView(raw), nil
}

func validatePublicViewReferences(view ModelView, schemaTypes map[string]string) *PublicViewError {
	if err := checkPublicList(view.CardFields, schemaTypes, cardFieldTypes, "card_fields"); err != nil {
		return err
	}
	return checkPublicList(view.DetailFields, schemaTypes, detailFieldTypes, "detail_fields")
}

func checkPublicList(fields []string, schemaTypes map[string]string, admitted map[string]bool, list string) *PublicViewError {
	for _, field := range fields {
		fieldType, exists := schemaTypes[field]
		if !exists {
			return &PublicViewError{Field: field, List: list, Reason: "unknown field"}
		}
		if !admitted[fieldType] {
			return &PublicViewError{Field: field, List: list, Reason: "type " + fieldType + " is not presentable in " + list}
		}
	}
	return nil
}

// ParseFieldSchemaTypes decodes {"fields":[{key,type...}]} into key → type.

// ParseFieldSchemaTypes decodes {"fields":[{key,type...}]} into key -> type.
func ParseFieldSchemaTypes(raw []byte) map[string]string {
	out := map[string]string{}
	if len(raw) == 0 {
		return out
	}
	var schema struct {
		Fields []struct {
			Key  string `json:"key"`
			Type string `json:"type"`
		} `json:"fields"`
	}
	if err := json.Unmarshal(raw, &schema); err != nil {
		return out
	}
	for _, field := range schema.Fields {
		if field.Key != "" {
			out[field.Key] = field.Type
		}
	}
	return out
}
