package query

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

type fieldPredicate struct {
	Field    string `json:"field"`
	Operator string `json:"operator"`
	Value    any    `json:"value"`
}

type filterPlan struct {
	Fields []fieldPredicate
}

func parseFilterPlan(filters map[string]any) (filterPlan, error) {
	plan := filterPlan{Fields: []fieldPredicate{}}
	for key := range filters {
		if key != "fields" {
			return filterPlan{}, fmt.Errorf("%w: unsupported filter %q", ErrInvalidQuery, key)
		}
	}
	if rawFields, ok := filters["fields"]; ok {
		fields, ok := rawFields.(map[string]any)
		if !ok || len(fields) > 20 {
			return filterPlan{}, fmt.Errorf("%w: fields must contain at most 20 fields", ErrInvalidQuery)
		}
		predicates, err := parsePredicates(fields)
		if err != nil {
			return filterPlan{}, err
		}
		plan.Fields = predicates
	}
	return plan, nil
}

func parsePredicates(fields map[string]any) ([]fieldPredicate, error) {
	names := make([]string, 0, len(fields))
	for name := range fields {
		names = append(names, name)
	}
	sort.Strings(names)
	result := make([]fieldPredicate, 0)
	for _, name := range names {
		if strings.TrimSpace(name) == "" || len(name) > 100 {
			return nil, fmt.Errorf("%w: invalid filter field", ErrInvalidQuery)
		}
		operations, ok := fields[name].(map[string]any)
		if !ok || len(operations) == 0 || len(operations) > 8 {
			return nil, fmt.Errorf("%w: field %q must have 1-8 operators", ErrInvalidQuery, name)
		}
		opNames := make([]string, 0, len(operations))
		for operator := range operations {
			opNames = append(opNames, operator)
		}
		sort.Strings(opNames)
		for _, operator := range opNames {
			value := operations[operator]
			if !validFilterOperator(operator) {
				return nil, fmt.Errorf("%w: unsupported operator %q", ErrInvalidQuery, operator)
			}
			if operator == "in" || operator == "contains_any" {
				values, ok := value.([]any)
				if !ok || len(values) == 0 || len(values) > 100 {
					return nil, fmt.Errorf("%w: %s requires 1-100 values", ErrInvalidQuery, operator)
				}
			}
			if (operator == "gte" || operator == "lte") && !isJSONNumber(value) && !isJSONString(value) {
				return nil, fmt.Errorf("%w: %s requires a number or string", ErrInvalidQuery, operator)
			}
			if operator == "exists" {
				if _, ok := value.(bool); !ok {
					return nil, fmt.Errorf("%w: exists requires a boolean", ErrInvalidQuery)
				}
			}
			result = append(result, fieldPredicate{Field: name, Operator: operator, Value: value})
		}
	}
	if len(result) > 40 {
		return nil, fmt.Errorf("%w: at most 40 predicates are allowed", ErrInvalidQuery)
	}
	return result, nil
}

func validFilterOperator(value string) bool {
	switch value {
	case "eq", "neq", "in", "contains", "contains_any", "gte", "lte", "exists":
		return true
	default:
		return false
	}
}

func (p filterPlan) fieldsJSON() string {
	encoded, _ := json.Marshal(p.Fields)
	return string(encoded)
}

func isJSONNumber(value any) bool {
	switch value.(type) {
	case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64, float32, float64, json.Number:
		return true
	default:
		return false
	}
}

func isJSONString(value any) bool {
	_, ok := value.(string)
	return ok
}

func (s Service) validateFilterFields(ctx context.Context, organizationID string, models []string, plan filterPlan) error {
	for _, predicate := range plan.Fields {
		var allowed bool
		if err := s.Store.Pool.QueryRow(ctx, `
			SELECT count(*) = cardinality($2::uuid[])
			   AND COALESCE(bool_and(EXISTS (
			       SELECT 1 FROM jsonb_array_elements(COALESCE(mv.field_schema->'fields', '[]'::jsonb)) field
			       WHERE field->>'key' = $3
			   )), false)
			FROM model.resource_models rm
			JOIN model.resource_model_versions mv ON mv.organization_id = rm.organization_id AND mv.id = rm.current_version_id
			WHERE rm.organization_id = $1::uuid AND rm.id = ANY($2::uuid[])
		`, organizationID, models, predicate.Field).Scan(&allowed); err != nil {
			return fmt.Errorf("validate filter schema: %w", err)
		}
		if !allowed {
			return fmt.Errorf("%w: filter field %q is not declared by every requested model", ErrInvalidQuery, predicate.Field)
		}
	}
	return nil
}
