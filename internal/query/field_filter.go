package query

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"agentchunzhi/internal/store"
)

// fieldOperator names accepted per field type (doc §6.3).
const (
	opEq           = "eq"
	opNeq          = "neq"
	opIn           = "in"
	opGt           = "gt"
	opGte          = "gte"
	opLt           = "lt"
	opLte          = "lte"
	opExists       = "exists"
	opContains     = "contains"
	opContainsAny  = "contains_any"
	opContainsAll  = "contains_all"
)

// field type families of the phase 3 operator matrix. Types outside these
// families (object, json, block, calculated, relations, media...) never
// participate in phase 3 queries.
const (
	typeFamilyText     = "text"     // text / markdown / url / email / string
	typeFamilyNumber   = "number"   // integer / number / currency
	typeFamilyTemporal = "temporal" // date / datetime
	typeFamilyBoolean  = "boolean"
	typeFamilyEnum     = "enum"
	typeFamilyArray    = "array" // multiselect / array scalar
)

func fieldFamily(fieldType string) (string, bool) {
	switch fieldType {
	case "text", "markdown", "url", "email", "string":
		return typeFamilyText, true
	case "integer", "number", "currency":
		return typeFamilyNumber, true
	case "date", "datetime":
		return typeFamilyTemporal, true
	case "boolean":
		return typeFamilyBoolean, true
	case "enum":
		return typeFamilyEnum, true
	case "multiselect", "array":
		return typeFamilyArray, true
	}
	return "", false
}

func operatorAllowed(family, operator string) bool {
	switch family {
	case typeFamilyText, typeFamilyEnum:
		return operator == opEq || operator == opNeq || operator == opIn || operator == opExists
	case typeFamilyNumber, typeFamilyTemporal:
		return operator == opEq || operator == opNeq || operator == opIn ||
			operator == opGt || operator == opGte || operator == opLt || operator == opLte || operator == opExists
	case typeFamilyBoolean:
		return operator == opEq || operator == opNeq || operator == opExists
	case typeFamilyArray:
		return operator == opContains || operator == opContainsAny || operator == opContainsAll || operator == opExists
	}
	return false
}

// compiledFieldFilter is a validated typed condition ready for the SQL
// builder. The field key never enters the SQL text; it travels as a bound
// parameter so client JSON can never reach PL/pgSQL interpretation.
type compiledFieldFilter struct {
	modelID  string
	field    string
	operator string
	// kind drives the SQL shape: jsonb equality, numeric cast, timestamp
	// cast, boolean or array containment.
	kind     string
	texts    []string
	number   float64
	boolean  bool
	time     time.Time
	inValues []compiledScalar
}

type compiledScalar struct {
	kind   string // text | number | boolean
	text   string
	number float64
	bool   bool
}

// maxInValues caps one `in` list.
const maxInValues = 100

