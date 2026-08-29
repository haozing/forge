package query

import (
	"strings"
	"testing"
)

func testSchema() map[string]map[string]fieldDefinition {
	return map[string]map[string]fieldDefinition{
		"00000000-0000-4000-8000-000000000002": {
			"title_text": {Key: "title_text", Type: "text"},
			"summary_md": {Key: "summary_md", Type: "markdown"},
			"shot_size":  {Key: "shot_size", Type: "enum"},
			"duration":   {Key: "duration", Type: "integer"},
			"price":      {Key: "price", Type: "currency"},
			"shot_day":   {Key: "shot_day", Type: "date"},
			"edited_at":  {Key: "edited_at", Type: "datetime"},
			"approved":   {Key: "approved", Type: "boolean"},
			"camera_set": {Key: "camera_set", Type: "multiselect"},
			"tag_list":   {Key: "tag_list", Type: "array"},
			"metadata":   {Key: "metadata", Type: "object"},
			"relation":   {Key: "relation", Type: "relation"},
		},
	}
}

const testModel = "00000000-0000-4000-8000-000000000002"

func TestTypedFieldFilterOperatorMatrix(t *testing.T) {
	schemas := testSchema()
	cases := []struct {
		name     string
		filter   FieldFilter
		contains string // substring expected in the rendered SQL fragment
	}{
		{"text eq", FieldFilter{testModel, "title_text", "eq", "wide"}, "::jsonb"},
		{"markdown neq", FieldFilter{testModel, "summary_md", "neq", "draft"}, "IS NOT NULL"},
		{"enum in", FieldFilter{testModel, "shot_size", "in", []any{"wide", "close"}}, " OR "},
		{"integer gt", FieldFilter{testModel, "duration", "gt", float64(30)}, "::numeric"},
		{"currency lte", FieldFilter{testModel, "price", "lte", float64(99.5)}, "::numeric"},
		{"date gte", FieldFilter{testModel, "shot_day", "gte", "2026-01-01"}, "::timestamptz"},
		{"datetime lt", FieldFilter{testModel, "edited_at", "lt", "2026-08-29T08:00:00Z"}, "::timestamptz"},
		{"boolean eq", FieldFilter{testModel, "approved", "eq", true}, "to_jsonb"},
		{"multiselect contains", FieldFilter{testModel, "camera_set", "contains", "A-cam"}, "@>"},
		{"multiselect contains_any", FieldFilter{testModel, "camera_set", "contains_any", []any{"A-cam", "B-cam"}}, "?|"},
		{"multiselect contains_all", FieldFilter{testModel, "camera_set", "contains_all", []any{"A-cam", "B-cam"}}, "?&"},
		{"array exists", FieldFilter{testModel, "tag_list", "exists", true}, "? "},
		{"text exists", FieldFilter{testModel, "title_text", "exists", false}, "? "},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			compiled, err := compileFieldFilters([]FieldFilter{tc.filter}, schemas)
			if err != nil {
				t.Fatalf("compile: %v", err)
			}
			builder := &sqlBuilder{}
			predicates := fieldPredicates(builder, compiled, "v.fields")
			if len(predicates) != 1 {
				t.Fatalf("expected one predicate, got %d", len(predicates))
			}
			if !strings.Contains(predicates[0], tc.contains) {
				t.Fatalf("predicate %q missing %q", predicates[0], tc.contains)
			}
			// The field key must travel as a bound parameter, never as a
			// literal inside the SQL text.
			if strings.Contains(predicates[0], tc.filter.Field) {
				t.Fatalf("field key leaked into SQL text: %q", predicates[0])
			}
		})
	}
}

func TestTypedFieldFilterRejectsUnsupportedCombinations(t *testing.T) {
	schemas := testSchema()
	cases := []struct {
		name   string
		filter FieldFilter
	}{
		{"unknown field", FieldFilter{testModel, "nope", "eq", "x"}},
		{"object field", FieldFilter{testModel, "metadata", "eq", "x"}},
		{"relation field", FieldFilter{testModel, "relation", "exists", true}},
		{"text gt", FieldFilter{testModel, "title_text", "gt", "a"}},
		{"boolean contains", FieldFilter{testModel, "approved", "contains", "x"}},
		{"multiselect eq", FieldFilter{testModel, "camera_set", "eq", "A-cam"}},
		{"date contains", FieldFilter{testModel, "shot_day", "contains", "2026"}},
		{"integer non-numeric value", FieldFilter{testModel, "duration", "gt", "thirty"}},
		{"date non-timestamp value", FieldFilter{testModel, "shot_day", "gt", "not-a-date"}},
		{"boolean non-bool value", FieldFilter{testModel, "approved", "eq", "yes"}},
		{"exists non-bool value", FieldFilter{testModel, "title_text", "exists", "yes"}},
		{"in empty list", FieldFilter{testModel, "shot_size", "in", []any{}}},
		{"in non-array", FieldFilter{testModel, "shot_size", "in", "wide"}},
		{"contains_any non-array", FieldFilter{testModel, "camera_set", "contains_any", "A-cam"}},
		{"unknown model", FieldFilter{"00000000-0000-4000-8000-000000000099", "title_text", "eq", "x"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := compileFieldFilters([]FieldFilter{tc.filter}, schemas)
			if err == nil {
				t.Fatal("expected rejection")
			}
			status, code := HTTPStatus(err)
			if status != 422 || code != "invalid_field_filter" {
				t.Fatalf("error = %v, want 422 invalid_field_filter", err)
			}
		})
	}
}

func TestTypedFieldFilterErrorsCarryIndexAndField(t *testing.T) {
	schemas := testSchema()
	filters := []FieldFilter{
		{testModel, "title_text", "eq", "ok"},
		{testModel, "duration", "gt", "thirty"},
	}
	_, err := compileFieldFilters(filters, schemas)
	if err == nil {
		t.Fatal("expected rejection")
	}
	message := err.Error()
	if !strings.Contains(message, "field_filters[1]") || !strings.Contains(message, "duration") {
		t.Fatalf("error %q must carry the array index and field key", message)
	}
}

func TestTypedFieldFilterNeverInlinesValues(t *testing.T) {
	schemas := testSchema()
	compiled, err := compileFieldFilters([]FieldFilter{
		{testModel, "title_text", "eq", "wide'; DROP TABLE assets; --"},
		{testModel, "duration", "gt", float64(30)},
	}, schemas)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	builder := &sqlBuilder{}
	predicates := fieldPredicates(builder, compiled, "v.fields")
	joined := strings.Join(predicates, " ")
	if strings.Contains(joined, "DROP TABLE") || strings.Contains(joined, "wide'") {
		t.Fatalf("client value leaked into SQL text: %q", joined)
	}
	// Every placeholder indexes into the bound argument list.
	if len(builder.args) < 4 {
		t.Fatalf("expected bound parameters, got %#v", builder.args)
	}
}
