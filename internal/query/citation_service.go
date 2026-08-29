package query

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
)

// citationVersion is the opaque CitationRef payload version (doc §6.5). The
// token is server-generated: clients never assemble asset or chunk ids.
const citationVersion = 1

// citationPayload carries the fields a validate request re-checks: session,
// item ordinal, asset version and the source/chunk checksums.
type citationPayload struct {
	Version        int    `json:"v"`
	SessionID      string `json:"s"`
	Ordinal        int    `json:"o"`
	AssetVersionID string `json:"ver"`
	SourceChecksum string `json:"sc"`
	ChunkChecksum  string `json:"cc"`
}

// buildCitationToken derives the opaque citation id of one session item.
func buildCitationToken(secret string, payload citationPayload) (string, error) {
	payload.Version = citationVersion
	body, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("encode citation payload: %w", err)
	}
	mac := citationMAC(secret, body)
	return base64.RawURLEncoding.EncodeToString(body) + "." +
		base64.RawURLEncoding.EncodeToString(mac), nil
}

func citationMAC(secret string, body []byte) []byte {
	key := secret
	if key == "" {
		key = "agentchunzhi-citation-test-secret"
	}
	mac := hmac.New(sha256.New, []byte(key))
	mac.Write([]byte("citation\x00"))
	mac.Write(body)
	return mac.Sum(nil)
}

// verifyCitationToken checks framing, HMAC and payload shape. Validation
// failures collapse into one 404 upstream; the error here only decides the
// control flow.
func verifyCitationToken(secret, token string) (citationPayload, error) {
	var payload citationPayload
	parts := strings.Split(token, ".")
	if len(parts) != 2 {
		return payload, ErrCitationRefNotFound
	}
	body, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return payload, ErrCitationRefNotFound
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return payload, ErrCitationRefNotFound
	}
	if !hmac.Equal(signature, citationMAC(secret, body)) {
		return payload, ErrCitationRefNotFound
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return payload, ErrCitationRefNotFound
	}
	if payload.Version != citationVersion || !ValidUUID(payload.SessionID) || payload.Ordinal < 0 {
		return payload, ErrCitationRefNotFound
	}
	return payload, nil
}
