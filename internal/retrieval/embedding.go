package retrieval

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
	"time"
)

// DefaultEmbeddingDimensions is the fixed v2 embedding ABI.
const DefaultEmbeddingDimensions = 1024

// EmbeddingBatchLimit caps one provider document batch.
const EmbeddingBatchLimit = 32

// Provider call budgets (doc §7.6): a single batch gets 5 seconds, a whole
// embedding job 30 seconds.
const (
	EmbedSingleCallTimeout = 5 * time.Second
	EmbedJobTimeout        = 30 * time.Second
)

// EmbeddingManifest is the deployment-side embedding identity. Provider
// endpoint, token and secret never enter the database; the manifest key maps
// to an instance registered through startup validation.
type EmbeddingManifest struct {
	Key          string
	ProviderKey  string
	Model        string
	ModelVersion string
	Dimensions   int
	Tokenizer    Tokenizer
	MaxTokens    int
}

// Normalize fills derived defaults and validates the manifest shape.
func (m EmbeddingManifest) Normalize() (EmbeddingManifest, error) {
	if strings.TrimSpace(m.Key) == "" {
		return m, fmt.Errorf("embedding manifest key is required")
	}
	if strings.TrimSpace(m.ProviderKey) == "" {
		return m, fmt.Errorf("embedding manifest provider key is required")
	}
	if strings.TrimSpace(m.Model) == "" || strings.TrimSpace(m.ModelVersion) == "" {
		return m, fmt.Errorf("embedding manifest model and model version are required")
	}
	if m.Dimensions != DefaultEmbeddingDimensions {
		return m, fmt.Errorf("embedding manifest dimensions must be %d", DefaultEmbeddingDimensions)
	}
	if m.Tokenizer == nil {
		m.Tokenizer = NewWordTokenizer()
	}
	if m.MaxTokens <= 0 {
		m.MaxTokens = MaxChunkTokens
	}
	return m, nil
}

// Fingerprint derives the manifest identity compared against profiles and
// worker heartbeats: provider key, model, version, dimensions, tokenizer and
// the fixed cosine metric.
func (m EmbeddingManifest) Fingerprint() string {
	tokenizer := TokenizerV1
	if m.Tokenizer != nil {
		tokenizer = m.Tokenizer.Name()
	}
	joined := strings.Join([]string{
		m.ProviderKey, m.Model, m.ModelVersion,
		fmt.Sprint(m.Dimensions), tokenizer, "cosine",
	}, "|")
	sum := sha256Sum([]byte(joined))
	return sum
}

// EmbeddingProvider separates document and query embedding so models that
// need different prompts stay expressible (doc §7.6).
type EmbeddingProvider interface {
	Manifest() EmbeddingManifest
	EmbedDocuments(ctx context.Context, texts []string) ([][]float32, error)
	EmbedQuery(ctx context.Context, text string) ([]float32, error)
}

// ValidateEmbeddingResponse enforces count, dimension and finiteness on a
// provider response.
func ValidateEmbeddingResponse(vectors [][]float32, wantCount, wantDimensions int) error {
	if len(vectors) != wantCount {
		return fmt.Errorf("provider returned %d embeddings, want %d", len(vectors), wantCount)
	}
	for i, vector := range vectors {
		if len(vector) != wantDimensions {
			return fmt.Errorf("provider embedding %d has dimension %d, want %d", i, len(vector), wantDimensions)
		}
		for j, value := range vector {
			if math.IsNaN(float64(value)) || math.IsInf(float64(value), 0) {
				return fmt.Errorf("provider embedding %d/%d is not finite", i, j)
			}
		}
	}
	return nil
}

// HTTPEmbeddingProvider calls a deployment-side embedding service. Protocol
// support: generic ({"inputs": [...]}), openai ({"model","input"}) and
// aliyun-multimodal; response parsing accepts embeddings/data/output shapes.
type HTTPEmbeddingProvider struct {
	manifest    EmbeddingManifest
	endpoint    string
	token       string
	protocol    string
	callTimeout time.Duration
	jobTimeout  time.Duration
	client      *http.Client
}

// NewHTTPEmbeddingProvider builds the HTTP provider. allowHosts constrains
// the endpoint to an explicit HTTPS allowlist (SSRF guard).
func NewHTTPEmbeddingProvider(manifest EmbeddingManifest, endpoint, token, protocol string, allowHosts []string) (*HTTPEmbeddingProvider, error) {
	normalized, err := manifest.Normalize()
	if err != nil {
		return nil, err
	}
	endpoint = strings.TrimSpace(endpoint)
	if err := ValidateProviderEndpoint(endpoint, allowHosts); err != nil {
		return nil, err
	}
	return &HTTPEmbeddingProvider{
		manifest:    normalized,
		endpoint:    endpoint,
		token:       token,
		protocol:    strings.TrimSpace(protocol),
		callTimeout: EmbedSingleCallTimeout,
		jobTimeout:  EmbedJobTimeout,
		client:      &http.Client{Timeout: EmbedSingleCallTimeout},
	}, nil
}

