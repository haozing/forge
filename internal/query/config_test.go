package query

import "testing"

// The ranking constants are contract: they must not drift silently (doc
// §10.5 — the values are locked here, not scattered through SQL).
func TestRankingConstantsAreLocked(t *testing.T) {
	if RRFK != 60 {
		t.Fatalf("RRF k = %d, want 60", RRFK)
	}
	if RRFWeightFulltext != 1.0 || RRFWeightSemantic != 1.0 {
		t.Fatalf("RRF weights = %v/%v, want 1.0/1.0", RRFWeightFulltext, RRFWeightSemantic)
	}
}

func TestCandidateWindowBounds(t *testing.T) {
	cases := []struct {
		topK, want int
	}{{1, 200}, {20, 200}, {30, 300}, {50, 500}, {100, 1000}, {200, 1000}}
	for _, tc := range cases {
		if got := candidateWindow(tc.topK); got != tc.want {
			t.Fatalf("candidateWindow(%d) = %d, want %d", tc.topK, got, tc.want)
		}
	}
}

func TestModesHaveNoLexicalAlias(t *testing.T) {
	for _, alias := range []string{"lexical", "vector", "keyword", ""} {
		if ValidMode(alias) {
			t.Fatalf("mode alias %q must not be accepted", alias)
		}
	}
	for _, mode := range []string{ModeStructured, ModeFulltext, ModeSemantic, ModeHybrid} {
		if !ValidMode(mode) {
			t.Fatalf("contract mode %q must be valid", mode)
		}
	}
	if !FulltextClass(ModeFulltext) || !FulltextClass(ModeSemantic) || !FulltextClass(ModeHybrid) {
		t.Fatal("fulltext/semantic/hybrid are index-backed modes")
	}
	if FulltextClass(ModeStructured) {
		t.Fatal("structured must not touch the projection index")
	}
}
