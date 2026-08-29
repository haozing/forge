// Package testkit provides deterministic test doubles for the retrieval v2
// pipeline: a hash-based embedding provider and fake HTTP embedding/reranker
// servers. Runtime code (cmd/, internal services) must never import this
// package; it exists for tests only.
package testkit

import (
	"context"
	"crypto/sha256"
	"fmt"
	"math"

	"agentchunzhi/internal/retrieval"
)

// HashEmbedding is the deterministic testing embedding provider: every text
// maps to a stable unit vector of the configured dimension. It implements
// retrieval.EmbeddingProvider.
type HashEmbedding struct {
	Dimensions int
}

// Manifest implements retrieval.EmbeddingProvider.
func (p HashEmbedding) Manifest() retrieval.EmbeddingManifest {
	dimensions := p.Dimensions
	if dimensions <= 0 {
		dimensions = 1024
	}
	manifest, err := retrieval.EmbeddingManifest{
		Key:          "hash-embedding@test",
		ProviderKey:  "testkit",
		Model:        "hash-embedding",
		ModelVersion: "v1",
		Dimensions:   dimensions,
		Tokenizer:    retrieval.NewWordTokenizer(),
		MaxTokens:    retrieval.MaxChunkTokens,
	}.Normalize()
	if err != nil {
		panic(fmt.Sprintf("testkit.HashEmbedding manifest is invalid: %v", err))
	}
	return manifest
}

// EmbedDocuments implements retrieval.EmbeddingProvider.
func (p HashEmbedding) EmbedDocuments(_ context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i, text := range texts {
		out[i] = hashVector(text, p.dimension())
	}
	return out, nil
}

// EmbedQuery implements retrieval.EmbeddingProvider.
func (p HashEmbedding) EmbedQuery(_ context.Context, text string) ([]float32, error) {
	return hashVector(text, p.dimension()), nil
}

func (p HashEmbedding) dimension() int {
	if p.Dimensions <= 0 {
		return 1024
	}
	return p.Dimensions
}

func hashVector(text string, dimensions int) []float32 {
	vector := make([]float32, dimensions)
	for block := 0; block < dimensions; block += 32 {
		sum := sha256.Sum256([]byte(fmt.Sprintf("%d:%s", block, text)))
		for j := 0; j < 32 && block+j < dimensions; j++ {
			vector[block+j] = float32(int(sum[j])-128) / 128
		}
	}
	var norm float64
	for _, value := range vector {
		norm += float64(value) * float64(value)
	}
	norm = math.Sqrt(norm)
	if norm > 0 {
		for i := range vector {
			vector[i] = float32(float64(vector[i]) / norm)
		}
	}
	return vector
}
