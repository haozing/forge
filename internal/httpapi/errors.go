package httpapi

// errors.go — the single v2 HTTP contract: request ids are injected by
// middleware, every error uses the stable envelope
// {"error":{"code","message","request_id","details"}} and every v2 list uses
// the cursor page envelope. Handlers never generate request ids themselves.

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

type requestIDContextKey struct{}

// RequestIDFromContext returns the middleware-assigned request id.
func RequestIDFromContext(ctx context.Context) string {
	if value, ok := ctx.Value(requestIDContextKey{}).(string); ok {
		return value
	}
	return ""
}

// requestIDWriter exposes the request id to helpers that only receive the
// ResponseWriter.
type requestIDWriter struct {
	http.ResponseWriter
	id string
}

func (w requestIDWriter) RequestID() string { return w.id }

type requestIDCarrier interface{ RequestID() string }

func requestIDFromWriter(w http.ResponseWriter) string {
	if carrier, ok := w.(requestIDCarrier); ok {
		return carrier.RequestID()
	}
	return ""
}

// generateRequestID produces an opaque id without timestamp semantics callers
// could mistake for ordering guarantees.
func generateRequestID() string {
	raw := make([]byte, 12)
	if _, err := rand.Read(raw); err != nil {
		return time.Now().UTC().Format("20060102T150405.000000000Z07:00")
	}
	return hex.EncodeToString(raw)
}

// withRequestID assigns the correlation id, echoes it in the response header
// and stores it for logging, audit and error envelopes.
func withRequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimSpace(r.Header.Get("X-Request-Id"))
		if id == "" || len(id) > 64 {
			id = generateRequestID()
		}
		w.Header().Set("X-Request-Id", id)
		next.ServeHTTP(requestIDWriter{ResponseWriter: w, id: id}, r.WithContext(context.WithValue(r.Context(), requestIDContextKey{}, id)))
	})
}

// writeJSON encodes value as the response body.
func writeJSONValue(w http.ResponseWriter, status int, value any) {
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

// errorBody is the fixed v2 error envelope.
type errorBody struct {
	Error errorDetail `json:"error"`
}

type errorDetail struct {
	Code      string         `json:"code"`
	Message   string         `json:"message"`
	RequestID string         `json:"request_id"`
	Details   map[string]any `json:"details,omitempty"`
}

// writeError emits the v2 error envelope. Kept compatible with the legacy
// call signature so handlers migrate mechanically.
func writeError(w http.ResponseWriter, status int, code string) {
	writeJSONValue(w, status, errorBody{Error: errorDetail{
		Code:      code,
		Message:   http.StatusText(status),
		RequestID: requestIDFromWriter(w),
	}})
}

// writeErrorDetail emits the envelope with structured details.
func writeErrorDetail(w http.ResponseWriter, status int, code, message string, details map[string]any) {
	writeJSONValue(w, status, errorBody{Error: errorDetail{
		Code:      code,
		Message:   message,
		RequestID: requestIDFromWriter(w),
		Details:   details,
	}})
}

// writeData emits the v2 success envelope {"data":..., "request_id":...}.
func writeData(w http.ResponseWriter, r *http.Request, status int, data any) {
	writeJSONValue(w, status, map[string]any{
		"data":       data,
		"request_id": RequestIDFromContext(r.Context()),
	})
}

// CursorPage is the only list envelope of the v2 contract.
type CursorPage struct {
	NextCursor *string `json:"next_cursor"`
	HasMore    bool    `json:"has_more"`
}

// pageFrom returns the cursor envelope for a fetched page of limit+1 items.
func pageFrom(count, limit int, next string) CursorPage {
	if count <= limit || next == "" {
		return CursorPage{NextCursor: nil, HasMore: false}
	}
	value := next
	return CursorPage{NextCursor: &value, HasMore: true}
}
