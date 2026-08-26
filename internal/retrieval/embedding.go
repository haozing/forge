package retrieval

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"strings"
	"time"
)

type EmbeddingProvider interface {
	Embed(context.Context, []string) ([][]float64, error)
}

type HashEmbeddingProvider struct{ Dimensions int }

func (p HashEmbeddingProvider) Embed(_ context.Context, texts []string) ([][]float64, error) {
	d := p.Dimensions
	if d <= 0 {
		d = DefaultEmbeddingDimensions
	}
	out := make([][]float64, len(texts))
	for i, text := range texts {
		v := make([]float64, d)
		for block := 0; block < d; block += 32 {
			sum := sha256.Sum256([]byte(fmt.Sprintf("%d:%s", block, text)))
			for j := 0; j < 32 && block+j < d; j++ {
				v[block+j] = float64(int(sum[j])-128) / 128
			}
		}
		var norm float64
		for _, x := range v {
			norm += x * x
		}
		norm = math.Sqrt(norm)
		if norm > 0 {
			for j := range v {
				v[j] /= norm
			}
		}
		out[i] = v
	}
	return out, nil
}

type HTTPEmbeddingProvider struct {
	Endpoint  string
	Token     string
	Model     string
	Protocol  string
	Dimension int
	Timeout   time.Duration
}

func (p HTTPEmbeddingProvider) Embed(ctx context.Context, texts []string) ([][]float64, error) {
	if strings.TrimSpace(p.Endpoint) == "" {
		return nil, fmt.Errorf("embedding endpoint is not configured")
	}
	payload := any(map[string]any{"inputs": texts, "model": p.Model})
	if strings.EqualFold(strings.TrimSpace(p.Protocol), "aliyun-multimodal") {
		contents := make([]map[string]string, len(texts))
		for i, text := range texts {
			contents[i] = map[string]string{"text": text}
		}
		dimension := p.Dimension
		if dimension <= 0 {
			dimension = DefaultEmbeddingDimensions
		}
		payload = map[string]any{
			"model":      p.Model,
			"input":      map[string]any{"contents": contents},
			"parameters": map[string]any{"dimension": dimension, "output_type": "dense"},
		}
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.Endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if p.Token != "" {
		req.Header.Set("Authorization", "Bearer "+p.Token)
	}
	client := &http.Client{Timeout: p.Timeout}
	if client.Timeout <= 0 {
		client.Timeout = 5 * time.Second
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("embedding provider returned %s", resp.Status)
	}
	var raw any
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, err
	}
	return parseEmbeddings(raw)
}
func parseEmbeddings(raw any) ([][]float64, error) {
	if obj, ok := raw.(map[string]any); ok {
		if output, found := obj["output"].(map[string]any); found {
			if v, exists := output["embeddings"]; exists {
				raw = v
			}
		} else if v, found := obj["embeddings"]; found {
			raw = v
		} else if v, found := obj["data"]; found {
			raw = v
		}
	}
	rows, ok := raw.([]any)
	if !ok {
		return nil, fmt.Errorf("embedding response must be an array")
	}
	out := make([][]float64, len(rows))
	for i, row := range rows {
		if obj, ok := row.(map[string]any); ok {
			row = obj["embedding"]
		}
		vals, ok := row.([]any)
		if !ok {
			return nil, fmt.Errorf("embedding row %d is invalid", i)
		}
		out[i] = make([]float64, len(vals))
		for j, val := range vals {
			num, ok := val.(float64)
			if !ok {
				return nil, fmt.Errorf("embedding value %d/%d is invalid", i, j)
			}
			out[i][j] = num
		}
	}
	return out, nil
}
