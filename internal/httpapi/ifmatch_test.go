package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestIfMatchWildcardDetection covers the "*" precondition parsing: the
// wildcard (bare or ETag-quoted) means "the resource exists is enough", every
// other token keeps the revision equality check.
func TestIfMatchWildcardDetection(t *testing.T) {
	for _, value := range []string{"*", `"*"`} {
		if !ifMatchWildcard(value) {
			t.Fatalf("If-Match %q must be detected as the wildcard", value)
		}
	}
	for _, value := range []string{"", `"12"`, "12", "**", `"W/"12""`} {
		if ifMatchWildcard(value) {
			t.Fatalf("If-Match %q must not be detected as the wildcard", value)
		}
	}
}

// TestRequireIfMatchPassesWildcardThrough: the guard only enforces presence;
// the wildcard value flows to the comparison sites unchanged.
func TestRequireIfMatchPassesWildcardThrough(t *testing.T) {
	request := httptest.NewRequest(http.MethodPatch, "/api/me", nil)
	request.Header.Set("If-Match", `"*"`)
	value, ok := requireIfMatch(httptest.NewRecorder(), request)
	if !ok || value != `"*"` {
		t.Fatalf("wildcard If-Match must pass through, got %q ok=%v", value, ok)
	}
	if !ifMatchWildcard(value) {
		t.Fatal("the passed-through value must be recognized by ifMatchWildcard")
	}
	missing := httptest.NewRequest(http.MethodPatch, "/api/me", nil)
	recorder := httptest.NewRecorder()
	if _, ok := requireIfMatch(recorder, missing); ok {
		t.Fatal("missing If-Match must be refused")
	}
	if recorder.Code != http.StatusPreconditionRequired || !strings.Contains(recorder.Body.String(), "precondition_required") {
		t.Fatalf("guard status = %d body = %s", recorder.Code, recorder.Body.String())
	}
}
