package asset

// field_predicate.go — typed compilation of the member asset list dynamic
// field filters. The phase 3 retrieval baseline removed the generic stored
// procedure that used to evaluate these predicates: they are now rendered
// here as parameterized SQL fragments against a fixed fields expression.
// Client JSON never reaches a generic interpreter; every value — including
// the field key — travels as a bound parameter.

import (
	"encoding/json"
	"fmt"
	"strings"
)

// fieldPredicate is one dynamic field condition.
type fieldPredicate struct {
	Field    string `json:"field"`
	Operator string `json:"operator"`
	Value    any    `json:"value"`
}

// predicateParams accumulates the positional parameters of one query.
type predicateParams struct {
	args []any
}

func (p *predicateParams) arg(value any) string {
	p.args = append(p.args, value)
	return fmt.Sprintf("$%d", len(p.args))
}

// compileFieldPredicatesSQL renders the predicates as an AND-joined condition
// evaluated against fieldsExpr (e.g. "v.fields"). An empty predicate list
// yields an empty string. Unsupported operators or value shapes fail with
// ErrInvalidInput instead of reaching the database.
func compileFieldPredicatesSQL(params *predicateParams, predicates []fieldPredicate, fieldsExpr string) (string, error) {
	if len(predicates) == 0 {
		return "", nil
	}
	fragments := make([]string, 0, len(predicates))
	for _, predicate := range predicates {
		fragment, err := compileFieldPredicate(params, predicate, fieldsExpr)
		if err != nil {
			return "", err
		}
		fragments = append(fragments, fragment)
	}
	return "(" + strings.Join(fragments, " AND ") + ")", nil
}

func compileFieldPredicate(params *predicateParams, predicate fieldPredicate, fieldsExpr string) (string, error) {
	keyParam := params.arg(predicate.Field)
	jsonValue := fmt.Sprintf("(%s->%s)", fieldsExpr, keyParam)
	textValue := fmt.Sprintf("(%s->>%s)", fieldsExpr, keyParam)
	switch predicate.Operator {
	case "exists":
		value, ok := predicate.Value.(bool)
		if !ok {
			return "", ErrInvalidInput
		}
		return fmt.Sprintf("(%s ? %s) = %s::boolean", fieldsExpr, keyParam, params.arg(value)), nil
	case "eq":
		return compileEquality(params, predicate.Value, jsonValue, textValue, false)
	case "neq":
		return compileEquality(params, predicate.Value, jsonValue, textValue, true)
	case "in":
		values, ok := predicate.Value.([]any)
		if !ok || len(values) == 0 || len(values) > 100 {
			return "", ErrInvalidInput
		}
		comparisons := make([]string, 0, len(values))
		for _, value := range values {
			fragment, err := compileEquality(params, value, jsonValue, textValue, false)
			if err != nil {
				return "", err
			}
			comparisons = append(comparisons, fragment)
		}
		return "(" + strings.Join(comparisons, " OR ") + ")", nil
	case "contains":
		return compileContains(params, predicate.Value, textValue, false)
	case "contains_any":
		values, ok := predicate.Value.([]any)
		if !ok || len(values) == 0 || len(values) > 100 {
			return "", ErrInvalidInput
		}
		comparisons := make([]string, 0, len(values))
		for _, value := range values {
			fragment, err := compileContains(params, value, textValue, false)
			if err != nil {
				return "", err
			}
			comparisons = append(comparisons, fragment)
		}
		return "(" + strings.Join(comparisons, " OR ") + ")", nil
	case "gte":
		number, ok := jsonFloat(predicate.Value)
		if !ok {
			return "", ErrInvalidInput
		}
		return fmt.Sprintf("NULLIF(%s, '')::numeric >= %s::numeric", textValue, params.arg(number)), nil
	case "lte":
		number, ok := jsonFloat(predicate.Value)
		if !ok {
			return "", ErrInvalidInput
		}
		return fmt.Sprintf("NULLIF(%s, '')::numeric <= %s::numeric", textValue, params.arg(number)), nil
	}
	return "", ErrInvalidInput
}

// compileEquality renders one equality-shaped comparison. Numeric values
// compare through a numeric cast so `10` and `10.0` agree; every other value
// compares as jsonb equality. negated flips eq to neq.
func compileEquality(params *predicateParams, value any, jsonValue, textValue string, negated bool) (string, error) {
	if number, ok := jsonFloat(value); ok {
		if negated {
			return fmt.Sprintf("(%s IS NOT NULL AND NULLIF(%s, '')::numeric <> %s::numeric)", jsonValue, textValue, params.arg(number)), nil
		}
		return fmt.Sprintf("NULLIF(%s, '')::numeric = %s::numeric", textValue, params.arg(number)), nil
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", ErrInvalidInput
	}
	if negated {
		return fmt.Sprintf("(%s IS NOT NULL AND %s <> %s::jsonb)", jsonValue, jsonValue, params.arg(string(encoded))), nil
	}
	return fmt.Sprintf("%s = %s::jsonb", jsonValue, params.arg(string(encoded))), nil
}

// compileContains renders the substring condition of untyped member filters:
// the JSON text form of the field is matched case-insensitively with an
// escaped LIKE pattern.
func compileContains(params *predicateParams, value any, textValue string, negated bool) (string, error) {
	text, ok := value.(string)
	if !ok {
		return "", ErrInvalidInput
	}
	pattern := "%" + escapeLike(text) + "%"
	if negated {
		return fmt.Sprintf("(%s NOT ILIKE %s)", textValue, params.arg(pattern)), nil
	}
	return fmt.Sprintf("(%s ILIKE %s)", textValue, params.arg(pattern)), nil
}

// escapeLike escapes the LIKE metacharacters of a user value.
func escapeLike(value string) string {
	replacer := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return replacer.Replace(value)
}

// jsonFloat accepts the JSON number shapes that reach the predicates.
func jsonFloat(value any) (float64, bool) {
	switch typed := value.(type) {
	case float64:
		return typed, true
	case json.Number:
		parsed, err := typed.Float64()
		return parsed, err == nil
	case int:
		return float64(typed), true
	case int64:
		return float64(typed), true
	}
	return 0, false
}