// Manifest implements EmbeddingProvider.
func (p *HTTPEmbeddingProvider) Manifest() EmbeddingManifest { return p.manifest }

// EmbedDocuments implements EmbeddingProvider. The caller is responsible for
// splitting work into batches of at most EmbeddingBatchLimit texts.
func (p *HTTPEmbeddingProvider) EmbedDocuments(ctx context.Context, texts []string) ([][]float32, error) {
	return p.embed(ctx, texts)
}

// EmbedQuery implements EmbeddingProvider.
func (p *HTTPEmbeddingProvider) EmbedQuery(ctx context.Context, text string) ([]float32, error) {
	vectors, err := p.embed(ctx, []string{text})
	if err != nil {
		return nil, err
	}
	return vectors[0], nil
}

func (p *HTTPEmbeddingProvider) embed(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}
	if len(texts) > EmbeddingBatchLimit {
		return nil, terminalProviderError("embedding_batch_too_large",
			fmt.Errorf("batch of %d exceeds the limit of %d", len(texts), EmbeddingBatchLimit))
	}
	payload := p.requestPayload(texts)
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, terminalProviderError("embedding_request_encode", err)
	}
	callCtx := ctx
	var cancel context.CancelFunc
	if p.jobTimeout > 0 {
		callCtx, cancel = context.WithTimeout(ctx, p.jobTimeout)
		defer cancel()
	}
	req, err := http.NewRequestWithContext(callCtx, http.MethodPost, p.endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, terminalProviderError("embedding_request_invalid", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if p.token != "" {
		req.Header.Set("Authorization", "Bearer "+p.token)
	}
	client := p.client
	if client == nil {
		client = &http.Client{Timeout: p.callTimeout}
	}
	resp, err := client.Do(req)
	if err != nil {
		// Timeouts and transport failures are retryable.
		return nil, retryableProviderError("embedding_transport_error", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
		return nil, retryableProviderError("embedding_provider_status",
			fmt.Errorf("embedding provider returned %s", resp.Status))
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, terminalProviderError("embedding_provider_status",
			fmt.Errorf("embedding provider returned %s", resp.Status))
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return nil, retryableProviderError("embedding_response_read", err)
	}
	vectors, err := parseEmbeddingPayload(raw)
	if err != nil {
		return nil, terminalProviderError("embedding_response_invalid", err)
	}
	if err := ValidateEmbeddingResponse(vectors, len(texts), p.manifest.Dimensions); err != nil {
		return nil, terminalProviderError("embedding_response_invalid", err)
	}
	return vectors, nil
}

func (p *HTTPEmbeddingProvider) requestPayload(texts []string) any {
	switch strings.ToLower(p.protocol) {
	case "aliyun-multimodal":
		contents := make([]map[string]string, len(texts))
		for i, text := range texts {
			contents[i] = map[string]string{"text": text}
		}
		return map[string]any{
			"model": p.manifest.Model,
			"input": map[string]any{"contents": contents},
			"parameters": map[string]any{
				"dimension":   p.manifest.Dimensions,
				"output_type": "dense",
			},
		}
	case "openai":
		return map[string]any{"model": p.manifest.Model, "input": texts}
	default:
		return map[string]any{"inputs": texts, "model": p.manifest.Model}
	}
}

// parseEmbeddingPayload accepts the common embedding response shapes:
// [[...]], {"embeddings":[...]}, {"data":[{"embedding":[...]}]} and the
// aliyun {"output":{"embeddings":[...]}} envelope.
func parseEmbeddingPayload(raw []byte) ([][]float32, error) {
	var decoded any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return nil, fmt.Errorf("embedding response is not JSON: %w", err)
	}
	if obj, ok := decoded.(map[string]any); ok {
		if output, found := obj["output"].(map[string]any); found {
			if v, exists := output["embeddings"]; exists {
				decoded = v
			}
		} else if v, found := obj["embeddings"]; found {
			decoded = v
		} else if v, found := obj["data"]; found {
			decoded = v
		}
	}
	rows, ok := decoded.([]any)
	if !ok {
		return nil, fmt.Errorf("embedding response must contain an array")
	}
	out := make([][]float32, len(rows))
	for i, row := range rows {
		if obj, ok := row.(map[string]any); ok {
			if embedding, found := obj["embedding"]; found {
				row = embedding
			}
		}
		values, ok := row.([]any)
		if !ok {
			return nil, fmt.Errorf("embedding row %d is not an array", i)
		}
		vector := make([]float32, len(values))
		for j, value := range values {
			num, ok := value.(float64)
			if !ok {
				return nil, fmt.Errorf("embedding value %d/%d is not numeric", i, j)
			}
			vector[j] = float32(num)
		}
		out[i] = vector
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// Reranker contract (independent, optional manifest)
// ---------------------------------------------------------------------------

// RerankerManifest is the independent reranker identity.
type RerankerManifest struct {
	Key          string
	ProviderKey  string
	Model        string
	ModelVersion string
}

// Fingerprint derives the reranker identity used in query audit rows.
func (m RerankerManifest) Fingerprint() string {
	return sha256Sum([]byte(strings.Join([]string{m.ProviderKey, m.Model, m.ModelVersion}, "|")))
}

// RerankerProvider scores documents against one query. Candidates sent to a
// reranker must already have passed candidate authorization.
type RerankerProvider interface {
	Manifest() RerankerManifest
	Rerank(ctx context.Context, query string, documents []string) ([]float32, error)
}

// HTTPRerankerProvider calls a deployment-side reranker service.
type HTTPRerankerProvider struct {
	manifest RerankerManifest
	endpoint string
	token    string
	protocol string
	timeout  time.Duration
	client   *http.Client
}

// NewHTTPRerankerProvider builds the HTTP reranker client.
func NewHTTPRerankerProvider(manifest RerankerManifest, endpoint, token, protocol string, allowHosts []string) (*HTTPRerankerProvider, error) {
	if strings.TrimSpace(manifest.Key) == "" || strings.TrimSpace(manifest.ProviderKey) == "" ||
		strings.TrimSpace(manifest.ModelVersion) == "" {
		return nil, fmt.Errorf("reranker manifest is incomplete")
	}
	endpoint = strings.TrimSpace(endpoint)
	if err := ValidateProviderEndpoint(endpoint, allowHosts); err != nil {
		return nil, err
	}
	return &HTTPRerankerProvider{
		manifest: manifest,
		endpoint: endpoint,
		token:    token,
		protocol: strings.TrimSpace(protocol),
		timeout:  time.Second,
		client:   &http.Client{Timeout: time.Second},
	}, nil
}

// Manifest implements RerankerProvider.
func (p *HTTPRerankerProvider) Manifest() RerankerManifest { return p.manifest }

// Rerank implements RerankerProvider. Scores must cover every candidate.
func (p *HTTPRerankerProvider) Rerank(ctx context.Context, query string, documents []string) ([]float32, error) {
	if len(documents) == 0 {
		return nil, nil
	}
	var payload any = map[string]any{
		"query":     query,
		"documents": documents,
	}
	endpoint := p.endpoint
	if strings.EqualFold(p.protocol, "aliyun") {
		payload = map[string]any{
			"model": p.manifest.ModelVersion,
			"input": map[string]any{"query": query, "documents": documents},
			"parameters": map[string]any{
				"return_documents": false,
				"top_n":            len(documents),
			},
		}
	} else {
		endpoint = strings.TrimRight(endpoint, "/") + "/rerank"
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, terminalProviderError("rerank_request_encode", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, terminalProviderError("rerank_request_invalid", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if p.token != "" {
		req.Header.Set("Authorization", "Bearer "+p.token)
	}
	client := p.client
	if client == nil {
		client = &http.Client{Timeout: p.timeout}
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, retryableProviderError("rerank_transport_error", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
		return nil, retryableProviderError("rerank_provider_status",
			fmt.Errorf("reranker returned %s", resp.Status))
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, terminalProviderError("rerank_provider_status",
			fmt.Errorf("reranker returned %s", resp.Status))
	}
	var decoded struct {
		Scores  []float32 `json:"scores"`
		Results []struct {
			Index float64 `json:"index"`
			Score float32 `json:"relevance_score"`
		} `json:"results"`
		Output *struct {
			Results []struct {
				Index float64 `json:"index"`
				Score float32 `json:"relevance_score"`
			} `json:"results"`
		} `json:"output"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		return nil, terminalProviderError("rerank_response_invalid", err)
	}
	if len(decoded.Scores) > 0 {
		if len(decoded.Scores) != len(documents) {
			return nil, terminalProviderError("rerank_response_invalid",
				fmt.Errorf("reranker returned %d scores for %d candidates", len(decoded.Scores), len(documents)))
		}
		return decoded.Scores, nil
	}
	results := decoded.Results
	if decoded.Output != nil {
		results = decoded.Output.Results
	}
	scores := make([]float32, len(documents))
	seen := make([]bool, len(documents))
	for _, item := range results {
		index := int(item.Index)
		if index < 0 || index >= len(scores) || seen[index] {
			return nil, terminalProviderError("rerank_response_invalid",
				fmt.Errorf("reranker returned invalid candidate index %d", index))
		}
		scores[index] = item.Score
		seen[index] = true
	}
	for i, ok := range seen {
		if !ok {
			return nil, terminalProviderError("rerank_response_invalid",
				fmt.Errorf("reranker omitted candidate index %d", i))
		}
	}
	return scores, nil
}
