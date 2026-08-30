package httpapi

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"agentchunzhi/internal/auth"
	agentquery "agentchunzhi/internal/query"
	"agentchunzhi/internal/site"
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
	files := []string{"v2_handlers.go", "router_groups.go", "v2_identity.go", "v2_organization.go", "v2_tag.go", "v2_sites.go", "v2_public_sites.go"}
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

// TestLegacyPublicAssetRoutesAreRetired pins the phase 5 retirement of the
// old public asset face (ledger row planned→retired): the registrations are
// gone from the router source and the paths answer the mux's bare 404
// without a redirect or a compatibility hint.
func TestLegacyPublicAssetRoutesAreRetired(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(".", "router_groups.go"))
	if err != nil {
		t.Fatal(err)
	}
	source := string(raw)
	for _, legacy := range []string{
		`"/api/public/workspaces/{workspaceId}/assets"`,
		`"/api/public/assets/{assetId}"`,
	} {
		if strings.Contains(source, legacy) {
			t.Fatalf("retired route %s must not be registered", legacy)
		}
	}
	// The legacy handler file itself must be gone.
	if _, err := os.Stat(filepath.Join(".", "public_assets.go")); err == nil {
		t.Fatal("public_assets.go must be deleted with its routes")
	}
	router := newRouter(Dependencies{})
	for _, path := range []string{
		"/api/public/workspaces/11111111-1111-1111-1111-111111111111/assets",
		"/api/public/assets/11111111-1111-1111-1111-111111111111",
	} {
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
		if recorder.Code != http.StatusNotFound {
			t.Fatalf("retired route %s answered %d, want 404", path, recorder.Code)
		}
	}
}

// TestPublicSiteV2RoutesRegistered pins the phase 5 public read face: the
// seven routes exist (an unwired service answers 500, never the mux 404) and
// a bare GET without a session is not bounced by authentication middleware.
func TestPublicSiteV2RoutesRegistered(t *testing.T) {
	router := newRouter(Dependencies{})
	for _, path := range []string{
		"/api/public/v2/sites/blog",
		"/api/public/v2/sites/blog/posts",
		"/api/public/v2/sites/blog/posts/some/path",
		"/api/public/v2/sites/blog/posts/nested/second",
		"/api/public/v2/sites/blog/sections/news",
		"/api/public/v2/sites/blog/tags",
		"/api/public/v2/sites/blog/tags/go",
		"/api/public/v2/sites/blog/search",
	} {
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
		if recorder.Code == http.StatusNotFound {
			t.Fatalf("route %s must be registered", path)
		}
		if strings.Contains(recorder.Body.String(), "404 page not found") {
			t.Fatalf("route %s fell through to the mux default: %s", path, recorder.Body.String())
		}
	}
}

// TestPublicFaceNeverRequiresIdempotencyKey proves the public face cannot be
// forced through the shared idempotency reservation: every public path,
// safe or otherwise, bypasses the middleware.
func TestPublicFaceNeverRequiresIdempotencyKey(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/api/public/v2/sites/blog/posts", nil)
	if requiresHTTPIdempotency(request) {
		t.Fatal("public GET must bypass the idempotency middleware")
	}
	request = httptest.NewRequest(http.MethodPost, "/api/public/v2/sites/blog/posts", nil)
	request.Header.Set("Idempotency-Key", "0123456789abcdef")
	if requiresHTTPIdempotency(request) {
		t.Fatal("public writes (hypothetical) must bypass the idempotency middleware")
	}
}