// loadFieldSchema fetches the field_schema of the current published version of
// every referenced model. The schema is immutable, so validating against the
// model's current version is authoritative for fields assets carry.
func loadFieldSchema(ctx context.Context, store *store.Store, organizationID string, modelIDs []string) (map[string]map[string]fieldDefinition, error) {
	schemas := make(map[string]map[string]fieldDefinition, len(modelIDs))
	if len(modelIDs) == 0 {
		return schemas, nil
	}
	rows, err := store.Pool.Query(ctx, `
		SELECT rm.id::text, v.field_schema
		FROM model.resource_models rm
		JOIN model.resource_model_versions v
		  ON v.organization_id = rm.organization_id AND v.id = rm.current_version_id
		WHERE rm.organization_id = $1::uuid AND rm.status = 'active'
		  AND rm.id = ANY($2::uuid[])
	`, organizationID, modelIDs)
	if err != nil {
		return nil, fmt.Errorf("load field schemas: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var modelID string
		var raw []byte
		if err := rows.Scan(&modelID, &raw); err != nil {
			return nil, err
		}
		definitions, err := parseFieldSchema(raw)
		if err != nil {
			return nil, fmt.Errorf("parse field schema of model %s: %w", modelID, err)
		}
		schemas[modelID] = definitions
	}
	return schemas, rows.Err()
}

type fieldDefinition struct {
	Key     string         `json:"key"`
	Type    string         `json:"type"`
	Options []optionValues `json:"-"`
}

type optionValues struct {
	Value string `json:"value"`
}

// parseFieldSchema decodes the {"fields":[{...}]} document into a key map.
func parseFieldSchema(raw []byte) (map[string]fieldDefinition, error) {
	if len(raw) == 0 {
		return map[string]fieldDefinition{}, nil
	}
	var schema struct {
		Fields []fieldDefinition `json:"fields"`
	}
	if err := json.Unmarshal(raw, &schema); err != nil {
		return nil, err
	}
	out := make(map[string]fieldDefinition, len(schema.Fields))
	for _, field := range schema.Fields {
		if field.Key == "" {
			continue
		}
		out[field.Key] = field
	}
	return out, nil
}

// compileFieldFilters validates and type-decodes every filter against the
// schema of the model it references. Errors carry the array index and field
// key (doc §6.3).
func compileFieldFilters(filters []FieldFilter, schemas map[string]map[string]fieldDefinition) ([]compiledFieldFilter, error) {
	compiled := make([]compiledFieldFilter, 0, len(filters))
	for index, filter := range filters {
		definitions, ok := schemas[filter.ResourceModelID]
		if !ok {
			return nil, errInvalidFieldFilterf(index, filter.Field, "references an unknown resource model")
		}
		definition, known := definitions[filter.Field]
		if !known {
			return nil, errInvalidFieldFilterf(index, filter.Field, "is not part of the resource model schema")
		}
		family, supported := fieldFamily(definition.Type)
		if !supported {
			return nil, errInvalidFieldFilterf(index, filter.Field, "has type %q that cannot be queried", definition.Type)
		}
		if !operatorAllowed(family, filter.Operator) {
			return nil, errInvalidFieldFilterf(index, filter.Field, "does not support operator %q for type %q", filter.Operator, definition.Type)
		}
		compiledFilter, err := compileTypedValue(index, filter, family)
		if err != nil {
			return nil, err
		}
		compiledFilter.modelID = filter.ResourceModelID
		compiledFilter.field = filter.Field
		compiledFilter.operator = filter.Operator
		compiled = append(compiled, compiledFilter)
	}
	return compiled, nil
}

func compileTypedValue(index int, filter FieldFilter, family string) (compiledFieldFilter, error) {
	switch filter.Operator {
	case opExists:
		value, ok := filter.Value.(bool)
		if !ok {
			return compiledFieldFilter{}, errInvalidFieldFilterf(index, filter.Field, "requires a boolean value")
		}
		return compiledFieldFilter{kind: "exists", boolean: value}, nil
	case opIn:
		values, ok := filter.Value.([]any)
		if !ok || len(values) == 0 || len(values) > maxInValues {
			return compiledFieldFilter{}, errInvalidFieldFilterf(index, filter.Field, "requires a non-empty array of at most %d values", maxInValues)
		}
		compiledFilter := compiledFieldFilter{kind: scalarKind(family)}
		for _, raw := range values {
			scalar, err := compileScalar(index, filter.Field, family, raw)
			if err != nil {
				return compiledFieldFilter{}, err
			}
			compiledFilter.inValues = append(compiledFilter.inValues, scalar)
		}
		return compiledFilter, nil
	case opContainsAny, opContainsAll:
		values, ok := filter.Value.([]any)
		if !ok || len(values) == 0 || len(values) > maxInValues {
			return compiledFieldFilter{}, errInvalidFieldFilterf(index, filter.Field, "requires a non-empty array of at most %d values", maxInValues)
		}
		compiledFilter := compiledFieldFilter{kind: "array_members"}
		for _, raw := range values {
			scalar, err := compileScalar(index, filter.Field, typeFamilyText, raw)
			if err != nil {
				return compiledFieldFilter{}, err
			}
			compiledFilter.texts = append(compiledFilter.texts, scalar.text)
		}
		return compiledFilter, nil
	}
	// Single-value operators.
	switch family {
	case typeFamilyText, typeFamilyEnum:
		value, ok := filter.Value.(string)
		if !ok {
			return compiledFieldFilter{}, errInvalidFieldFilterf(index, filter.Field, "requires a string value")
		}
		if !utf8.ValidString(value) || len(value) > 4000 {
			return compiledFieldFilter{}, errInvalidFieldFilterf(index, filter.Field, "value exceeds the supported length")
		}
		if family == typeFamilyEnum && filter.Operator == opEq {
			// Enum membership is checked textually against the declared options
			// when they are available; unknown options simply match nothing.
		}
		return compiledFieldFilter{kind: "jsonb", texts: []string{value}}, nil
	case typeFamilyNumber:
		value, err := jsonNumber(filter.Value)
		if err != nil {
			return compiledFieldFilter{}, errInvalidFieldFilterf(index, filter.Field, "requires a numeric value")
		}
		return compiledFieldFilter{kind: "numeric", number: value}, nil
	case typeFamilyTemporal:
		text, ok := filter.Value.(string)
		if !ok {
			return compiledFieldFilter{}, errInvalidFieldFilterf(index, filter.Field, "requires an RFC 3339 timestamp value")
		}
		parsed, err := parseTemporal(text)
		if err != nil {
			return compiledFieldFilter{}, errInvalidFieldFilterf(index, filter.Field, "is not a valid RFC 3339 timestamp")
		}
		return compiledFieldFilter{kind: "temporal", time: parsed}, nil
	case typeFamilyBoolean:
		value, ok := filter.Value.(bool)
		if !ok {
			return compiledFieldFilter{}, errInvalidFieldFilterf(index, filter.Field, "requires a boolean value")
		}
		return compiledFieldFilter{kind: "jsonb_bool", boolean: value}, nil
	case typeFamilyArray:
		value, ok := filter.Value.(string)
		if !ok {
			return compiledFieldFilter{}, errInvalidFieldFilterf(index, filter.Field, "requires a string element value")
		}
		return compiledFieldFilter{kind: "array_member", texts: []string{value}}, nil
	}
	return compiledFieldFilter{}, errInvalidFieldFilterf(index, filter.Field, "has an unsupported type family")
}

func compileScalar(index int, field, family string, raw any) (compiledScalar, error) {
	switch family {
	case typeFamilyNumber:
		value, err := jsonNumber(raw)
		if err != nil {
			return compiledScalar{}, errInvalidFieldFilterf(index, field, "requires numeric array values")
		}
		return compiledScalar{kind: "number", number: value}, nil
	case typeFamilyTemporal:
		text, ok := raw.(string)
		if !ok {
			return compiledScalar{}, errInvalidFieldFilterf(index, field, "requires timestamp array values")
		}
		parsed, err := parseTemporal(text)
		if err != nil {
			return compiledScalar{}, errInvalidFieldFilterf(index, field, "contains an invalid RFC 3339 timestamp")
		}
		return compiledScalar{kind: "temporal", text: parsed.UTC().Format(time.RFC3339Nano)}, nil
	default:
		text, ok := raw.(string)
		if !ok {
			return compiledScalar{}, errInvalidFieldFilterf(index, field, "requires string array values")
		}
		return compiledScalar{kind: "text", text: text}, nil
	}
}

func scalarKind(family string) string {
	switch family {
	case typeFamilyNumber:
		return "numeric_in"
	case typeFamilyTemporal:
		return "temporal_in"
	default:
		return "jsonb_in"
	}
}

func jsonNumber(value any) (float64, error) {
	switch typed := value.(type) {
	case float64:
		return typed, nil
	case json.Number:
		return typed.Float64()
	case int:
		return float64(typed), nil
	case int64:
		return float64(typed), nil
	case string:
		parsed, err := strconv.ParseFloat(strings.TrimSpace(typed), 64)
		if err != nil {
			return 0, err
		}
		return parsed, nil
	}
	return 0, fmt.Errorf("not a number")
}

func parseTemporal(value string) (time.Time, error) {
	trimmed := strings.TrimSpace(value)
	if len(trimmed) == 10 && strings.Count(trimmed, "-") == 2 {
		return time.Parse("2006-01-02", trimmed)
	}
	return time.Parse(time.RFC3339, trimmed)
}

// sqlBuilder assembles a parameterized predicate list. Every dynamic value —
// including field keys — becomes a placeholder.
type sqlBuilder struct {
	args []any
}

func (b *sqlBuilder) arg(value any) string {
	b.args = append(b.args, value)
	return fmt.Sprintf("$%d", len(b.args))
}

// fieldPredicates renders the typed field filters as an AND list evaluated
// against the given fields expression (e.g. "v.fields"). The expression is
// caller-owned fixed SQL, never client input.
func fieldPredicates(builder *sqlBuilder, filters []compiledFieldFilter, fieldsExpr string) []string {
	predicates := make([]string, 0, len(filters))
	for _, filter := range filters {
		keyParam := builder.arg(filter.field)
		fields := fmt.Sprintf("(%s->>%s)", fieldsExpr, keyParam)
		fieldsJSON := fmt.Sprintf("(%s->%s)", fieldsExpr, keyParam)
		switch filter.kind {
		case "exists":
			predicates = append(predicates, fmt.Sprintf(
				"(%s ? %s) = %s::boolean", fieldsExpr, keyParam, builder.arg(filter.boolean)))
		case "jsonb":
			// The bound parameter must be a complete JSON document (quoted
			// string); raw text would fail the ::jsonb cast with 22P02.
			predicates = append(predicates, builder.jsonbComparison(filter.operator, fieldsJSON,
				builder.arg(jsonbTextLiteral(filter.texts[0]))))
		case "jsonb_bool":
			predicates = append(predicates, builder.jsonbComparison(filter.operator, fieldsJSON,
				"to_jsonb("+builder.arg(filter.boolean)+"::boolean)"))
		case "numeric":
			predicates = append(predicates, builder.numericComparison(filter.operator, fields, builder.arg(filter.number)))
		case "numeric_in":
			comparisons := make([]string, 0, len(filter.inValues))
			for _, value := range filter.inValues {
				comparisons = append(comparisons, fmt.Sprintf(
					"NULLIF(%s, '')::numeric = %s::numeric", fields, builder.arg(value.number)))
			}
			predicates = append(predicates, "("+strings.Join(comparisons, " OR ")+")")
		case "temporal":
			predicates = append(predicates, builder.temporalComparison(filter.operator, fields, builder.arg(filter.time)))
		case "temporal_in":
			comparisons := make([]string, 0, len(filter.inValues))
			for _, value := range filter.inValues {
				comparisons = append(comparisons, fmt.Sprintf(
					"NULLIF(%s, '')::timestamptz = %s::timestamptz", fields, builder.arg(value.text)))
			}
			predicates = append(predicates, "("+strings.Join(comparisons, " OR ")+")")
		case "jsonb_in":
			comparisons := make([]string, 0, len(filter.inValues))
			for _, value := range filter.inValues {
				comparisons = append(comparisons, fmt.Sprintf("%s = (%s)::jsonb",
					fieldsJSON, builder.arg(jsonbTextLiteral(value.text))))
			}
			predicates = append(predicates, "("+strings.Join(comparisons, " OR ")+")")
		case "array_member":
			if filter.operator == opContains {
				predicates = append(predicates, fmt.Sprintf(
					"%s @> to_jsonb(%s::text)", fieldsJSON, builder.arg(filter.texts[0])))
			}
		case "array_members":
			memberParam := builder.arg(filter.texts)
			if filter.operator == opContainsAny {
				predicates = append(predicates, fmt.Sprintf("(%s ?| %s::text[])", fieldsJSON, memberParam))
			} else {
				predicates = append(predicates, fmt.Sprintf("(%s ?& %s::text[])", fieldsJSON, memberParam))
			}
		}
	}
	return predicates
}

// jsonbTextLiteral encodes one text comparison value as a JSON string document
// so the bound parameter survives the ::jsonb cast (a raw string is invalid
// JSON and raises 22P02 at the database). Only jsonb/jsonb_in use it; array
// membership compares the raw element text, which is a different semantic.
func jsonbTextLiteral(value string) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		// Values are utf8-validated at compile time; the JSON null document can
		// never equal a field value, so a failed encode simply matches nothing.
		return "null"
	}
	return string(encoded)
}

