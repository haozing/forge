package asset

import (
	"strings"
	"testing"
)

func TestCompileFieldPredicatesSQLProducesParameterizedFragments(t *testing.T) {
	params := &predicateParams{}
	fragment, err := compileFieldPredicatesSQL(params, []fieldPredicate{
		{Field: "shot_size", Operator: "eq", Value: "wide"},
	}, "v.fields")
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	if !strings.Contains(fragment, "(v.fields->$1) = $2::jsonb") {
		t.Fatalf("unexpected fragment: %q", fragment)
	}
	if len(params.args) != 2 || params.args[1] != `"wide"` {
		t.Fatalf("args = %#v, want the JSON-encoded value bound", params.args)
	}
}

func TestCompileFieldPredicatesNumericComparison(t *testing.T) {
	params := &predicateParams{}
	fragment, err := compileFieldPredicatesSQL(params, []fieldPredicate{
		{Field: "duration", Operator: "gte", Value: float64(30)},
	}, "v.fields")
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	if !strings.Contains(fragment, `NULLIF((v.fields->>$1), '')::numeric >= $2::numeric`) {
		t.Fatalf("unexpected numeric fragment: %q", fragment)
	}
}

func TestCompileFieldPredicatesInBuildsOrGroup(t *testing.T) {
	params := &predicateParams{}
	fragment, err := compileFieldPredicatesSQL(params, []fieldPredicate{
		{Field: "shot_size", Operator: "in", Value: []any{"wide", "close"}},
	}, "v.fields")
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	if !strings.Contains(fragment, " OR ") {
		t.Fatalf("in must expand to an OR group: %q", fragment)
	}
	if strings.Count(fragment, "::jsonb") != 2 {
		t.Fatalf("each in value must be its own jsonb equality: %q", fragment)
	}
}

func TestCompileFieldPredicatesContainsEscapesLikeMeta(t *testing.T) {
	params := &predicateParams{}
	fragment, err := compileFieldPredicatesSQL(params, []fieldPredicate{
		{Field: "title", Operator: "contains", Value: `50%_off`},
	}, "v.fields")
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	if !strings.Contains(fragment, "ILIKE") {
		t.Fatalf("contains must use ILIKE: %q", fragment)
	}
	if params.args[1] != `%50\%\_off%` {
		t.Fatalf("LIKE pattern not escaped: %#v", params.args[1])
	}
}

func TestCompileFieldPredicatesExistsAndNeq(t *testing.T) {
	params := &predicateParams{}
	fragment, err := compileFieldPredicatesSQL(params, []fieldPredicate{
		{Field: "approved", Operator: "exists", Value: true},
		{Field: "shot_size", Operator: "neq", Value: "wide"},
	}, "v.fields")
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	if !strings.Contains(fragment, "(v.fields ? $1) = $2::boolean") {
		t.Fatalf("unexpected exists fragment: %q", fragment)
	}
	if !strings.Contains(fragment, "(v.fields->$3) IS NOT NULL AND (v.fields->$3) <> $4::jsonb") {
		t.Fatalf("neq must require presence: %q", fragment)
	}
}

func TestCompileFieldPredicatesRejectsInvalidInput(t *testing.T) {
	cases := [][]fieldPredicate{
		{{Field: "approved", Operator: "exists", Value: "yes"}},
		{{Field: "shot_size", Operator: "in", Value: "wide"}},
		{{Field: "shot_size", Operator: "in", Value: []any{}}},
		{{Field: "title", Operator: "contains", Value: float64(5)}},
		{{Field: "shot_size", Operator: "between", Value: "x"}},
		{{Field: "duration", Operator: "gte", Value: "thirty"}},
	}
	for index, predicates := range cases {
		params := &predicateParams{}
		if _, err := compileFieldPredicatesSQL(params, predicates, "v.fields"); err != ErrInvalidInput {
			t.Fatalf("case %d: got %v, want ErrInvalidInput", index, err)
		}
	}
}

func TestCompileFieldPredicatesEmptyListYieldsEmptyFragment(t *testing.T) {
	params := &predicateParams{}
	fragment, err := compileFieldPredicatesSQL(params, nil, "v.fields")
	if err != nil || fragment != "" {
		t.Fatalf("empty predicates: fragment=%q err=%v", fragment, err)
	}
}
