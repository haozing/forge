package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestHealth(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()

	NewHandler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, rec.Code)
	}
}

func TestHealthRejectsNonGet(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/healthz", nil)
	rec := httptest.NewRecorder()

	NewHandler().ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected status %d, got %d", http.StatusMethodNotAllowed, rec.Code)
	}
}

func TestLegacyStructuredQueryRouteIsRemoved(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/open/v1/query/structured?model_id=00000000-0000-4000-8000-000000000001", nil)
	rec := httptest.NewRecorder()

	NewHandler().ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected status %d, got %d", http.StatusNotFound, rec.Code)
	}
}

func TestLegacyFulltextQueryRouteIsRemoved(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/open/v1/query/fulltext?q=hello", nil)
	rec := httptest.NewRecorder()

	NewHandler().ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected status %d, got %d", http.StatusNotFound, rec.Code)
	}
}

func TestUnifiedQueryRequiresAPIKey(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/api/open/v1/query", strings.NewReader(`{"mode":"lexical","query":"hello"}`))
	rec := httptest.NewRecorder()

	NewHandler().ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected status %d, got %d", http.StatusUnauthorized, rec.Code)
	}
}

func TestUnifiedQueryRejectsUnavailableMode(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/api/open/v1/query", strings.NewReader(`{"mode":"semantic","query":"hello"}`))
	rec := httptest.NewRecorder()

	NewHandler().ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("authentication must run before mode validation, got %d", rec.Code)
	}
}

func TestConversationMessagesListRequiresSession(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/frontend/conversations/00000000-0000-4000-8000-000000000001/messages", nil)
	rec := httptest.NewRecorder()

	NewHandler().ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected status %d, got %d", http.StatusUnauthorized, rec.Code)
	}
}

func TestConversationMediaRegisterRequiresSession(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/api/frontend/conversations/00000000-0000-4000-8000-000000000001/media", strings.NewReader(`{"attachment_id":"00000000-0000-4000-8000-000000000002","media_kind":"audio"}`))
	req.Header.Set("Idempotency-Key", "conversation-media-register")
	rec := httptest.NewRecorder()

	NewHandler().ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected status %d, got %d", http.StatusUnauthorized, rec.Code)
	}
}

func TestConversationMediaTranscriptionRequiresSession(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/api/frontend/conversation-media/00000000-0000-4000-8000-000000000001/transcribe", nil)
	req.Header.Set("Idempotency-Key", "conversation-media-transcribe")
	rec := httptest.NewRecorder()

	NewHandler().ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected status %d, got %d", http.StatusUnauthorized, rec.Code)
	}
}

func TestSemanticQueryIsNotExposed(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/open/v1/query/semantic?q=hello", nil)
	rec := httptest.NewRecorder()

	NewHandler().ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected status %d, got %d", http.StatusNotFound, rec.Code)
	}
}

func TestAssetPublishRequiresAPIKey(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/api/open/v1/assets/00000000-0000-4000-8000-000000000001/publish", strings.NewReader(`{"version_id":"00000000-0000-4000-8000-000000000002"}`))
	rec := httptest.NewRecorder()

	NewHandler().ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected status %d, got %d", http.StatusUnauthorized, rec.Code)
	}
}

func TestAssetCreateRequiresAPIKey(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/api/open/v1/assets", strings.NewReader(`{"resource_model_id":"00000000-0000-4000-8000-000000000001"}`))
	rec := httptest.NewRecorder()
	NewHandler().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected status %d, got %d", http.StatusUnauthorized, rec.Code)
	}
}

func TestAssetUpdateRequiresAPIKey(t *testing.T) {
	req := httptest.NewRequest(http.MethodPatch, "/api/open/v1/assets/00000000-0000-4000-8000-000000000001", strings.NewReader(`{"title":"updated"}`))
	rec := httptest.NewRecorder()
	NewHandler().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected status %d, got %d", http.StatusUnauthorized, rec.Code)
	}
}

func TestAssetReferencesRequiresAPIKey(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/open/v1/assets/00000000-0000-4000-8000-000000000001/references", nil)
	rec := httptest.NewRecorder()
	NewHandler().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected status %d, got %d", http.StatusUnauthorized, rec.Code)
	}
}

func TestAgentTaskCreateRequiresAPIKey(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/api/open/v1/agent/tasks", strings.NewReader(`{"operation":"prepare_asset"}`))
	req.Header.Set("Idempotency-Key", "agent-task-create-idempotency")
	rec := httptest.NewRecorder()
	NewHandler().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected status %d, got %d", http.StatusUnauthorized, rec.Code)
	}
}

func TestAgentTaskGetRequiresAPIKey(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/open/v1/agent/tasks/00000000-0000-4000-8000-000000000001", nil)
	rec := httptest.NewRecorder()
	NewHandler().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected status %d, got %d", http.StatusUnauthorized, rec.Code)
	}
}

func TestAgentRegistrationRequiresMemberSession(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/api/admin/agent-users", strings.NewReader(`{"display_name":"Eino Agent"}`))
	rec := httptest.NewRecorder()
	NewHandler().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected status %d, got %d", http.StatusUnauthorized, rec.Code)
	}
}

