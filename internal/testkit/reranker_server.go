package testkit

import (
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"

	"agentchunzhi/internal/retrieval"
)

// EmbeddingFake is a scripted httptest server implementing the generic
// embedding HTTP protocol ({"inputs": [...]}). Failure modes cover the
// provider contract: 429/500 retries, terminal 400s, wrong dimensions, NaN
// payloads and malformed bodies.
type EmbeddingFake struct {
	Server *httptest.Server

	mu          sync.Mutex
	requests    int
	statusCode  int // first N responses use this status (0 = always 200)
	failFirst   int
	dimensions  int
	corruptRows bool
	malformed   bool
}

// NewEmbeddingFake starts a fake embedding server.
func NewEmbeddingFake(dimensions int) *EmbeddingFake {
	if dimensions <= 0 {
		dimensions = 1024
	}
	fake := &EmbeddingFake{dimensions: dimensions}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /", func(w http.ResponseWriter, r *http.Request) {
		fake.mu.Lock()
		fake.requests++
		requestNo := fake.requests
		status, failFirst := fake.statusCode, fake.failFirst
		corrupt, malformed := fake.corruptRows, fake.malformed
		fake.mu.Unlock()

		if status != 0 && requestNo <= failFirst {
			w.WriteHeader(status)
			_, _ = w.Write([]byte(`{"error":"overloaded"}`))
			return
		}
		var payload struct {
			Inputs []string `json:"inputs"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil || len(payload.Inputs) == 0 {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"bad request"}`))
			return
		}
		if malformed {
			_, _ = w.Write([]byte("not-json"))
			return
		}
		vectors := make([][]float32, len(payload.Inputs))
		for i, text := range payload.Inputs {
			if corrupt {
				// One NaN value and a missing trailing dimension.
				row := hashVector(text, fake.dimensions)
				row[0] = float32(math.NaN())
				vectors[i] = row[:fake.dimensions-1]
				continue
			}
			vectors[i] = hashVector(text, fake.dimensions)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"embeddings": vectors})
	})
	fake.Server = httptest.NewServer(mux)
	return fake
}

// URL returns the fake endpoint.
func (f *EmbeddingFake) URL() string { return f.Server.URL }

// Requests returns the number of embedding calls received.
func (f *EmbeddingFake) Requests() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.requests
}

// FailNextN makes the next N requests answer with the given status code
// (429/500 exercise the retryable classification).
func (f *EmbeddingFake) FailNextN(status int, n int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.statusCode = status
	f.failFirst = f.requests + n
}

// CorruptResponses makes every response NaN/short-dimensioned (terminal).
func (f *EmbeddingFake) CorruptResponses() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.corruptRows = true
}

// MalformedResponses makes every response a non-JSON body (terminal).
func (f *EmbeddingFake) MalformedResponses() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.malformed = true
}

// Close stops the server.
func (f *EmbeddingFake) Close() { f.Server.Close() }

// Provider builds an allowlisted retrieval.EmbeddingProvider for the fake
// endpoint.
func (f *EmbeddingFake) Provider() (retrieval.EmbeddingProvider, error) {
	host := strings.TrimPrefix(strings.TrimPrefix(f.URL(), "https://"), "http://")
	return retrieval.NewHTTPEmbeddingProvider(f.manifest(), f.URL(), "", "generic", []string{host})
}

func (f *EmbeddingFake) manifest() retrieval.EmbeddingManifest {
	return retrieval.EmbeddingManifest{
		Key:          "fake-embedding@test",
		ProviderKey:  "testkit",
		Model:        "fake-embedding",
		ModelVersion: "v1",
		Dimensions:   f.dimensions,
		Tokenizer:    retrieval.NewWordTokenizer(),
		MaxTokens:    retrieval.MaxChunkTokens,
	}
}

// RerankerFake is a scripted httptest server implementing the generic
// rerank protocol ({"query","documents"} -> {"scores":[...]}).
type RerankerFake struct {
	Server *httptest.Server

	mu       sync.Mutex
	requests int
	failWith int
	partial  bool
}

// NewRerankerFake starts a fake reranker server.
func NewRerankerFake() *RerankerFake {
	fake := &RerankerFake{}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /rerank", func(w http.ResponseWriter, r *http.Request) {
		fake.mu.Lock()
		fake.requests++
		failWith, partial := fake.failWith, fake.partial
		fake.mu.Unlock()
		if failWith != 0 {
			w.WriteHeader(failWith)
			_, _ = w.Write([]byte(`{"error":"unavailable"}`))
			return
		}
		var payload struct {
			Query     string   `json:"query"`
			Documents []string `json:"documents"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		scores := make([]float64, len(payload.Documents))
		for i := range payload.Documents {
			// Deterministic pseudo scores derived from the index.
			scores[i] = 1.0 / float64(i+1)
		}
		if partial {
			scores = scores[:len(scores)-1]
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"scores": scores})
	})
	fake.Server = httptest.NewServer(mux)
	return fake
}

// URL returns the fake endpoint.
func (f *RerankerFake) URL() string { return f.Server.URL }

// Requests returns the number of rerank calls received.
func (f *RerankerFake) Requests() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.requests
}

// FailWith makes every response answer with the given status.
func (f *RerankerFake) FailWith(status int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failWith = status
}

// ResetFailures clears the scripted failure mode.
func (f *RerankerFake) ResetFailures() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failWith = 0
}

// OmitLastCandidate makes every response omit the final index (terminal).
func (f *RerankerFake) OmitLastCandidate() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.partial = true
}

// Close stops the server.
func (f *RerankerFake) Close() { f.Server.Close() }

// Provider builds an allowlisted retrieval.RerankerProvider for the fake.
func (f *RerankerFake) Provider() (retrieval.RerankerProvider, error) {
	host := strings.TrimPrefix(strings.TrimPrefix(f.URL(), "https://"), "http://")
	return retrieval.NewHTTPRerankerProvider(retrieval.RerankerManifest{
		Key:          "fake-reranker@test",
		ProviderKey:  "testkit",
		Model:        "fake-reranker",
		ModelVersion: "v1",
	}, f.URL(), "", "generic", []string{host})
}
