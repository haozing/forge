package query

import (
	"encoding/base64"
	"testing"
)

var base64RawURL = base64.RawURLEncoding

func TestValidUUID(t *testing.T) {
	if !ValidUUID("00000000-0000-4000-8000-000000000001") {
		t.Fatalf("expected valid UUID")
	}
	if ValidUUID("not-a-uuid") {
		t.Fatalf("expected invalid UUID")
	}
}

func TestValidateRequestModeAndText(t *testing.T) {
	scope := QueryAccessScope{
		WorkspaceIDs:        []string{"00000000-0000-4000-8000-000000000001"},
		ResourceModelIDs:    []string{"00000000-0000-4000-8000-000000000002"},
		AllowedVisibilities: []string{"workspace", "organization", "public"},
	}
	// The legacy lexical alias must fail as an invalid mode.
	err := validateRequest(scope, &Request{Mode: "lexical", Query: "hello"})
	if err != ErrInvalidQueryMode {
		t.Fatalf("lexical alias: got %v, want ErrInvalidQueryMode", err)
	}
	// Full-text modes require text.
	if err := validateRequest(scope, &Request{Mode: ModeFulltext}); err != ErrQueryTextRequired {
		t.Fatalf("empty fulltext query: got %v, want ErrQueryTextRequired", err)
	}
	// Structured forbids text.
	if err := validateRequest(scope, &Request{Mode: ModeStructured, Query: "hello"}); err != ErrStructuredQueryTextNotAllowed {
		t.Fatalf("structured with text: got %v, want ErrStructuredQueryTextNotAllowed", err)
	}
	// Overlong text is rejected for full-text modes.
	long := Request{Mode: ModeFulltext, Query: makeRuneString(MaxQueryRunes + 1)}
	if err := validateRequest(scope, &long); err != ErrInvalidRequest {
		t.Fatalf("overlong query: got %v, want ErrInvalidRequest", err)
	}
	// Default top_k is applied.
	req := Request{Mode: ModeFulltext, Query: "hello"}
	if err := validateRequest(scope, &req); err != nil {
		t.Fatalf("valid request rejected: %v", err)
	}
	if req.TopK != DefaultTopK {
		t.Fatalf("top_k = %d, want default %d", req.TopK, DefaultTopK)
	}
	// top_k beyond the maximum fails.
	if err := validateRequest(scope, &Request{Mode: ModeFulltext, Query: "x", TopK: MaxTopK + 1}); err != ErrInvalidRequest {
		t.Fatalf("top_k %d: got %v, want ErrInvalidRequest", MaxTopK+1, err)
	}
}

func TestValidateRequestVisibilityCanOnlyNarrow(t *testing.T) {
	scope := QueryAccessScope{
		WorkspaceIDs:        []string{"00000000-0000-4000-8000-000000000001"},
		ResourceModelIDs:    []string{"00000000-0000-4000-8000-000000000002"},
		AllowedVisibilities: []string{"organization", "public"},
	}
	if err := validateRequest(scope, &Request{Mode: ModeFulltext, Query: "x", Visibility: []string{"workspace"}}); err != ErrInvalidVisibility {
		t.Fatalf("widened visibility: got %v, want ErrInvalidVisibility", err)
	}
	if err := validateRequest(scope, &Request{Mode: ModeFulltext, Query: "x", Visibility: []string{"public"}}); err != nil {
		t.Fatalf("narrowed visibility rejected: %v", err)
	}
}

func TestValidateRequestTagGroups(t *testing.T) {
	scope := QueryAccessScope{
		WorkspaceIDs:        []string{"00000000-0000-4000-8000-000000000001"},
		ResourceModelIDs:    []string{"00000000-0000-4000-8000-000000000002"},
		AllowedVisibilities: []string{"organization", "public"},
	}
	conflict := Request{Mode: ModeFulltext, Query: "x", TagsAny: []string{"release"}, TagsNone: []string{"Release "}}
	if err := validateRequest(scope, &conflict); err != ErrInvalidTagFilter {
		t.Fatalf("cross-group conflict: got %v, want ErrInvalidTagFilter", err)
	}
	normalized := Request{Mode: ModeFulltext, Query: "x", TagsAny: []string{"Release ", "release"}}
	if err := validateRequest(scope, &normalized); err != nil {
		t.Fatalf("normalized keys rejected: %v", err)
	}
	if len(normalized.TagsAny) != 1 {
		t.Fatalf("duplicate normalized keys were not deduped: %#v", normalized.TagsAny)
	}
}

