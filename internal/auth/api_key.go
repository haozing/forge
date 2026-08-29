package auth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"agentchunzhi/internal/store"
)

var ErrUnauthenticated = errors.New("unauthenticated")

// Subject types stored in identity.users.user_type.
const (
	UserTypeMember = "member"
	UserTypeAgent  = "agent"
)

type Principal struct {
	UserID         string
	OrganizationID string
	UserType       string
	Capabilities   []string
}

func (p Principal) HasCapability(capability string) bool {
	for _, value := range p.Capabilities {
		if value == capability {
			return true
		}
	}
	return false
}

type APIKeyAuthenticator struct {
	Store *store.Store
}

func (a APIKeyAuthenticator) Authenticate(ctx context.Context, r *http.Request) (Principal, error) {
	raw, ok := bearerToken(r.Header.Get("Authorization"))
	if !ok || a.Store == nil || a.Store.Pool == nil {
		return Principal{}, ErrUnauthenticated
	}
	hash := hashAPIKey(raw)
	var p Principal
	var capabilitiesJSON []byte
	err := a.Store.Pool.QueryRow(ctx, `
		SELECT u.id, u.organization_id, u.user_type, k.capabilities
		FROM identity.api_keys k
		JOIN identity.users u ON u.id = k.user_id
		JOIN organization.organizations o ON o.id = u.organization_id
		WHERE k.key_hash = $1
		  AND k.status = 'active'
		  AND u.status = 'active'
		  AND o.status = 'active'
		  AND (k.expires_at IS NULL OR k.expires_at > now())
	`, hash).Scan(&p.UserID, &p.OrganizationID, &p.UserType, &capabilitiesJSON)
	if err != nil {
		return Principal{}, ErrUnauthenticated
	}
	if json.Unmarshal(capabilitiesJSON, &p.Capabilities) != nil {
		return Principal{}, ErrUnauthenticated
	}
	if _, err := a.Store.Pool.Exec(ctx, `UPDATE identity.api_keys SET last_used_at = now() WHERE key_hash = $1`, hash); err != nil {
		return Principal{}, ErrUnauthenticated
	}
	return p, nil
}

func bearerToken(value string) (string, bool) {
	parts := strings.Fields(value)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") || parts[1] == "" {
		return "", false
	}
	return parts[1], true
}

func HashAPIKey(raw string) string {
	return hashAPIKey(raw)
}

func hashAPIKey(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}
