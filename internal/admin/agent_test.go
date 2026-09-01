package admin

import (
	"strings"
	"testing"
	"time"

	"agentchunzhi/internal/auth"
)

func TestNewAPIKeyIsOpaqueAndUnique(t *testing.T) {
	first, err := newAPIKey()
	if err != nil {
		t.Fatalf("generate first key: %v", err)
	}
	second, err := newAPIKey()
	if err != nil {
		t.Fatalf("generate second key: %v", err)
	}
	if first == second {
		t.Fatal("generated API keys must be unique")
	}
	if !strings.HasPrefix(first, "ak_") || len(first) < 40 {
		t.Fatalf("unexpected API key format: %q", first)
	}
	if auth.HashAPIKey(first) == first {
		t.Fatal("API key storage value must be hashed")
	}
}

func TestValidInputRequiresAllRegistrationNames(t *testing.T) {
	valid := RegisterAgentInput{
		DisplayName:     "Knowledge Agent",
		ApiKeyName:      "agent-production",
		ApplicationName: "Knowledge Assistant",
		ModelEndpointID: "00000000-0000-4000-8000-000000000001",
		RuntimeMode:     "rag",
		Capabilities:    []string{"query.read", "reference.read"},
		AnswerPosture:   "co_create",
		IdempotencyKey:  "register-agent-idempotency",
	}
	if !validInput(valid) {
		t.Fatal("expected complete registration input to be valid")
	}
	valid.DisplayName = ""
	if validInput(valid) {
		t.Fatal("expected empty display name to be rejected")
	}
}

func TestValidIdempotencyKeyRejectsShortOrControlValues(t *testing.T) {
	if validIdempotencyKey("too-short") {
		t.Fatal("expected short idempotency key to be rejected")
	}
	if validIdempotencyKey("valid-agent-key-\x00") {
		t.Fatal("expected control character in idempotency key to be rejected")
	}
	if !validIdempotencyKey("register-agent-idempotency") {
		t.Fatal("expected normal idempotency key to be accepted")
	}
}

func TestNormalizeCapabilitiesDeduplicatesAndTrims(t *testing.T) {
	got := normalizeCapabilities([]string{" query.read ", "query.read", "", "reference.read", "reference.read", "unknown"})
	if len(got) != 2 || got[0] != "query.read" || got[1] != "reference.read" {
		t.Fatalf("unexpected capabilities: %#v", got)
	}
}

func TestNormalizeActionsSortsAndRejectsUnknownActions(t *testing.T) {
	got, ok := normalizeActions([]string{"publish", "read", "read"})
	if !ok || len(got) != 2 || got[0] != "publish" || got[1] != "read" {
		t.Fatalf("unexpected normalized actions: %#v (ok=%v)", got, ok)
	}
	if _, ok := normalizeActions([]string{"asset.read"}); ok {
		t.Fatal("unsupported action names must be rejected")
	}
}

func TestValidExpiryRejectsPastKeys(t *testing.T) {
	if !validExpiry(nil) {
		t.Fatal("a key without an expiry should be valid")
	}
	future := time.Now().UTC().Add(time.Hour)
	if !validExpiry(&future) {
		t.Fatal("a future expiry should be valid")
	}
	past := time.Now().UTC().Add(-time.Minute)
	if validExpiry(&past) {
		t.Fatal("an expired replacement key should be rejected")
	}
}
