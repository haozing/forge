package site

// modelview.go — the per-model field display whitelist (CMS plan §7.2
// model_views: card_fields / detail_fields). The whitelist decides which
// structured fields of a bound asset's published version a site publishes;
// an empty or absent view publishes zero fields (fail-closed — CMS plan
// §8.4: fields ride the release whitelist, never raw).
//
// Admission is scalar-only by design: markdown/object/array/asset_reference
// values have no safe one-line presentation and stay out of v1.

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// ModelView is one model's field display whitelist. Array order is the
// render order.
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

// ModelViewError carries the offending model/field so the PATCH handler can
// answer 422 with actionable details instead of a bare validation_failed.
type ModelViewError struct {
	ModelID string `json:"model_id"`
	Field   string `json:"field,omitempty"`
	Reason  string `json:"reason"`
}

func (e *ModelViewError) Error() string {
	if e.Field != "" {
		return fmt.Sprintf("model %s field %s: %s", e.ModelID, e.Field, e.Reason)
	}
	return fmt.Sprintf("model %s: %s", e.ModelID, e.Reason)
}

// ValidateModelViewShape enforces the pure structural rules: known keys,
// deduplicated fields, per-list limits. Reference checks (model ownership,
// field existence, type admission) live in ValidateModelViewReferences.
func ValidateModelViewShape(views map[string]ModelView) *ModelViewError {
	for modelID, view := range views {
		if !validID(modelID) {
			return &ModelViewError{ModelID: modelID, Reason: "key must be a resource model uuid"}
		}
		if err := validateFieldList(view.CardFields, maxCardFields, modelID, "card_fields"); err != nil {
			return err
		}
		if err := validateFieldList(view.DetailFields, maxDetailFields, modelID, "detail_fields"); err != nil {
			return err
		}
	}
	return nil
}

func validateFieldList(fields []string, max int, modelID, list string) *ModelViewError {
	if len(fields) > max {
		return &ModelViewError{ModelID: modelID, Field: list, Reason: fmt.Sprintf("at most %d fields", max)}
	}
	seen := map[string]bool{}
	for _, field := range fields {
		if field == "" {
			return &ModelViewError{ModelID: modelID, Field: list, Reason: "empty field key"}
		}
		if seen[field] {
			return &ModelViewError{ModelID: modelID, Field: field, Reason: "duplicate field in " + list}
		}
		seen[field] = true
	}
	return nil
}

// ValidateModelViewReferences checks every model key against this
// workspace's active resource models and every field against the model's
// current published field_schema, admitting scalar types only. Runs inside
// the caller's transaction.
func ValidateModelViewReferences(ctx context.Context, tx pgx.Tx, organizationID, workspaceID string, views map[string]ModelView) *ModelViewError {
	if len(views) == 0 {
		return nil
	}
	ids := make([]string, 0, len(views))
	for modelID := range views {
		ids = append(ids, modelID)
	}
	rows, err := tx.Query(ctx, `
		SELECT rm.id::text, mv.field_schema
		FROM model.resource_models rm
		JOIN model.resource_model_versions mv ON mv.id = rm.current_version_id
		WHERE rm.organization_id = $1::uuid
		  AND (rm.workspace_id = NULLIF($2, '')::uuid OR rm.workspace_id IS NULL)
		  AND rm.status = 'active' AND rm.id::text = ANY($3::text[])
	`, organizationID, workspaceID, ids)
	if err != nil {
		return &ModelViewError{Reason: "load resource models failed"}
	}
	defer rows.Close()
	schemas := map[string]map[string]string{}
	for rows.Next() {
		var modelID string
		var schema json.RawMessage
		if err := rows.Scan(&modelID, &schema); err != nil {
			return &ModelViewError{Reason: "load resource models failed"}
		}
		schemas[modelID] = ParseFieldSchema(schema)
	}
	if err := rows.Err(); err != nil {
		return &ModelViewError{Reason: "load resource models failed"}
	}
	for modelID, view := range views {
		types, ok := schemas[modelID]
		if !ok {
			return &ModelViewError{ModelID: modelID, Reason: "unknown or inactive resource model"}
		}
		if err := checkFieldList(view.CardFields, types, cardFieldTypes, modelID, "card_fields"); err != nil {
			return err
		}
		if err := checkFieldList(view.DetailFields, types, detailFieldTypes, modelID, "detail_fields"); err != nil {
			return err
		}
	}
	return nil
}

