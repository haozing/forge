package query

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestHTTPRerankerAliyunTextPayload(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
			t.Fatalf("Authorization = %q", got)
		}
		var payload struct {
			Model string `json:"model"`
			Input struct {
				Query     string   `json:"query"`
				Documents []string `json:"documents"`
			} `json:"input"`
			Parameters struct {
				ReturnDocuments bool `json:"return_documents"`
				TopN            int  `json:"top_n"`
			} `json:"parameters"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode payload: %v", err)
		}
		if payload.Model != "qwen3-vl-rerank" || payload.Input.Query != "query" {
			t.Fatalf("unexpected model/query: %#v", payload)
		}
		if len(payload.Input.Documents) != 2 || payload.Input.Documents[0] != "first" || payload.Input.Documents[1] != "second" {
			t.Fatalf("documents = %#v", payload.Input.Documents)
		}
		if !payload.Parameters.ReturnDocuments || payload.Parameters.TopN != 2 {
			t.Fatalf("parameters = %#v", payload.Parameters)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"output":{"results":[{"index":0,"relevance_score":0.9},{"index":1,"relevance_score":0.2}]}}`))
	}))
	defer server.Close()

	scores, err := (HTTPReranker{
		Endpoint: server.URL, Token: "test-token", ModelVersion: "qwen3-vl-rerank",
		Protocol: "aliyun", Timeout: time.Second,
	}).Rerank(context.Background(), "query", []RerankCandidate{{Text: "first"}, {Text: "second"}})
	if err != nil {
		t.Fatalf("Rerank: %v", err)
	}
	if len(scores) != 2 || scores[0] != 0.9 || scores[1] != 0.2 {
		t.Fatalf("scores = %#v", scores)
	}
}
