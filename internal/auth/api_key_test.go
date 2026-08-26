package auth

import "testing"

func TestBearerToken(t *testing.T) {
	if token, ok := bearerToken("Bearer secret"); !ok || token != "secret" {
		t.Fatalf("expected bearer token")
	}
	if _, ok := bearerToken("Basic secret"); ok {
		t.Fatalf("basic credentials must be rejected")
	}
}

func TestHashAPIKeyIsDeterministic(t *testing.T) {
	if HashAPIKey("secret") != HashAPIKey("secret") {
		t.Fatalf("hash must be deterministic")
	}
	if HashAPIKey("secret") == HashAPIKey("other") {
		t.Fatalf("different keys must not share a hash")
	}
}
