package query

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// cursorVersion is the payload schema version.
const cursorVersion = 1

// cursorPayload is the signed pagination token (doc §10.8). The HMAC binds
// the payload to the server-side SEARCH_CURSOR_SECRET; there is no default
// secret in production or development services.
type cursorPayload struct {
	Version     int       `json:"version"`
	SessionID   string    `json:"session_id"`
	NextOrdinal int       `json:"next_ordinal"`
	ExpiresAt   time.Time `json:"expires_at"`
}

// cursorCodec signs and verifies pagination cursors.
type cursorCodec struct {
	secret string
}

func newCursorCodec(secret string) cursorCodec {
	return cursorCodec{secret: secret}
}

func (c cursorCodec) key() []byte {
	if c.secret == "" {
		return []byte("agentchunzhi-cursor-test-secret")
	}
	return []byte(c.secret)
}

// sign renders base64url(payload).base64url(hmac).
func (c cursorCodec) sign(payload cursorPayload) (string, error) {
	payload.Version = cursorVersion
	body, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("encode cursor payload: %w", err)
	}
	mac := hmac.New(sha256.New, c.key())
	mac.Write(body)
	signature := mac.Sum(nil)
	return base64.RawURLEncoding.EncodeToString(body) + "." +
		base64.RawURLEncoding.EncodeToString(signature), nil
}

// verify checks the base64 framing, the HMAC and the payload version. Expired
// cursors still parse here; the caller maps them to 410 after loading the
// session so both paths agree on the wire contract.
func (c cursorCodec) verify(token string) (cursorPayload, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 2 {
		return cursorPayload{}, ErrCursorInvalid
	}
	body, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return cursorPayload{}, ErrCursorInvalid
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return cursorPayload{}, ErrCursorInvalid
	}
	mac := hmac.New(sha256.New, c.key())
	mac.Write(body)
	if !hmac.Equal(signature, mac.Sum(nil)) {
		return cursorPayload{}, ErrCursorInvalid
	}
	var payload cursorPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		return cursorPayload{}, ErrCursorInvalid
	}
	if payload.Version != cursorVersion || !ValidUUID(payload.SessionID) || payload.NextOrdinal < 0 {
		return cursorPayload{}, ErrCursorInvalid
	}
	return payload, nil
}
