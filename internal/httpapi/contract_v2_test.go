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
	files := []string{"v2_handlers.go", "router_groups.go", "v2_identity.go", "v2_organization.go", "v2_tag.go"}
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
		`"/api/v2/workspaces/{workspaceId}/assets/{assetId}/suggestions"`,
		`"/api/v2/workspaces/{workspaceId}/assets/{assetId}/suggestions/accept-batch"`,
		`"/api/v2/workspaces/{workspaceId}/assets/{assetId}/processing-results"`,
		`"/api/v2/workspaces/{workspaceId}/assets/{assetId}/prepare"`,
		`"/api/v2/workspaces/{workspaceId}/suggestions/{kind}/{suggestionId}/accept"`,
		`"/api/v2/workspaces/{workspaceId}/suggestions/{kind}/{suggestionId}/reject"`,
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

// TestIdempotencyCoverageMatrix locks the v2 replay-coverage surface: member
// v2 and open v2 writes are covered, public v2 and safe methods are not.
func TestIdempotencyCoverageMatrix(t *testing.T) {
	cases := []struct {
		method, path string
		want         bool
	}{
		{http.MethodPost, "/api/v2/workspaces/w/publication-requests", true},
		{http.MethodPatch, "/api/v2/assets/a/draft", true},
		{http.MethodPost, "/api/v2/assets/a/publish", true},
		{http.MethodPost, "/api/open/v2/hooks/assets", true},
		{http.MethodPost, "/api/open/v2/query", false},
		{http.MethodGet, "/api/v2/assets/a/draft", false},
		{http.MethodPost, "/api/public/v2/password-resets/complete", false},
		{http.MethodPost, "/api/public/v2/organization-invitations/accept", false},
		{http.MethodPost, "/api/frontend/workspaces", true},
		{http.MethodPost, "/api/v2/workspaces/w/query", false},
		// Phase 4 suggestion surface: the four write commands are covered, the
		// two queue reads are safe methods.
		{http.MethodPost, "/api/v2/workspaces/w/suggestions/field/s/accept", true},
		{http.MethodPost, "/api/v2/workspaces/w/suggestions/tag/s/reject", true},
		{http.MethodPost, "/api/v2/workspaces/w/assets/a/suggestions/accept-batch", true},
		{http.MethodPost, "/api/v2/workspaces/w/assets/a/prepare", true},
		// Phase 4 open agent tasks: create is a write command, the detail read
		// is safe.
		{http.MethodPost, "/api/open/v2/agent-tasks", true},
		{http.MethodGet, "/api/open/v2/agent-tasks/t", false},
		{http.MethodGet, "/api/v2/workspaces/w/assets/a/suggestions", false},
		{http.MethodGet, "/api/v2/workspaces/w/assets/a/processing-results", false},
	}
	for _, tc := range cases {
		request := httptest.NewRequest(tc.method, tc.path, nil)
		if got := requiresHTTPIdempotency(request); got != tc.want {
			t.Fatalf("requiresHTTPIdempotency(%s %s) = %v, want %v", tc.method, tc.path, got, tc.want)
		}
	}
}

// TestIdempotencyOperationNamespaces locks the per-surface operation keys so
// replay storage cannot collide across API families.
func TestIdempotencyOperationNamespaces(t *testing.T) {
	cases := []struct{ path, prefix string }{
		{"/api/v2/assets/a/publish", "v2.http:"},
		{"/api/open/v2/hooks/assets", "open.http:"},
		{"/api/frontend/workspaces", "frontend.http:"},
	}
	for _, tc := range cases {
		request := httptest.NewRequest(http.MethodPost, tc.path, nil)
		if got := idempotencyOperation(request); !strings.HasPrefix(got, tc.prefix) {
			t.Fatalf("idempotencyOperation(%s) = %q, want prefix %q", tc.path, got, tc.prefix)
		}
	}
}

// TestDraftPatchRequiresIfMatch: the v2 draft autosave refuses a missing
// If-Match precondition with 428.
func TestDraftPatchRequiresIfMatch(t *testing.T) {
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPatch, "/api/v2/assets/x/draft", nil)
	if _, ok := requireIfMatchV2(recorder, request); ok {
		t.Fatal("missing If-Match must be refused")
	}
	if recorder.Code != http.StatusPreconditionRequired {
		t.Fatalf("status = %d, want 428", recorder.Code)
	}
	if !strings.Contains(recorder.Body.String(), "precondition_required") {
		t.Fatalf("body = %s", recorder.Body.String())
	}
}

// TestAtoiDefaultFallsBackAndClamps: invalid and non-positive limits fall
// back to the default, oversized values are clamped instead of overflowing.
func TestAtoiDefaultFallsBackAndClamps(t *testing.T) {
	cases := []struct {
		value string
		want  int
	}{
		{"", 20},
		{"abc", 20},
		{"0", 20},
		{"-5", 20},
		{" 7", 20},
		{"7x", 20},
		{"007", 7},
		{"50", 50},
		{"200", 200},
		{"201", 200},
		{"100000", 200},
		{"99999999999999999999", 20},
	}
	for _, tc := range cases {
		if got := atoiDefault(tc.value, 20); got != tc.want {
			t.Fatalf("atoiDefault(%q) = %d, want %d", tc.value, got, tc.want)
		}
	}
}