func TestRequestHashBindsCanonicalForm(t *testing.T) {
	base := Request{Mode: ModeHybrid, Query: " hello ", TopK: 20}
	first := RequestHash(NormalizedRequest(base), "secret")
	second := RequestHash(NormalizedRequest(Request{Mode: ModeHybrid, Query: "hello", TopK: 50}), "secret")
	if first != second {
		t.Fatal("top_k must not affect the canonical request hash")
	}
	different := RequestHash(NormalizedRequest(Request{Mode: ModeFulltext, Query: "hello"}), "secret")
	if first == different {
		t.Fatal("different queries must hash differently")
	}
	if RequestHash(NormalizedRequest(base), "other") == first {
		t.Fatal("the hash must be secret-keyed")
	}
}

func TestNormalizedRequestSortsAndDedupes(t *testing.T) {
	normalized := NormalizedRequest(Request{
		Mode:             ModeStructured,
		ResourceModelIDs: []string{"00000000-0000-4000-8000-000000000002", "00000000-0000-4000-8000-000000000001", "00000000-0000-4000-8000-000000000002"},
	})
	if len(normalized.ResourceModelIDs) != 2 || normalized.ResourceModelIDs[0] > normalized.ResourceModelIDs[1] {
		t.Fatalf("ids not sorted/deduped: %#v", normalized.ResourceModelIDs)
	}
}

func TestRequestHashMatchesValidatorRewrite(t *testing.T) {
	// Cursor pages recompute the hash from the raw request before validation,
	// while the stored session hash was computed after validateRequest rewrote
	// the tag groups in place. Both sides must agree for non-normalized keys.
	raw := Request{
		Mode:     ModeFulltext,
		Query:    " hello ",
		TagsAny:  []string{"Release ", "release"},
		TagsAll:  []string{"Featured"},
		TagsNone: []string{"draft"},
	}
	scope := QueryAccessScope{
		WorkspaceIDs:        []string{"00000000-0000-4000-8000-000000000001"},
		ResourceModelIDs:    []string{"00000000-0000-4000-8000-000000000002"},
		AllowedVisibilities: []string{"organization", "public"},
	}
	beforeValidation := RequestHash(NormalizedRequest(raw), "secret")

	rewritten := NormalizedRequest(raw)
	if err := validateRequest(scope, &rewritten); err != nil {
		t.Fatalf("valid request rejected: %v", err)
	}
	afterValidation := RequestHash(rewritten, "secret")
	if beforeValidation != afterValidation {
		t.Fatalf("hash before validation %s != hash after tag rewrite %s", beforeValidation, afterValidation)
	}

	// Groups the tag domain rejects (contradictions, garbage keys) stay raw in
	// the normalized form; validation rejects those requests anyway.
	invalid := Request{Mode: ModeFulltext, Query: "x", TagsAny: []string{"a"}, TagsNone: []string{"A "}}
	normalizedInvalid := NormalizedRequest(invalid)
	if len(normalizedInvalid.TagsAny) != 1 || normalizedInvalid.TagsAny[0] != "a" {
		t.Fatalf("rejected groups must stay raw: %#v", normalizedInvalid.TagsAny)
	}
	if err := validateRequest(scope, &invalid); err != ErrInvalidTagFilter {
		t.Fatalf("contradictory groups: got %v, want ErrInvalidTagFilter", err)
	}
}

func makeRuneString(count int) string {
	runes := make([]rune, count)
	for index := range runes {
		runes[index] = 'a'
	}
	return string(runes)
}
