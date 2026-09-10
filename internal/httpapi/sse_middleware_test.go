package httpapi

// sse_middleware_test.go — the middleware chain must not strip http.Flusher:
// the SSE handlers (notification stream, agent chat) assert w.(http.Flusher)
// and degrade to stream_unavailable when a wrapper hides it. Live deployment
// testing on 2026-09-10 caught exactly that failure via requestIDWriter.

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

type flushRecorder struct {
	header  http.Header
	flushed bool
	written bool
}

func newFlushRecorder() *flushRecorder {
	return &flushRecorder{header: http.Header{}}
}

func (f *flushRecorder) Header() http.Header         { return f.header }
func (f *flushRecorder) Write(b []byte) (int, error) { f.written = true; return len(b), nil }
func (f *flushRecorder) WriteHeader(int)             {}
func (f *flushRecorder) Flush()                      { f.flushed = true }

func TestRequestIDWriterForwardsFlush(t *testing.T) {
	base := newFlushRecorder()
	var w http.ResponseWriter = requestIDWriter{ResponseWriter: base, id: "req-test"}
	flusher, ok := w.(http.Flusher)
	if !ok {
		t.Fatal("requestIDWriter must implement http.Flusher")
	}
	flusher.Flush()
	if !base.flushed {
		t.Fatal("Flush was not forwarded to the underlying writer")
	}
}

// The full default chain (request id + JSON defaults) must preserve flush
// capability for the streaming faces.
func TestSSEFlusherSurvivesMiddlewareChain(t *testing.T) {
	var sawFlusher, flushed bool
	handler := withJSONDefaults(withRequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flusher, ok := w.(http.Flusher)
		sawFlusher = ok
		if ok {
			flusher.Flush()
			flushed = true
		}
	})))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/workspaces/w/notifications/stream", nil))
	if !sawFlusher {
		t.Fatal("middleware chain stripped http.Flusher from the response writer")
	}
	if !flushed {
		t.Fatal("Flush through the middleware chain did not reach the writer")
	}
}
