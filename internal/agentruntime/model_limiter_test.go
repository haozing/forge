package agentruntime

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type modelRoundTripFunc func(*http.Request) (*http.Response, error)

func (f modelRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func TestModelLimitedRoundTripperReleasesOnResponseEOF(t *testing.T) {
	var calls atomic.Int32
	transport := modelLimitedRoundTripper{
		limiter: NewModelRequestLimiter(1),
		base: modelRoundTripFunc(func(*http.Request) (*http.Response, error) {
			calls.Add(1)
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader("ok"))}, nil
		}),
	}
	request, _ := http.NewRequest(http.MethodPost, "https://api.openai.com/v1/responses", nil)
	response, err := transport.RoundTrip(request)
	if err != nil {
		t.Fatalf("first model request: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	blocked, _ := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.openai.com/v1/responses", nil)
	if _, err := transport.RoundTrip(blocked); err == nil {
		t.Fatal("second model request should wait for the concurrency slot")
	}
	if calls.Load() != 1 {
		t.Fatalf("upstream calls while blocked = %d, want 1", calls.Load())
	}

	if _, err := io.ReadAll(response.Body); err != nil {
		t.Fatalf("read first response: %v", err)
	}
	third, _ := http.NewRequest(http.MethodPost, "https://api.openai.com/v1/responses", nil)
	thirdResponse, err := transport.RoundTrip(third)
	if err != nil {
		t.Fatalf("third model request after EOF: %v", err)
	}
	thirdResponse.Body.Close()
	if calls.Load() != 2 {
		t.Fatalf("upstream calls after release = %d, want 2", calls.Load())
	}
}
