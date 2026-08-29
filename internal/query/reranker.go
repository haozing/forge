package query

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// RerankCandidate is one scope-filtered candidate sent to the reranker. Only
// the query text and the chunk content leave the process — never identifiers
// a provider could echo back into results.
type RerankCandidate struct {
	ID   string
	Text string
}

// Reranker reorders the top fused candidates. Implementations must return one
// score per candidate in candidate order or an error; the caller then keeps
// the RRF order and marks the response degraded (doc §10.5).
type Reranker interface {
	Rerank(ctx context.Context, query string, candidates []RerankCandidate) ([]float64, error)
}

// HTTPReranker calls a deployment-side reranking endpoint. Protocol support:
// aliyun ({"model","input"}) and the generic {"query","candidates"} shape.
type HTTPReranker struct {
	Endpoint     string
	Token        string
	ModelVersion string
	Protocol     string
	Timeout      time.Duration
}

func (r HTTPReranker) Rerank(ctx context.Context, query string, candidates []RerankCandidate) ([]float64, error) {
	if strings.TrimSpace(r.Endpoint) == "" {
		return nil, fmt.Errorf("reranker endpoint is not configured")
	}
	var payloadValue any = map[string]any{"query": query, "candidates": candidates, "model_version": r.ModelVersion, "protocol_version": "r3"}
	if strings.EqualFold(strings.TrimSpace(r.Protocol), "aliyun") {
		documents := make([]string, len(candidates))
		for i, candidate := range candidates {
			documents[i] = candidate.Text
		}
		payloadValue = map[string]any{
			"model": r.ModelVersion,
			"input": map[string]any{
				"query":     query,
				"documents": documents,
			},
			"parameters": map[string]any{"return_documents": true, "top_n": len(candidates)},
		}
	}
	payload, err := json.Marshal(payloadValue)
	if err != nil {
		return nil, err
	}
	endpoint := strings.TrimSpace(r.Endpoint)
	if !strings.EqualFold(strings.TrimSpace(r.Protocol), "aliyun") {
		endpoint = strings.TrimRight(endpoint, "/") + "/rerank"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if r.Token != "" {
		req.Header.Set("Authorization", "Bearer "+r.Token)
	}
	client := &http.Client{Timeout: r.Timeout}
	if client.Timeout <= 0 {
		client.Timeout = time.Second
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("reranker returned %s", resp.Status)
	}
	var raw struct {
		Scores []float64 `json:"scores"`
		Output *struct {
			Results []struct {
				Index          int     `json:"index"`
				RelevanceScore float64 `json:"relevance_score"`
			} `json:"results"`
		} `json:"output"`
		Results []struct {
			Index          int     `json:"index"`
			RelevanceScore float64 `json:"relevance_score"`
		} `json:"results"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, err
	}
	if len(raw.Scores) > 0 {
		if len(raw.Scores) != len(candidates) {
			return nil, fmt.Errorf("reranker returned %d scores for %d candidates", len(raw.Scores), len(candidates))
		}
		return raw.Scores, nil
	}
	results := raw.Results
	if raw.Output != nil {
		results = raw.Output.Results
	}
	scores := make([]float64, len(candidates))
	seen := make([]bool, len(candidates))
	for _, item := range results {
		if item.Index < 0 || item.Index >= len(scores) {
			return nil, fmt.Errorf("reranker returned invalid result index %d", item.Index)
		}
		scores[item.Index] = item.RelevanceScore
		seen[item.Index] = true
	}
	for i := range seen {
		if !seen[i] {
			return nil, fmt.Errorf("reranker omitted candidate index %d", i)
		}
	}
	return scores, nil
}
