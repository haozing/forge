package query

import "time"

// Phase 3 query constants (doc §6.2, §10, §16.7). The ranking constants live
// here so tests can lock them; they never leak into ad-hoc SQL literals.
const (
	// DefaultTopK is the page size when the request omits top_k.
	DefaultTopK = 20
	// MaxTopK caps one page.
	MaxTopK = 50
	// MaxQueryRunes bounds the query text after trimming (Unicode code points).
	MaxQueryRunes = 1000
	// MaxResourceModelIDs caps explicit resource_model_ids per request.
	MaxResourceModelIDs = 50
	// MaxFieldFilters caps the typed field_filters array.
	MaxFieldFilters = 40
	// MaxSessionAssets caps the frozen asset-level snapshot per session.
	MaxSessionAssets = 500
	// MaxCandidateWindow is the upper bound of the recall window.
	MaxCandidateWindow = 1000
	// MinCandidateWindow is the lower bound of the recall window.
	MinCandidateWindow = 200
	// CandidateWindowFactor grows the window with top_k.
	CandidateWindowFactor = 10

	// RRFK is the weighted Reciprocal Rank Fusion constant (doc §10.5).
	RRFK = 60
	// RRFWeightFulltext and RRFWeightSemantic keep both branches equal in
	// phase 3.
	RRFWeightFulltext = 1.0
	// RRFWeightSemantic mirrors the fulltext weight.
	RRFWeightSemantic = 1.0

	// RerankCandidateLimit caps the reranker input (already scope-filtered).
	RerankCandidateLimit = 50
	// ChunksPerAsset caps how many chunks per asset the session snapshot keeps.
	ChunksPerAsset = 3

	// SemanticOverfetchFactor multiplies the HNSW fetch before scope filtering.
	SemanticOverfetchFactor = 5

	// DefaultSessionTTL is the search session lifetime.
	DefaultSessionTTL = 10 * time.Minute
	// DefaultQueryTimeout is the total request budget.
	DefaultQueryTimeout = 5 * time.Second

	// CitationExcerptRunes caps the primary citation excerpt.
	CitationExcerptRunes = 500

	// MaxCitationRefs caps one references/validate request.
	MaxCitationRefs = 50
)

// candidateWindow computes min(max(200, top_k*10), 1000).
func candidateWindow(topK int) int {
	window := topK * CandidateWindowFactor
	if window < MinCandidateWindow {
		window = MinCandidateWindow
	}
	if window > MaxCandidateWindow {
		window = MaxCandidateWindow
	}
	return window
}