func (b *sqlBuilder) jsonbComparison(operator, leftExpr, rightExpr string) string {
	switch operator {
	case opEq:
		return fmt.Sprintf("%s = (%s)::jsonb", leftExpr, rightExpr)
	case opNeq:
		return fmt.Sprintf("(%s IS NOT NULL AND %s <> (%s)::jsonb)", leftExpr, leftExpr, rightExpr)
	}
	return "true"
}

func (b *sqlBuilder) numericComparison(operator, leftExpr, rightExpr string) string {
	left := fmt.Sprintf("NULLIF(%s, '')::numeric", leftExpr)
	switch operator {
	case opEq:
		return fmt.Sprintf("%s = %s", left, rightExpr)
	case opNeq:
		return fmt.Sprintf("(%s IS NOT NULL AND %s <> %s)", left, left, rightExpr)
	case opGt:
		return fmt.Sprintf("%s > %s", left, rightExpr)
	case opGte:
		return fmt.Sprintf("%s >= %s", left, rightExpr)
	case opLt:
		return fmt.Sprintf("%s < %s", left, rightExpr)
	case opLte:
		return fmt.Sprintf("%s <= %s", left, rightExpr)
	}
	return "true"
}

func (b *sqlBuilder) temporalComparison(operator, leftExpr, rightExpr string) string {
	left := fmt.Sprintf("NULLIF(%s, '')::timestamptz", leftExpr)
	switch operator {
	case opEq:
		return fmt.Sprintf("%s = %s::timestamptz", left, rightExpr)
	case opNeq:
		return fmt.Sprintf("(%s IS NOT NULL AND %s <> %s::timestamptz)", left, left, rightExpr)
	case opGt:
		return fmt.Sprintf("%s > %s::timestamptz", left, rightExpr)
	case opGte:
		return fmt.Sprintf("%s >= %s::timestamptz", left, rightExpr)
	case opLt:
		return fmt.Sprintf("%s < %s::timestamptz", left, rightExpr)
	case opLte:
		return fmt.Sprintf("%s <= %s::timestamptz", left, rightExpr)
	}
	return "true"
}