// TestPublicSiteErrorContract pins the wire mapping of the public read face:
// missing/disabled/malformed targets collapse into one 404, the throttle
// refusal answers 429 with Retry-After, query contract errors keep their
// fixed status/code, and infrastructure failures answer 500.
func TestPublicSiteErrorContract(t *testing.T) {
	throttled := &site.PublicRateLimitError{RetryAfter: 90 * time.Second}
	recorder := httptest.NewRecorder()
	writePublicSiteError(recorder, throttled)
	if recorder.Code != http.StatusTooManyRequests {
		t.Fatalf("throttle refusal = %d, want 429", recorder.Code)
	}
	if recorder.Header().Get("Retry-After") != "90" {
		t.Fatalf("Retry-After = %q, want 90", recorder.Header().Get("Retry-After"))
	}
	if !strings.Contains(recorder.Body.String(), `"code":"rate_limited"`) {
		t.Fatalf("body = %s, want rate_limited", recorder.Body.String())
	}
	for _, tc := range []struct {
		err  error
		code string
	}{
		{site.ErrSiteNotFound, "site_not_found"},
		{site.ErrSiteDisabled, "site_not_found"},
		{site.ErrPathInvalid, "site_not_found"},
		{site.ErrPublicThrottleUnavailable, "database_unavailable"},
		{agentquery.ErrPublicSiteContentUnavailable, "site_content_unavailable"},
		{agentquery.ErrInvalidTagFilter, "invalid_tag_filter"},
		{agentquery.ErrCursorInvalid, "invalid_cursor"},
	} {
		recorder := httptest.NewRecorder()
		writePublicSiteError(recorder, tc.err)
		if !strings.Contains(recorder.Body.String(), fmt.Sprintf(`"code":%q`, tc.code)) {
			t.Fatalf("error %v: body = %s, want code %q (status %d)", tc.err, recorder.Body.String(), tc.code, recorder.Code)
		}
	}
	recorder = httptest.NewRecorder()
	writePublicSiteError(recorder, errors.New("boom"))
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("unknown error = %d, want 500", recorder.Code)
	}
}

// TestPublicCacheHeaders304Branch pins the conditional-GET behavior of the
// public face: a matching If-None-Match writes 304 (validator + cache policy
// kept, no payload), a mismatch falls through to the normal 200 path.
func TestPublicCacheHeaders304Branch(t *testing.T) {
	etag := "feedc0de"
	request := httptest.NewRequest(http.MethodGet, "/api/public/v2/sites/blog", nil)
	request.Header.Set("If-None-Match", `"`+etag+`"`)
	recorder := httptest.NewRecorder()
	if publicCacheHeaders(recorder, request, etag) != true {
		t.Fatal("a matching If-None-Match must short-circuit")
	}
	if recorder.Code != http.StatusNotModified {
		t.Fatalf("status = %d, want 304", recorder.Code)
	}
	if recorder.Header().Get("ETag") != `"`+etag+`"` {
		t.Fatalf("ETag = %q, want the quoted validator", recorder.Header().Get("ETag"))
	}
	if recorder.Header().Get("Cache-Control") != site.PublicCacheControl {
		t.Fatalf("Cache-Control = %q, want the D4 policy", recorder.Header().Get("Cache-Control"))
	}
	if recorder.Body.Len() != 0 {
		t.Fatal("a 304 answer must not carry a body")
	}

	request = httptest.NewRequest(http.MethodGet, "/api/public/v2/sites/blog", nil)
	recorder = httptest.NewRecorder()
	if publicCacheHeaders(recorder, request, etag) != false {
		t.Fatal("without If-None-Match the handler must fall through to 200")
	}
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want implicit 200", recorder.Code)
	}
}

// TestPublicSiteHandlersGuardWiring proves an unwired PublicReader answers a
// deterministic 500 (bootstrapping defect) rather than a misleading 404.
func TestPublicSiteHandlersGuardWiring(t *testing.T) {
	deps := Dependencies{SessionService: auth.SessionService{}}
	router := newRouter(deps)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/public/v2/sites/blog", nil))
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("unwired public reader = %d, want 500", recorder.Code)
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
		// Phase 5 public site face: safe reads never take part in the replay
		// contract — not even a hypothetical write under the public prefix.
		{http.MethodGet, "/api/public/v2/sites/blog", false},
		{http.MethodGet, "/api/public/v2/sites/blog/posts", false},
		{http.MethodGet, "/api/public/v2/sites/blog/posts/some/path", false},
		{http.MethodGet, "/api/public/v2/sites/blog/search", false},
		{http.MethodPost, "/api/public/v2/sites/blog/posts", false},
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
