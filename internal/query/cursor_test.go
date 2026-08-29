package query

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestCursorSignVerifyRoundtrip(t *testing.T) {
	codec := newCursorCodec("test-cursor-secret-at-least-32-bytes!")
	payload := cursorPayload{
		SessionID:   "00000000-0000-4000-8000-000000000001",
		NextOrdinal: 20,
		ExpiresAt:   time.Now().UTC().Add(time.Minute),
	}
	token, err := codec.sign(payload)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	decoded, err := codec.verify(token)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if decoded.SessionID != payload.SessionID || decoded.NextOrdinal != payload.NextOrdinal {
		t.Fatalf("decoded = %#v", decoded)
	}
	if decoded.Version != cursorVersion {
		t.Fatalf("version = %d, want %d", decoded.Version, cursorVersion)
	}
}

func TestCursorTamperIsRejected(t *testing.T) {
	codec := newCursorCodec("test-cursor-secret-at-least-32-bytes!")
	token, err := codec.sign(cursorPayload{
		SessionID:   "00000000-0000-4000-8000-000000000001",
		NextOrdinal: 4,
		ExpiresAt:   time.Now().UTC().Add(time.Minute),
	})
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	// Flip the payload body without re-signing.
	body, _, _ := strings.Cut(token, ".")
	tampered := flipBase64Char(body) + token[strings.Index(token, "."):]
	if _, err := codec.verify(tampered); err == nil {
		t.Fatal("tampered payload must be rejected")
	}
	// A different secret must reject the token as well.
	other := newCursorCodec("another-cursor-secret-32-bytes-long!")
	if _, err := other.verify(token); err == nil {
		t.Fatal("token signed under a different secret must be rejected")
	}
}

func TestCursorExpiryIsDetectable(t *testing.T) {
	codec := newCursorCodec("test-cursor-secret-at-least-32-bytes!")
	expired := time.Now().UTC().Add(-time.Minute)
	token, err := codec.sign(cursorPayload{
		SessionID:   "00000000-0000-4000-8000-000000000001",
		NextOrdinal: 0,
		ExpiresAt:   expired,
	})
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	payload, err := codec.verify(token)
	if err != nil {
		t.Fatalf("verify expired cursor shape: %v", err)
	}
	if time.Now().UTC().After(payload.ExpiresAt) {
		// The service maps this to 410 search_session_expired.
		if ErrSearchSessionExpired == nil {
			t.Fatal("expired cursor must map to search_session_expired")
		}
		return
	}
	t.Fatal("expired payload should carry a past timestamp")
}

func TestCursorRejectsGarbage(t *testing.T) {
	codec := newCursorCodec("test-cursor-secret-at-least-32-bytes!")
	for _, token := range []string{"", "garbage", "a.b", "....", "eyJhIjoxfQ.not-base64!!"} {
		if _, err := codec.verify(token); err == nil {
			t.Fatalf("garbage cursor %q must be rejected", token)
		}
	}
}

func TestCursorPayloadJSONShape(t *testing.T) {
	codec := newCursorCodec("test-cursor-secret-at-least-32-bytes!")
	token, err := codec.sign(cursorPayload{
		SessionID:   "00000000-0000-4000-8000-000000000001",
		NextOrdinal: 7,
		ExpiresAt:   time.Unix(1787990400, 0).UTC(),
	})
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	body := token[:strings.Index(token, ".")]
	var raw map[string]any
	if err := json.Unmarshal(mustDecodeBase64URL(t, body), &raw); err != nil {
		t.Fatalf("payload is not JSON: %v", err)
	}
	for _, key := range []string{"version", "session_id", "next_ordinal", "expires_at"} {
		if _, ok := raw[key]; !ok {
			t.Fatalf("payload missing %q: %#v", key, raw)
		}
	}
}

func TestCitationTokenSignVerifyTamper(t *testing.T) {
	secret := "test-query-hash-secret-32-bytes-long"
	payload := citationPayload{
		SessionID:      "00000000-0000-4000-8000-000000000002",
		Ordinal:        3,
		AssetVersionID: "00000000-0000-4000-8000-000000000003",
		SourceChecksum: "sha256:source",
		ChunkChecksum:  "sha256:chunk",
	}
	token, err := buildCitationToken(secret, payload)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	decoded, err := verifyCitationToken(secret, token)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if decoded.SessionID != payload.SessionID || decoded.Ordinal != payload.Ordinal ||
		decoded.AssetVersionID != payload.AssetVersionID ||
		decoded.SourceChecksum != payload.SourceChecksum ||
		decoded.ChunkChecksum != payload.ChunkChecksum {
		t.Fatalf("decoded = %#v", decoded)
	}
	// Tampering with any payload byte breaks the HMAC.
	tampered := flipBase64Char(token[:strings.Index(token, ".")]) + token[strings.Index(token, "."):]
	if _, err := verifyCitationToken(secret, tampered); err == nil {
		t.Fatal("tampered citation token must be rejected")
	}
	// A different secret rejects the token.
	if _, err := verifyCitationToken("other-query-hash-secret-32-bytes!", token); err == nil {
		t.Fatal("citation token under a different secret must be rejected")
	}
}

func flipBase64Char(value string) string {
	if value == "" {
		return "B"
	}
	if value[0] == 'A' {
		return "B" + value[1:]
	}
	return "A" + value[1:]
}

func mustDecodeBase64URL(t *testing.T, value string) []byte {
	t.Helper()
	raw, err := base64RawURL.DecodeString(value)
	if err != nil {
		t.Fatalf("decode base64url: %v", err)
	}
	return raw
}