func TestAgentPolicyUpdateRequiresMemberSession(t *testing.T) {
	req := httptest.NewRequest(http.MethodPut, "/api/admin/agent-users/00000000-0000-4000-8000-000000000001/access-policy", strings.NewReader(`{"resource_model_id":"00000000-0000-4000-8000-000000000002","actions":["read"]}`))
	req.Header.Set("Idempotency-Key", "agent-policy-idempotency")
	rec := httptest.NewRecorder()
	NewHandler().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected status %d, got %d", http.StatusUnauthorized, rec.Code)
	}
}

func TestAgentAPIKeyRotationRequiresMemberSession(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/api/admin/agent-users/00000000-0000-4000-8000-000000000001/api-keys/rotate", strings.NewReader(`{"name":"eino-production"}`))
	req.Header.Set("Idempotency-Key", "agent-key-rotation-idempotency")
	rec := httptest.NewRecorder()
	NewHandler().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected status %d, got %d", http.StatusUnauthorized, rec.Code)
	}
}

func TestAgentApplicationListRequiresMemberSession(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/admin/agent-applications", nil)
	rec := httptest.NewRecorder()
	NewHandler().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected status %d, got %d", http.StatusUnauthorized, rec.Code)
	}
}

func TestAgentApplicationReadRequiresMemberSession(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/admin/agent-applications/00000000-0000-4000-8000-000000000001", nil)
	rec := httptest.NewRecorder()
	NewHandler().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected status %d, got %d", http.StatusUnauthorized, rec.Code)
	}
}

func TestAgentApplicationStatusRequiresMemberSession(t *testing.T) {
	req := httptest.NewRequest(http.MethodPatch, "/api/admin/agent-applications/00000000-0000-4000-8000-000000000001/status", strings.NewReader(`{"status":"disabled"}`))
	req.Header.Set("Idempotency-Key", "agent-application-status")
	rec := httptest.NewRecorder()
	NewHandler().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected status %d, got %d", http.StatusUnauthorized, rec.Code)
	}
}

func TestParseOptionalExpiry(t *testing.T) {
	if value, ok := parseOptionalExpiry(nil); !ok || value != nil {
		t.Fatal("nil expiry should remain unset")
	}
	valid := "2027-08-22T00:00:00Z"
	value, ok := parseOptionalExpiry(&valid)
	if !ok || value == nil || !value.Equal(time.Date(2027, 8, 22, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("unexpected parsed expiry: %v (ok=%v)", value, ok)
	}
	invalid := "not-a-date"
	if _, ok := parseOptionalExpiry(&invalid); ok {
		t.Fatal("invalid expiry should be rejected")
	}
}

func TestAgentApplicationSessionRequiresMemberSession(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/api/frontend/agent-applications/00000000-0000-4000-8000-000000000001/sessions", nil)
	req.Header.Set("Idempotency-Key", "agent-session-idempotency")
	rec := httptest.NewRecorder()
	NewHandler().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected status %d, got %d", http.StatusUnauthorized, rec.Code)
	}
}

func TestAgentSessionReferenceValidationRequiresMemberSession(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/api/frontend/agent-sessions/00000000-0000-4000-8000-000000000001/references/validate", strings.NewReader(`{"references":[]}`))
	rec := httptest.NewRecorder()
	NewHandler().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected status %d, got %d", http.StatusUnauthorized, rec.Code)
	}
}

func TestAgentSessionChatRequiresMemberSession(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/api/frontend/agent-sessions/00000000-0000-4000-8000-000000000001/chat", strings.NewReader(`{"query":"hello"}`))
	rec := httptest.NewRecorder()
	NewHandler().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected status %d, got %d", http.StatusUnauthorized, rec.Code)
	}
}

func TestAgentSessionChatStreamRequiresMemberSession(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/api/frontend/agent-sessions/00000000-0000-4000-8000-000000000001/chat/stream", strings.NewReader(`{"query":"hello"}`))
	rec := httptest.NewRecorder()
	NewHandler().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected status %d, got %d", http.StatusUnauthorized, rec.Code)
	}
}

func TestAttachmentDownloadRequiresAPIKey(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/open/v1/attachments/00000000-0000-4000-8000-000000000001/download", nil)
	rec := httptest.NewRecorder()

	NewHandler().ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected status %d, got %d", http.StatusUnauthorized, rec.Code)
	}
}

func TestReadyRequiresDatabase(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	rec := httptest.NewRecorder()

	NewHandler().ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected status %d, got %d", http.StatusServiceUnavailable, rec.Code)
	}
}

func TestSessionRejectsMalformedRequest(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/api/public/v2/sessions", nil)
	rec := httptest.NewRecorder()

	NewHandler().ServeHTTP(rec, req)

	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected status %d, got %d", http.StatusUnprocessableEntity, rec.Code)
	}
}

func TestCurrentUserRequiresSession(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/v2/me", nil)
	rec := httptest.NewRecorder()

	NewHandler().ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected status %d, got %d", http.StatusUnauthorized, rec.Code)
	}
}

