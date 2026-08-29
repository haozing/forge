package query

import (
	"fmt"
	"sort"
)

// hybridOutcome applies the degradation matrix of doc §10.6 to the parallel
// branch results. It returns the executed mode, the accumulated degradation
// reasons and whether both recall paths failed (503 retrieval_unavailable).
// A policy-sanctioned single branch is not a technical degradation.
func hybridOutcome(requestedMode string, reasons []string, fulltextPlanned, semanticPlanned bool, fulltextErr, semanticErr error) (string, []string, bool) {
	executed := requestedMode
	switch {
	case fulltextErr != nil && semanticErr != nil:
		return executed, reasons, true
	case fulltextErr != nil:
		reasons = append(reasons, ReasonFulltextUnavailable)
		executed = ModeSemantic
	case semanticErr != nil:
		reasons = append(reasons, ReasonSemanticProviderUnavailable)
		executed = ModeFulltext
	}
	return executed, reasons, false
}

// rrfScore computes the weighted Reciprocal Rank Fusion contribution of one
// branch. k=60 and both weights 1.0 are locked by config_test (doc §10.5).
func rrfScore(rank int, weight float64) float64 {
	if rank <= 0 {
		return 0
	}
	return weight / float64(RRFK+rank)
}

// fusedCandidate merges the lexical and semantic branches by chunk id and
// carries the fusion state through rerank and collapse.
type fusedCandidate struct {
	chunk      chunkCandidate
	rrf        float64
	rerank     float64
	finalScore float64
}

// fuseCandidates builds the fused list: every chunk keeps its branch ranks,
// RRF is summed per chunk and the list sorts by RRF score with the chunk id
// as the stable tie-break.
func fuseCandidates(lexical, semantic []chunkCandidate) []fusedCandidate {
	index := make(map[string]int, len(lexical)+len(semantic))
	fused := make([]fusedCandidate, 0, len(lexical)+len(semantic))
	add := func(candidate chunkCandidate, weight float64, rank int) {
		if position, ok := index[candidate.ChunkID]; ok {
			fused[position].rrf += rrfScore(rank, weight)
			return
		}
		index[candidate.ChunkID] = len(fused)
		fused = append(fused, fusedCandidate{chunk: candidate, rrf: rrfScore(rank, weight)})
	}
	for _, candidate := range lexical {
		add(candidate, RRFWeightFulltext, candidate.LexicalRank)
	}
	for _, candidate := range semantic {
		add(candidate, RRFWeightSemantic, candidate.SemanticRank)
	}
	sortFusedByRRF(fused)
	return fused
}

func sortFusedByRRF(fused []fusedCandidate) {
	sort.SliceStable(fused, func(i, j int) bool {
		if fused[i].rrf != fused[j].rrf {
			return fused[i].rrf > fused[j].rrf
		}
		return fused[i].chunk.ChunkID < fused[j].chunk.ChunkID
	})
}

// sortFusedByRerank orders by rerank score and uses the RRF score plus chunk
// id as the stable tie-break (doc §10.5 rule 4).
func sortFusedByRerank(fused []fusedCandidate) {
	sort.SliceStable(fused, func(i, j int) bool {
		if fused[i].rerank != fused[j].rerank {
			return fused[i].rerank > fused[j].rerank
		}
		if fused[i].rrf != fused[j].rrf {
			return fused[i].rrf > fused[j].rrf
		}
		return fused[i].chunk.ChunkID < fused[j].chunk.ChunkID
	})
}

// sortFusedByBranchScore orders single-branch results: lexical branch keeps
// the PGroonga score, semantic branch the cosine similarity; the chunk id
// breaks ties.
func sortFusedByBranchScore(fused []fusedCandidate) {
	sort.SliceStable(fused, func(i, j int) bool {
		if fused[i].chunk.Score != fused[j].chunk.Score {
			return fused[i].chunk.Score > fused[j].chunk.Score
		}
		return fused[i].chunk.ChunkID < fused[j].chunk.ChunkID
	})
}

// collapsedAsset is the asset-level entry after collapse (doc §10.7): the
// first chunk is the primary citation, up to ChunksPerAsset chunks stay in the
// session snapshot, and the asset score is the primary chunk score (no summation).
type collapsedAsset struct {
	AssetID         string
	AssetVersionID  string
	WorkspaceID     string
	ResourceModelID string
	Score           float64
	Primary         fusedCandidate
	Alternates      []fusedCandidate
}

// collapseAssets scans the ordered chunk list and builds one asset candidate
// per AssetVersion. The asset id is the final stable tie-break.
func collapseAssets(fused []fusedCandidate) []collapsedAsset {
	byVersion := make(map[string]int, len(fused))
	out := make([]collapsedAsset, 0, len(fused))
	for _, candidate := range fused {
		if position, ok := byVersion[candidate.chunk.AssetVersionID]; ok {
			if len(out[position].Alternates) < ChunksPerAsset-1 {
				out[position].Alternates = append(out[position].Alternates, candidate)
			}
			continue
		}
		byVersion[candidate.chunk.AssetVersionID] = len(out)
		out = append(out, collapsedAsset{
			AssetID:         candidate.chunk.AssetID,
			AssetVersionID:  candidate.chunk.AssetVersionID,
			WorkspaceID:     candidate.chunk.WorkspaceID,
			ResourceModelID: candidate.chunk.ResourceModelID,
			Score:           candidate.finalScore,
			Primary:         candidate,
		})
	}
	return out
}

// truncateCandidates caps the frozen asset snapshot (doc §6.2: 500 assets).
func truncateCandidates(assets []collapsedAsset) []collapsedAsset {
	if len(assets) > MaxSessionAssets {
		return assets[:MaxSessionAssets]
	}
	return assets
}

// scorePointer returns a heap-safe pointer for optional scores.
func scorePointer(value float64) *float64 {
	copyValue := value
	return &copyValue
}

var _ = fmt.Sprintf
