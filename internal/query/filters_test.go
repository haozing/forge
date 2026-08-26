package query

import (
	"errors"
	"testing"
)

func TestParseFilterPlan(t *testing.T) {
	plan, err := parseFilterPlan(map[string]any{
		"quality_gte": "human_confirmed",
		"fields": map[string]any{
			"mount": map[string]any{"eq": "E"},
			"price": map[string]any{"gte": 100, "lte": 1000},
		},
		"tags": map[string]any{"contains_any": []any{"camera", "night"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.QualityGTE != "human_confirmed" || len(plan.Fields) != 3 || len(plan.Tags) != 1 || plan.Fields[0].Field != "mount" {
		t.Fatalf("unexpected plan: %#v", plan)
	}
}

func TestParseFilterPlanRejectsUnknownInput(t *testing.T) {
	_, err := parseFilterPlan(map[string]any{"sql": "select 1"})
	if !errors.Is(err, ErrInvalidQuery) {
		t.Fatalf("expected invalid query, got %v", err)
	}
	_, err = parseFilterPlan(map[string]any{"fields": map[string]any{"price": map[string]any{"like": "%"}}})
	if !errors.Is(err, ErrInvalidQuery) {
		t.Fatalf("expected invalid operator, got %v", err)
	}
}