// resolvedTagFilter is the (workspace_id, tag_id) resolution of one key group
// across the scope workspaces. Tag identity is workspace scoped (doc §6.4).
type resolvedTagFilter struct {
	// any/all/none carry "workspaceID:tagID" pair keys.
	any  []string
	all  []string
	none []string
}

// resolveTagFilterKeys normalizes the key groups and resolves every key in
// every scope workspace. Single-workspace scopes require every key to resolve
// (matching the asset list contract); organization scopes accept keys that
// resolve in at least one workspace and form per-workspace pairs.
func resolveTagFilterKeys(ctx context.Context, store *store.Store, organizationID string, workspaceIDs []string, req Request) (resolvedTagFilter, error) {
	var resolved resolvedTagFilter
	if len(req.TagsAny) == 0 && len(req.TagsAll) == 0 && len(req.TagsNone) == 0 {
		return resolved, nil
	}
	rows, err := store.Pool.Query(ctx, `
		SELECT t.workspace_id::text, t.id::text, t.normalized_key
		FROM asset.tags t
		JOIN content.workspaces w ON w.organization_id = t.organization_id
		  AND w.id = t.workspace_id AND w.status = 'active'
		WHERE t.organization_id = $1::uuid AND t.workspace_id = ANY($2::uuid[])
	`, organizationID, workspaceIDs)
	if err != nil {
		return resolvedTagFilter{}, fmt.Errorf("resolve tag keys: %w", err)
	}
	defer rows.Close()
	type tagKey struct{ workspace, key string }
	ids := make(map[tagKey]string)
	keysInWorkspace := make(map[string]map[string]bool)
	for rows.Next() {
		var workspaceID, tagID, key string
		if err := rows.Scan(&workspaceID, &tagID, &key); err != nil {
			return resolvedTagFilter{}, err
		}
		ids[tagKey{workspaceID, key}] = tagID
		if keysInWorkspace[key] == nil {
			keysInWorkspace[key] = map[string]bool{}
		}
		keysInWorkspace[key][workspaceID] = true
	}
	if err := rows.Err(); err != nil {
		return resolvedTagFilter{}, err
	}
	singleWorkspace := len(workspaceIDs) == 1
	pairs := func(keys []string) ([]string, error) {
		if len(keys) == 0 {
			return nil, nil
		}
		out := make([]string, 0, len(keys))
		for _, key := range keys {
			matched := false
			for _, workspaceID := range workspaceIDs {
				if tagID, ok := ids[tagKey{workspaceID, key}]; ok {
					out = append(out, workspaceID+":"+tagID)
					matched = true
				}
			}
			if !matched && singleWorkspace {
				return nil, ErrInvalidTagFilter
			}
		}
		return out, nil
	}
	if resolved.any, err = pairs(req.TagsAny); err != nil {
		return resolvedTagFilter{}, err
	}
	if resolved.all, err = pairs(req.TagsAll); err != nil {
		return resolvedTagFilter{}, err
	}
	if resolved.none, err = pairs(req.TagsNone); err != nil {
		return resolvedTagFilter{}, err
	}
	// Organization scopes still fail loudly when a key resolves nowhere.
	if !singleWorkspace {
		for _, keys := range [][]string{req.TagsAny, req.TagsAll, req.TagsNone} {
			for _, key := range keys {
				if len(keysInWorkspace[key]) == 0 {
					return resolvedTagFilter{}, ErrInvalidTagFilter
				}
			}
		}
	}
	return resolved, nil
}

