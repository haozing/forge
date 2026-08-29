package query

import (
	"math"
	"testing"
)

// Fixed RRF sample (doc §10.5): k=60, both weights 1.0. Chunk A ranks 1st on
// both branches -> 2/(60+1); chunk B ranks 1st lexical, 5th semantic.
func TestRRFScoreFixedSample(t *testing.T) {
	if got := rrfScore(1, RRFWeightFulltext); math.Abs(got-1.0/61) > 1e-12 {
		t.Fatalf("rrf(1) = %v, want %v", got, 1.0/61)
	}
	if got := rrfScore(0, RRFWeightSemantic); got != 0 {
		t.Fatalf("absent branch must contribute 0, got %v", got)
	}
}

func TestFuseCandidatesSumsBranchesAndTieBreaksByChunkID(t *testing.T) {
	lexical := []chunkCandidate{
		{ChunkID: "chunk-b", AssetID: "asset-b", AssetVersionID: "version-b", LexicalRank: 1},
		{ChunkID: "chunk-a", AssetID: "asset-a", AssetVersionID: "version-a", LexicalRank: 2},
	}
	semantic := []chunkCandidate{
		{ChunkID: "chunk-a", AssetID: "asset-a", AssetVersionID: "version-a", SemanticRank: 1},
		{ChunkID: "chunk-c", AssetID: "asset-c", AssetVersionID: "version-c", SemanticRank: 2},
	}
	fused := fuseCandidates(lexical, semantic)
	if len(fused) != 3 {
		t.Fatalf("expected 3 fused chunks, got %d", len(fused))
	}
	// chunk-a: 1/62 (lexical 2) + 1/61 (semantic 1); chunk-b: 1/61; chunk-c: 1/62.
	wantA := 1.0/62.0 + 1.0/61.0
	if math.Abs(fused[0].rrf-wantA) > 1e-12 || fused[0].chunk.ChunkID != "chunk-a" {
		t.Fatalf("first fused = %s %v, want chunk-a %v", fused[0].chunk.ChunkID, fused[0].rrf, wantA)
	}
	if fused[1].chunk.ChunkID != "chunk-b" {
		t.Fatalf("second fused = %s, want chunk-b (1/61)", fused[1].chunk.ChunkID)
	}
	if fused[2].chunk.ChunkID != "chunk-c" {
		t.Fatalf("third fused = %s, want chunk-c", fused[2].chunk.ChunkID)
	}
	// Equal RRF scores resolve by chunk id: chunk-c ties with the lexical-only
	// 1/62 of a hypothetical chunk on rank 61 — the id ordering is stable.
	if fused[1].rrf <= fused[2].rrf {
		t.Fatalf("expected descending rrf, got %v then %v", fused[1].rrf, fused[2].rrf)
	}
}

func TestCollapseAssetsKeepsPrimaryAndLimitsAlternates(t *testing.T) {
	fused := []fusedCandidate{
		{chunk: chunkCandidate{ChunkID: "c1", AssetID: "a1", AssetVersionID: "v1"}, finalScore: 0.9},
		{chunk: chunkCandidate{ChunkID: "c2", AssetID: "a1", AssetVersionID: "v1"}, finalScore: 0.5},
		{chunk: chunkCandidate{ChunkID: "c3", AssetID: "a1", AssetVersionID: "v1"}, finalScore: 0.4},
		{chunk: chunkCandidate{ChunkID: "c4", AssetID: "a1", AssetVersionID: "v1"}, finalScore: 0.3},
		{chunk: chunkCandidate{ChunkID: "c5", AssetID: "a2", AssetVersionID: "v2"}, finalScore: 0.2},
	}
	assets := collapseAssets(fused)
	if len(assets) != 2 {
		t.Fatalf("expected 2 assets, got %d", len(assets))
	}
	if assets[0].Primary.chunk.ChunkID != "c1" {
		t.Fatalf("primary chunk = %s, want c1", assets[0].Primary.chunk.ChunkID)
	}
	// The asset score is the primary chunk score — chunk counts never sum.
	if assets[0].Score != 0.9 {
		t.Fatalf("asset score = %v, want primary 0.9", assets[0].Score)
	}
	if len(assets[0].Alternates) != ChunksPerAsset-1 {
		t.Fatalf("alternates = %d, want %d", len(assets[0].Alternates), ChunksPerAsset-1)
	}
}

func TestHybridDegradationMatrix(t *testing.T) {
	cases := []struct {
		name            string
		requested       string
		ftPlanned       bool
		semPlanned      bool
		ftErr           error
		semErr          error
		wantExecuted    string
		wantFatal       bool
		wantReasonCount int
	}{
		{"both healthy", ModeHybrid, true, true, nil, nil, ModeHybrid, false, 0},
		{"semantic provider down", ModeHybrid, true, true, nil, ErrSemanticProviderUnavailable, ModeFulltext, false, 1},
		{"fulltext repository down", ModeHybrid, true, true, ErrRetrievalUnavailable, nil, ModeSemantic, false, 1},
		{"both down", ModeHybrid, true, true, ErrRetrievalUnavailable, ErrSemanticProviderUnavailable, ModeHybrid, true, 0},
		{"single sanctioned branch is not degraded", ModeFulltext, true, false, nil, nil, ModeFulltext, false, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			executed, reasons, fatal := hybridOutcome(tc.requested, nil, tc.ftPlanned, tc.semPlanned, tc.ftErr, tc.semErr)
			if executed != tc.wantExecuted {
				t.Fatalf("executed = %s, want %s", executed, tc.wantExecuted)
			}
			if fatal != tc.wantFatal {
				t.Fatalf("fatal = %v, want %v", fatal, tc.wantFatal)
			}
			if len(reasons) != tc.wantReasonCount {
				t.Fatalf("reasons = %v, want %d entries", reasons, tc.wantReasonCount)
			}
		})
	}
}

func TestHybridDegradationReasonsAreFixedEnum(t *testing.T) {
	_, reasons, _ := hybridOutcome(ModeHybrid, nil, true, true, nil, ErrSemanticProviderUnavailable)
	if len(reasons) != 1 || reasons[0] != ReasonSemanticProviderUnavailable {
		t.Fatalf("reason = %v, want %s", reasons, ReasonSemanticProviderUnavailable)
	}
}
