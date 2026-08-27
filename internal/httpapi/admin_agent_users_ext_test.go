package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAgentAPIKeyRevokeAllRequiresMemberSession(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/api/admin/agent-users/00000000-0000-4000-8000-000000000001/api-keys/revoke-all", nil)
	req.SetPathValue("agentUserId", "00000000-0000-4000-8000-000000000001")
	req.Header.Set("Idempotency-Key", "agent-key-revoke-all-1")
	rec := httptest.NewRecorder()

	revokeAllAgentAPIKeys(Dependencies{}).ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected %d, got %d: %s", http.StatusUnauthorized, rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"code":"unauthorized"`) {
		t.Fatalf("expected unified unauthorized code, got %s", rec.Body.String())
	}
}

func TestAgentAPIKeyRevokeAllRejectsOtherMethods(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/admin/agent-users/00000000-0000-4000-8000-000000000001/api-keys/revoke-all", nil)
	req.SetPathValue("agentUserId", "00000000-0000-4000-8000-000000000001")
	rec := httptest.NewRecorder()

	revokeAllAgentAPIKeys(Dependencies{}).ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed || !strings.Contains(rec.Body.String(), "method_not_allowed") {
		t.Fatalf("expected 405 method_not_allowed, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestAgentOnboardingRequiresMemberSession(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/admin/agent-users/00000000-0000-4000-8000-000000000001/onboarding", nil)
	req.SetPathValue("agentUserId", "00000000-0000-4000-8000-000000000001")
	rec := httptest.NewRecorder()

	getAgentUserOnboarding(Dependencies{}).ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected %d, got %d: %s", http.StatusUnauthorized, rec.Code, rec.Body.String())
	}
}

func TestAgentOnboardingRejectsOtherMethods(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/api/admin/agent-users/00000000-0000-4000-8000-000000000001/onboarding", nil)
	req.SetPathValue("agentUserId", "00000000-0000-4000-8000-000000000001")
	rec := httptest.NewRecorder()

	getAgentUserOnboarding(Dependencies{}).ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed || !strings.Contains(rec.Body.String(), "method_not_allowed") {
		t.Fatalf("expected 405 method_not_allowed, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestRequestBaseURLPrefersForwardedProto(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "http://internal-host/api", nil)
	if got := requestBaseURL(r); got != "http://internal-host" {
		t.Fatalf("plain host request should stay on http, got %q", got)
	}
	r.Header.Set("X-Forwarded-Proto", "https")
	if got := requestBaseURL(r); got != "https://internal-host" {
		t.Fatalf("proxied https must be respected, got %q", got)
	}
}