// TestPhase1LegacyRoutesAreRetired pins the phase 1 route retirement ledger:
// the legacy identity/workspace paths answer 404 without compatibility
// redirects once the v2 replacements are registered.
func TestPhase1LegacyRoutesAreRetired(t *testing.T) {
	paths := []string{
		"/api/me",
		"/api/me/profile",
		"/api/sessions",
		"/api/frontend/me/preferences",
		"/api/frontend/workspaces",
		"/api/frontend/workspaces/00000000-0000-4000-8000-000000000001",
		"/api/frontend/workspaces/00000000-0000-4000-8000-000000000001/members",
		"/api/frontend/workspaces/00000000-0000-4000-8000-000000000001/member-invitations",
		"/api/frontend/workspaces/00000000-0000-4000-8000-000000000001/settings",
		"/api/frontend/workspaces/00000000-0000-4000-8000-000000000001/counts",
		"/api/frontend/workspaces/00000000-0000-4000-8000-000000000001/stats",
		"/api/frontend/workspaces/00000000-0000-4000-8000-000000000001/activity",
		"/api/frontend/workspaces/00000000-0000-4000-8000-000000000001/audit-logs",
		"/api/frontend/workspace-members/00000000-0000-4000-8000-000000000001",
	}
	handler := NewHandler()
	for _, path := range paths {
		t.Run(path, func(t *testing.T) {
			for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodDelete} {
				req := httptest.NewRequest(method, path, nil)
				rec := httptest.NewRecorder()
				handler.ServeHTTP(rec, req)
				if rec.Code != http.StatusNotFound {
					t.Fatalf("%s %s: retired route answered %d, want 404", method, path, rec.Code)
				}
			}
		})
	}
}

// TestV2IdentityRoutesRequireSession covers the member identity surface: all
// of them answer 401 without a session cookie.
func TestV2IdentityRoutesRequireSession(t *testing.T) {
	cases := []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/api/v2/me"},
		{http.MethodPatch, "/api/v2/me"},
		{http.MethodGet, "/api/v2/me/preferences"},
		{http.MethodPut, "/api/v2/me/password"},
		{http.MethodGet, "/api/v2/sessions"},
		{http.MethodDelete, "/api/v2/sessions/current"},
		{http.MethodDelete, "/api/v2/sessions/00000000-0000-4000-8000-000000000001"},
		{http.MethodGet, "/api/v2/organization"},
		{http.MethodGet, "/api/v2/organization/members"},
		{http.MethodGet, "/api/v2/organization/invitations"},
		{http.MethodGet, "/api/v2/workspaces"},
		{http.MethodGet, "/api/v2/workspaces/00000000-0000-4000-8000-000000000001/members"},
	}
	handler := NewHandler()
	for _, tc := range cases {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, tc.path, nil)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("expected status %d, got %d", http.StatusUnauthorized, rec.Code)
			}
		})
	}
}

// TestOriginPolicyRejectsForeignOrigins covers the CSRF Origin allowlist for
// unsafe v2 requests.
func TestOriginPolicyRejectsForeignOrigins(t *testing.T) {
	deps := Dependencies{AllowedOrigins: []string{"https://app.example.com"}}
	handler := NewHandlerWithDeps(deps)

	req := httptest.NewRequest(http.MethodPost, "/api/public/v2/sessions", strings.NewReader(`{"email":"a@b.co","password":"x"}`))
	req.Header.Set("Origin", "https://evil.example")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("foreign origin answered %d, want 403", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "origin_not_allowed") {
		t.Fatalf("body = %s", rec.Body.String())
	}

	// Allowed origin passes through to the handler (which then fails on the
	// unconfigured database with a validation error, not 403).
	req = httptest.NewRequest(http.MethodPost, "/api/public/v2/sessions", strings.NewReader(`{}`))
	req.Header.Set("Origin", "https://app.example.com")
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code == http.StatusForbidden {
		t.Fatal("allowed origin must not be rejected by the origin policy")
	}

	// GET requests are exempt from the policy.
	req = httptest.NewRequest(http.MethodGet, "/api/v2/me", nil)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code == http.StatusForbidden {
		t.Fatal("GET must be exempt from the origin policy")
	}

	// Without configuration the policy is a no-op.
	req = httptest.NewRequest(http.MethodPost, "/api/public/v2/sessions", strings.NewReader(`{}`))
	rec = httptest.NewRecorder()
	NewHandler().ServeHTTP(rec, req)
	if rec.Code == http.StatusForbidden {
		t.Fatal("no AllowedOrigins configured: policy must be disabled")
	}
}

func TestMemberAttachmentDownloadRequiresSession(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/attachments/00000000-0000-4000-8000-000000000001/download", nil)
	rec := httptest.NewRecorder()

	NewHandler().ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected status %d, got %d", http.StatusUnauthorized, rec.Code)
	}
}

func TestAttachmentStatusRequiresSession(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/attachments/00000000-0000-4000-8000-000000000001", nil)
	res := httptest.NewRecorder()
	NewHandler().ServeHTTP(res, req)
	if res.Code != http.StatusUnauthorized {
		t.Fatalf("expected unauthorized, got %d", res.Code)
	}
}
