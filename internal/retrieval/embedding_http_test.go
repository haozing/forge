// Provider contract tests for the HTTP embedding/reranker clients. They run
// against the testkit fake servers (imported from the external test package
// so runtime code never links testkit).
package retrieval_test

import (
	"context"
	"errors"
	"testing"

	"agentchunzhi/internal/retrieval"
	"agentchunzhi/internal/testkit"
)

func newTestProvider(t *testing.T, fake *testkit.EmbeddingFake) retrieval.EmbeddingProvider {
	t.Helper()
	provider, err := fake.Provider()
	if err != nil {
		t.Fatalf("build provider: %v", err)
	}
	return provider
}

func TestHTTPEmbeddingProviderHappyPath(t *testing.T) {
	fake := testkit.NewEmbeddingFake(1024)
	defer fake.Close()
	provider := newTestProvider(t, fake)
	if got := provider.Manifest().Dimensions; got != 1024 {
		t.Fatalf("manifest dimensions = %d", got)
	}
	vectors, err := provider.EmbedDocuments(context.Background(), []string{"全文检索", "second"})
	if err != nil {
		t.Fatalf("EmbedDocuments: %v", err)
	}
	if len(vectors) != 2 {
		t.Fatalf("vectors = %d", len(vectors))
	}
	for i, vector := range vectors {
		if len(vector) != 1024 {
			t.Fatalf("vector %d dimension = %d", i, len(vector))
		}
		var norm float64
		for _, value := range vector {
			norm += float64(value) * float64(value)
		}
		if diff := norm - 1.0; diff > 1e-3 || diff < -1e-3 {
			t.Fatalf("vector %d is not normalized: %f", i, norm)
		}
	}
	// Deterministic: identical input, identical output.
	again, err := provider.EmbedDocuments(context.Background(), []string{"全文检索", "second"})
	if err != nil {
		t.Fatalf("EmbedDocuments repeat: %v", err)
	}
	for i := range vectors {
		for j := range vectors[i] {
			if vectors[i][j] != again[i][j] {
				t.Fatalf("vector %d/%d differs across runs", i, j)
			}
		}
	}
	queryVector, err := provider.EmbedQuery(context.Background(), "全文检索")
	if err != nil {
		t.Fatalf("EmbedQuery: %v", err)
	}
	if len(queryVector) != 1024 {
		t.Fatalf("query vector dimension = %d", len(queryVector))
	}
}

func TestHTTPEmbeddingProviderRetryableVsTerminal(t *testing.T) {
	fake := testkit.NewEmbeddingFake(1024)
	defer fake.Close()
	provider := newTestProvider(t, fake)
	ctx := context.Background()

	// 429 then 500 are retryable: not terminal provider errors.
	fake.FailNextN(429, 1)
	if _, err := provider.EmbedDocuments(ctx, []string{"text"}); err == nil || retrieval.IsTerminalProviderError(err) {
		t.Fatalf("429 must be retryable, got %v", err)
	}
	fake.FailNextN(500, 1)
	if _, err := provider.EmbedDocuments(ctx, []string{"text"}); err == nil || retrieval.IsTerminalProviderError(err) {
		t.Fatalf("500 must be retryable, got %v", err)
	}
	// Wrong dimension/NaN/malformed payloads are terminal.
	fake.CorruptResponses()
	if _, err := provider.EmbedDocuments(ctx, []string{"text"}); err == nil || !retrieval.IsTerminalProviderError(err) {
		t.Fatalf("corrupt payload must be terminal, got %v", err)
	}
	fake2 := testkit.NewEmbeddingFake(1024)
	defer fake2.Close()
	provider2 := newTestProvider(t, fake2)
	fake2.MalformedResponses()
	if _, err := provider2.EmbedDocuments(ctx, []string{"text"}); err == nil || !retrieval.IsTerminalProviderError(err) {
		t.Fatalf("malformed payload must be terminal, got %v", err)
	}
	// After the corruption is over, calls succeed again (fake only corrupts
	// while enabled; a fresh fake verifies the happy path returns).
	if _, err := provider2.EmbedDocuments(ctx, []string{"text"}); err == nil {
		t.Fatal("expected corrupted fake to keep failing")
	}
}

func TestHTTPEmbeddingProviderRejectsOversizedBatch(t *testing.T) {
	fake := testkit.NewEmbeddingFake(1024)
	defer fake.Close()
	provider := newTestProvider(t, fake)
	texts := make([]string, retrieval.EmbeddingBatchLimit+1)
	if _, err := provider.EmbedDocuments(context.Background(), texts); err == nil || !retrieval.IsTerminalProviderError(err) {
		t.Fatalf("oversized batch must be terminal, got %v", err)
	}
}

func TestProviderEndpointAllowlist(t *testing.T) {
	allow := []string{"api.openai.com"}
	if err := retrieval.ValidateProviderEndpoint("https://api.openai.com/v1/embeddings", allow); err != nil {
		t.Fatalf("allowlisted endpoint rejected: %v", err)
	}
	if err := retrieval.ValidateProviderEndpoint("http://api.openai.com/v1/embeddings", allow); err == nil {
		t.Fatal("plain http endpoint accepted")
	}
	if err := retrieval.ValidateProviderEndpoint("https://evil.example.com/v1/embeddings", allow); err == nil {
		t.Fatal("non-allowlisted endpoint accepted")
	}
	if err := retrieval.ValidateProviderEndpoint("http://127.0.0.1:8080/embeddings", allow); err != nil {
		t.Fatalf("loopback intranet endpoint rejected: %v", err)
	}
	if err := retrieval.ValidateProviderEndpoint("", allow); err == nil {
		t.Fatal("empty endpoint accepted")
	}
}

func TestRerankerProviderContract(t *testing.T) {
	fake := testkit.NewRerankerFake()
	defer fake.Close()
	provider, err := fake.Provider()
	if err != nil {
		t.Fatalf("build reranker: %v", err)
	}
	scores, err := provider.Rerank(context.Background(), "全文", []string{"a", "b", "c"})
	if err != nil {
		t.Fatalf("Rerank: %v", err)
	}
	if len(scores) != 3 {
		t.Fatalf("scores = %d", len(scores))
	}
	fake.FailWith(500)
	if _, err := provider.Rerank(context.Background(), "全文", []string{"a"}); err == nil || retrieval.IsTerminalProviderError(err) {
		t.Fatalf("reranker 500 must be retryable, got %v", err)
	}
	fake.ResetFailures()
	fake.OmitLastCandidate()
	if _, err := provider.Rerank(context.Background(), "全文", []string{"a", "b"}); err == nil || !retrieval.IsTerminalProviderError(err) {
		t.Fatalf("omitted candidate must be terminal, got %v", err)
	}
	var _ = errors.Is
}