func checkFieldList(fields []string, schemaTypes map[string]string, admitted map[string]bool, modelID, list string) *ModelViewError {
	for _, field := range fields {
		fieldType, exists := schemaTypes[field]
		if !exists {
			return &ModelViewError{ModelID: modelID, Field: field, Reason: "unknown field in " + list}
		}
		if !admitted[fieldType] {
			return &ModelViewError{ModelID: modelID, Field: field, Reason: fmt.Sprintf("type %s is not presentable in %s", fieldType, list)}
		}
	}
	return nil
}

// WhitelistFor returns the view for one model; absent models publish zero
// fields.
func WhitelistFor(views map[string]ModelView, modelID string) ModelView {
	return views[modelID]
}

// PublicFieldValue is one whitelisted field rendered for the public faces:
// ordered by the whitelist, typed by the version's frozen field_schema.
type PublicFieldValue struct {
	Key   string          `json:"key"`
	Type  string          `json:"type"`
	Value json.RawMessage `json:"value"`
}

// WhitelistFields projects schema-declared fields onto the whitelist: output
// follows whitelist order, skips keys the version does not carry, and is
// empty when the whitelist is empty (fail-closed).
func WhitelistFields(projected map[string]json.RawMessage, schemaTypes map[string]string, view ModelView, card bool) []PublicFieldValue {
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
		out = append(out, PublicFieldValue{Key: key, Type: fieldType, Value: value})
	}
	return out
}

// PruneModelViews drops whitelist entries that no longer exist in the
// models' current field_schema (model versions move between write and
// publish). Returns the pruned copy and how many field entries were dropped;
// publishing records that count in the release audit.
func PruneModelViews(views map[string]ModelView, schemas map[string]map[string]string) (map[string]ModelView, int) {
	pruned := 0
	out := make(map[string]ModelView, len(views))
	for modelID, view := range views {
		types, ok := schemas[modelID]
		if !ok {
			pruned += len(view.CardFields) + len(view.DetailFields)
			continue // model gone/inactive: publish zero fields for it
		}
		next := ModelView{
			CardFields:   pruneFieldList(view.CardFields, types, &pruned),
			DetailFields: pruneFieldList(view.DetailFields, types, &pruned),
		}
		if len(next.CardFields) > 0 || len(next.DetailFields) > 0 {
			out[modelID] = next
		}
	}
	return out, pruned
}

func pruneFieldList(fields []string, types map[string]string, pruned *int) []string {
	kept := make([]string, 0, len(fields))
	for _, field := range fields {
		if _, exists := types[field]; exists {
			kept = append(kept, field)
		} else {
			*pruned++
		}
	}
	return kept
}

// LoadModelSchemasForPrune loads the current published field_schema per model
// id for PruneModelViews. Runs inside the release transaction.
func LoadModelSchemasForPrune(ctx context.Context, tx pgx.Tx, organizationID string, views map[string]ModelView) (map[string]map[string]string, error) {
	if len(views) == 0 {
		return map[string]map[string]string{}, nil
	}
	ids := make([]string, 0, len(views))
	for modelID := range views {
		ids = append(ids, modelID)
	}
	rows, err := tx.Query(ctx, `
		SELECT rm.id::text, mv.field_schema
		FROM model.resource_models rm
		JOIN model.resource_model_versions mv ON mv.id = rm.current_version_id
		WHERE rm.organization_id = $1::uuid AND rm.id::text = ANY($2::text[])
		  AND rm.status = 'active'
	`, organizationID, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	schemas := map[string]map[string]string{}
	for rows.Next() {
		var modelID string
		var schema json.RawMessage
		if err := rows.Scan(&modelID, &schema); err != nil {
			return nil, err
		}
		schemas[modelID] = ParseFieldSchema(schema)
	}
	return schemas, rows.Err()
}