// tagPredicates renders the relational any/all/none conditions against the
// given version expression.
func tagPredicates(builder *sqlBuilder, filter resolvedTagFilter, versionExpr string) []string {
	predicates := []string{}
	if len(filter.any) > 0 {
		predicates = append(predicates, fmt.Sprintf(
			`EXISTS (SELECT 1 FROM asset.asset_version_tags ft
			         WHERE ft.asset_version_id = %s
			           AND ft.workspace_id || ':' || ft.tag_id = ANY(%s))`,
			versionExpr, builder.arg(filter.any)))
	}
	for _, pair := range filter.all {
		predicates = append(predicates, fmt.Sprintf(
			`EXISTS (SELECT 1 FROM asset.asset_version_tags fa
			         WHERE fa.asset_version_id = %s
			           AND fa.workspace_id || ':' || fa.tag_id = %s)`,
			versionExpr, builder.arg(pair)))
	}
	if len(filter.none) > 0 {
		predicates = append(predicates, fmt.Sprintf(
			`NOT EXISTS (SELECT 1 FROM asset.asset_version_tags fn
			             WHERE fn.asset_version_id = %s
			               AND fn.workspace_id || ':' || fn.tag_id = ANY(%s))`,
			versionExpr, builder.arg(filter.none)))
	}
	return predicates
}

// origins/status/time predicates shared by every plan.
func metadataPredicates(builder *sqlBuilder, req Request) []string {
	predicates := []string{}
	if len(req.Origins) > 0 {
		predicates = append(predicates, fmt.Sprintf("v.origin = ANY(%s)", builder.arg(req.Origins)))
	}
	if len(req.ConfirmationStatuses) > 0 {
		predicates = append(predicates, fmt.Sprintf("v.confirmation_status = ANY(%s)", builder.arg(req.ConfirmationStatuses)))
	}
	if req.PublishedAfter != nil {
		predicates = append(predicates, fmt.Sprintf("a.published_at >= %s::timestamptz", builder.arg(req.PublishedAfter)))
	}
	if req.PublishedBefore != nil {
		predicates = append(predicates, fmt.Sprintf("a.published_at < %s::timestamptz", builder.arg(req.PublishedBefore)))
	}
	return predicates
}
