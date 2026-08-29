package httpapi

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestErrorEnvelopeShape(t *testing.T) {
	recorder := httptest.NewRecorder()
	requestIDWriterInstance := requestIDWriter{ResponseWriter: recorder, id: "req-test-1"}
	writeError(requestIDWriterInstance, http.StatusConflict, "state_conflict")
	body := recorder.Body.String()
	for _, fragment := range []string{`"error"`, `"code":"state_conflict"`, `"request_id":"req-test-1"`} {
		if !strings.Contains(body, fragment) {
			t.Fatalf("error envelope missing %s: %s", fragment, body)
		}
	}
	if recorder.Code != http.StatusConflict {
		t.Fatalf("status = %d", recorder.Code)
	}
}

func TestRequestIDMiddlewareEchoesAndPropagates(t *testing.T) {
	handler := withRequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if RequestIDFromContext(r.Context()) == "" {
			t.Fatal("request id must be in context")
		}
		w.WriteHeader(http.StatusOK)
	}))
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if recorder.Header().Get("X-Request-Id") == "" {
		t.Fatal("response must echo the request id")
	}

	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if recorder.Header().Get("X-Request-Id") == "" {
		t.Fatal("missing inbound header must generate an id")
	}
}

// TestV2HandlersNeverTouchStore is the architecture guard: the v2 handler
// files must depend on domain services only, never on the raw store.
func TestV2HandlersNeverTouchStore(t *testing.T) {
	files := []string{"v2_handlers.go", "router_groups.go"}
	for _, name := range files {
		raw, err := os.ReadFile(filepath.Join(".", name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if forbidden := regexp.MustCompile(`deps\.Store|\.Pool\.`); forbidden.Match(raw) {
			t.Fatalf("%s must not access the raw store from handlers", name)
		}
	}
}

func TestLegacyReviewRoutesAreRetired(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(".", "router_groups.go"))
	if err != nil {
		t.Fatal(err)
	}
	source := string(raw)
	for _, legacy := range []string{
		`"/api/frontend/workspaces/{workspaceId}/reviews"`,
		`"/api/frontend/reviews/{reviewId}"`,
		`"/api/frontend/reviews/batch"`,
		`"/api/frontend/assets/{assetId}/submit-review"`,
	} {
		if strings.Contains(source, legacy) {
			t.Fatalf("retired route %s must not be registered", legacy)
		}
	}
	for _, required := range []string{
		`"/api/v2/workspaces/{workspaceId}/publication-requests"`,
		`"/api/v2/assets/{assetId}/draft"`,
		`"/api/v2/assets/{assetId}/commit-draft"`,
		`"/api/v2/asset-versions/{versionId}/confirm"`,
	} {
		if !strings.Contains(source, required) {
			t.Fatalf("v2 route %s must be registered", required)
		}
	}
}

func TestIdempotencyKeyRequiredForV2Writes(t *testing.T) {
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/v2/assets/x/publish", nil)
	if _, ok := requireIdempotencyKeyV2(recorder, request); ok {
		t.Fatal("missing Idempotency-Key must be refused")
	}
	if recorder.Code != http.StatusPreconditionRequired {
		t.Fatalf("status = %d, want 428", recorder.Code)
	}
	if !strings.Contains(recorder.Body.String(), "idempotency_key_required") {
		t.Fatalf("body = %s", recorder.Body.String())
	}
}
